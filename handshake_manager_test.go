package nebula

import (
	"net"
	"testing"
	"time"

	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/test"
	"github.com/slackhq/nebula/udp"
	"github.com/stretchr/testify/assert"
)

func Test_NewHandshakeManagerVpnIp(t *testing.T) {
	l := test.NewLogger()
	_, tuncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, vpncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, localrange, _ := net.ParseCIDR("10.1.1.1/24")
	ip := iputil.Ip2VpnIp(net.ParseIP("172.1.1.2"))
	preferredRanges := []*net.IPNet{localrange}
	mw := &mockEncWriter{}
	mainHM := NewHostMap(l, "test", vpncidr, preferredRanges)
	lh := &LightHouse{
		atomicStaticList:  make(map[iputil.VpnIp]struct{}),
		atomicLighthouses: make(map[iputil.VpnIp]struct{}),
	}

	blah := NewHandshakeManager(l, tuncidr, preferredRanges, mainHM, lh, &udp.Conn{}, defaultHandshakeConfig)

	now := time.Now()
	blah.NextOutboundHandshakeTimerTick(now, mw)

	var initCalled bool
	initFunc := func(*HostInfo) {
		initCalled = true
	}

	i := blah.AddVpnIp(ip, initFunc)
	assert.True(t, initCalled)

	initCalled = false
	i2 := blah.AddVpnIp(ip, initFunc)
	assert.False(t, initCalled)
	assert.Same(t, i, i2)

	i.remotes = NewRemoteList()
	i.HandshakeReady = true

	// Adding something to pending should not affect the main hostmap
	assert.Len(t, mainHM.Hosts, 0)

	// Confirm they are in the pending index list
	assert.Contains(t, blah.pendingHostMap.Hosts, ip)

	// Jump ahead `HandshakeRetries` ticks, offset by one to get the sleep logic right
	for i := 1; i <= DefaultHandshakeRetries+1; i++ {
		now = now.Add(time.Duration(i) * DefaultHandshakeTryInterval)
		blah.NextOutboundHandshakeTimerTick(now, mw)
	}

	// Confirm they are still in the pending index list
	assert.Contains(t, blah.pendingHostMap.Hosts, ip)

	// Tick 1 more time, a minute will certainly flush it out
	blah.NextOutboundHandshakeTimerTick(now.Add(time.Minute), mw)

	// Confirm they have been removed
	assert.NotContains(t, blah.pendingHostMap.Hosts, ip)
}

func Test_NewHandshakeManagerTrigger(t *testing.T) {
	l := test.NewLogger()
	_, tuncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, vpncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, localrange, _ := net.ParseCIDR("10.1.1.1/24")
	ip := iputil.Ip2VpnIp(net.ParseIP("172.1.1.2"))
	preferredRanges := []*net.IPNet{localrange}
	mw := &mockEncWriter{}
	mainHM := NewHostMap(l, "test", vpncidr, preferredRanges)
	lh := &LightHouse{
		addrMap:           make(map[iputil.VpnIp]*RemoteList),
		l:                 l,
		atomicStaticList:  make(map[iputil.VpnIp]struct{}),
		atomicLighthouses: make(map[iputil.VpnIp]struct{}),
	}

	blah := NewHandshakeManager(l, tuncidr, preferredRanges, mainHM, lh, &udp.Conn{}, defaultHandshakeConfig)

	now := time.Now()
	blah.NextOutboundHandshakeTimerTick(now, mw)

	assert.Equal(t, 0, testCountTimerWheelEntries(blah.OutboundHandshakeTimer))

	hi := blah.AddVpnIp(ip, nil)
	hi.HandshakeReady = true
	assert.Equal(t, 1, testCountTimerWheelEntries(blah.OutboundHandshakeTimer))
	assert.Equal(t, 0, hi.HandshakeCounter, "Should not have attempted a handshake yet")

	// Trigger the same method the channel will but, this should set our remotes pointer
	blah.handleOutbound(ip, mw, true)
	assert.Equal(t, 1, hi.HandshakeCounter, "Trigger should have done a handshake attempt")
	assert.NotNil(t, hi.remotes, "Manager should have set my remotes pointer")

	// Make sure the trigger doesn't double schedule the timer entry
	assert.Equal(t, 1, testCountTimerWheelEntries(blah.OutboundHandshakeTimer))

	uaddr := udp.NewAddrFromString("10.1.1.1:4242")
	hi.remotes.unlockedPrependV4(ip, NewIp4AndPort(uaddr.IP, uint32(uaddr.Port)))

	// We now have remotes but only the first trigger should have pushed things forward
	blah.handleOutbound(ip, mw, true)
	assert.Equal(t, 1, hi.HandshakeCounter, "Trigger should have not done a handshake attempt")
	assert.Equal(t, 1, testCountTimerWheelEntries(blah.OutboundHandshakeTimer))
}

func testCountTimerWheelEntries(tw *SystemTimerWheel) (c int) {
	for _, i := range tw.wheel {
		n := i.Head
		for n != nil {
			c++
			n = n.Next
		}
	}
	return c
}

type mockEncWriter struct {
}

func (mw *mockEncWriter) SendMessageToVpnIp(t header.MessageType, st header.MessageSubType, vpnIp iputil.VpnIp, p, nb, out []byte) {
	return
}
