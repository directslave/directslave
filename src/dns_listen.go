package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Listen addresses of the integrated DNS server, one directive per family:
//
//	dns_listen_ip4  192.0.2.10, 192.0.2.11
//	dns_listen_ip6  2001:db8::10, 2001:db8::11
//
// Each takes a list separated by commas and/or spaces, "*" for every address of
// that family, or "none" to leave the family off. IPv4 and IPv6 always get
// separate sockets: the IPv6 ones are opened IPV6_V6ONLY, so "*" in both
// directives is 0.0.0.0 plus [::] side by side rather than one dual-stack
// socket that behaves differently on Linux and FreeBSD.

// dnsListenFamily is one parsed directive.
type dnsListenFamily struct {
	name     string       // "ip4" / "ip6", for messages
	network  string       // "4" / "6", appended to udp/tcp
	any      bool         // "*": the wildcard address of the family
	addrs    []netip.Addr // explicit addresses, deduplicated, in config order
	explicit bool         // set in the config (not a default); a bind failure is then fatal
}

func (family dnsListenFamily) enabled() bool {
	return family.any || len(family.addrs) > 0
}

// hosts lists what to bind: the wildcard, or every explicit address.
func (family dnsListenFamily) hosts() []string {
	if family.any {
		if family.network == "4" {
			return []string{"0.0.0.0"}
		}
		return []string{"::"}
	}

	hosts := make([]string, 0, len(family.addrs))
	for _, addr := range family.addrs {
		hosts = append(hosts, addr.String())
	}
	return hosts
}

// parseDNSListen validates one directive. Hostnames are refused on purpose: a
// listener has to be bound before privileges are dropped and before anything
// can be resolved reliably, and a name that resolves differently tomorrow is
// not an address.
func parseDNSListen(name string, network string, value string, explicit bool) (dnsListenFamily, error) {
	family := dnsListenFamily{name: name, network: network, explicit: explicit}

	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	})

	seen := make(map[netip.Addr]bool)

	for _, field := range fields {
		field = strings.TrimSpace(field)

		switch strings.ToLower(field) {
		case "":
			continue
		case "none", "off", "no":
			if len(fields) > 1 {
				return family, fmt.Errorf("dns_listen_%s: %q cannot be combined with addresses", name, field)
			}
			return family, nil
		case "*", "any":
			family.any = true
			continue
		}

		// Brackets are harmless habit from URLs and are accepted for IPv6.
		addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(field, "["), "]"))
		if err != nil {
			return family, fmt.Errorf("dns_listen_%s: %q is not an IP address", name, field)
		}

		switch {
		case network == "4" && addr.Is4() == false:
			return family, fmt.Errorf("dns_listen_%s: %s is not an IPv4 address", name, field)
		case network == "6" && addr.Is4In6():
			return family, fmt.Errorf("dns_listen_%s: %s is an IPv4-mapped address; put %s into dns_listen_ip4",
				name, field, addr.Unmap())
		case network == "6" && addr.Is6() == false:
			return family, fmt.Errorf("dns_listen_%s: %s is not an IPv6 address", name, field)
		case addr.IsUnspecified():
			return family, fmt.Errorf("dns_listen_%s: use * instead of %s", name, field)
		}

		if seen[addr] {
			continue
		}
		seen[addr] = true
		family.addrs = append(family.addrs, addr)
	}

	// The wildcard and a specific address of the same family overlap, and the
	// second bind fails with "address already in use"; say so now instead.
	if family.any && len(family.addrs) > 0 {
		return family, fmt.Errorf("dns_listen_%s: * already covers every address, drop the explicit ones or the *", name)
	}

	return family, nil
}

// dnsListenFamilies parses both directives from the loaded configuration.
func dnsListenFamilies() (ip4 dnsListenFamily, ip6 dnsListenFamily, err error) {
	ip4, err = parseDNSListen("ip4", "4", Config.dns_listen_ip4, Config.dns_listen_ip4_set)
	if err != nil {
		return ip4, ip6, err
	}

	ip6, err = parseDNSListen("ip6", "6", Config.dns_listen_ip6, Config.dns_listen_ip6_set)
	if err != nil {
		return ip4, ip6, err
	}

	if ip4.enabled() == false && ip6.enabled() == false {
		return ip4, ip6, fmt.Errorf("dns_listen_ip4 and dns_listen_ip6 are both off; the DNS server would listen nowhere")
	}

	return ip4, ip6, nil
}

// dnsListenDescription is the one-line summary used in the startup banner, the
// DNS log and --check.
func dnsListenDescription() string {
	ip4, ip6, err := dnsListenFamilies()
	if err != nil {
		return "INVALID: " + err.Error()
	}

	port := dnsListenPort()
	var parts []string

	for _, family := range []dnsListenFamily{ip4, ip6} {
		for _, host := range family.hosts() {
			parts = append(parts, net.JoinHostPort(host, port))
		}
	}

	return strings.Join(parts, ", ")
}

func dnsListenPort() string {
	port := strings.TrimSpace(Config.dns_port)
	if port == "" {
		return "53"
	}
	return port
}

// splitLegacyDNSListen maps the old single "dns_listen" onto the two new
// directives, so a config that was never touched keeps listening where it did.
// "*" meant every address of both families.
func splitLegacyDNSListen(value string) (ip4 string, ip6 string) {
	var four, six []string

	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		field = strings.TrimSpace(field)
		if field == "*" {
			return "*", "*"
		}

		addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(field, "["), "]"))
		if err != nil {
			// Left in place so the parser reports it with the directive name.
			four = append(four, field)
			continue
		}
		if addr.Is4() || addr.Is4In6() {
			four = append(four, addr.Unmap().String())
		} else {
			six = append(six, addr.String())
		}
	}

	ip4, ip6 = "none", "none"
	if len(four) > 0 {
		ip4 = strings.Join(four, ", ")
	}
	if len(six) > 0 {
		ip6 = strings.Join(six, ", ")
	}
	return ip4, ip6
}
