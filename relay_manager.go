package nebula

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
)

type relayManager struct {
	l       *logrus.Logger
	hostmap *HostMap
	amRelay atomic.Bool
}

func NewRelayManager(ctx context.Context, l *logrus.Logger, hostmap *HostMap, c *config.C) *relayManager {
	rm := &relayManager{
		l:       l,
		hostmap: hostmap,
	}
	rm.reload(c, true)
	c.RegisterReloadCallback(func(c *config.C) {
		err := rm.reload(c, false)
		if err != nil {
			l.WithError(err).Error("Failed to reload relay_manager")
		}
	})
	return rm
}

func (rm *relayManager) reload(c *config.C, initial bool) error {
	if initial || c.HasChanged("relay.am_relay") {
		rm.setAmRelay(c.GetBool("relay.am_relay", false))
	}
	return nil
}

func (rm *relayManager) GetAmRelay() bool {
	return rm.amRelay.Load()
}

func (rm *relayManager) setAmRelay(v bool) {
	rm.amRelay.Store(v)
}

// AddRelay finds an available relay index on the hostmap, and associates the relay info with it.
// relayHostInfo is the Nebula peer which can be used as a relay to access the target vpnIp.
func AddRelay(l *logrus.Logger, relayHostInfo *HostInfo, hm *HostMap, vpnIp iputil.VpnIp, remoteIdx *uint32, relayType int, state int) (uint32, error) {
	hm.Lock()
	defer hm.Unlock()
	for i := 0; i < 32; i++ {
		index, err := generateIndex(l)
		if err != nil {
			return 0, err
		}

		_, inRelays := hm.Relays[index]
		if !inRelays {
			// Avoid standing up a relay that can't be used since only the primary hostinfo
			// will be pointed to by the relay logic
			//TODO: if there was an existing primary and it had relay state, should we merge?
			hm.unlockedMakePrimary(relayHostInfo)

			hm.Relays[index] = relayHostInfo
			newRelay := Relay{
				Type:       relayType,
				State:      state,
				LocalIndex: index,
				PeerIp:     vpnIp,
			}

			if remoteIdx != nil {
				newRelay.RemoteIndex = *remoteIdx
			}
			relayHostInfo.relayState.InsertRelay(vpnIp, index, &newRelay)

			return index, nil
		}
	}

	return 0, errors.New("failed to generate unique localIndexId")
}

// EstablishRelay updates a Requested Relay to become an Established Relay, which can pass traffic.
func (rm *relayManager) EstablishRelay(relayHostInfo *HostInfo, m *NebulaControl) (*Relay, error) {
	relay, ok := relayHostInfo.relayState.CompleteRelayByIdx(m.InitiatorRelayIndex, m.ResponderRelayIndex)
	if !ok {
		rm.l.WithFields(logrus.Fields{"relay": relayHostInfo.vpnIp,
			"initiatorRelayIndex": m.InitiatorRelayIndex,
			"relayFrom":           m.RelayFromIp,
			"relayTo":             m.RelayToIp}).Info("relayManager failed to update relay")
		return nil, fmt.Errorf("unknown relay")
	}

	return relay, nil
}

func (rm *relayManager) HandleControlMsg(h *HostInfo, m *NebulaControl, f *Interface) {

	switch m.Type {
	case NebulaControl_CreateRelayRequest:
		rm.handleCreateRelayRequest(h, f, m)
	case NebulaControl_CreateRelayResponse:
		rm.handleCreateRelayResponse(h, f, m)
	}

}

func (rm *relayManager) handleCreateRelayResponse(h *HostInfo, f *Interface, m *NebulaControl) {
	rm.l.WithFields(logrus.Fields{
		"relayFrom":           iputil.VpnIp(m.RelayFromIp),
		"relayTo":             iputil.VpnIp(m.RelayToIp),
		"initiatorRelayIndex": m.InitiatorRelayIndex,
		"responderRelayIndex": m.ResponderRelayIndex,
		"vpnIp":               h.vpnIp}).
		Info("handleCreateRelayResponse")
	target := iputil.VpnIp(m.RelayToIp)

	relay, err := rm.EstablishRelay(h, m)
	if err != nil {
		rm.l.WithError(err).Error("Failed to update relay for relayTo")
		return
	}
	// Do I need to complete the relays now?
	if relay.Type == TerminalType {
		return
	}
	// I'm the middle man. Let the initiator know that the I've established the relay they requested.
	peerHostInfo, err := rm.hostmap.QueryVpnIp(relay.PeerIp)
	if err != nil {
		rm.l.WithError(err).WithField("relayTo", relay.PeerIp).Error("Can't find a HostInfo for peer")
		return
	}
	peerRelay, ok := peerHostInfo.relayState.QueryRelayForByIp(target)
	if !ok {
		rm.l.WithField("relayTo", peerHostInfo.vpnIp).Error("peerRelay does not have Relay state for relayTo")
		return
	}
	peerRelay.State = Established
	resp := NebulaControl{
		Type:                NebulaControl_CreateRelayResponse,
		ResponderRelayIndex: peerRelay.LocalIndex,
		InitiatorRelayIndex: peerRelay.RemoteIndex,
		RelayFromIp:         uint32(peerHostInfo.vpnIp),
		RelayToIp:           uint32(target),
	}
	msg, err := resp.Marshal()
	if err != nil {
		rm.l.
			WithError(err).Error("relayManager Failed to marshal Control CreateRelayResponse message to create relay")
	} else {
		f.sendMessageToVpnIp(header.Control, 0, peerHostInfo, msg, make([]byte, 12), make([]byte, mtu))
		rm.l.WithFields(logrus.Fields{
			"relayFrom":           iputil.VpnIp(resp.RelayFromIp),
			"relayTo":             iputil.VpnIp(resp.RelayToIp),
			"initiatorRelayIndex": resp.InitiatorRelayIndex,
			"responderRelayIndex": resp.ResponderRelayIndex,
			"vpnIp":               peerHostInfo.vpnIp}).
			Info("send CreateRelayResponse")
	}
}

func (rm *relayManager) handleCreateRelayRequest(h *HostInfo, f *Interface, m *NebulaControl) {

	from := iputil.VpnIp(m.RelayFromIp)
	target := iputil.VpnIp(m.RelayToIp)

	logMsg := rm.l.WithFields(logrus.Fields{
		"relayFrom":           from,
		"relayTo":             target,
		"initiatorRelayIndex": m.InitiatorRelayIndex,
		"vpnIp":               h.vpnIp})

	logMsg.Info("handleCreateRelayRequest")
	// Is the target of the relay me?
	if target == f.myVpnIp {
		existingRelay, ok := h.relayState.QueryRelayForByIp(from)
		if ok {
			switch existingRelay.State {
			case Requested:
				ok = h.relayState.CompleteRelayByIP(from, m.InitiatorRelayIndex)
				if !ok {
					logMsg.Error("Relay State not found")
					return
				}
			case Established:
				if existingRelay.RemoteIndex != m.InitiatorRelayIndex {
					// We got a brand new Relay request, because its index is different than what we saw before.
					// This should never happen. The peer should never change an index, once created.
					logMsg.WithFields(logrus.Fields{
						"existingRemoteIndex": existingRelay.RemoteIndex}).Error("Existing relay mismatch with CreateRelayRequest")
					return
				}
			}
		} else {
			_, err := AddRelay(rm.l, h, f.hostMap, from, &m.InitiatorRelayIndex, TerminalType, Established)
			if err != nil {
				logMsg.WithError(err).Error("Failed to add relay")
				return
			}
		}

		relay, ok := h.relayState.QueryRelayForByIp(from)
		if !ok {
			logMsg.Error("Relay State not found")
			return
		}

		resp := NebulaControl{
			Type:                NebulaControl_CreateRelayResponse,
			ResponderRelayIndex: relay.LocalIndex,
			InitiatorRelayIndex: relay.RemoteIndex,
			RelayFromIp:         uint32(from),
			RelayToIp:           uint32(target),
		}
		msg, err := resp.Marshal()
		if err != nil {
			logMsg.
				WithError(err).Error("relayManager Failed to marshal Control CreateRelayResponse message to create relay")
		} else {
			f.sendMessageToVpnIp(header.Control, 0, h, msg, make([]byte, 12), make([]byte, mtu))
			rm.l.WithFields(logrus.Fields{
				"relayFrom":           iputil.VpnIp(resp.RelayFromIp),
				"relayTo":             iputil.VpnIp(resp.RelayToIp),
				"initiatorRelayIndex": resp.InitiatorRelayIndex,
				"responderRelayIndex": resp.ResponderRelayIndex,
				"vpnIp":               h.vpnIp}).
				Info("send CreateRelayResponse")
		}
		return
	} else {
		// the target is not me. Create a relay to the target, from me.
		if !rm.GetAmRelay() {
			return
		}
		peer, err := rm.hostmap.QueryVpnIp(target)
		if err != nil {
			// Try to establish a connection to this host. If we get a future relay request,
			// we'll be ready!
			f.getOrHandshake(target)
			return
		}
		if peer.remote == nil {
			// Only create relays to peers for whom I have a direct connection
			return
		}
		sendCreateRequest := false
		var index uint32
		targetRelay, ok := peer.relayState.QueryRelayForByIp(from)
		if ok {
			index = targetRelay.LocalIndex
			if targetRelay.State == Requested {
				sendCreateRequest = true
			}
		} else {
			// Allocate an index in the hostMap for this relay peer
			index, err = AddRelay(rm.l, peer, f.hostMap, from, nil, ForwardingType, Requested)
			if err != nil {
				return
			}
			sendCreateRequest = true
		}
		if sendCreateRequest {
			// Send a CreateRelayRequest to the peer.
			req := NebulaControl{
				Type:                NebulaControl_CreateRelayRequest,
				InitiatorRelayIndex: index,
				RelayFromIp:         uint32(h.vpnIp),
				RelayToIp:           uint32(target),
			}
			msg, err := req.Marshal()
			if err != nil {
				logMsg.
					WithError(err).Error("relayManager Failed to marshal Control message to create relay")
			} else {
				f.sendMessageToVpnIp(header.Control, 0, peer, msg, make([]byte, 12), make([]byte, mtu))
				rm.l.WithFields(logrus.Fields{
					"relayFrom":           iputil.VpnIp(req.RelayFromIp),
					"relayTo":             iputil.VpnIp(req.RelayToIp),
					"initiatorRelayIndex": req.InitiatorRelayIndex,
					"responderRelayIndex": req.ResponderRelayIndex,
					"vpnIp":               target}).
					Info("send CreateRelayRequest")
			}
		}
		// Also track the half-created Relay state just received
		relay, ok := h.relayState.QueryRelayForByIp(target)
		if !ok {
			// Add the relay
			state := Requested
			if targetRelay != nil && targetRelay.State == Established {
				state = Established
			}
			_, err := AddRelay(rm.l, h, f.hostMap, target, &m.InitiatorRelayIndex, ForwardingType, state)
			if err != nil {
				logMsg.
					WithError(err).Error("relayManager Failed to allocate a local index for relay")
				return
			}
		} else {
			switch relay.State {
			case Established:
				if relay.RemoteIndex != m.InitiatorRelayIndex {
					// We got a brand new Relay request, because its index is different than what we saw before.
					// This should never happen. The peer should never change an index, once created.
					logMsg.WithFields(logrus.Fields{
						"existingRemoteIndex": relay.RemoteIndex}).Error("Existing relay mismatch with CreateRelayRequest")
					return
				}
				resp := NebulaControl{
					Type:                NebulaControl_CreateRelayResponse,
					ResponderRelayIndex: relay.LocalIndex,
					InitiatorRelayIndex: relay.RemoteIndex,
					RelayFromIp:         uint32(h.vpnIp),
					RelayToIp:           uint32(target),
				}
				msg, err := resp.Marshal()
				if err != nil {
					rm.l.
						WithError(err).Error("relayManager Failed to marshal Control CreateRelayResponse message to create relay")
				} else {
					f.sendMessageToVpnIp(header.Control, 0, h, msg, make([]byte, 12), make([]byte, mtu))
					rm.l.WithFields(logrus.Fields{
						"relayFrom":           iputil.VpnIp(resp.RelayFromIp),
						"relayTo":             iputil.VpnIp(resp.RelayToIp),
						"initiatorRelayIndex": resp.InitiatorRelayIndex,
						"responderRelayIndex": resp.ResponderRelayIndex,
						"vpnIp":               h.vpnIp}).
						Info("send CreateRelayResponse")
				}

			case Requested:
				// Keep waiting for the other relay to complete
			}
		}
	}
}

func (rm *relayManager) RemoveRelay(localIdx uint32) {
	rm.hostmap.RemoveRelay(localIdx)
}
