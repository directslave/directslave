package main

import (
	"net"
	"time"

	"github.com/miekg/dns"
)

const dnsMaxUDPSize = 1232

func handleDnsRequest(w dns.ResponseWriter, r *dns.Msg) {
	started := time.Now()
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = true
	m.AuthenticatedData = false
	m.CheckingDisabled = r.CheckingDisabled
	m.RecursionAvailable = false

	do, size, valid := prepareDNSEDNS(m, r)
	if len(r.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
	} else if valid {
		switch r.Opcode {
		case dns.OpcodeQuery:
			handleDNSQuery(m, w)
		case dns.OpcodeNotify:
			if r.Question[0].Qtype != dns.TypeSOA || r.Question[0].Qclass != dns.ClassINET {
				m.Rcode = dns.RcodeFormatError
			} else {
				handleDNSNotify(m, w)
			}
		default:
			m.Rcode = dns.RcodeNotImplemented
		}
	}
	// The response OPT advertises server capacity and echoes the request DO.
	if opt := m.IsEdns0(); opt != nil {
		opt.SetDo(do)
	}
	fitDNSResponse(m, w, size)
	writeErr := w.WriteMsg(m)
	if r.Opcode == dns.OpcodeQuery {
		queueDNSQueryLog(w, r, m, time.Since(started), writeErr)
	}
	if writeErr != nil {
		dnsLogPrintln("SERVER: failed to write DNS response:", writeErr)
	}
}

func prepareDNSEDNS(m, r *dns.Msg) (bool, int, bool) {
	var opt *dns.OPT
	for _, rr := range r.Extra {
		if item, ok := rr.(*dns.OPT); ok {
			if opt != nil || item.Hdr.Name != "." {
				m.Rcode = dns.RcodeFormatError
				return false, dns.MinMsgSize, false
			}
			opt = item
		}
	}
	if opt == nil {
		return false, dns.MinMsgSize, true
	}
	m.SetEdns0(dnsMaxUDPSize, opt.Do())
	size := int(opt.UDPSize())
	if size < dns.MinMsgSize {
		size = dns.MinMsgSize
	}
	if size > dnsMaxUDPSize {
		size = dnsMaxUDPSize
	}
	if opt.Version() != 0 {
		m.Rcode = dns.RcodeBadVers
		return opt.Do(), size, false
	}
	return opt.Do(), size, true
}

func handleDNSQuery(m *dns.Msg, w dns.ResponseWriter) {
	if len(m.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		return
	}
	query := m.Question[0]
	name := dnsName(query.Name)
	if query.Qclass == dns.ClassCHAOS && query.Qtype == dns.TypeTXT && name == "version.bind." {
		value, _ := dns.NewRR("version.bind. 0 CH TXT \"DirectDNS/" + CURRENT + "\"")
		m.Answer = append(m.Answer, value)
		return
	}
	if query.Qclass != dns.ClassINET {
		m.Rcode = dns.RcodeRefused
		return
	}
	// This secondary consumes AXFR from its configured primary. There is no
	// downstream-transfer ACL, so inbound AXFR/IXFR requests remain refused.
	if query.Qtype == dns.TypeAXFR || query.Qtype == dns.TypeIXFR {
		m.Rcode = dns.RcodeRefused
		return
	}
	do := m.IsEdns0() != nil && m.IsEdns0().Do()
	if err := answerDNSQuestion(m, query, do); err != nil {
		m.Rcode = dns.RcodeServerFailure
		m.Answer, m.Ns = nil, nil
		keepDNSOPT(m)
		return
	}
	m.Answer = uniqueDNSRecords(m.Answer)
	m.Ns = uniqueDNSRecords(m.Ns)
	m.Extra = uniqueDNSRecords(m.Extra)
}

func uniqueDNSRecords(records []dns.RR) []dns.RR {
	if len(records) < 2 {
		return records
	}
	seen := make(map[string]bool, len(records))
	result := make([]dns.RR, 0, len(records))
	for _, rr := range records {
		key := rr.String()
		if !seen[key] {
			seen[key] = true
			result = append(result, rr)
		}
	}
	return result
}

func keepDNSOPT(m *dns.Msg) {
	opt := m.IsEdns0()
	m.Extra = nil
	if opt != nil {
		m.Extra = []dns.RR{opt}
	}
}

// Never return a silently cut RRset or strip its required signatures. If a
// complete Answer+Authority cannot fit, return TC and let the client use TCP.
func fitDNSResponse(m *dns.Msg, w dns.ResponseWriter, size int) {
	if _, tcp := w.RemoteAddr().(*net.TCPAddr); tcp {
		if m.Len() > dns.MaxMsgSize {
			m.Rcode = dns.RcodeServerFailure
			m.Answer, m.Ns = nil, nil
			keepDNSOPT(m)
		}
		return
	}
	if m.Len() <= size {
		return
	}
	// RFC 9471 requires TC if in-domain referral glue does not fit.
	for _, rr := range m.Ns {
		ns, ok := rr.(*dns.NS)
		if !ok {
			continue
		}
		for _, extra := range m.Extra {
			if (extra.Header().Rrtype == dns.TypeA || extra.Header().Rrtype == dns.TypeAAAA) &&
				dns.IsSubDomain(dnsName(ns.Hdr.Name), dnsName(extra.Header().Name)) {
				m.Truncated = true
			}
		}
	}
	keepDNSOPT(m)
	if m.Len() > size {
		m.Answer, m.Ns = nil, nil
		m.Truncated = true
	}
}

func handleDNSNotify(m *dns.Msg, w dns.ResponseWriter) {
	peer := GetPeer(w)
	for _, query := range m.Question {
		zone := dnsCanonicalZone(query.Name)
		dnsLogPrintln("SERVER: Received notify from", peer.SrcIP, "for", zone)

		value, ok := zones.Load(zone)
		if !ok {
			m.Rcode = dns.RcodeRefused
			continue
		}
		cfg := value.(*ZoneConfig)
		allowed := false
		for _, master := range cfg.Masters {
			if sameIP(master, peer.SrcIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			m.Rcode = dns.RcodeRefused
			continue
		}

		m.Authoritative = true
		InitZoneTransfer(zone, peer.SrcIP)
	}
}
