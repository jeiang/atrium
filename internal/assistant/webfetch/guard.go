package webfetch

import (
	"net/netip"
)

func prefixes(texts ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(texts))
	for i, t := range texts {
		out[i] = netip.MustParsePrefix(t)
	}
	return out
}

func inAny(ip netip.Addr, nets []netip.Prefix) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")

	privateV4 = prefixes("0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
		"192.0.0.170/31", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "255.255.255.255/32")
	privateV4Exceptions = prefixes("192.0.0.9/32", "192.0.0.10/32")

	privateV6 = prefixes("::1/128", "::/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32",
		"2002::/16", "3fff::/20", "fc00::/7", "fe80::/10")
	privateV6Exceptions = prefixes("2001:1::1/128", "2001:1::2/128", "2001:3::/32", "2001:4:112::/48", "2001:20::/28",
		"2001:30::/28")

	multicastV4 = netip.MustParsePrefix("224.0.0.0/4")
	multicastV6 = netip.MustParsePrefix("ff00::/8")
	siteLocalV6 = netip.MustParsePrefix("fec0::/10")

	embeddedIPv4 = prefixes("64:ff9b::/96", "::/96", "::ffff:0:0:0/96")
)

func DefaultAllowed(ip netip.Addr) bool {
	ip = ip.WithZone("")
	if ip.Is6() {
		switch {
		case siteLocalV6.Contains(ip):
			return false
		case ip.Is4In6():
			ip = ip.Unmap()
		case inAny(ip, embeddedIPv4):
			b := ip.As16()
			ip = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		}
	}
	if ip.Is4() {
		return !cgnat.Contains(ip) && !notGlobal(ip, privateV4, privateV4Exceptions) && !multicastV4.Contains(ip)
	}
	return !notGlobal(ip, privateV6, privateV6Exceptions) && !multicastV6.Contains(ip)
}

func notGlobal(ip netip.Addr, private, exceptions []netip.Prefix) bool {
	return inAny(ip, private) && !inAny(ip, exceptions)
}
