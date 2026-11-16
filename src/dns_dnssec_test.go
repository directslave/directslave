package main

import (
	"context"
	"crypto"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type dnsTestZone struct {
	zone *ZoneConfig
	keys []*dns.DNSKEY
}

func testRR(t *testing.T, text string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(text)
	if err != nil {
		t.Fatal(err)
	}
	return rr
}

// Generate real signatures at test time so tests never depend on an expired
// static fixture. The second key exercises DNSKEY/signature rollover.
func signedTestZone(t *testing.T, mode string) dnsTestZone {
	t.Helper()
	z := &ZoneConfig{Zone: "example.test."}
	base := []string{
		"@ 3600 IN SOA ns.example.test. hostmaster.example.test. 4294967294 3600 600 86400 300",
		"@ 3600 IN NS ns.example.test.", "ns 3600 IN A 127.0.0.1",
		"www 3600 IN A 192.0.2.1", "www 3600 IN A 192.0.2.2",
		"alias 3600 IN CNAME www.example.test.", "aliasnx 3600 IN CNAME missing.example.test.",
		"outside 3600 IN CNAME outside.invalid.", "loop 3600 IN CNAME loop.example.test.",
		"*.wild 3600 IN A 192.0.2.3", "node.wild 3600 IN TXT \"blocks the higher wildcard\"",
		"*.cw 3600 IN CNAME www.example.test.", "leaf.ent 3600 IN A 192.0.2.4",
		"dname 3600 IN DNAME target.example.test.", "foo.target 3600 IN A 192.0.2.5",
		"secure 3600 IN NS ns.secure.example.test.", "ns.secure 3600 IN A 127.0.0.2",
		"secure 3600 IN DS 12345 13 2 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		"insecure 3600 IN NS ns.insecure.example.test.", "ns.insecure 3600 IN A 127.0.0.3",
		"unsigned.deep 3600 IN NS ns.unsigned.deep.example.test.", "ns.unsigned.deep 3600 IN A 127.0.0.4",
		"@ 3600 IN CAA 0 issue \"example.test\"",
		"opaque 3600 IN TYPE65280 \\# 4 01020304",
		"@ 3600 IN CDS 12345 13 2 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}
	for _, text := range base {
		parts := strings.SplitN(text, " ", 2)
		name := parts[0]
		if name == "@" {
			name = z.Zone
		} else {
			name += "." + z.Zone
		}
		z.RRs = append(z.RRs, testRR(t, name+" "+parts[1]))
	}
	for i := 0; i < 10; i++ {
		z.RRs = append(z.RRs, testRR(t, fmt.Sprintf("large.example.test. 3600 IN TXT \"%02d%s\"", i, strings.Repeat("x", 200))))
	}
	if mode == "unsigned" {
		return dnsTestZone{zone: z}
	}
	var keys []*dns.DNSKEY
	var private []crypto.Signer
	for _, algorithm := range []uint8{dns.ECDSAP256SHA256, dns.ED25519} {
		key := &dns.DNSKEY{Hdr: dns.RR_Header{Name: z.Zone, Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600}, Flags: 257, Protocol: 3, Algorithm: algorithm}
		priv, err := key.Generate(256)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		private = append(private, priv.(crypto.Signer))
		z.RRs = append(z.RRs, key)
	}
	cdnskey := &dns.CDNSKEY{DNSKEY: *keys[0]}
	cdnskey.Hdr.Rrtype = dns.TypeCDNSKEY
	z.RRs = append(z.RRs, cdnskey)
	if mode != "nsec" {
		z.RRs = append(z.RRs, testRR(t, "example.test. 3600 IN NSEC3PARAM 1 0 0 A1B2"))
	}
	sets := make(map[string]map[uint16][]dns.RR)
	cuts := make(map[string]bool)
	for _, rr := range z.RRs {
		h := rr.Header()
		addIndexedRR(sets, h.Name, h.Rrtype, rr)
		if h.Rrtype == dns.TypeNS && h.Name != z.Zone {
			cuts[h.Name] = true
		}
	}
	belowCut := func(name string) bool {
		for cut := range cuts {
			if name != cut && dns.IsSubDomain(cut, name) {
				return true
			}
		}
		return false
	}
	auth := make(map[string]bool)
	for name := range sets {
		if belowCut(name) {
			continue
		}
		if mode == "optout" && cuts[name] && len(sets[name][dns.TypeDS]) == 0 {
			continue
		}
		auth[name] = true
		if mode != "nsec" {
			for p := dnsParent(name); dns.IsSubDomain(z.Zone, p); p = dnsParent(p) {
				auth[p] = true
				if p == z.Zone {
					break
				}
			}
		}
	}
	var owners []string
	for name := range auth {
		owners = append(owners, name)
	}
	if mode == "nsec" {
		sort.Slice(owners, func(i, j int) bool { return dnsOrderKey(owners[i]) < dnsOrderKey(owners[j]) })
		for i, name := range owners {
			bitmap := []uint16{dns.TypeRRSIG, dns.TypeNSEC}
			for typ := range sets[name] {
				bitmap = append(bitmap, typ)
			}
			sort.Slice(bitmap, func(i, j int) bool { return bitmap[i] < bitmap[j] })
			z.RRs = append(z.RRs, &dns.NSEC{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNSEC, Class: dns.ClassINET, Ttl: 300}, NextDomain: owners[(i+1)%len(owners)], TypeBitMap: bitmap})
		}
	} else {
		hashNames := make(map[string]string)
		var hashes []string
		for _, name := range owners {
			hash := dns.HashName(name, dns.SHA1, 0, "A1B2")
			hashes = append(hashes, hash)
			hashNames[hash] = name
		}
		sort.Strings(hashes)
		for i, hash := range hashes {
			name := hashNames[hash]
			var bitmap []uint16
			for typ := range sets[name] {
				bitmap = append(bitmap, typ)
			}
			if len(bitmap) > 0 && !(cuts[name] && len(sets[name][dns.TypeDS]) == 0) {
				bitmap = append(bitmap, dns.TypeRRSIG)
			}
			sort.Slice(bitmap, func(i, j int) bool { return bitmap[i] < bitmap[j] })
			flags := uint8(0)
			if mode == "optout" {
				flags = 1
			}
			z.RRs = append(z.RRs, &dns.NSEC3{Hdr: dns.RR_Header{Name: hash + "." + z.Zone, Rrtype: dns.TypeNSEC3, Class: dns.ClassINET, Ttl: 300}, Hash: dns.SHA1, Flags: flags, Iterations: 0, SaltLength: 2, Salt: "A1B2", HashLength: 20, NextDomain: hashes[(i+1)%len(hashes)], TypeBitMap: bitmap})
		}
	}
	sets = make(map[string]map[uint16][]dns.RR)
	for _, rr := range z.RRs {
		addIndexedRR(sets, rr.Header().Name, rr.Header().Rrtype, rr)
	}
	for name, types := range sets {
		if belowCut(name) {
			continue
		}
		for typ, set := range types {
			if cuts[name] && typ == dns.TypeNS {
				continue
			}
			count := 1
			if typ == dns.TypeDNSKEY {
				count = 2
			}
			for i := 0; i < count; i++ {
				key := keys[i]
				sig := &dns.RRSIG{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: set[0].Header().Ttl}, Algorithm: key.Algorithm, KeyTag: key.KeyTag(), SignerName: z.Zone, Inception: uint32(time.Now().Add(-time.Hour).Unix()), Expiration: uint32(time.Now().Add(24 * time.Hour).Unix())}
				if err := sig.Sign(private[i], set); err != nil {
					t.Fatal(err)
				}
				z.RRs = append(z.RRs, sig)
			}
		}
	}
	return dnsTestZone{z, keys}
}

func publishTestZone(t *testing.T, fixture dnsTestZone) {
	t.Helper()
	z := fixture.zone
	var ok bool
	z, ok = ImportSOA(z)
	if !ok {
		t.Fatal("invalid SOA")
	}
	idx, err := buildZoneIndex(z)
	if err != nil {
		t.Fatal(err)
	}
	z.index = idx
	zones.Store(z.Zone, z)
	t.Cleanup(func() { zones.Delete(z.Zone) })
}

type dnsTestWriter struct {
	tcp   bool
	reply *dns.Msg
}

func (w *dnsTestWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
}
func (w *dnsTestWriter) RemoteAddr() net.Addr {
	if w.tcp {
		return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	}
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (w *dnsTestWriter) WriteMsg(m *dns.Msg) error {
	wire, err := m.Pack()
	if err != nil {
		return err
	}
	w.reply = new(dns.Msg)
	return w.reply.Unpack(wire)
}
func (w *dnsTestWriter) Write(p []byte) (int, error) {
	w.reply = new(dns.Msg)
	err := w.reply.Unpack(p)
	return len(p), err
}
func (w *dnsTestWriter) Close() error        { return nil }
func (w *dnsTestWriter) TsigStatus() error   { return nil }
func (w *dnsTestWriter) TsigTimersOnly(bool) {}
func (w *dnsTestWriter) Hijack()             {}

func queryTest(t *testing.T, name string, typ uint16, do, tcp bool) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, typ)
	q.RecursionDesired = false
	if do {
		q.SetEdns0(1232, true)
	}
	w := &dnsTestWriter{tcp: tcp}
	handleDnsRequest(w, q)
	if w.reply == nil {
		t.Fatal("no reply")
	}
	return w.reply
}

func countType(rrs []dns.RR, typ uint16) int {
	n := 0
	for _, rr := range rrs {
		if rr.Header().Rrtype == typ {
			n++
		}
	}
	return n
}

// Independently verify every returned signature against the test trust anchor.
func verifyTestSignatures(t *testing.T, m *dns.Msg, keys []*dns.DNSKEY) {
	t.Helper()
	for _, section := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range section {
			sig, ok := rr.(*dns.RRSIG)
			if !ok {
				continue
			}
			var set []dns.RR
			for _, candidate := range section {
				if candidate.Header().Rrtype == sig.TypeCovered && strings.EqualFold(candidate.Header().Name, sig.Hdr.Name) {
					set = append(set, candidate)
				}
			}
			valid := false
			for _, key := range keys {
				if sig.KeyTag == key.KeyTag() && sig.Verify(key, set) == nil {
					valid = true
					break
				}
			}
			if !valid {
				t.Fatalf("invalid signature for %s/%s\n%s", sig.Hdr.Name, dns.TypeToString[sig.TypeCovered], m)
			}
		}
	}
}

func TestDNSSECAnswers(t *testing.T) {
	for _, mode := range []string{"nsec", "nsec3", "optout"} {
		t.Run(mode, func(t *testing.T) {
			fixture := signedTestZone(t, mode)
			publishTestZone(t, fixture)
			cases := []struct {
				name   string
				typ    uint16
				rcode  int
				answer bool
				proof  bool
			}{
				{"example.test.", dns.TypeDNSKEY, 0, true, false}, {"example.test.", dns.TypeSOA, 0, true, false},
				{"www.example.test.", dns.TypeA, 0, true, false}, {"WWW.EXAMPLE.TEST.", dns.TypeA, 0, true, false},
				{"www.example.test.", dns.TypeAAAA, 0, false, true}, {"missing.example.test.", dns.TypeA, dns.RcodeNameError, false, true},
				{"ent.example.test.", dns.TypeA, 0, false, true}, {"x.ent.example.test.", dns.TypeA, dns.RcodeNameError, false, true},
				{"x.wild.example.test.", dns.TypeA, 0, true, true}, {"x.y.wild.example.test.", dns.TypeA, 0, true, true},
				{"x.wild.example.test.", dns.TypeAAAA, 0, false, true}, {"x.node.wild.example.test.", dns.TypeA, dns.RcodeNameError, false, true},
				{"x.cw.example.test.", dns.TypeA, 0, true, true}, {"alias.example.test.", dns.TypeA, 0, true, false},
				{"aliasnx.example.test.", dns.TypeA, dns.RcodeNameError, true, true}, {"outside.example.test.", dns.TypeA, 0, true, false},
				{"foo.dname.example.test.", dns.TypeA, 0, true, false}, {"secure.example.test.", dns.TypeDS, 0, true, false},
				{"insecure.example.test.", dns.TypeDS, 0, false, true},
				{"unsigned.deep.example.test.", dns.TypeDS, 0, false, true},
				{"deep.example.test.", dns.TypeA, 0, false, true}, {"example.test.", dns.TypeDS, 0, false, true},
				{"example.test.", dns.TypeCDS, 0, true, false}, {"example.test.", dns.TypeCDNSKEY, 0, true, false},
				{"opaque.example.test.", 65280, 0, true, false},
			}
			for _, c := range cases {
				t.Run(fmt.Sprintf("%s/%s", c.name, dns.TypeToString[c.typ]), func(t *testing.T) {
					m := queryTest(t, c.name, c.typ, true, true)
					if m.Rcode != c.rcode || (len(m.Answer) > 0) != c.answer || !m.Authoritative {
						t.Fatalf("unexpected answer: %s", m)
					}
					if c.proof && countType(m.Ns, dns.TypeNSEC)+countType(m.Ns, dns.TypeNSEC3) == 0 {
						t.Fatalf("missing denial proof: %s", m)
					}
					if m.AuthenticatedData || m.RecursionAvailable || m.IsEdns0() == nil || !m.IsEdns0().Do() {
						t.Fatal("bad response flags")
					}
					verifyTestSignatures(t, m, fixture.keys)
				})
			}
			m := queryTest(t, "example.test.", dns.TypeDNSKEY, true, true)
			if countType(m.Answer, dns.TypeRRSIG) != 2 {
				t.Fatal("lost rollover signatures")
			}
			m = queryTest(t, "www.example.test.", dns.TypeAAAA, true, true)
			for _, rr := range m.Ns {
				if rr.Header().Rrtype == dns.TypeSOA || (rr.Header().Rrtype == dns.TypeRRSIG && rr.(*dns.RRSIG).TypeCovered == dns.TypeSOA) {
					if rr.Header().Ttl != 300 {
						t.Fatal("negative TTL not capped")
					}
				}
			}
			if fixture.zone.index.soa.Hdr.Ttl != 3600 {
				t.Fatal("shared SOA mutated")
			}
			for _, name := range []string{"secure.example.test.", "insecure.example.test.", "host.insecure.example.test."} {
				m := queryTest(t, name, dns.TypeA, true, true)
				if m.Rcode != 0 || m.Authoritative || len(m.Answer) > 0 || countType(m.Ns, dns.TypeNS) != 1 || countType(m.Extra, dns.TypeA) != 1 {
					t.Fatalf("bad referral: %s", m)
				}
				verifyTestSignatures(t, m, fixture.keys)
				for _, rr := range m.Ns {
					if sig, ok := rr.(*dns.RRSIG); ok && sig.TypeCovered == dns.TypeNS {
						t.Fatal("delegation NS must not be signed")
					}
				}
			}
			m = queryTest(t, "loop.example.test.", dns.TypeA, true, true)
			if m.Rcode != dns.RcodeServerFailure {
				t.Fatal("loop not stopped")
			}
			if mode != "nsec" {
				for _, typ := range []uint16{dns.TypeA, dns.TypeNSEC3, dns.TypeRRSIG, dns.TypeANY} {
					name := fixture.zone.index.nsec3.hashes[0] + ".example.test."
					m := queryTest(t, name, typ, true, true)
					if m.Rcode != dns.RcodeNameError {
						t.Fatalf("NSEC3-only owner exposed: %s", m)
					}
				}
			}
		})
	}
}

func TestDNSSECDOAndTransport(t *testing.T) {
	fixture := signedTestZone(t, "nsec")
	publishTestZone(t, fixture)
	for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeANY} {
		m := queryTest(t, "www.example.test.", typ, false, true)
		if countType(m.Answer, dns.TypeRRSIG)+countType(m.Ns, dns.TypeRRSIG)+countType(m.Ns, dns.TypeNSEC) != 0 {
			t.Fatal("DNSSEC leaked with DO=0")
		}
	}
	for _, typ := range []uint16{dns.TypeDNSKEY, dns.TypeRRSIG, dns.TypeNSEC} {
		m := queryTest(t, "example.test.", typ, false, true)
		if countType(m.Answer, typ) == 0 {
			t.Fatal("explicit DNSSEC query requires DO")
		}
	}
	for _, do := range []bool{false, true} {
		m := queryTest(t, "large.example.test.", dns.TypeTXT, do, false)
		if !m.Truncated || len(m.Answer) != 0 {
			t.Fatalf("large UDP response not truncated safely: %s", m)
		}
		wire, _ := m.Pack()
		limit := 512
		if do {
			limit = 1232
		}
		if len(wire) > limit {
			t.Fatal("UDP exceeds advertised size")
		}
		m = queryTest(t, "large.example.test.", dns.TypeTXT, do, true)
		if m.Truncated || countType(m.Answer, dns.TypeTXT) != 10 {
			t.Fatal("TCP did not return complete RRset")
		}
		verifyTestSignatures(t, m, fixture.keys)
	}
	q := new(dns.Msg)
	q.SetQuestion("www.example.test.", dns.TypeA)
	q.SetEdns0(128, true)
	q.CheckingDisabled = true
	q.AuthenticatedData = true
	w := &dnsTestWriter{}
	handleDnsRequest(w, q)
	if !w.reply.CheckingDisabled || w.reply.AuthenticatedData {
		t.Fatal("AD/CD flags")
	}
	q.IsEdns0().SetVersion(1)
	w = &dnsTestWriter{}
	handleDnsRequest(w, q)
	if w.reply.Rcode != dns.RcodeBadVers || w.reply.IsEdns0().Version() != 0 {
		t.Fatal("EDNS BADVERS")
	}
	q.IsEdns0().SetVersion(0)
	q.Extra = append(q.Extra, dns.Copy(q.Extra[0]))
	w = &dnsTestWriter{}
	handleDnsRequest(w, q)
	if w.reply.Rcode != dns.RcodeFormatError {
		t.Fatal("duplicate OPT accepted")
	}
	q.Extra = nil
	q.Question = append(q.Question, q.Question[0])
	w = &dnsTestWriter{}
	handleDnsRequest(w, q)
	if w.reply.Rcode != dns.RcodeFormatError {
		t.Fatal("multiple questions accepted")
	}
	for _, typ := range []uint16{dns.TypeAXFR, dns.TypeIXFR} {
		m := queryTest(t, "example.test.", typ, true, true)
		if m.Rcode != dns.RcodeRefused {
			t.Fatal("downstream AXFR/IXFR must be refused")
		}
	}
}

func TestDNSSECParentDS(t *testing.T) {
	parent := signedTestZone(t, "nsec")
	publishTestZone(t, parent)
	child := &ZoneConfig{Zone: "secure.example.test.", RRs: []dns.RR{testRR(t, "secure.example.test. 300 IN SOA ns.secure.example.test. hostmaster.secure.example.test. 1 3600 600 86400 300"), testRR(t, "secure.example.test. 300 IN NS ns.secure.example.test.")}}
	publishTestZone(t, dnsTestZone{zone: child})
	m := queryTest(t, child.Zone, dns.TypeDS, true, true)
	if countType(m.Answer, dns.TypeDS) != 1 {
		t.Fatalf("DS not from parent: %s", m)
	}
	verifyTestSignatures(t, m, parent.keys)
	m = queryTest(t, child.Zone, dns.TypeSOA, true, true)
	if m.Answer[0].(*dns.SOA).Serial != 1 {
		t.Fatal("SOA not from child")
	}
}

func TestDNSSECUnsignedAndBroken(t *testing.T) {
	fixture := signedTestZone(t, "unsigned")
	publishTestZone(t, fixture)
	m := queryTest(t, "missing.example.test.", dns.TypeA, true, true)
	if m.Rcode != dns.RcodeNameError || countType(m.Ns, dns.TypeRRSIG) > 0 {
		t.Fatal("unsigned zone failed")
	}
	fixture = signedTestZone(t, "nsec")
	publishTestZone(t, fixture)
	delete(fixture.zone.index.sigs["www.example.test."], dns.TypeA)
	m = queryTest(t, "www.example.test.", dns.TypeA, true, true)
	if m.Rcode != dns.RcodeServerFailure || len(m.Answer) > 0 {
		t.Fatal("missing signature served")
	}
	fixture.zone.index.nsec = nil
	m = queryTest(t, "missing.example.test.", dns.TypeA, true, true)
	if m.Rcode != dns.RcodeServerFailure {
		t.Fatal("missing proof served")
	}
}

func TestDNSCanonicalOrder(t *testing.T) {
	ordered := []string{"example.", "a.example.", "yljkjljk.a.example.", "Z.a.example.", "zABC.a.EXAMPLE.", "z.example.", "\\001.z.example.", "*.z.example.", "\\128.z.example."}
	for i := 1; i < len(ordered); i++ {
		if dnsOrderKey(ordered[i-1]) >= dnsOrderKey(ordered[i]) {
			t.Fatalf("canonical order: %s >= %s", ordered[i-1], ordered[i])
		}
	}
	if dnsName("\\119WW.example.") != dnsName("www.example.") {
		t.Fatal("wire-equivalent name mismatch")
	}
	if dnsOrderKey("a\\.b.example.") != dnsOrderKey("a\\046b.example.") {
		t.Fatal("escaped dot order mismatch")
	}
}

func startTestDNSServer(t *testing.T, handler dns.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", ln.Addr().String())
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	tcpSrv := &dns.Server{Listener: ln, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	udpSrv := &dns.Server{PacketConn: udp, Handler: handler, NotifyStartedFunc: func() { ready <- struct{}{} }}
	go tcpSrv.ActivateAndServe()
	go udpSrv.ActivateAndServe()
	<-ready
	<-ready
	t.Cleanup(func() { _ = tcpSrv.Shutdown(); _ = udpSrv.Shutdown() })
	return ln.Addr().String()
}

func TestDNSSECTransferAndStorage(t *testing.T) {
	for _, mode := range []string{"nsec", "nsec3", "optout"} {
		t.Run(mode, func(t *testing.T) {
			fixture := signedTestZone(t, mode)
			address := startTestDNSServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
				if r.Question[0].Qtype == dns.TypeAXFR {
					ch := make(chan *dns.Envelope, 2)
					ch <- &dns.Envelope{RR: fixture.zone.RRs}
					ch <- &dns.Envelope{RR: []dns.RR{fixture.zone.RRs[0]}}
					close(ch)
					tr := new(dns.Transfer)
					_ = tr.Out(w, r, ch)
					return
				}
				m := new(dns.Msg)
				m.SetReply(r)
				m.Authoritative = true
				if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
					m.Truncated = true
				} else {
					m.Answer = []dns.RR{fixture.zone.RRs[0]}
				}
				_ = w.WriteMsg(m)
			}))
			serial, err := getSerialAt(fixture.zone.Zone, address)
			if err != nil || serial != 4294967294 {
				t.Fatalf("SOA TCP fallback: %d %v", serial, err)
			}
			rrs, err := fetchAXFR(fixture.zone.Zone, address)
			if err != nil {
				t.Fatal(err)
			}
			if len(rrs) != len(fixture.zone.RRs) {
				t.Fatal("AXFR lost records")
			}
			filename := filepath.Join(t.TempDir(), "signed.db")
			if err := WriteZoneFile(fixture.zone.Zone, filename, rrs); err != nil {
				t.Fatal(err)
			}
			if !ImportZoneFile(fixture.zone.Zone, filename, "127.0.0.1", false) {
				t.Fatal("disk import failed")
			}
			t.Cleanup(func() { stopAllZoneTimers(fixture.zone.Zone); zones.Delete(fixture.zone.Zone) })
			for _, name := range []string{"www.example.test.", "absent.example.test.", "x.wild.example.test."} {
				m := queryTest(t, name, dns.TypeA, true, true)
				if m.Rcode == dns.RcodeServerFailure {
					t.Fatalf("import lost DNSSEC: %s", m)
				}
				verifyTestSignatures(t, m, fixture.keys)
			}
			broken := append([]dns.RR{}, rrs...)
			last := dns.Copy(rrs[0]).(*dns.SOA)
			last.Serial++
			broken = append(broken, last)
			if _, err := validateAXFRRecords(fixture.zone.Zone, broken); err == nil {
				t.Fatal("AXFR accepted mismatched closing SOA")
			}
		})
	}
	if !dnsSerialNewer(0, 4294967295) || dnsSerialNewer(4294967295, 0) || dnsSerialNewer(1, 1) || dnsSerialNewer(0x80000000, 0) {
		t.Fatal("RFC 1982 serial comparison")
	}
}

func TestDNSSECConcurrentSnapshot(t *testing.T) {
	fixture := signedTestZone(t, "nsec3")
	publishTestZone(t, fixture)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				m := queryTest(t, "x.wild.example.test.", dns.TypeAAAA, true, true)
				if m.Rcode != 0 {
					t.Errorf("concurrent query: %s", m)
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		copyZone := *fixture.zone
		copyZone.LastAttempt = time.Now()
		zones.Store(copyZone.Zone, &copyZone)
	}
	wg.Wait()
	if fixture.zone.index.soa.Hdr.Ttl != 3600 {
		t.Fatal("concurrent negative response changed stored TTL")
	}
}

// Optional end-to-end validation with an independent resolver. Run with
// DIRECTDNS_UNBOUND_HOST=/path/to/unbound-host go test -run TestDNSSECUnbound -v .
func TestDNSSECUnbound(t *testing.T) {
	binary := os.Getenv("DIRECTDNS_UNBOUND_HOST")
	if binary == "" {
		t.Skip("set DIRECTDNS_UNBOUND_HOST to run independent DNSSEC validation")
	}
	for _, mode := range []string{"nsec", "nsec3", "optout"} {
		t.Run(mode, func(t *testing.T) {
			fixture := signedTestZone(t, mode)
			publishTestZone(t, fixture)
			authority := startTestDNSServer(t, dns.HandlerFunc(handleDnsRequest))
			dir := t.TempDir()
			_, authPort, _ := net.SplitHostPort(authority)
			config := fmt.Sprintf(`server:
    do-ip6: no
    do-not-query-localhost: no
    module-config: "validator iterator"
    trust-anchor: "%s"
    local-zone: "example.test." transparent
    qname-minimisation: no
    aggressive-nsec: no
    val-log-level: 2
    verbosity: 0
stub-zone:
    name: "example.test."
    stub-addr: 127.0.0.1@%s
    stub-first: no
`, fixture.keys[0].String(), authPort)
			conf := filepath.Join(dir, "unbound.conf")
			if err := os.WriteFile(conf, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				name  string
				typ   uint16
				rcode int
			}{
				{"example.test.", dns.TypeDNSKEY, 0}, {"www.example.test.", dns.TypeA, 0},
				{"www.example.test.", dns.TypeAAAA, 0}, {"absent.example.test.", dns.TypeA, dns.RcodeNameError},
				{"ent.example.test.", dns.TypeA, 0}, {"x.ent.example.test.", dns.TypeA, dns.RcodeNameError},
				{"x.wild.example.test.", dns.TypeA, 0}, {"x.y.wild.example.test.", dns.TypeA, 0},
				{"x.wild.example.test.", dns.TypeAAAA, 0}, {"x.node.wild.example.test.", dns.TypeA, dns.RcodeNameError},
				{"x.cw.example.test.", dns.TypeA, 0}, {"alias.example.test.", dns.TypeA, 0},
				{"aliasnx.example.test.", dns.TypeA, dns.RcodeNameError}, {"foo.dname.example.test.", dns.TypeA, 0},
				{"large.example.test.", dns.TypeTXT, 0}, {"secure.example.test.", dns.TypeDS, 0},
				{"insecure.example.test.", dns.TypeDS, 0},
				{"unsigned.deep.example.test.", dns.TypeDS, 0},
				{"deep.example.test.", dns.TypeA, 0}, {"example.test.", dns.TypeCDS, 0}, {"example.test.", dns.TypeCDNSKEY, 0},
			}
			for _, c := range cases {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				cmd := exec.CommandContext(ctx, binary, "-C", conf, "-v", "-t", dns.TypeToString[c.typ], c.name)
				output, err := cmd.CombinedOutput()
				cancel()
				text := string(output)
				if ctx.Err() == context.DeadlineExceeded {
					t.Fatalf("validator timed out for %s: %s", c.name, text)
				}
				if err != nil && c.rcode != dns.RcodeNameError {
					t.Fatalf("validator failed: %v %s", err, text)
				}
				// Opt-Out proofs can be insecure even with valid signatures.
				wantSecure := mode != "optout" || (c.rcode == 0 && (c.typ == dns.TypeDNSKEY || c.typ == dns.TypeTXT || c.typ == dns.TypeCDS || c.typ == dns.TypeCDNSKEY || c.name == "www.example.test." || c.name == "secure.example.test." || c.name == "alias.example.test." || c.name == "foo.dname.example.test."))
				if strings.Contains(text, "BOGUS") || strings.Contains(text, "(error)") || (wantSecure && !strings.Contains(text, "(secure)")) {
					t.Fatalf("independent validator rejected %s/%s: %s", c.name, dns.TypeToString[c.typ], text)
				}
				if c.rcode == dns.RcodeNameError && !strings.Contains(text, "NXDOMAIN") {
					t.Fatalf("expected validated NXDOMAIN: %s", text)
				}
				t.Logf("%s %s: secure=%t insecure=%t", c.name, dns.TypeToString[c.typ], strings.Contains(text, "(secure)"), strings.Contains(text, "(insecure)"))
			}
		})
	}
}

func TestDNSSECNSEC3ChainSelection(t *testing.T) {
	fixture := signedTestZone(t, "nsec3")
	// A not-yet-complete rollover chain must not replace the usable chain.
	fixture.zone.RRs = append([]dns.RR{testRR(t, "example.test. 300 IN NSEC3PARAM 1 0 5 CAFE")}, fixture.zone.RRs...)
	idx, err := buildZoneIndex(fixture.zone)
	if err != nil {
		t.Fatal(err)
	}
	if idx.nsec3 == nil || idx.nsec3.param.Salt != "A1B2" {
		t.Fatal("selected incomplete rollover chain")
	}
	fixture.zone.index = idx
	zones.Store(fixture.zone.Zone, fixture.zone)
	t.Cleanup(func() { zones.Delete(fixture.zone.Zone) })
	m := queryTest(t, "absent.example.test.", dns.TypeA, true, true)
	if m.Rcode != dns.RcodeNameError {
		t.Fatal("rollover broke negative answer")
	}
	verifyTestSignatures(t, m, fixture.keys)
	// A malformed/unknown-only NSEC3 chain must not load as an unsigned zone.
	for _, rr := range fixture.zone.RRs {
		if n, ok := rr.(*dns.NSEC3); ok {
			n.Hash = 255
		}
	}
	if _, err := buildZoneIndex(fixture.zone); err == nil {
		t.Fatal("unknown-only NSEC3 chain accepted")
	}
}

func TestDNSSECNSEC3EmptySaltAndIterations(t *testing.T) {
	// RFC 5155 hashing vectors, including the parser's empty-salt spelling.
	p := testRR(t, "example.test. 300 IN NSEC3PARAM 1 0 0 -").(*dns.NSEC3PARAM)
	chain := &nsec3Chain{param: p}
	if chain.hash("example.") == "" {
		t.Fatal("empty salt is unusable")
	}
	p = &dns.NSEC3PARAM{Hash: dns.SHA1, Iterations: 12, Salt: "AABBCCDD"}
	chain.param = p
	if got := chain.hash("example."); got != "0P9MHAVEQVM6T7VBL5LOP2U3T2RP3TOM" {
		t.Fatalf("RFC 5155 hash mismatch: %s", got)
	}
}
