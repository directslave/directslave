package main

import (
	"encoding/hex"
	"net"
	"net/netip"
	"strings"
)

// An IP address has many spellings, and DirectSlave compares addresses in
// several places: the primary of a zone against the address a NOTIFY or an API
// call comes from. "2001:DB8::1", "2001:db8:0:0:0:0:0:1" and "[2001:db8::1]"
// are one address, and so are "192.0.2.1" and "::ffff:192.0.2.1". Everything
// that stores or compares an address goes through canonicalIP, so there is
// exactly one spelling per address: the dotted form for IPv4, RFC 5952 for
// IPv6.

// canonicalIP returns the one spelling of an address, or false when value is
// not an IP address. Brackets around the address are accepted. A zone
// ("fe80::1%em0") is dropped: it names an interface of this host, and a peer
// address never carries the same one.
func canonicalIP(value string) (string, bool) {
	value = strings.TrimSpace(value)

	if len(value) > 2 && value[0] == '[' && value[len(value)-1] == ']' {
		value = value[1 : len(value)-1]
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return "", false
	}

	return addr.Unmap().WithZone("").String(), true
}

// canonicalIPOrSame is canonicalIP for values that are passed on even when
// they are not an address: "none", or whatever an old control file holds.
func canonicalIPOrSame(value string) string {
	if canonical, ok := canonicalIP(value); ok {
		return canonical
	}

	return value
}

// sameIP reports whether two spellings are the same address. Values that are
// not addresses are equal only when they are the same string.
func sameIP(a string, b string) bool {
	left, leftOK := canonicalIP(a)
	right, rightOK := canonicalIP(b)

	if leftOK && rightOK {
		return left == right
	}

	return a == b
}

// hostOfAddr returns the address part of "host:port" as net/http and the DNS
// server report it: "192.0.2.1:53" and "[2001:db8::1]:53". Cutting at the
// first ':' is what made every IPv6 client "[2001".
func hostOfAddr(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return canonicalIPOrSame(host)
	}

	// No port: a bare address, with or without brackets.
	return canonicalIPOrSame(addr)
}

// ipSortKey makes addresses sort numerically, IPv4 before IPv6, so 192.0.2.9
// comes before 192.0.2.154 and 2001:db8::9 before 2001:db8::10.
func ipSortKey(address string) (string, bool) {
	canonical, ok := canonicalIP(address)
	if ok == false {
		return "", false
	}

	addr, err := netip.ParseAddr(canonical)
	if err != nil {
		return "", false
	}

	family := "4"
	if addr.Is6() {
		family = "6"
	}

	raw := addr.As16()

	return family + hex.EncodeToString(raw[:]), true
}

// joinListenAddr builds the address for a listener. "*" and "" mean every
// address of both families; an IPv6 literal gets its brackets.
func joinListenAddr(host string, port string) string {
	host = strings.TrimSpace(host)

	if host == "*" || host == "" {
		return ":" + port
	}

	return net.JoinHostPort(canonicalIPOrSame(host), port)
}
