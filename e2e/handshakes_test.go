//go:build e2e_testing
// +build e2e_testing

package e2e

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/e2e/router"
	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/udp"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
)

func BenchmarkHotPath(b *testing.B) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, _, _, _ := newSimpleServer(ca, caKey, "me", net.IP{10, 0, 0, 1}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 2}, nil)

	// Put their info in our lighthouse
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)

	// Start the servers
	myControl.Start()
	theirControl.Start()

	r := router.NewR(b, myControl, theirControl)
	r.CancelFlowLogs()

	for n := 0; n < b.N; n++ {
		myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))
		_ = r.RouteForAllUntilTxTun(theirControl)
	}

	myControl.Stop()
	theirControl.Stop()
}

func TestGoodHandshake(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me", net.IP{10, 0, 0, 1}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 2}, nil)

	// Put their info in our lighthouse
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)

	// Start the servers
	myControl.Start()
	theirControl.Start()

	t.Log("Send a udp packet through to begin standing up the tunnel, this should come out the other side")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))

	t.Log("Have them consume my stage 0 packet. They have a tunnel now")
	theirControl.InjectUDPPacket(myControl.GetFromUDP(true))

	t.Log("Get their stage 1 packet so that we can play with it")
	stage1Packet := theirControl.GetFromUDP(true)

	t.Log("I consume a garbage packet with a proper nebula header for our tunnel")
	// this should log a statement and get ignored, allowing the real handshake packet to complete the tunnel
	badPacket := stage1Packet.Copy()
	badPacket.Data = badPacket.Data[:len(badPacket.Data)-header.Len]
	myControl.InjectUDPPacket(badPacket)

	t.Log("Have me consume their real stage 1 packet. I have a tunnel now")
	myControl.InjectUDPPacket(stage1Packet)

	t.Log("Wait until we see my cached packet come through")
	myControl.WaitForType(1, 0, theirControl)

	t.Log("Make sure our host infos are correct")
	assertHostInfoPair(t, myUdpAddr, theirUdpAddr, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl)

	t.Log("Get that cached packet and make sure it looks right")
	myCachedPacket := theirControl.GetFromTun(true)
	assertUdpPacket(t, []byte("Hi from me"), myCachedPacket, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)

	t.Log("Do a bidirectional tunnel test")
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)
	myControl.Stop()
	theirControl.Stop()
	//TODO: assert hostmaps
}

func TestWrongResponderHandshake(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})

	// The IPs here are chosen on purpose:
	// The current remote handling will sort by preference, public, and then lexically.
	// So we need them to have a higher address than evil (we could apply a preference though)
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me", net.IP{10, 0, 0, 100}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 99}, nil)
	evilControl, evilVpnIp, evilUdpAddr, _ := newSimpleServer(ca, caKey, "evil", net.IP{10, 0, 0, 2}, nil)

	// Add their real udp addr, which should be tried after evil.
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)

	// Put the evil udp addr in for their vpn Ip, this is a case of being lied to by the lighthouse.
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, evilUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl, evilControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()
	evilControl.Start()

	t.Log("Start the handshake process, we will route until we see our cached packet get sent to them")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))
	r.RouteForAllExitFunc(func(p *udp.Packet, c *nebula.Control) router.ExitType {
		h := &header.H{}
		err := h.Parse(p.Data)
		if err != nil {
			panic(err)
		}

		if p.ToIp.Equal(theirUdpAddr.IP) && p.ToPort == uint16(theirUdpAddr.Port) && h.Type == 1 {
			return router.RouteAndExit
		}

		return router.KeepRouting
	})

	//TODO: Assert pending hostmap - I should have a correct hostinfo for them now

	t.Log("My cached packet should be received by them")
	myCachedPacket := theirControl.GetFromTun(true)
	assertUdpPacket(t, []byte("Hi from me"), myCachedPacket, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)

	t.Log("Test the tunnel with them")
	assertHostInfoPair(t, myUdpAddr, theirUdpAddr, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl)
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	t.Log("Flush all packets from all controllers")
	r.FlushAll()

	t.Log("Ensure ensure I don't have any hostinfo artifacts from evil")
	assert.Nil(t, myControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(evilVpnIp.IP), true), "My pending hostmap should not contain evil")
	assert.Nil(t, myControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(evilVpnIp.IP), false), "My main hostmap should not contain evil")
	//NOTE: if evil lost the handshake race it may still have a tunnel since me would reject the handshake since the tunnel is complete

	//TODO: assert hostmaps for everyone
	r.RenderHostmaps("Final hostmaps", myControl, theirControl, evilControl)
	t.Log("Success!")
	myControl.Stop()
	theirControl.Stop()
}

func TestStage1Race(t *testing.T) {
	// This tests ensures that two hosts handshaking with each other at the same time will allow traffic to flow
	// But will eventually collapse down to a single tunnel

	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me  ", net.IP{10, 0, 0, 1}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 2}, nil)

	// Put their info in our lighthouse and vice versa
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	theirControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()

	t.Log("Trigger a handshake to start on both me and them")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))
	theirControl.InjectTunUDPPacket(myVpnIpNet.IP, 80, 80, []byte("Hi from them"))

	t.Log("Get both stage 1 handshake packets")
	myHsForThem := myControl.GetFromUDP(true)
	theirHsForMe := theirControl.GetFromUDP(true)

	r.Log("Now inject both stage 1 handshake packets")
	r.InjectUDPPacket(theirControl, myControl, theirHsForMe)
	r.InjectUDPPacket(myControl, theirControl, myHsForThem)

	r.Log("Route until they receive a message packet")
	myCachedPacket := r.RouteForAllUntilTxTun(theirControl)
	assertUdpPacket(t, []byte("Hi from me"), myCachedPacket, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)

	r.Log("Their cached packet should be received by me")
	theirCachedPacket := r.RouteForAllUntilTxTun(myControl)
	assertUdpPacket(t, []byte("Hi from them"), theirCachedPacket, theirVpnIpNet.IP, myVpnIpNet.IP, 80, 80)

	r.Log("Do a bidirectional tunnel test")
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	myHostmapHosts := myControl.ListHostmapHosts(false)
	myHostmapIndexes := myControl.ListHostmapIndexes(false)
	theirHostmapHosts := theirControl.ListHostmapHosts(false)
	theirHostmapIndexes := theirControl.ListHostmapIndexes(false)

	// We should have two tunnels on both sides
	assert.Len(t, myHostmapHosts, 1)
	assert.Len(t, theirHostmapHosts, 1)
	assert.Len(t, myHostmapIndexes, 2)
	assert.Len(t, theirHostmapIndexes, 2)

	r.RenderHostmaps("Starting hostmaps", myControl, theirControl)

	r.Log("Spin until connection manager tears down a tunnel")

	for len(myControl.GetHostmap().Indexes)+len(theirControl.GetHostmap().Indexes) > 2 {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		t.Log("Connection manager hasn't ticked yet")
		time.Sleep(time.Second)
	}

	myFinalHostmapHosts := myControl.ListHostmapHosts(false)
	myFinalHostmapIndexes := myControl.ListHostmapIndexes(false)
	theirFinalHostmapHosts := theirControl.ListHostmapHosts(false)
	theirFinalHostmapIndexes := theirControl.ListHostmapIndexes(false)

	// We should only have a single tunnel now on both sides
	assert.Len(t, myFinalHostmapHosts, 1)
	assert.Len(t, theirFinalHostmapHosts, 1)
	assert.Len(t, myFinalHostmapIndexes, 1)
	assert.Len(t, theirFinalHostmapIndexes, 1)

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)
	myControl.Stop()
	theirControl.Stop()
}

func TestUncleanShutdownRaceLoser(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me  ", net.IP{10, 0, 0, 1}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 2}, nil)

	// Teach my how to get to the relay and that their can be reached via the relay
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	theirControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()

	r.Log("Trigger a handshake from me to them")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))

	p := r.RouteForAllUntilTxTun(theirControl)
	assertUdpPacket(t, []byte("Hi from me"), p, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)

	r.Log("Nuke my hostmap")
	myHostmap := myControl.GetHostmap()
	myHostmap.Hosts = map[iputil.VpnIp]*nebula.HostInfo{}
	myHostmap.Indexes = map[uint32]*nebula.HostInfo{}
	myHostmap.RemoteIndexes = map[uint32]*nebula.HostInfo{}

	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me again"))
	p = r.RouteForAllUntilTxTun(theirControl)
	assertUdpPacket(t, []byte("Hi from me again"), p, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)

	r.Log("Assert the tunnel works")
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	r.Log("Wait for the dead index to go away")
	start := len(theirControl.GetHostmap().Indexes)
	for {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		if len(theirControl.GetHostmap().Indexes) < start {
			break
		}
		time.Sleep(time.Second)
	}

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)
}

func TestUncleanShutdownRaceWinner(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me  ", net.IP{10, 0, 0, 1}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 2}, nil)

	// Teach my how to get to the relay and that their can be reached via the relay
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	theirControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()

	r.Log("Trigger a handshake from me to them")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))

	p := r.RouteForAllUntilTxTun(theirControl)
	assertUdpPacket(t, []byte("Hi from me"), p, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)
	r.RenderHostmaps("Final hostmaps", myControl, theirControl)

	r.Log("Nuke my hostmap")
	theirHostmap := theirControl.GetHostmap()
	theirHostmap.Hosts = map[iputil.VpnIp]*nebula.HostInfo{}
	theirHostmap.Indexes = map[uint32]*nebula.HostInfo{}
	theirHostmap.RemoteIndexes = map[uint32]*nebula.HostInfo{}

	theirControl.InjectTunUDPPacket(myVpnIpNet.IP, 80, 80, []byte("Hi from them again"))
	p = r.RouteForAllUntilTxTun(myControl)
	assertUdpPacket(t, []byte("Hi from them again"), p, theirVpnIpNet.IP, myVpnIpNet.IP, 80, 80)
	r.RenderHostmaps("Derp hostmaps", myControl, theirControl)

	r.Log("Assert the tunnel works")
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	r.Log("Wait for the dead index to go away")
	start := len(myControl.GetHostmap().Indexes)
	for {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		if len(myControl.GetHostmap().Indexes) < start {
			break
		}
		time.Sleep(time.Second)
	}

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)
}

func TestRelays(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, _, _ := newSimpleServer(ca, caKey, "me     ", net.IP{10, 0, 0, 1}, m{"relay": m{"use_relays": true}})
	relayControl, relayVpnIpNet, relayUdpAddr, _ := newSimpleServer(ca, caKey, "relay  ", net.IP{10, 0, 0, 128}, m{"relay": m{"am_relay": true}})
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them   ", net.IP{10, 0, 0, 2}, m{"relay": m{"use_relays": true}})

	// Teach my how to get to the relay and that their can be reached via the relay
	myControl.InjectLightHouseAddr(relayVpnIpNet.IP, relayUdpAddr)
	myControl.InjectRelays(theirVpnIpNet.IP, []net.IP{relayVpnIpNet.IP})
	relayControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, relayControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	relayControl.Start()
	theirControl.Start()

	t.Log("Trigger a handshake from me to them via the relay")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))

	p := r.RouteForAllUntilTxTun(theirControl)
	r.Log("Assert the tunnel works")
	assertUdpPacket(t, []byte("Hi from me"), p, myVpnIpNet.IP, theirVpnIpNet.IP, 80, 80)
	r.RenderHostmaps("Final hostmaps", myControl, relayControl, theirControl)
	//TODO: assert we actually used the relay even though it should be impossible for a tunnel to have occurred without it
}

func TestStage1RaceRelays(t *testing.T) {
	//NOTE: this is a race between me and relay resulting in a full tunnel from me to them via relay
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me     ", net.IP{10, 0, 0, 1}, m{"relay": m{"use_relays": true}})
	relayControl, relayVpnIpNet, relayUdpAddr, _ := newSimpleServer(ca, caKey, "relay  ", net.IP{10, 0, 0, 128}, m{"relay": m{"am_relay": true}})
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them   ", net.IP{10, 0, 0, 2}, m{"relay": m{"use_relays": true}})

	// Teach my how to get to the relay and that their can be reached via the relay
	myControl.InjectLightHouseAddr(relayVpnIpNet.IP, relayUdpAddr)
	theirControl.InjectLightHouseAddr(relayVpnIpNet.IP, relayUdpAddr)

	myControl.InjectRelays(theirVpnIpNet.IP, []net.IP{relayVpnIpNet.IP})
	theirControl.InjectRelays(myVpnIpNet.IP, []net.IP{relayVpnIpNet.IP})

	relayControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	relayControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, relayControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	relayControl.Start()
	theirControl.Start()

	r.Log("Get a tunnel between me and relay")
	assertTunnel(t, myVpnIpNet.IP, relayVpnIpNet.IP, myControl, relayControl, r)

	r.Log("Get a tunnel between them and relay")
	assertTunnel(t, theirVpnIpNet.IP, relayVpnIpNet.IP, theirControl, relayControl, r)

	r.Log("Trigger a handshake from both them and me via relay to them and me")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))
	theirControl.InjectTunUDPPacket(myVpnIpNet.IP, 80, 80, []byte("Hi from them"))

	r.Log("Wait for a packet from them to me")
	p := r.RouteForAllUntilTxTun(myControl)
	_ = p

	myControl.Stop()
	theirControl.Stop()
	relayControl.Stop()
	//
	////TODO: assert hostmaps
}

func TestStage1RaceRelays2(t *testing.T) {
	//NOTE: this is a race between me and relay resulting in a full tunnel from me to them via relay
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, _ := newSimpleServer(ca, caKey, "me     ", net.IP{10, 0, 0, 1}, m{"relay": m{"use_relays": true}})
	relayControl, relayVpnIpNet, relayUdpAddr, _ := newSimpleServer(ca, caKey, "relay  ", net.IP{10, 0, 0, 128}, m{"relay": m{"am_relay": true}})
	theirControl, theirVpnIpNet, theirUdpAddr, _ := newSimpleServer(ca, caKey, "them   ", net.IP{10, 0, 0, 2}, m{"relay": m{"use_relays": true}})
	l := NewTestLogger()

	// Teach my how to get to the relay and that their can be reached via the relay
	myControl.InjectLightHouseAddr(relayVpnIpNet.IP, relayUdpAddr)
	theirControl.InjectLightHouseAddr(relayVpnIpNet.IP, relayUdpAddr)

	myControl.InjectRelays(theirVpnIpNet.IP, []net.IP{relayVpnIpNet.IP})
	theirControl.InjectRelays(myVpnIpNet.IP, []net.IP{relayVpnIpNet.IP})

	relayControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	relayControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, relayControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	relayControl.Start()
	theirControl.Start()

	r.Log("Get a tunnel between me and relay")
	l.Info("Get a tunnel between me and relay")
	assertTunnel(t, myVpnIpNet.IP, relayVpnIpNet.IP, myControl, relayControl, r)

	r.Log("Get a tunnel between them and relay")
	l.Info("Get a tunnel between them and relay")
	assertTunnel(t, theirVpnIpNet.IP, relayVpnIpNet.IP, theirControl, relayControl, r)

	r.Log("Trigger a handshake from both them and me via relay to them and me")
	l.Info("Trigger a handshake from both them and me via relay to them and me")
	myControl.InjectTunUDPPacket(theirVpnIpNet.IP, 80, 80, []byte("Hi from me"))
	theirControl.InjectTunUDPPacket(myVpnIpNet.IP, 80, 80, []byte("Hi from them"))

	//r.RouteUntilAfterMsgType(myControl, header.Control, header.MessageNone)
	//r.RouteUntilAfterMsgType(theirControl, header.Control, header.MessageNone)

	r.Log("Wait for a packet from them to me")
	l.Info("Wait for a packet from them to me; myControl")
	r.RouteForAllUntilTxTun(myControl)
	l.Info("Wait for a packet from them to me; theirControl")
	r.RouteForAllUntilTxTun(theirControl)

	r.Log("Assert the tunnel works")
	l.Info("Assert the tunnel works")
	assertTunnel(t, theirVpnIpNet.IP, myVpnIpNet.IP, theirControl, myControl, r)

	t.Log("Wait until we remove extra tunnels")
	l.Info("Wait until we remove extra tunnels")
	l.WithFields(
		logrus.Fields{
			"myControl":    len(myControl.GetHostmap().Indexes),
			"theirControl": len(theirControl.GetHostmap().Indexes),
			"relayControl": len(relayControl.GetHostmap().Indexes),
		}).Info("Waiting for hostinfos to be removed...")
	hostInfos := len(myControl.GetHostmap().Indexes) + len(theirControl.GetHostmap().Indexes) + len(relayControl.GetHostmap().Indexes)
	retries := 60
	for hostInfos > 6 && retries > 0 {
		hostInfos = len(myControl.GetHostmap().Indexes) + len(theirControl.GetHostmap().Indexes) + len(relayControl.GetHostmap().Indexes)
		l.WithFields(
			logrus.Fields{
				"myControl":    len(myControl.GetHostmap().Indexes),
				"theirControl": len(theirControl.GetHostmap().Indexes),
				"relayControl": len(relayControl.GetHostmap().Indexes),
			}).Info("Waiting for hostinfos to be removed...")
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		t.Log("Connection manager hasn't ticked yet")
		time.Sleep(time.Second)
		retries--
	}

	r.Log("Assert the tunnel works")
	l.Info("Assert the tunnel works")
	assertTunnel(t, theirVpnIpNet.IP, myVpnIpNet.IP, theirControl, myControl, r)

	myControl.Stop()
	theirControl.Stop()
	relayControl.Stop()

	//
	////TODO: assert hostmaps
}

func TestRehandshaking(t *testing.T) {
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, myConfig := newSimpleServer(ca, caKey, "me  ", net.IP{10, 0, 0, 2}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, theirConfig := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 1}, nil)

	// Put their info in our lighthouse and vice versa
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	theirControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()

	t.Log("Stand up a tunnel between me and them")
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	r.RenderHostmaps("Starting hostmaps", myControl, theirControl)

	r.Log("Renew my certificate and spin until their sees it")
	_, _, myNextPrivKey, myNextPEM := newTestCert(ca, caKey, "me", time.Now(), time.Now().Add(5*time.Minute), myVpnIpNet, nil, []string{"new group"})

	caB, err := ca.MarshalToPEM()
	if err != nil {
		panic(err)
	}

	myConfig.Settings["pki"] = m{
		"ca":   string(caB),
		"cert": string(myNextPEM),
		"key":  string(myNextPrivKey),
	}
	rc, err := yaml.Marshal(myConfig.Settings)
	assert.NoError(t, err)
	myConfig.ReloadConfigString(string(rc))

	for {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		c := theirControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(myVpnIpNet.IP), false)
		if len(c.Cert.Details.Groups) != 0 {
			// We have a new certificate now
			break
		}

		time.Sleep(time.Second)
	}

	// Flip their firewall to only allowing the new group to catch the tunnels reverting incorrectly
	rc, err = yaml.Marshal(theirConfig.Settings)
	assert.NoError(t, err)
	var theirNewConfig m
	assert.NoError(t, yaml.Unmarshal(rc, &theirNewConfig))
	theirFirewall := theirNewConfig["firewall"].(map[interface{}]interface{})
	theirFirewall["inbound"] = []m{{
		"proto": "any",
		"port":  "any",
		"group": "new group",
	}}
	rc, err = yaml.Marshal(theirNewConfig)
	assert.NoError(t, err)
	theirConfig.ReloadConfigString(string(rc))

	r.Log("Spin until there is only 1 tunnel")
	for len(myControl.GetHostmap().Indexes)+len(theirControl.GetHostmap().Indexes) > 2 {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		t.Log("Connection manager hasn't ticked yet")
		time.Sleep(time.Second)
	}

	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
	myFinalHostmapHosts := myControl.ListHostmapHosts(false)
	myFinalHostmapIndexes := myControl.ListHostmapIndexes(false)
	theirFinalHostmapHosts := theirControl.ListHostmapHosts(false)
	theirFinalHostmapIndexes := theirControl.ListHostmapIndexes(false)

	// Make sure the correct tunnel won
	c := theirControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(myVpnIpNet.IP), false)
	assert.Contains(t, c.Cert.Details.Groups, "new group")

	// We should only have a single tunnel now on both sides
	assert.Len(t, myFinalHostmapHosts, 1)
	assert.Len(t, theirFinalHostmapHosts, 1)
	assert.Len(t, myFinalHostmapIndexes, 1)
	assert.Len(t, theirFinalHostmapIndexes, 1)

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)

	myControl.Stop()
	theirControl.Stop()
}

func TestRehandshakingLoser(t *testing.T) {
	// The purpose of this test is that the race loser renews their certificate and rehandshakes. The final tunnel
	// Should be the one with the new certificate
	ca, _, caKey, _ := newTestCaCert(time.Now(), time.Now().Add(10*time.Minute), []*net.IPNet{}, []*net.IPNet{}, []string{})
	myControl, myVpnIpNet, myUdpAddr, myConfig := newSimpleServer(ca, caKey, "me  ", net.IP{10, 0, 0, 2}, nil)
	theirControl, theirVpnIpNet, theirUdpAddr, theirConfig := newSimpleServer(ca, caKey, "them", net.IP{10, 0, 0, 1}, nil)

	// Put their info in our lighthouse and vice versa
	myControl.InjectLightHouseAddr(theirVpnIpNet.IP, theirUdpAddr)
	theirControl.InjectLightHouseAddr(myVpnIpNet.IP, myUdpAddr)

	// Build a router so we don't have to reason who gets which packet
	r := router.NewR(t, myControl, theirControl)
	defer r.RenderFlow()

	// Start the servers
	myControl.Start()
	theirControl.Start()

	t.Log("Stand up a tunnel between me and them")
	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)

	tt1 := myControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(theirVpnIpNet.IP), false)
	tt2 := theirControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(myVpnIpNet.IP), false)
	fmt.Println(tt1.LocalIndex, tt2.LocalIndex)

	r.RenderHostmaps("Starting hostmaps", myControl, theirControl)

	r.Log("Renew their certificate and spin until mine sees it")
	_, _, theirNextPrivKey, theirNextPEM := newTestCert(ca, caKey, "them", time.Now(), time.Now().Add(5*time.Minute), theirVpnIpNet, nil, []string{"their new group"})

	caB, err := ca.MarshalToPEM()
	if err != nil {
		panic(err)
	}

	theirConfig.Settings["pki"] = m{
		"ca":   string(caB),
		"cert": string(theirNextPEM),
		"key":  string(theirNextPrivKey),
	}
	rc, err := yaml.Marshal(theirConfig.Settings)
	assert.NoError(t, err)
	theirConfig.ReloadConfigString(string(rc))

	for {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		theirCertInMe := myControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(theirVpnIpNet.IP), false)

		_, theirNewGroup := theirCertInMe.Cert.Details.InvertedGroups["their new group"]
		if theirNewGroup {
			break
		}

		time.Sleep(time.Second)
	}

	// Flip my firewall to only allowing the new group to catch the tunnels reverting incorrectly
	rc, err = yaml.Marshal(myConfig.Settings)
	assert.NoError(t, err)
	var myNewConfig m
	assert.NoError(t, yaml.Unmarshal(rc, &myNewConfig))
	theirFirewall := myNewConfig["firewall"].(map[interface{}]interface{})
	theirFirewall["inbound"] = []m{{
		"proto": "any",
		"port":  "any",
		"group": "their new group",
	}}
	rc, err = yaml.Marshal(myNewConfig)
	assert.NoError(t, err)
	myConfig.ReloadConfigString(string(rc))

	r.Log("Spin until there is only 1 tunnel")
	for len(myControl.GetHostmap().Indexes)+len(theirControl.GetHostmap().Indexes) > 2 {
		assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
		t.Log("Connection manager hasn't ticked yet")
		time.Sleep(time.Second)
	}

	assertTunnel(t, myVpnIpNet.IP, theirVpnIpNet.IP, myControl, theirControl, r)
	myFinalHostmapHosts := myControl.ListHostmapHosts(false)
	myFinalHostmapIndexes := myControl.ListHostmapIndexes(false)
	theirFinalHostmapHosts := theirControl.ListHostmapHosts(false)
	theirFinalHostmapIndexes := theirControl.ListHostmapIndexes(false)

	// Make sure the correct tunnel won
	theirCertInMe := myControl.GetHostInfoByVpnIp(iputil.Ip2VpnIp(theirVpnIpNet.IP), false)
	assert.Contains(t, theirCertInMe.Cert.Details.Groups, "their new group")

	// We should only have a single tunnel now on both sides
	assert.Len(t, myFinalHostmapHosts, 1)
	assert.Len(t, theirFinalHostmapHosts, 1)
	assert.Len(t, myFinalHostmapIndexes, 1)
	assert.Len(t, theirFinalHostmapIndexes, 1)

	r.RenderHostmaps("Final hostmaps", myControl, theirControl)
	myControl.Stop()
	theirControl.Stop()
}

//TODO: test
// Race winner renews and handshakes
// Race loser renews and handshakes
// Does race winner repin the cert to old?
//TODO: add a test with many lies
