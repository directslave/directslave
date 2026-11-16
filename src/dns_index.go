package main

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// zoneIndex belongs to one immutable ZoneConfig snapshot. Transfers and disk
// imports build it before publication; query handlers never mutate shared RRs.
type zoneIndex struct {
	sets   map[string]map[uint16][]dns.RR
	sigs   map[string]map[uint16][]dns.RR
	names  map[string]bool // authoritative names, including empty non-terminals
	cuts   map[string]bool
	soa    *dns.SOA
	signed bool
	nsec   []nsecEntry
	nsec3  *nsec3Chain
}

type nsecEntry struct {
	key string
	rr  *dns.NSEC
}

type nsec3Chain struct {
	param   *dns.NSEC3PARAM
	hashes  []string
	records map[string]*dns.NSEC3
}

// Normalize wire-equivalent spellings (including escaped octets) without
// changing the records received from the primary.
func dnsName(name string) string {
	buf := make([]byte, 256)
	n, err := dns.PackDomainName(dns.Fqdn(name), buf, 0, nil, false)
	if err != nil {
		return strings.ToLower(dns.Fqdn(name))
	}
	for i := 0; i < n && buf[i] != 0; {
		size := int(buf[i])
		i++
		for end := i + size; i < end; i++ {
			if buf[i] >= 'A' && buf[i] <= 'Z' {
				buf[i] += 'a' - 'A'
			}
		}
	}
	result, _, err := dns.UnpackDomainName(buf[:n], 0)
	if err != nil {
		return strings.ToLower(dns.Fqdn(name))
	}
	return result
}

func dnsParent(name string) string {
	if name == "." {
		return "."
	}
	i, end := dns.NextLabel(name, 0)
	if end {
		return "."
	}
	return name[i:]
}

func dnsWildcard(name string) string {
	if name == "." {
		return "*."
	}
	return "*." + name
}

// RFC 4034 canonical name order compares decoded labels, right to left.
// Hex encoding preserves octet order; NUL separates labels and sorts a shorter
// label before its extension, including names containing escaped NUL or dots.
func dnsOrderKey(name string) string {
	buf := make([]byte, 256)
	n, err := dns.PackDomainName(dnsName(name), buf, 0, nil, false)
	if err != nil {
		return ""
	}
	var labels [][]byte
	for i := 0; i < n && buf[i] != 0; {
		size := int(buf[i])
		i++
		labels = append(labels, buf[i:i+size])
		i += size
	}
	var key strings.Builder
	for i := len(labels) - 1; i >= 0; i-- {
		key.WriteString(hex.EncodeToString(labels[i]))
		key.WriteByte(0)
	}
	return key.String()
}

func addIndexedRR(table map[string]map[uint16][]dns.RR, name string, typ uint16, rr dns.RR) {
	if table[name] == nil {
		table[name] = make(map[uint16][]dns.RR)
	}
	table[name][typ] = append(table[name][typ], rr)
}

func buildZoneIndex(zone *ZoneConfig) (*zoneIndex, error) {
	idx := &zoneIndex{
		sets: make(map[string]map[uint16][]dns.RR), sigs: make(map[string]map[uint16][]dns.RR),
		names: make(map[string]bool), cuts: make(map[string]bool),
	}
	for _, rr := range zone.RRs {
		if rr == nil {
			return nil, fmt.Errorf("nil zone record")
		}
		h := rr.Header()
		name := dnsName(h.Name)
		if h.Class != dns.ClassINET || !dns.IsSubDomain(zone.Zone, name) {
			return nil, fmt.Errorf("record outside IN zone: %s", h.String())
		}
		addIndexedRR(idx.sets, name, h.Rrtype, rr)
		if sig, ok := rr.(*dns.RRSIG); ok {
			addIndexedRR(idx.sigs, name, sig.TypeCovered, rr)
		}
		if soa, ok := rr.(*dns.SOA); ok {
			if name != zone.Zone || idx.soa != nil {
				return nil, fmt.Errorf("invalid or duplicate SOA")
			}
			idx.soa = soa
		}
		if h.Rrtype == dns.TypeNS && name != zone.Zone {
			idx.cuts[name] = true
		}
	}
	if idx.soa == nil {
		return nil, fmt.Errorf("no apex SOA")
	}
	idx.signed = len(idx.sets[zone.Zone][dns.TypeDNSKEY]) > 0 || len(idx.sigs[zone.Zone][dns.TypeSOA]) > 0
	for name, sets := range idx.sets {
		cut := idx.delegation(name, zone.Zone)
		if cut != "" && cut != name {
			continue
		} // glue is not authoritative
		visible := false
		for typ, set := range sets {
			if typ == dns.TypeNSEC3 {
				continue
			}
			if typ == dns.TypeRRSIG {
				for _, rr := range set {
					if rr.(*dns.RRSIG).TypeCovered != dns.TypeNSEC3 {
						visible = true
					}
				}
			} else {
				visible = true
			}
		}
		// RFC 5155 7.2.8: NSEC3-only owner names do not enter the DNS tree.
		if visible {
			for p := name; ; p = dnsParent(p) {
				idx.names[p] = true
				if p == zone.Zone || p == "." {
					break
				}
			}
		}
		for _, rr := range sets[dns.TypeNSEC] {
			n := rr.(*dns.NSEC)
			if !dns.IsSubDomain(zone.Zone, dnsName(n.NextDomain)) {
				return nil, fmt.Errorf("NSEC next name outside zone")
			}
			idx.nsec = append(idx.nsec, nsecEntry{dnsOrderKey(name), n})
		}
	}
	sort.Slice(idx.nsec, func(i, j int) bool { return idx.nsec[i].key < idx.nsec[j].key })
	// NSEC3PARAM announces complete chains. During rollover, select one whole
	// supported chain; never combine proofs from different salts/iterations.
	for _, rr := range idx.sets[zone.Zone][dns.TypeNSEC3PARAM] {
		param := rr.(*dns.NSEC3PARAM)
		if param.Hash != dns.SHA1 || param.Flags != 0 {
			continue
		}
		chain := &nsec3Chain{param: param, records: make(map[string]*dns.NSEC3)}
		for name, sets := range idx.sets {
			if dnsParent(name) != zone.Zone {
				continue
			}
			for _, rr := range sets[dns.TypeNSEC3] {
				n := rr.(*dns.NSEC3)
				if n.Hash != param.Hash || n.Iterations != param.Iterations || !strings.EqualFold(n.Salt, param.Salt) || n.Flags > 1 {
					continue
				}
				hash := strings.ToUpper(dns.SplitDomainName(name)[0])
				if len(hash) != 32 || len(n.NextDomain) != 32 {
					continue
				}
				if _, exists := chain.records[hash]; !exists {
					chain.hashes = append(chain.hashes, hash)
				}
				chain.records[hash] = n
			}
		}
		sort.Strings(chain.hashes)
		complete := len(chain.hashes) > 0
		for i, hash := range chain.hashes {
			if !strings.EqualFold(chain.records[hash].NextDomain, chain.hashes[(i+1)%len(chain.hashes)]) {
				complete = false
				break
			}
		}
		if complete {
			idx.nsec3 = chain
			break
		}
	}
	if idx.signed && idx.nsec3 == nil && len(idx.nsec) == 0 {
		return nil, fmt.Errorf("signed zone has no usable NSEC or announced complete SHA-1 NSEC3 chain")
	}
	return idx, nil
}

func (idx *zoneIndex) delegation(name, apex string) string {
	cut := ""
	for p := name; p != apex && p != "."; p = dnsParent(p) {
		if idx.cuts[p] {
			cut = p
		}
	}
	return cut
}

func (idx *zoneIndex) closest(name, apex string) string {
	for p := name; ; p = dnsParent(p) {
		if idx.names[p] || p == apex || p == "." {
			return p
		}
	}
}

func dnsNextCloser(name, closest string) string {
	for name != closest && name != "." {
		p := dnsParent(name)
		if p == closest {
			return name
		}
		name = p
	}
	return name
}

// zoneSigningState reports what the apex of a loaded zone actually carries.
//
// A zone counts as signed only when both halves are present: the DNSKEY set a
// validating resolver will ask for, and a signature over the apex SOA, which is
// the proof that anything in this zone is signed at all. One without the other
// is not a weaker form of signed, it is a broken zone - keys published with
// nothing signed, or signatures no resolver can ever validate - and it is worth
// counting separately rather than folding into either total.
//
// Both answers come from the index that was built when the zone was published,
// so this costs two map lookups per zone and never walks the records.
func zoneSigningState(zinfo *ZoneConfig) (keys bool, sigs bool) {
	if zinfo == nil || zinfo.index == nil {
		return false, false
	}

	apex := zinfo.Zone

	return len(zinfo.index.sets[apex][dns.TypeDNSKEY]) > 0,
		len(zinfo.index.sigs[apex][dns.TypeSOA]) > 0
}
