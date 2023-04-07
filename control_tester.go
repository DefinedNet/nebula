//go:build e2e_testing
// +build e2e_testing

package nebula

import (
	"net"

	"github.com/slackhq/nebula/cert"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/udp"
)

// WaitForType will pipe all messages from this control device into the pipeTo control device
// returning after a message matching the criteria has been piped
func (c *Control) WaitForType(msgType header.MessageType, subType header.MessageSubType, pipeTo *Control) {
	h := &header.H{}
	for {
		p := c.f.outside.Get(true)
		if err := h.Parse(p.Data); err != nil {
			panic(err)
		}
		pipeTo.InjectUDPPacket(p)
		if h.Type == msgType && h.Subtype == subType {
			return
		}
	}
}

// WaitForTypeByIndex is similar to WaitForType except it adds an index check
// Useful if you have many nodes communicating and want to wait to find a specific nodes packet
func (c *Control) WaitForTypeByIndex(toIndex uint32, msgType header.MessageType, subType header.MessageSubType, pipeTo *Control) {
	h := &header.H{}
	for {
		p := c.f.outside.Get(true)
		if err := h.Parse(p.Data); err != nil {
			panic(err)
		}
		pipeTo.InjectUDPPacket(p)
		if h.RemoteIndex == toIndex && h.Type == msgType && h.Subtype == subType {
			return
		}
	}
}

// InjectLightHouseAddr will push toAddr into the local lighthouse cache for the vpnIp
// This is necessary if you did not configure static hosts or are not running a lighthouse
func (c *Control) InjectLightHouseAddr(vpnIp net.IP, toAddr *net.UDPAddr) {
	c.f.lightHouse.Lock()
	remoteList := c.f.lightHouse.unlockedGetRemoteList(iputil.Ip2VpnIp(vpnIp))
	remoteList.Lock()
	defer remoteList.Unlock()
	c.f.lightHouse.Unlock()

	iVpnIp := iputil.Ip2VpnIp(vpnIp)
	if v4 := toAddr.IP.To4(); v4 != nil {
		remoteList.unlockedPrependV4(iVpnIp, NewIp4AndPort(v4, uint32(toAddr.Port)))
	} else {
		remoteList.unlockedPrependV6(iVpnIp, NewIp6AndPort(toAddr.IP, uint32(toAddr.Port)))
	}
}

// InjectRelays will push relayVpnIps into the local lighthouse cache for the vpnIp
// This is necessary to inform an initiator of possible relays for communicating with a responder
func (c *Control) InjectRelays(vpnIp net.IP, relayVpnIps []net.IP) {
	c.f.lightHouse.Lock()
	remoteList := c.f.lightHouse.unlockedGetRemoteList(iputil.Ip2VpnIp(vpnIp))
	remoteList.Lock()
	defer remoteList.Unlock()
	c.f.lightHouse.Unlock()

	iVpnIp := iputil.Ip2VpnIp(vpnIp)
	uVpnIp := []uint32{}
	for _, rVPnIp := range relayVpnIps {
		uVpnIp = append(uVpnIp, uint32(iputil.Ip2VpnIp(rVPnIp)))
	}

	remoteList.unlockedSetRelay(iVpnIp, iVpnIp, uVpnIp)
}

// GetFromTun will pull a packet off the tun side of nebula
func (c *Control) GetFromTun(block bool) []byte {
	return c.f.inside.(*overlay.TestTun).Get(block)
}

// GetFromUDP will pull a udp packet off the udp side of nebula
func (c *Control) GetFromUDP(block bool) *udp.Packet {
	return c.f.outside.Get(block)
}

func (c *Control) GetUDPTxChan() <-chan *udp.Packet {
	return c.f.outside.TxPackets
}

func (c *Control) GetTunTxChan() <-chan []byte {
	return c.f.inside.(*overlay.TestTun).TxPackets
}

// InjectUDPPacket will inject a packet into the udp side of nebula
func (c *Control) InjectUDPPacket(p *udp.Packet) {
	c.f.outside.Send(p)
}

// InjectTunUDPPacket puts a udp packet on the tun interface. Using UDP here because it's a simpler protocol
func (c *Control) InjectTunUDPPacket(toIp net.IP, toPort uint16, fromPort uint16, data []byte) {
	ip := layers.IPv4{
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    c.f.inside.Cidr().IP,
		DstIP:    toIp,
	}

	udp := layers.UDP{
		SrcPort: layers.UDPPort(fromPort),
		DstPort: layers.UDPPort(toPort),
	}
	err := udp.SetNetworkLayerForChecksum(&ip)
	if err != nil {
		panic(err)
	}

	buffer := gopacket.NewSerializeBuffer()
	opt := gopacket.SerializeOptions{
		ComputeChecksums: true,
		FixLengths:       true,
	}
	err = gopacket.SerializeLayers(buffer, opt, &ip, &udp, gopacket.Payload(data))
	if err != nil {
		panic(err)
	}

	c.f.inside.(*overlay.TestTun).Send(buffer.Bytes())
}

func (c *Control) GetVpnIp() iputil.VpnIp {
	return c.f.myVpnIp
}

func (c *Control) GetUDPAddr() string {
	return c.f.outside.Addr.String()
}

func (c *Control) KillPendingTunnel(vpnIp net.IP) bool {
	hostinfo, ok := c.f.handshakeManager.pendingHostMap.Hosts[iputil.Ip2VpnIp(vpnIp)]
	if !ok {
		return false
	}

	c.f.handshakeManager.pendingHostMap.DeleteHostInfo(hostinfo)
	return true
}

func (c *Control) GetHostmap() *HostMap {
	return c.f.hostMap
}

func (c *Control) GetCert() *cert.NebulaCertificate {
	return c.f.certState.Load().certificate
}

func (c *Control) ReHandshake(vpnIp iputil.VpnIp) {
	hostinfo := c.f.handshakeManager.AddVpnIp(vpnIp, c.f.initHostInfo)
	ixHandshakeStage0(c.f, vpnIp, hostinfo)

	// If this is a static host, we don't need to wait for the HostQueryReply
	// We can trigger the handshake right now
	if _, ok := c.f.lightHouse.GetStaticHostList()[hostinfo.vpnIp]; ok {
		select {
		case c.f.handshakeManager.trigger <- hostinfo.vpnIp:
		default:
		}
	}
}
