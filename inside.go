package nebula

import (
	"sync/atomic"

	"github.com/flynn/noise"
	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula/firewall"
	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/udp"
)

func (f *Interface) consumeInsidePacket(packet []byte, fwPacket *firewall.Packet, nb, out []byte, q int, localCache firewall.ConntrackCache) {
	err := newPacket(packet, false, fwPacket)
	if err != nil {
		f.l.WithField("packet", packet).Debugf("Error while validating outbound packet: %s", err)
		return
	}

	// Ignore local broadcast packets
	if f.dropLocalBroadcast && fwPacket.RemoteIP == f.localBroadcast {
		return
	}

	// Ignore packets from self to self
	if fwPacket.RemoteIP == f.myVpnIp {
		return
	}

	// Ignore broadcast packets
	if f.dropMulticast && isMulticast(fwPacket.RemoteIP) {
		return
	}

	hostinfo := f.getOrHandshake(fwPacket.RemoteIP)
	if hostinfo == nil {
		if f.l.Level >= logrus.DebugLevel {
			f.l.WithField("vpnIp", fwPacket.RemoteIP).
				WithField("fwPacket", fwPacket).
				Debugln("dropping outbound packet, vpnIp not in our CIDR or in unsafe routes")
		}
		return
	}
	ci := hostinfo.ConnectionState

	if ci.ready == false {
		// Because we might be sending stored packets, lock here to stop new things going to
		// the packet queue.
		ci.queueLock.Lock()
		if !ci.ready {
			hostinfo.cachePacket(f.l, header.Message, 0, packet, f.sendMessageNow, f.cachedPacketMetrics)
			ci.queueLock.Unlock()
			return
		}
		ci.queueLock.Unlock()
	}

	dropReason := f.firewall.Drop(packet, *fwPacket, false, hostinfo, f.caPool, localCache)
	if dropReason == nil {
		f.sendNoMetrics(header.Message, 0, ci, hostinfo, hostinfo.remote, packet, nb, out, q)

	} else if f.l.Level >= logrus.DebugLevel {
		hostinfo.logger(f.l).
			WithField("fwPacket", fwPacket).
			WithField("reason", dropReason).
			Debugln("dropping outbound packet")
	}
}

// getOrHandshake returns nil if the vpnIp is not routable
func (f *Interface) getOrHandshake(vpnIp iputil.VpnIp) *HostInfo {
	//TODO: we can find contains without converting back to bytes
	if f.hostMap.vpnCIDR.Contains(vpnIp.ToIP()) == false {
		vpnIp = f.inside.RouteFor(vpnIp)
		if vpnIp == 0 {
			return nil
		}
	}
	hostinfo, err := f.hostMap.PromoteBestQueryVpnIp(vpnIp, f)

	//if err != nil || hostinfo.ConnectionState == nil {
	if err != nil {
		hostinfo, err = f.handshakeManager.pendingHostMap.QueryVpnIp(vpnIp)
		if err != nil {
			hostinfo = f.handshakeManager.AddVpnIp(vpnIp, f.initHostInfo)
		}
	}
	ci := hostinfo.ConnectionState

	if ci != nil && ci.eKey != nil && ci.ready {
		return hostinfo
	}

	// Handshake is not ready, we need to grab the lock now before we start the handshake process
	hostinfo.Lock()
	defer hostinfo.Unlock()

	// Double check, now that we have the lock
	ci = hostinfo.ConnectionState
	if ci != nil && ci.eKey != nil && ci.ready {
		return hostinfo
	}

	// If we have already created the handshake packet, we don't want to call the function at all.
	if !hostinfo.HandshakeReady {
		ixHandshakeStage0(f, vpnIp, hostinfo)
		// FIXME: Maybe make XX selectable, but probably not since psk makes it nearly pointless for us.
		//xx_handshakeStage0(f, ip, hostinfo)

		// If this is a static host, we don't need to wait for the HostQueryReply
		// We can trigger the handshake right now
		if _, ok := f.lightHouse.GetStaticHostList()[vpnIp]; ok {
			select {
			case f.handshakeManager.trigger <- vpnIp:
			default:
			}
		}
	}

	return hostinfo
}

// initHostInfo is the init function to pass to (*HandshakeManager).AddVpnIP that
// will create the initial Noise ConnectionState
func (f *Interface) initHostInfo(hostinfo *HostInfo) {
	hostinfo.ConnectionState = f.newConnectionState(f.l, true, noise.HandshakeIX, []byte{}, 0)
}

func (f *Interface) sendMessageNow(t header.MessageType, st header.MessageSubType, hostInfo *HostInfo, p, nb, out []byte) {
	fp := &firewall.Packet{}
	err := newPacket(p, false, fp)
	if err != nil {
		f.l.Warnf("error while parsing outgoing packet for firewall check; %v", err)
		return
	}

	// check if packet is in outbound fw rules
	dropReason := f.firewall.Drop(p, *fp, false, hostInfo, f.caPool, nil)
	if dropReason != nil {
		if f.l.Level >= logrus.DebugLevel {
			f.l.WithField("fwPacket", fp).
				WithField("reason", dropReason).
				Debugln("dropping cached packet")
		}
		return
	}

	f.sendNoMetrics(header.Message, st, hostInfo.ConnectionState, hostInfo, hostInfo.remote, p, nb, out, 0)
}

// SendMessageToVpnIp handles real ip:port lookup and sends to the current best known address for vpnIp
func (f *Interface) SendMessageToVpnIp(t header.MessageType, st header.MessageSubType, vpnIp iputil.VpnIp, p, nb, out []byte) {
	hostInfo := f.getOrHandshake(vpnIp)
	if hostInfo == nil {
		if f.l.Level >= logrus.DebugLevel {
			f.l.WithField("vpnIp", vpnIp).
				Debugln("dropping SendMessageToVpnIp, vpnIp not in our CIDR or in unsafe routes")
		}
		return
	}

	if !hostInfo.ConnectionState.ready {
		// Because we might be sending stored packets, lock here to stop new things going to
		// the packet queue.
		hostInfo.ConnectionState.queueLock.Lock()
		if !hostInfo.ConnectionState.ready {
			hostInfo.cachePacket(f.l, t, st, p, f.sendMessageToVpnIp, f.cachedPacketMetrics)
			hostInfo.ConnectionState.queueLock.Unlock()
			return
		}
		hostInfo.ConnectionState.queueLock.Unlock()
	}

	f.sendMessageToVpnIp(t, st, hostInfo, p, nb, out)
	return
}

func (f *Interface) sendMessageToVpnIp(t header.MessageType, st header.MessageSubType, hostInfo *HostInfo, p, nb, out []byte) {
	f.send(t, st, hostInfo.ConnectionState, hostInfo, hostInfo.remote, p, nb, out)
}

func (f *Interface) send(t header.MessageType, st header.MessageSubType, ci *ConnectionState, hostinfo *HostInfo, remote *udp.Addr, p, nb, out []byte) {
	f.messageMetrics.Tx(t, st, 1)
	f.sendNoMetrics(t, st, ci, hostinfo, remote, p, nb, out, 0)
}

func (f *Interface) sendNoMetrics(t header.MessageType, st header.MessageSubType, ci *ConnectionState, hostinfo *HostInfo, remote *udp.Addr, p, nb, out []byte, q int) {
	if ci.eKey == nil {
		//TODO: log warning
		return
	}

	var err error
	//TODO: enable if we do more than 1 tun queue
	//ci.writeLock.Lock()
	c := atomic.AddUint64(&ci.atomicMessageCounter, 1)

	//l.WithField("trace", string(debug.Stack())).Error("out Header ", &Header{Version, t, st, 0, hostinfo.remoteIndexId, c}, p)
	out = header.Encode(out, header.Version, t, st, hostinfo.remoteIndexId, c)
	f.connectionManager.Out(hostinfo.vpnIp)

	// Query our LH if we haven't since the last time we've been rebound, this will cause the remote to punch against
	// all our IPs and enable a faster roaming.
	if t != header.CloseTunnel && hostinfo.lastRebindCount != f.rebindCount {
		//NOTE: there is an update hole if a tunnel isn't used and exactly 256 rebinds occur before the tunnel is
		// finally used again. This tunnel would eventually be torn down and recreated if this action didn't help.
		f.lightHouse.QueryServer(hostinfo.vpnIp, f)
		hostinfo.lastRebindCount = f.rebindCount
		if f.l.Level >= logrus.DebugLevel {
			f.l.WithField("vpnIp", hostinfo.vpnIp).Debug("Lighthouse update triggered for punch due to rebind counter")
		}
	}

	out, err = ci.eKey.EncryptDanger(out, out, p, c, nb)
	//TODO: see above note on lock
	//ci.writeLock.Unlock()
	if err != nil {
		hostinfo.logger(f.l).WithError(err).
			WithField("udpAddr", remote).WithField("counter", c).
			WithField("attemptedCounter", c).
			Error("Failed to encrypt outgoing packet")
		return
	}

	err = f.writers[q].WriteTo(out, remote)
	if err != nil {
		hostinfo.logger(f.l).WithError(err).
			WithField("udpAddr", remote).Error("Failed to write outgoing packet")
	}
	return
}

func isMulticast(ip iputil.VpnIp) bool {
	// Class D multicast
	if (((ip >> 24) & 0xff) & 0xf0) == 0xe0 {
		return true
	}

	return false
}
