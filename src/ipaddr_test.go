package main

import (
	"sort"
	"testing"
)

func TestCanonicalIP(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"192.0.2.1", "192.0.2.1", true},
		{" 192.0.2.1 ", "192.0.2.1", true},
		{"::ffff:192.0.2.1", "192.0.2.1", true},
		{"2001:db8::1", "2001:db8::1", true},
		{"2001:DB8::1", "2001:db8::1", true},
		{"2001:db8:0:0:0:0:0:1", "2001:db8::1", true},
		{"2001:0db8:0000:0000:0000:0000:0000:0001", "2001:db8::1", true},
		{"[2001:db8::1]", "2001:db8::1", true},
		{"fe80::1%em0", "fe80::1", true},
		{"::1", "::1", true},
		{"none", "", false},
		{"", "", false},
		{"ns1.example.com", "", false},
		{"192.0.2.256", "", false},
		{"192.0.2.1:53", "", false},
		{"[2001", "", false},
	}

	for _, c := range cases {
		got, ok := canonicalIP(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("canonicalIP(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSameIP(t *testing.T) {
	same := [][2]string{
		{"2001:DB8::1", "2001:db8:0:0::1"},
		{"192.0.2.1", "::ffff:192.0.2.1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"none", "none"},
	}
	for _, pair := range same {
		if sameIP(pair[0], pair[1]) == false {
			t.Errorf("sameIP(%q, %q) = false", pair[0], pair[1])
		}
	}

	different := [][2]string{
		{"2001:db8::1", "2001:db8::2"},
		{"192.0.2.1", "192.0.2.10"},
		{"192.0.2.1", "2001:db8::1"},
		{"none", "192.0.2.1"},
		{"", "192.0.2.1"},
	}
	for _, pair := range different {
		if sameIP(pair[0], pair[1]) {
			t.Errorf("sameIP(%q, %q) = true", pair[0], pair[1])
		}
	}
}

// The remote address of an HTTP request, as net/http reports it.
func TestHostOfAddr(t *testing.T) {
	cases := map[string]string{
		"192.0.2.7:51234":          "192.0.2.7",
		"[2001:db8::7]:51234":      "2001:db8::7",
		"[2001:DB8:0:0::7]:51234":  "2001:db8::7",
		"[::ffff:192.0.2.7]:51234": "192.0.2.7",
		"[fe80::1%em0]:51234":      "fe80::1",
		"[::1]:2222":               "::1",
		"192.0.2.7":                "192.0.2.7",
		"2001:db8::7":              "2001:db8::7",
		"not-an-address":           "not-an-address",
	}

	for in, want := range cases {
		if got := hostOfAddr(in); got != want {
			t.Errorf("hostOfAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientIPv6(t *testing.T) {
	if got := ClientIP("[2001:db8::7]:51234"); got != "2001:db8::7" {
		t.Fatalf("ClientIP = %q", got)
	}
	if got := ClientIP("192.0.2.7:51234"); got != "192.0.2.7" {
		t.Fatalf("ClientIP = %q", got)
	}
}

func TestIPSortOrder(t *testing.T) {
	addresses := []string{"2001:db8::10", "192.0.2.154", "2001:db8::9", "192.0.2.9", "10.0.0.1", "::1"}

	sort.Slice(addresses, func(i, j int) bool { return compareMasters(addresses[i], addresses[j]) < 0 })

	want := []string{"10.0.0.1", "192.0.2.9", "192.0.2.154", "::1", "2001:db8::9", "2001:db8::10"}
	for i := range want {
		if addresses[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", addresses, want)
		}
	}
}

func TestJoinListenAddr(t *testing.T) {
	cases := []struct{ host, port, want string }{
		{"*", "2222", ":2222"},
		{"", "2222", ":2222"},
		{"192.0.2.20", "2222", "192.0.2.20:2222"},
		{"2001:db8::20", "2222", "[2001:db8::20]:2222"},
		{"[2001:db8::20]", "2224", "[2001:db8::20]:2224"},
		{"::", "2222", "[::]:2222"},
	}

	for _, c := range cases {
		if got := joinListenAddr(c.host, c.port); got != c.want {
			t.Errorf("joinListenAddr(%q, %q) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

// What the zone declaration looks like for an IPv6 primary, and that every
// pattern reading the control file gets the address back.
func TestZoneTemplateIPv6(t *testing.T) {
	previous := Config
	Config.named_workdir = "/usr/local/directslave/var/zones"
	Config.retry_time = "1200"
	Config.named_format = "text"
	defer func() { Config = previous }()

	line := zoneTemplate("example.com", "2001:DB8:0:0::53")

	for name, re := range map[string]interface {
		FindStringSubmatch(string) []string
	}{"dashboard": dashboardZoneRe, "cli": cliZoneLineRe, "stats": declaredZoneRe} {
		ret := re.FindStringSubmatch(line[:len(line)-1])
		if len(ret) == 0 {
			t.Fatalf("%s pattern does not match %q", name, line)
		}
		if got := ret[len(ret)-1]; got != "2001:db8::53" {
			t.Fatalf("%s pattern read primary %q from %q", name, got, line)
		}
	}
}
