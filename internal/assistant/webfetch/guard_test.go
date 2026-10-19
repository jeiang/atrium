package webfetch_test

import (
	"net/netip"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/webfetch"
)

// The addresses of tests/test_webfetch.py: NAT64, IPv4-compatible, SIIT and site-local forms that carry or
// stand for a private address are refused, the same forms around a public IPv4 address are allowed.
func TestDefaultAllowedEmbeddedAddresses(t *testing.T) {
	blocked := []string{
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::a00:1", "64:ff9b::c0a8:101", "64:ff9b::6440:1", "64:ff9b:1::a00:1",
		"64:ff9b:1::808:808", "::7f00:1", "::a9fe:a9fe", "::a00:1", "::", "::1", "::ffff:7f00:1", "::ffff:a9fe:a9fe",
		"::ffff:0:7f00:1", "::ffff:0:a9fe:a9fe", "fec0::1", "feff:ffff::1", "fe80::1", "fc00::1", "ff02::1", "2001:db8::1",
		"127.0.0.1", "10.0.0.5", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1",
	}
	allowed := []string{"64:ff9b::5db8:d822", "::5db8:d822", "::ffff:5db8:d822", "::ffff:0:5db8:d822", "2606:4700::1111", "93.184.216.34"}
	for _, a := range blocked {
		if webfetch.DefaultAllowed(netip.MustParseAddr(a)) {
			t.Errorf("DefaultAllowed(%s) = true", a)
		}
	}
	for _, a := range allowed {
		if !webfetch.DefaultAllowed(netip.MustParseAddr(a)) {
			t.Errorf("DefaultAllowed(%s) = false", a)
		}
	}
}

func TestDefaultAllowedIgnoresAZone(t *testing.T) {
	if webfetch.DefaultAllowed(netip.MustParseAddr("fe80::1%eth0")) || webfetch.DefaultAllowed(netip.MustParseAddr("::1%lo")) {
		t.Error("a zoned link-local or loopback address is allowed")
	}
	if !webfetch.DefaultAllowed(netip.MustParseAddr("2606:4700::1111%eth0")) {
		t.Error("a zoned global address is refused")
	}
}
