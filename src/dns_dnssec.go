package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

func dnsBitmapHas(bitmap []uint16, typ uint16) bool {
	for _, t := range bitmap {
		if t == typ {
			return true
		}
	}
	return false
}

func dnsNoType(bitmap []uint16, typ uint16) bool {
	return !dnsBitmapHas(bitmap, typ) && !dnsBitmapHas(bitmap, dns.TypeCNAME)
}

// A strict open interval, including the last-to-first wrap of an NSEC chain.
func dnsInInterval(value, owner, next string) bool {
	if value == owner || value == next {
		return false
	}
	if owner < next {
		return owner < value && value < next
	}
	return value > owner || value < next
}

func (idx *zoneIndex) nsecExact(name string) *dns.NSEC {
	for _, rr := range idx.sets[name][dns.TypeNSEC] {
		return rr.(*dns.NSEC)
	}
	return nil
}

func (idx *zoneIndex) nsecCover(name string) *dns.NSEC {
	if len(idx.nsec) == 0 {
		return nil
	}
	key := dnsOrderKey(name)
	i := sort.Search(len(idx.nsec), func(i int) bool { return idx.nsec[i].key >= key }) - 1
	if i < 0 {
		i = len(idx.nsec) - 1
	}
	e := idx.nsec[i]
	if dnsInInterval(key, e.key, dnsOrderKey(e.rr.NextDomain)) {
		return e.rr
	}
	return nil
}

func (chain *nsec3Chain) hash(name string) string {
	return strings.ToUpper(dns.HashName(name, chain.param.Hash, chain.param.Iterations, chain.param.Salt))
}

func (chain *nsec3Chain) match(name string) *dns.NSEC3 { return chain.records[chain.hash(name)] }

func (chain *nsec3Chain) cover(name string) *dns.NSEC3 {
	hash := chain.hash(name)
	if hash == "" || len(chain.hashes) == 0 || chain.records[hash] != nil {
		return nil
	}
	i := sort.SearchStrings(chain.hashes, hash) - 1
	if i < 0 {
		i = len(chain.hashes) - 1
	}
	rr := chain.records[chain.hashes[i]]
	if dnsInInterval(hash, chain.hashes[i], strings.ToUpper(rr.NextDomain)) {
		return rr
	}
	return nil
}

// denialProof returns records in their original form. The response builder
// adds the signatures of each entire RRset and deduplicates repeated proofs.
// Missing proof material is an error, never an unsigned negative response.
func (idx *zoneIndex) denialProof(apex, name string, qtype uint16, wildcard string, exists, positive, delegation bool) ([]dns.RR, error) {
	if !idx.signed {
		return nil, nil
	}
	var proof []dns.RR
	failed := false
	addNSEC := func(rr *dns.NSEC) {
		if rr == nil {
			failed = true
		} else {
			proof = append(proof, rr)
		}
	}
	addNSEC3 := func(rr *dns.NSEC3) {
		if rr == nil {
			failed = true
		} else {
			proof = append(proof, rr)
		}
	}
	closest := idx.closest(name, apex)
	if wildcard != "" {
		closest = dnsParent(wildcard)
	}
	if chain := idx.nsec3; chain != nil {
		switch {
		case wildcard != "":
			addNSEC3(chain.cover(dnsNextCloser(name, closest)))
			if !positive {
				addNSEC3(chain.match(closest))
				rr := chain.match(wildcard)
				if rr != nil && !dnsNoType(rr.TypeBitMap, qtype) {
					failed = true
				}
				addNSEC3(rr)
			}
		case exists:
			rr := chain.match(name)
			if rr != nil {
				if !dnsNoType(rr.TypeBitMap, qtype) {
					failed = true
				}
				addNSEC3(rr)
			} else if delegation || len(idx.sets[name]) == 0 {
				// Opt-Out can omit an insecure delegation and its otherwise
				// empty ancestors. Find the closest *provable* encloser.
				p := dnsParent(name)
				for p != apex && p != "." && chain.match(p) == nil {
					p = dnsParent(p)
				}
				addNSEC3(chain.match(p))
				cover := chain.cover(dnsNextCloser(name, p))
				if cover != nil && cover.Flags&1 == 0 {
					failed = true
				}
				addNSEC3(cover)
			} else {
				failed = true
			}
		default:
			addNSEC3(chain.match(closest))
			addNSEC3(chain.cover(dnsNextCloser(name, closest)))
			addNSEC3(chain.cover(dnsWildcard(closest)))
		}
	} else {
		switch {
		case wildcard != "":
			addNSEC(idx.nsecCover(name))
			if !positive {
				rr := idx.nsecExact(wildcard)
				if rr != nil && !dnsNoType(rr.TypeBitMap, qtype) {
					failed = true
				}
				addNSEC(rr)
			}
		case exists:
			rr := idx.nsecExact(name)
			if rr != nil {
				if !dnsNoType(rr.TypeBitMap, qtype) {
					failed = true
				}
			} else {
				// NSEC does not create records at empty non-terminals. A
				// covering interval ending at a descendant proves existence.
				rr = idx.nsecCover(name)
				if rr != nil && !dns.IsSubDomain(name, dnsName(rr.NextDomain)) {
					failed = true
				}
			}
			addNSEC(rr)
		default:
			addNSEC(idx.nsecCover(name))
			addNSEC(idx.nsecCover(dnsWildcard(closest)))
		}
	}
	if failed {
		return nil, fmt.Errorf("incomplete DNSSEC denial proof for %s/%s", name, dns.TypeToString[qtype])
	}
	return proof, nil
}

// appendRRset keeps data and all rollover signatures together. RRSIGs do not
// themselves have signatures. Glue and delegation NS sets are added separately.
func (idx *zoneIndex) appendRRset(section *[]dns.RR, owner string, typ uint16, expanded string, do bool, ttlLimit *uint32) error {
	set := idx.sets[owner][typ]
	if len(set) == 0 {
		return nil
	}
	signatures := idx.sigs[owner][typ]
	if do && idx.signed && typ != dns.TypeRRSIG && len(signatures) == 0 {
		return fmt.Errorf("missing RRSIG(%s) at %s", dns.TypeToString[typ], owner)
	}
	appendRecords := func(records []dns.RR) {
		for _, rr := range records {
			if expanded != "" || ttlLimit != nil {
				rr = dns.Copy(rr)
				if expanded != "" {
					rr.Header().Name = expanded
				}
				if ttlLimit != nil && rr.Header().Ttl > *ttlLimit {
					rr.Header().Ttl = *ttlLimit
				}
			}
			*section = append(*section, rr)
		}
	}
	appendRecords(set)
	if do && typ != dns.TypeRRSIG {
		appendRecords(signatures)
	}
	return nil
}

func (idx *zoneIndex) appendProof(m *dns.Msg, apex, name string, qtype uint16, wildcard string, exists, positive, delegation bool) error {
	proof, err := idx.denialProof(apex, name, qtype, wildcard, exists, positive, delegation)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, rr := range proof {
		owner := dnsName(rr.Header().Name)
		key := fmt.Sprintf("%s/%d", owner, rr.Header().Rrtype)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := idx.appendRRset(&m.Ns, owner, rr.Header().Rrtype, "", true, nil); err != nil {
			return err
		}
	}
	return nil
}
