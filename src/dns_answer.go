package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// DS belongs to the parent side when both parent and child are served. If
// only the child is served, RFC 4035 3.1.4.1 requires child-apex NODATA.
func zoneForQuestion(name string, qtype uint16) (*ZoneConfig, bool) {
	zone, ok := Zread(name)
	if ok && qtype == dns.TypeDS && name == zone.Zone && name != "." {
		if parent, found := Zread(dnsParent(name)); found {
			return parent, true
		}
	}
	return zone, ok
}

func answerDNSQuestion(m *dns.Msg, query dns.Question, do bool) error {
	name := dnsName(query.Name)
	zone, ok := zoneForQuestion(name, query.Qtype)
	if !ok {
		m.Rcode = dns.RcodeRefused
		return nil
	}
	m.Authoritative = true
	seen := make(map[string]bool)
	for hop := 0; hop < 16; hop++ {
		if zone.New == 1 || zone.Unchecked || zone.index == nil {
			return fmt.Errorf("zone %s is not ready", zone.Zone)
		}
		if seen[name] {
			return fmt.Errorf("CNAME/DNAME loop at %s", name)
		}
		seen[name] = true
		idx := zone.index
		cut := idx.delegation(name, zone.Zone)
		// Direct NSEC/RRSIG queries at a delegation consistently return the
		// parent-side records; all other types except DS receive a referral.
		parentData := name == cut && (query.Qtype == dns.TypeDS || query.Qtype == dns.TypeNSEC || query.Qtype == dns.TypeRRSIG)
		if cut != "" && !parentData {
			if len(m.Answer) == 0 {
				m.Authoritative = false
			}
			return idx.appendReferral(m, zone.Zone, cut, do)
		}

		// DNAME is considered before descendant names and wildcard matching.
		dnameOwner := ""
		for p := dnsParent(name); dns.IsSubDomain(zone.Zone, p); p = dnsParent(p) {
			if len(idx.sets[p][dns.TypeDNAME]) > 0 {
				dnameOwner = p
			}
			if p == zone.Zone || p == "." {
				break
			}
		}
		if dnameOwner != "" && name != dnameOwner {
			if err := idx.appendRRset(&m.Answer, dnameOwner, dns.TypeDNAME, "", do, nil); err != nil {
				return err
			}
			dname := idx.sets[dnameOwner][dns.TypeDNAME][0].(*dns.DNAME)
			labels := dns.SplitDomainName(name)
			prefix := strings.Join(labels[:len(labels)-dns.CountLabel(dnameOwner)], ".")
			target := prefix + "." + strings.TrimPrefix(dnsName(dname.Target), ".")
			buf := make([]byte, 256)
			n, err := dns.PackDomainName(target, buf, 0, nil, false)
			if err != nil || n > 255 {
				m.Rcode = dns.RcodeYXDomain
				return nil
			}
			m.Answer = append(m.Answer, &dns.CNAME{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: dname.Hdr.Ttl}, Target: target})
			// This CNAME is authenticated by the DNAME signature, not an RRSIG(CNAME).
			if query.Qtype == dns.TypeCNAME || query.Qtype == dns.TypeANY {
				return nil
			}
			name = dnsName(target)
			zone, ok = zoneForQuestion(name, query.Qtype)
			if !ok {
				return nil
			}
			continue
		}

		owner, wildcard := name, ""
		exists := idx.names[name]
		if !exists {
			candidate := dnsWildcard(idx.closest(name, zone.Zone))
			if idx.names[candidate] {
				owner, wildcard, exists = candidate, candidate, true
			}
		}
		types := []uint16{query.Qtype}
		if query.Qtype == dns.TypeANY {
			types = nil
			for typ := range idx.sets[owner] {
				// RRSIGs are emitted next to their covered RRset with DO=1.
				if typ == dns.TypeRRSIG && do {
					continue
				}
				if !do && dnsAutomaticType(typ) {
					continue
				}
				types = append(types, typ)
			}
			sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
		}
		cname := false
		if exists && query.Qtype != dns.TypeANY && len(idx.sets[owner][query.Qtype]) == 0 && query.Qtype != dns.TypeCNAME && len(idx.sets[owner][dns.TypeCNAME]) > 0 {
			types = []uint16{dns.TypeCNAME}
			cname = true
		}
		before := len(m.Answer)
		expanded := ""
		if wildcard != "" {
			expanded = name
		}
		if exists {
			for _, typ := range types {
				if err := idx.appendRRset(&m.Answer, owner, typ, expanded, do, nil); err != nil {
					return err
				}
			}
		}
		positive := len(m.Answer) > before
		if !positive {
			if !exists {
				m.Rcode = dns.RcodeNameError
			}
			ttl := idx.soa.Minttl
			if err := idx.appendRRset(&m.Ns, zone.Zone, dns.TypeSOA, "", do, &ttl); err != nil {
				return err
			}
		}
		if do && idx.signed && (!positive || wildcard != "") {
			if err := idx.appendProof(m, zone.Zone, name, query.Qtype, wildcard, exists, positive, cut != ""); err != nil {
				return err
			}
		}
		if !cname {
			return nil
		}
		name = dnsName(idx.sets[owner][dns.TypeCNAME][0].(*dns.CNAME).Target)
		zone, ok = zoneForQuestion(name, query.Qtype)
		if !ok {
			return nil
		} // authoritative server never recurses
	}
	return fmt.Errorf("CNAME/DNAME chain exceeds 16 links")
}

// Records added by DNSSEC processing are omitted from ordinary ANY responses.
// Explicit queries for these types are answered regardless of the DO bit.
func dnsAutomaticType(typ uint16) bool {
	switch typ {
	case dns.TypeRRSIG, dns.TypeNSEC, dns.TypeNSEC3:
		return true
	}
	return false
}

func (idx *zoneIndex) appendReferral(m *dns.Msg, apex, cut string, do bool) error {
	ns := idx.sets[cut][dns.TypeNS]
	m.Ns = append(m.Ns, ns...)
	if do && idx.signed {
		if len(idx.sets[cut][dns.TypeDS]) > 0 {
			if err := idx.appendRRset(&m.Ns, cut, dns.TypeDS, "", true, nil); err != nil {
				return err
			}
		} else if err := idx.appendProof(m, apex, cut, dns.TypeDS, "", true, false, true); err != nil {
			return err
		}
	}
	for _, rr := range ns {
		target := dnsName(rr.(*dns.NS).Ns)
		if !dns.IsSubDomain(apex, target) {
			continue
		}
		for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA} {
			// In-bailiwick glue beneath a cut is unsigned. Addresses hosted
			// authoritatively in this zone retain their signatures.
			if idx.delegation(target, apex) != "" {
				m.Extra = append(m.Extra, idx.sets[target][typ]...)
			} else if err := idx.appendRRset(&m.Extra, target, typ, "", do, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
