package nebula

import (
	"fmt"
	"net"
	"testing"

	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/header"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/test"
	"github.com/slackhq/nebula/udp"
	"github.com/stretchr/testify/assert"
)

//TODO: Add a test to ensure udpAddr is copied and not reused

func TestOldIPv4Only(t *testing.T) {
	// This test ensures our new ipv6 enabled LH protobuf IpAndPorts works with the old style to enable backwards compatibility
	b := []byte{8, 129, 130, 132, 80, 16, 10}
	var m Ip4AndPort
	err := m.Unmarshal(b)
	assert.NoError(t, err)
	assert.Equal(t, "10.1.1.1", iputil.VpnIp(m.GetIp()).String())
}

func TestNewLhQuery(t *testing.T) {
	myIp := net.ParseIP("192.1.1.1")
	myIpint := iputil.Ip2VpnIp(myIp)

	// Generating a new lh query should work
	a := NewLhQueryByInt(myIpint)

	// The result should be a nebulameta protobuf
	assert.IsType(t, &NebulaMeta{}, a)

	// It should also Marshal fine
	b, err := a.Marshal()
	assert.Nil(t, err)

	// and then Unmarshal fine
	n := &NebulaMeta{}
	err = n.Unmarshal(b)
	assert.Nil(t, err)

}

func Test_lhStaticMapping(t *testing.T) {
	l := test.NewLogger()
	_, myVpnNet, _ := net.ParseCIDR("10.128.0.1/16")
	lh1 := "10.128.0.2"

	c := config.NewC(l)
	c.Settings["lighthouse"] = map[interface{}]interface{}{"hosts": []interface{}{lh1}}
	c.Settings["static_host_map"] = map[interface{}]interface{}{lh1: []interface{}{"1.1.1.1:4242"}}
	_, err := NewLightHouseFromConfig(l, c, myVpnNet, nil, nil)
	assert.Nil(t, err)

	lh2 := "10.128.0.3"
	c = config.NewC(l)
	c.Settings["lighthouse"] = map[interface{}]interface{}{"hosts": []interface{}{lh1, lh2}}
	c.Settings["static_host_map"] = map[interface{}]interface{}{lh1: []interface{}{"100.1.1.1:4242"}}
	_, err = NewLightHouseFromConfig(l, c, myVpnNet, nil, nil)
	assert.EqualError(t, err, "lighthouse 10.128.0.3 does not have a static_host_map entry")
}

func BenchmarkLighthouseHandleRequest(b *testing.B) {
	l := test.NewLogger()
	_, myVpnNet, _ := net.ParseCIDR("10.128.0.1/0")

	c := config.NewC(l)
	lh, err := NewLightHouseFromConfig(l, c, myVpnNet, nil, nil)
	if !assert.NoError(b, err) {
		b.Fatal()
	}

	hAddr := udp.NewAddrFromString("4.5.6.7:12345")
	hAddr2 := udp.NewAddrFromString("4.5.6.7:12346")
	lh.addrMap[3] = NewRemoteList()
	lh.addrMap[3].unlockedSetV4(
		3,
		3,
		[]*Ip4AndPort{
			NewIp4AndPort(hAddr.IP, uint32(hAddr.Port)),
			NewIp4AndPort(hAddr2.IP, uint32(hAddr2.Port)),
		},
		func(iputil.VpnIp, *Ip4AndPort) bool { return true },
	)

	rAddr := udp.NewAddrFromString("1.2.2.3:12345")
	rAddr2 := udp.NewAddrFromString("1.2.2.3:12346")
	lh.addrMap[2] = NewRemoteList()
	lh.addrMap[2].unlockedSetV4(
		3,
		3,
		[]*Ip4AndPort{
			NewIp4AndPort(rAddr.IP, uint32(rAddr.Port)),
			NewIp4AndPort(rAddr2.IP, uint32(rAddr2.Port)),
		},
		func(iputil.VpnIp, *Ip4AndPort) bool { return true },
	)

	mw := &mockEncWriter{}

	b.Run("notfound", func(b *testing.B) {
		lhh := lh.NewRequestHandler()
		req := &NebulaMeta{
			Type: NebulaMeta_HostQuery,
			Details: &NebulaMetaDetails{
				VpnIp:       4,
				Ip4AndPorts: nil,
			},
		}
		p, err := req.Marshal()
		assert.NoError(b, err)
		for n := 0; n < b.N; n++ {
			lhh.HandleRequest(rAddr, 2, p, mw)
		}
	})
	b.Run("found", func(b *testing.B) {
		lhh := lh.NewRequestHandler()
		req := &NebulaMeta{
			Type: NebulaMeta_HostQuery,
			Details: &NebulaMetaDetails{
				VpnIp:       3,
				Ip4AndPorts: nil,
			},
		}
		p, err := req.Marshal()
		assert.NoError(b, err)

		for n := 0; n < b.N; n++ {
			lhh.HandleRequest(rAddr, 2, p, mw)
		}
	})
}

func TestLighthouse_Memory(t *testing.T) {
	l := test.NewLogger()

	myUdpAddr0 := &udp.Addr{IP: net.ParseIP("10.0.0.2"), Port: 4242}
	myUdpAddr1 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4242}
	myUdpAddr2 := &udp.Addr{IP: net.ParseIP("172.16.0.2"), Port: 4242}
	myUdpAddr3 := &udp.Addr{IP: net.ParseIP("100.152.0.2"), Port: 4242}
	myUdpAddr4 := &udp.Addr{IP: net.ParseIP("24.15.0.2"), Port: 4242}
	myUdpAddr5 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4243}
	myUdpAddr6 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4244}
	myUdpAddr7 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4245}
	myUdpAddr8 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4246}
	myUdpAddr9 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4247}
	myUdpAddr10 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4248}
	myUdpAddr11 := &udp.Addr{IP: net.ParseIP("192.168.0.2"), Port: 4249}
	myVpnIp := iputil.Ip2VpnIp(net.ParseIP("10.128.0.2"))

	theirUdpAddr0 := &udp.Addr{IP: net.ParseIP("10.0.0.3"), Port: 4242}
	theirUdpAddr1 := &udp.Addr{IP: net.ParseIP("192.168.0.3"), Port: 4242}
	theirUdpAddr2 := &udp.Addr{IP: net.ParseIP("172.16.0.3"), Port: 4242}
	theirUdpAddr3 := &udp.Addr{IP: net.ParseIP("100.152.0.3"), Port: 4242}
	theirUdpAddr4 := &udp.Addr{IP: net.ParseIP("24.15.0.3"), Port: 4242}
	theirVpnIp := iputil.Ip2VpnIp(net.ParseIP("10.128.0.3"))

	c := config.NewC(l)
	c.Settings["lighthouse"] = map[interface{}]interface{}{"am_lighthouse": true}
	c.Settings["listen"] = map[interface{}]interface{}{"port": 4242}
	lh, err := NewLightHouseFromConfig(l, c, &net.IPNet{IP: net.IP{10, 128, 0, 1}, Mask: net.IPMask{255, 255, 255, 0}}, nil, nil)
	assert.NoError(t, err)
	lhh := lh.NewRequestHandler()

	// Test that my first update responds with just that
	newLHHostUpdate(myUdpAddr0, myVpnIp, []*udp.Addr{myUdpAddr1, myUdpAddr2}, lhh)
	r := newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, myUdpAddr1, myUdpAddr2)

	// Ensure we don't accumulate addresses
	newLHHostUpdate(myUdpAddr0, myVpnIp, []*udp.Addr{myUdpAddr3}, lhh)
	r = newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, myUdpAddr3)

	// Grow it back to 2
	newLHHostUpdate(myUdpAddr0, myVpnIp, []*udp.Addr{myUdpAddr1, myUdpAddr4}, lhh)
	r = newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, myUdpAddr1, myUdpAddr4)

	// Update a different host and ask about it
	newLHHostUpdate(theirUdpAddr0, theirVpnIp, []*udp.Addr{theirUdpAddr1, theirUdpAddr2, theirUdpAddr3, theirUdpAddr4}, lhh)
	r = newLHHostRequest(theirUdpAddr0, theirVpnIp, theirVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, theirUdpAddr1, theirUdpAddr2, theirUdpAddr3, theirUdpAddr4)

	// Have both hosts ask about the other
	r = newLHHostRequest(theirUdpAddr0, theirVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, myUdpAddr1, myUdpAddr4)

	r = newLHHostRequest(myUdpAddr0, myVpnIp, theirVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, theirUdpAddr1, theirUdpAddr2, theirUdpAddr3, theirUdpAddr4)

	// Make sure we didn't get changed
	r = newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, myUdpAddr1, myUdpAddr4)

	// Ensure proper ordering and limiting
	// Send 12 addrs, get 10 back, the last 2 removed, allowing the duplicate to remain (clients dedupe)
	newLHHostUpdate(
		myUdpAddr0,
		myVpnIp,
		[]*udp.Addr{
			myUdpAddr1,
			myUdpAddr2,
			myUdpAddr3,
			myUdpAddr4,
			myUdpAddr5,
			myUdpAddr5, //Duplicated on purpose
			myUdpAddr6,
			myUdpAddr7,
			myUdpAddr8,
			myUdpAddr9,
			myUdpAddr10,
			myUdpAddr11, // This should get cut
		}, lhh)

	r = newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(
		t,
		r.msg.Details.Ip4AndPorts,
		myUdpAddr1, myUdpAddr2, myUdpAddr3, myUdpAddr4, myUdpAddr5, myUdpAddr5, myUdpAddr6, myUdpAddr7, myUdpAddr8, myUdpAddr9,
	)

	// Make sure we won't add ips in our vpn network
	bad1 := &udp.Addr{IP: net.ParseIP("10.128.0.99"), Port: 4242}
	bad2 := &udp.Addr{IP: net.ParseIP("10.128.0.100"), Port: 4242}
	good := &udp.Addr{IP: net.ParseIP("1.128.0.99"), Port: 4242}
	newLHHostUpdate(myUdpAddr0, myVpnIp, []*udp.Addr{bad1, bad2, good}, lhh)
	r = newLHHostRequest(myUdpAddr0, myVpnIp, myVpnIp, lhh)
	assertIp4InArray(t, r.msg.Details.Ip4AndPorts, good)
}

func TestLighthouse_reload(t *testing.T) {
	l := test.NewLogger()
	c := config.NewC(l)
	c.Settings["lighthouse"] = map[interface{}]interface{}{"am_lighthouse": true}
	c.Settings["listen"] = map[interface{}]interface{}{"port": 4242}
	lh, err := NewLightHouseFromConfig(l, c, &net.IPNet{IP: net.IP{10, 128, 0, 1}, Mask: net.IPMask{255, 255, 255, 0}}, nil, nil)
	assert.NoError(t, err)

	c.Settings["static_host_map"] = map[interface{}]interface{}{"10.128.0.2": []interface{}{"1.1.1.1:4242"}}
	lh.reload(c, false)
}

func newLHHostRequest(fromAddr *udp.Addr, myVpnIp, queryVpnIp iputil.VpnIp, lhh *LightHouseHandler) testLhReply {
	req := &NebulaMeta{
		Type: NebulaMeta_HostQuery,
		Details: &NebulaMetaDetails{
			VpnIp: uint32(queryVpnIp),
		},
	}

	b, err := req.Marshal()
	if err != nil {
		panic(err)
	}

	filter := NebulaMeta_HostQueryReply
	w := &testEncWriter{
		metaFilter: &filter,
	}
	lhh.HandleRequest(fromAddr, myVpnIp, b, w)
	return w.lastReply
}

func newLHHostUpdate(fromAddr *udp.Addr, vpnIp iputil.VpnIp, addrs []*udp.Addr, lhh *LightHouseHandler) {
	req := &NebulaMeta{
		Type: NebulaMeta_HostUpdateNotification,
		Details: &NebulaMetaDetails{
			VpnIp:       uint32(vpnIp),
			Ip4AndPorts: make([]*Ip4AndPort, len(addrs)),
		},
	}

	for k, v := range addrs {
		req.Details.Ip4AndPorts[k] = &Ip4AndPort{Ip: uint32(iputil.Ip2VpnIp(v.IP)), Port: uint32(v.Port)}
	}

	b, err := req.Marshal()
	if err != nil {
		panic(err)
	}

	w := &testEncWriter{}
	lhh.HandleRequest(fromAddr, vpnIp, b, w)
}

//TODO: this is a RemoteList test
//func Test_lhRemoteAllowList(t *testing.T) {
//	l := NewLogger()
//	c := NewConfig(l)
//	c.Settings["remoteallowlist"] = map[interface{}]interface{}{
//		"10.20.0.0/12": false,
//	}
//	allowList, err := c.GetAllowList("remoteallowlist", false)
//	assert.Nil(t, err)
//
//	lh1 := "10.128.0.2"
//	lh1IP := net.ParseIP(lh1)
//
//	udpServer, _ := NewListener(l, "0.0.0.0", 0, true)
//
//	lh := NewLightHouse(l, true, &net.IPNet{IP: net.IP{0, 0, 0, 1}, Mask: net.IPMask{255, 255, 255, 0}}, []uint32{ip2int(lh1IP)}, 10, 10003, udpServer, false, 1, false)
//	lh.SetRemoteAllowList(allowList)
//
//	// A disallowed ip should not enter the cache but we should end up with an empty entry in the addrMap
//	remote1IP := net.ParseIP("10.20.0.3")
//	remotes := lh.unlockedGetRemoteList(ip2int(remote1IP))
//	remotes.unlockedPrependV4(ip2int(remote1IP), NewIp4AndPort(remote1IP, 4242))
//	assert.NotNil(t, lh.addrMap[ip2int(remote1IP)])
//	assert.Empty(t, lh.addrMap[ip2int(remote1IP)].CopyAddrs([]*net.IPNet{}))
//
//	// Make sure a good ip enters the cache and addrMap
//	remote2IP := net.ParseIP("10.128.0.3")
//	remote2UDPAddr := NewUDPAddr(remote2IP, uint16(4242))
//	lh.addRemoteV4(ip2int(remote2IP), ip2int(remote2IP), NewIp4AndPort(remote2UDPAddr.IP, uint32(remote2UDPAddr.Port)), false, false)
//	assertUdpAddrInArray(t, lh.addrMap[ip2int(remote2IP)].CopyAddrs([]*net.IPNet{}), remote2UDPAddr)
//
//	// Another good ip gets into the cache, ordering is inverted
//	remote3IP := net.ParseIP("10.128.0.4")
//	remote3UDPAddr := NewUDPAddr(remote3IP, uint16(4243))
//	lh.addRemoteV4(ip2int(remote2IP), ip2int(remote2IP), NewIp4AndPort(remote3UDPAddr.IP, uint32(remote3UDPAddr.Port)), false, false)
//	assertUdpAddrInArray(t, lh.addrMap[ip2int(remote2IP)].CopyAddrs([]*net.IPNet{}), remote2UDPAddr, remote3UDPAddr)
//
//	// If we exceed the length limit we should only have the most recent addresses
//	addedAddrs := []*udpAddr{}
//	for i := 0; i < 11; i++ {
//		remoteUDPAddr := NewUDPAddr(net.IP{10, 128, 0, 4}, uint16(4243+i))
//		lh.addRemoteV4(ip2int(remote2IP), ip2int(remote2IP), NewIp4AndPort(remoteUDPAddr.IP, uint32(remoteUDPAddr.Port)), false, false)
//		// The first entry here is a duplicate, don't add it to the assert list
//		if i != 0 {
//			addedAddrs = append(addedAddrs, remoteUDPAddr)
//		}
//	}
//
//	// We should only have the last 10 of what we tried to add
//	assert.True(t, len(addedAddrs) >= 10, "We should have tried to add at least 10 addresses")
//	assertUdpAddrInArray(
//		t,
//		lh.addrMap[ip2int(remote2IP)].CopyAddrs([]*net.IPNet{}),
//		addedAddrs[0],
//		addedAddrs[1],
//		addedAddrs[2],
//		addedAddrs[3],
//		addedAddrs[4],
//		addedAddrs[5],
//		addedAddrs[6],
//		addedAddrs[7],
//		addedAddrs[8],
//		addedAddrs[9],
//	)
//}

func Test_ipMaskContains(t *testing.T) {
	assert.True(t, ipMaskContains(iputil.Ip2VpnIp(net.ParseIP("10.0.0.1")), 32-24, iputil.Ip2VpnIp(net.ParseIP("10.0.0.255"))))
	assert.False(t, ipMaskContains(iputil.Ip2VpnIp(net.ParseIP("10.0.0.1")), 32-24, iputil.Ip2VpnIp(net.ParseIP("10.0.1.1"))))
	assert.True(t, ipMaskContains(iputil.Ip2VpnIp(net.ParseIP("10.0.0.1")), 32, iputil.Ip2VpnIp(net.ParseIP("10.0.1.1"))))
}

type testLhReply struct {
	nebType    header.MessageType
	nebSubType header.MessageSubType
	vpnIp      iputil.VpnIp
	msg        *NebulaMeta
}

type testEncWriter struct {
	lastReply  testLhReply
	metaFilter *NebulaMeta_MessageType
}

func (tw *testEncWriter) SendVia(via *HostInfo, relay *Relay, ad, nb, out []byte, nocopy bool) {
}
func (tw *testEncWriter) Handshake(vpnIp iputil.VpnIp) {
}

func (tw *testEncWriter) SendMessageToHostInfo(t header.MessageType, st header.MessageSubType, hostinfo *HostInfo, p, _, _ []byte) {
	msg := &NebulaMeta{}
	err := msg.Unmarshal(p)
	if tw.metaFilter == nil || msg.Type == *tw.metaFilter {
		tw.lastReply = testLhReply{
			nebType:    t,
			nebSubType: st,
			vpnIp:      hostinfo.vpnIp,
			msg:        msg,
		}
	}

	if err != nil {
		panic(err)
	}
}

func (tw *testEncWriter) SendMessageToVpnIp(t header.MessageType, st header.MessageSubType, vpnIp iputil.VpnIp, p, _, _ []byte) {
	msg := &NebulaMeta{}
	err := msg.Unmarshal(p)
	if tw.metaFilter == nil || msg.Type == *tw.metaFilter {
		tw.lastReply = testLhReply{
			nebType:    t,
			nebSubType: st,
			vpnIp:      vpnIp,
			msg:        msg,
		}
	}

	if err != nil {
		panic(err)
	}
}

// assertIp4InArray asserts every address in want is at the same position in have and that the lengths match
func assertIp4InArray(t *testing.T, have []*Ip4AndPort, want ...*udp.Addr) {
	if !assert.Len(t, have, len(want)) {
		return
	}

	for k, w := range want {
		if !(have[k].Ip == uint32(iputil.Ip2VpnIp(w.IP)) && have[k].Port == uint32(w.Port)) {
			assert.Fail(t, fmt.Sprintf("Response did not contain: %v:%v at %v; %v", w.IP, w.Port, k, translateV4toUdpAddr(have)))
		}
	}
}

// assertUdpAddrInArray asserts every address in want is at the same position in have and that the lengths match
func assertUdpAddrInArray(t *testing.T, have []*udp.Addr, want ...*udp.Addr) {
	if !assert.Len(t, have, len(want)) {
		return
	}

	for k, w := range want {
		if !(have[k].IP.Equal(w.IP) && have[k].Port == w.Port) {
			assert.Fail(t, fmt.Sprintf("Response did not contain: %v at %v; %v", w, k, have))
		}
	}
}

func translateV4toUdpAddr(ips []*Ip4AndPort) []*udp.Addr {
	addrs := make([]*udp.Addr, len(ips))
	for k, v := range ips {
		addrs[k] = NewUDPAddrFromLH4(v)
	}
	return addrs
}
