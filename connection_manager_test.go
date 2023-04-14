package nebula

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/slackhq/nebula/cert"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/iputil"
	"github.com/slackhq/nebula/test"
	"github.com/slackhq/nebula/udp"
	"github.com/stretchr/testify/assert"
)

var vpnIp iputil.VpnIp

func newTestLighthouse() *LightHouse {
	lh := &LightHouse{
		l:       test.NewLogger(),
		addrMap: map[iputil.VpnIp]*RemoteList{},
	}
	lighthouses := map[iputil.VpnIp]struct{}{}
	staticList := map[iputil.VpnIp]struct{}{}

	lh.lighthouses.Store(&lighthouses)
	lh.staticList.Store(&staticList)

	return lh
}

func Test_NewConnectionManagerTest(t *testing.T) {
	l := test.NewLogger()
	//_, tuncidr, _ := net.ParseCIDR("1.1.1.1/24")
	_, vpncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, localrange, _ := net.ParseCIDR("10.1.1.1/24")
	vpnIp = iputil.Ip2VpnIp(net.ParseIP("172.1.1.2"))
	preferredRanges := []*net.IPNet{localrange}

	// Very incomplete mock objects
	hostMap := NewHostMap(l, "test", vpncidr, preferredRanges)
	cs := &CertState{
		rawCertificate:      []byte{},
		privateKey:          []byte{},
		certificate:         &cert.NebulaCertificate{},
		rawCertificateNoKey: []byte{},
	}

	lh := newTestLighthouse()
	ifce := &Interface{
		hostMap:          hostMap,
		inside:           &test.NoopTun{},
		outside:          &udp.Conn{},
		firewall:         &Firewall{},
		lightHouse:       lh,
		handshakeManager: NewHandshakeManager(l, vpncidr, preferredRanges, hostMap, lh, &udp.Conn{}, defaultHandshakeConfig),
		l:                l,
	}
	ifce.certState.Store(cs)

	// Create manager
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	punchy := NewPunchyFromConfig(l, config.NewC(l))
	nc := newConnectionManager(ctx, l, ifce, 5, 10, punchy)
	p := []byte("")
	nb := make([]byte, 12, 12)
	out := make([]byte, mtu)

	// Add an ip we have established a connection w/ to hostmap
	hostinfo := &HostInfo{
		vpnIp:         vpnIp,
		localIndexId:  1099,
		remoteIndexId: 9901,
	}
	hostinfo.ConnectionState = &ConnectionState{
		certState: cs,
		H:         &noise.HandshakeState{},
	}
	nc.hostMap.unlockedAddHostInfo(hostinfo, ifce)

	// We saw traffic out to vpnIp
	nc.Out(hostinfo.localIndexId)
	nc.In(hostinfo.localIndexId)
	assert.NotContains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Hosts, hostinfo.vpnIp)
	assert.Contains(t, nc.hostMap.Indexes, hostinfo.localIndexId)
	assert.Contains(t, nc.out, hostinfo.localIndexId)

	// Do a traffic check tick, should not be pending deletion but should not have any in/out packets recorded
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.NotContains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.out, hostinfo.localIndexId)
	assert.NotContains(t, nc.in, hostinfo.localIndexId)

	// Do another traffic check tick, this host should be pending deletion now
	nc.Out(hostinfo.localIndexId)
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.Contains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.out, hostinfo.localIndexId)
	assert.NotContains(t, nc.in, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Indexes, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Hosts, hostinfo.vpnIp)

	// Do a final traffic check tick, the host should now be removed
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.NotContains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.hostMap.Hosts, hostinfo.vpnIp)
	assert.NotContains(t, nc.hostMap.Indexes, hostinfo.localIndexId)
}

func Test_NewConnectionManagerTest2(t *testing.T) {
	l := test.NewLogger()
	//_, tuncidr, _ := net.ParseCIDR("1.1.1.1/24")
	_, vpncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, localrange, _ := net.ParseCIDR("10.1.1.1/24")
	preferredRanges := []*net.IPNet{localrange}

	// Very incomplete mock objects
	hostMap := NewHostMap(l, "test", vpncidr, preferredRanges)
	cs := &CertState{
		rawCertificate:      []byte{},
		privateKey:          []byte{},
		certificate:         &cert.NebulaCertificate{},
		rawCertificateNoKey: []byte{},
	}

	lh := newTestLighthouse()
	ifce := &Interface{
		hostMap:          hostMap,
		inside:           &test.NoopTun{},
		outside:          &udp.Conn{},
		firewall:         &Firewall{},
		lightHouse:       lh,
		handshakeManager: NewHandshakeManager(l, vpncidr, preferredRanges, hostMap, lh, &udp.Conn{}, defaultHandshakeConfig),
		l:                l,
	}
	ifce.certState.Store(cs)

	// Create manager
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	punchy := NewPunchyFromConfig(l, config.NewC(l))
	nc := newConnectionManager(ctx, l, ifce, 5, 10, punchy)
	p := []byte("")
	nb := make([]byte, 12, 12)
	out := make([]byte, mtu)

	// Add an ip we have established a connection w/ to hostmap
	hostinfo := &HostInfo{
		vpnIp:         vpnIp,
		localIndexId:  1099,
		remoteIndexId: 9901,
	}
	hostinfo.ConnectionState = &ConnectionState{
		certState: cs,
		H:         &noise.HandshakeState{},
	}
	nc.hostMap.unlockedAddHostInfo(hostinfo, ifce)

	// We saw traffic out to vpnIp
	nc.Out(hostinfo.localIndexId)
	nc.In(hostinfo.localIndexId)
	assert.NotContains(t, nc.pendingDeletion, hostinfo.vpnIp)
	assert.Contains(t, nc.hostMap.Hosts, hostinfo.vpnIp)
	assert.Contains(t, nc.hostMap.Indexes, hostinfo.localIndexId)

	// Do a traffic check tick, should not be pending deletion but should not have any in/out packets recorded
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.NotContains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.out, hostinfo.localIndexId)
	assert.NotContains(t, nc.in, hostinfo.localIndexId)

	// Do another traffic check tick, this host should be pending deletion now
	nc.Out(hostinfo.localIndexId)
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.Contains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.out, hostinfo.localIndexId)
	assert.NotContains(t, nc.in, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Indexes, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Hosts, hostinfo.vpnIp)

	// We saw traffic, should no longer be pending deletion
	nc.In(hostinfo.localIndexId)
	nc.doTrafficCheck(hostinfo.localIndexId, p, nb, out, time.Now())
	assert.NotContains(t, nc.pendingDeletion, hostinfo.localIndexId)
	assert.NotContains(t, nc.out, hostinfo.localIndexId)
	assert.NotContains(t, nc.in, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Indexes, hostinfo.localIndexId)
	assert.Contains(t, nc.hostMap.Hosts, hostinfo.vpnIp)
}

// Check if we can disconnect the peer.
// Validate if the peer's certificate is invalid (expired, etc.)
// Disconnect only if disconnectInvalid: true is set.
func Test_NewConnectionManagerTest_DisconnectInvalid(t *testing.T) {
	now := time.Now()
	l := test.NewLogger()
	ipNet := net.IPNet{
		IP:   net.IPv4(172, 1, 1, 2),
		Mask: net.IPMask{255, 255, 255, 0},
	}
	_, vpncidr, _ := net.ParseCIDR("172.1.1.1/24")
	_, localrange, _ := net.ParseCIDR("10.1.1.1/24")
	preferredRanges := []*net.IPNet{localrange}
	hostMap := NewHostMap(l, "test", vpncidr, preferredRanges)

	// Generate keys for CA and peer's cert.
	pubCA, privCA, _ := ed25519.GenerateKey(rand.Reader)
	caCert := cert.NebulaCertificate{
		Details: cert.NebulaCertificateDetails{
			Name:      "ca",
			NotBefore: now,
			NotAfter:  now.Add(1 * time.Hour),
			IsCA:      true,
			PublicKey: pubCA,
		},
	}
	caCert.Sign(privCA)
	ncp := &cert.NebulaCAPool{
		CAs: cert.NewCAPool().CAs,
	}
	ncp.CAs["ca"] = &caCert

	pubCrt, _, _ := ed25519.GenerateKey(rand.Reader)
	peerCert := cert.NebulaCertificate{
		Details: cert.NebulaCertificateDetails{
			Name:      "host",
			Ips:       []*net.IPNet{&ipNet},
			Subnets:   []*net.IPNet{},
			NotBefore: now,
			NotAfter:  now.Add(60 * time.Second),
			PublicKey: pubCrt,
			IsCA:      false,
			Issuer:    "ca",
		},
	}
	peerCert.Sign(privCA)

	cs := &CertState{
		rawCertificate:      []byte{},
		privateKey:          []byte{},
		certificate:         &cert.NebulaCertificate{},
		rawCertificateNoKey: []byte{},
	}

	lh := newTestLighthouse()
	ifce := &Interface{
		hostMap:           hostMap,
		inside:            &test.NoopTun{},
		outside:           &udp.Conn{},
		firewall:          &Firewall{},
		lightHouse:        lh,
		handshakeManager:  NewHandshakeManager(l, vpncidr, preferredRanges, hostMap, lh, &udp.Conn{}, defaultHandshakeConfig),
		l:                 l,
		disconnectInvalid: true,
		caPool:            ncp,
	}
	ifce.certState.Store(cs)

	// Create manager
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	punchy := NewPunchyFromConfig(l, config.NewC(l))
	nc := newConnectionManager(ctx, l, ifce, 5, 10, punchy)
	ifce.connectionManager = nc
	hostinfo, _ := nc.hostMap.AddVpnIp(vpnIp, nil)
	hostinfo.ConnectionState = &ConnectionState{
		certState: cs,
		peerCert:  &peerCert,
		H:         &noise.HandshakeState{},
	}

	// Move ahead 45s.
	// Check if to disconnect with invalid certificate.
	// Should be alive.
	nextTick := now.Add(45 * time.Second)
	invalid := nc.isInvalidCertificate(nextTick, hostinfo)
	assert.False(t, invalid)

	// Move ahead 61s.
	// Check if to disconnect with invalid certificate.
	// Should be disconnected.
	nextTick = now.Add(61 * time.Second)
	invalid = nc.isInvalidCertificate(nextTick, hostinfo)
	assert.True(t, invalid)
}
