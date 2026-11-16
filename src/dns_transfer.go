package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var (
	transferQueueOnce sync.Once
	TransferQueue     chan struct{}
	activeTransfers   sync.Map
)

func initTransferQueue() {
	transferQueueOnce.Do(func() {
		depth := Config.transfer_queue
		if depth <= 0 {
			depth = 20
		}
		TransferQueue = make(chan struct{}, depth)
	})
}

// dnsCanonicalZone is the single form a zone is known by in memory, in timers
// and on the wire: lower case, punycode, fully qualified. Queries always arrive
// in that form, so a zone declared in its native script has to be converted
// here or it is never found.
func dnsCanonicalZone(zone string) string {
	zone = strings.ToLower(strings.TrimSpace(zone))

	if isASCIIName(zone) == false {
		if converted, err := ToPunycode(zone); err == nil {
			zone = converted
		}
	}

	return dns.Fqdn(zone)
}

func InitZoneTransfer(zone string, master string) {
	zone = dnsCanonicalZone(zone)
	zrec, ok := zones.Load(zone)
	if !ok {
		dnsLogPrintf("TRANSFER ERROR: notify %s from primary %s - zone is not served by us\n", zone, master)
		return
	}

	cfg := zrec.(*ZoneConfig)
	allowed := false
	for _, server := range cfg.Masters {
		if sameIP(server, master) {
			allowed = true
			break
		}
	}
	if !allowed {
		dnsLogPrintf("TRANSFER ERROR: notify %s primary %s - not allowed to transfer zone to us\n", zone, master)
		return
	}

	go DoZoneTransfer(zone, master)
}

func scheduleTransferRetry(zone string, zinfo *ZoneConfig) {
	scheduleTransferRetryWithReason(zone, zinfo, "")
}

func scheduleTransferRetryWithReason(zone string, zinfo *ZoneConfig, reason string) {
	if reason != "" {
		recordZoneFailure(zone, reason)
	}

	// The primary could not be reached or did not deliver a usable zone. A zone
	// that is still waiting for its startup check must not stay dark because of
	// that: a primary outage would otherwise become an outage of the secondary.
	// The stored copy is answered again and stays valid until its expire timer.
	if zinfo.Unchecked {
		if markZoneChecked(zone) {
			dnsLogPrintln("STARTUP CHECK WARNING:", zone, "not verified; answering the stored copy until expire")
		}
	}

	stopTimer(zone, "refresh")
	retry := int(zinfo.Retry)
	if retry <= 0 {
		retry = Config.dns_new_retry
	}
	setupTimer(zone, retry, "retry")
}

// DoZoneTransfer compares the SOA serial with the primary and transfers the
// zone only when the primary holds a newer one.
func DoZoneTransfer(zone string, master string) {
	doZoneTransfer(zone, master, false)
}

// ForceZoneTransfer always pulls the zone from the primary, regardless of the
// serial it advertises. It backs "--domain reload".
func ForceZoneTransfer(zone string, master string) {
	doZoneTransfer(zone, master, true)
}

func doZoneTransfer(zone string, master string, force bool) {
	initTransferQueue()
	zone = dnsCanonicalZone(zone)

	// A NOTIFY storm or a refresh timer should not launch overlapping AXFRs
	// for the same zone.
	if _, loaded := activeTransfers.LoadOrStore(zone, struct{}{}); loaded {
		dnsLogPrintln("TRANSFER: transfer already active for", zone, "- skipping duplicate trigger")
		return
	}
	defer activeTransfers.Delete(zone)

	TransferQueue <- struct{}{}
	defer func() { <-TransferQueue }()

	begin := time.Now()
	value, ok := zones.Load(zone)
	if !ok {
		dnsLogPrintf("TRANSFER ERROR: %s - zone is not served by us\n", zone)
		return
	}
	zinfo := value.(*ZoneConfig)
	filename := zinfo.File

	// A delete/edit can happen while a network operation is in progress. Keep
	// the pointer loaded at transfer start as a generation marker so a stale
	// transfer can never resurrect or overwrite a changed zone.
	isCurrent := func() bool {
		current, exists := zones.Load(zone)
		return exists && current.(*ZoneConfig) == zinfo
	}

	if force == false {
		received, err := GetSerial(zone, master)
		if err != nil {
			dnsLogPrintf("TRANSFER ERROR: %s primary %s response - %s; will retry in %d sec until expire in %d sec",
				zone, master, err, zinfo.Retry, zinfo.Expire)
			scheduleTransferRetryWithReason(zone, zinfo, "SOA probe: "+err.Error())
			return
		}

		if !isCurrent() {
			dnsLogPrintln("TRANSFER: stale transfer discarded for", zone)
			return
		}

		if received == zinfo.Serial && zinfo.New == 0 {
			dnsLogPrintf("TRANSFER: %s primary %s - SOA serial %d in sync with master %d; no need for transfer",
				zone, master, zinfo.Serial, received)

			// The stored copy has just been confirmed against the primary, so a
			// zone withheld by the startup check may be answered from now on.
			if zinfo.Unchecked {
				markZoneChecked(zone)
			}
			recordZoneProbe(zone)

			stopTimer(zone, "retry")
			refresh := int(zinfo.Refresh)
			if refresh <= 0 {
				refresh = Config.dns_new_refresh
			}
			expire := int(zinfo.Expire)
			if expire <= 0 {
				expire = Config.dns_new_expire
			}
			setupTimer(zone, refresh, "refresh")
			setupTimer(zone, expire, "expire")
			return
		}
		if zinfo.New == 0 && !dnsSerialNewer(received, zinfo.Serial) {
			scheduleTransferRetryWithReason(zone, zinfo, fmt.Sprintf("primary SOA serial %d is not newer than %d (RFC 1982)", received, zinfo.Serial))
			return
		}
	} else {
		dnsLogPrintf("TRANSFER: %s primary %s - forced refetch requested, skipping the serial check", zone, master)
	}

	dnsLogPrintf("TRANSFER: Transfer started for %s from %s; queue depth: %d", zone, master, len(TransferQueue))

	records, err := fetchAXFR(zone, net.JoinHostPort(master, "53"))
	if err != nil {
		dnsLogPrintf("TRANSFER ERROR: %s primary %s - %s", zone, master, err)
		scheduleTransferRetryWithReason(zone, zinfo, "AXFR: "+err.Error())
		return
	}

	if !isCurrent() {
		dnsLogPrintln("TRANSFER: stale AXFR result discarded for", zone)
		return
	}

	if filename == "" {
		filename = Config.named_workdir + "/" + strings.TrimSuffix(zone, ".") + ".db"
	}

	bytes := 0
	for _, rr := range records {
		bytes += len(rr.String()) + 1
	}

	now := time.Now()
	newcfg := &ZoneConfig{
		Zone:          zone,
		Masters:       []string{master},
		File:          filename,
		RRs:           records,
		LastConfirmed: now,
		LastTransfer:  now,
		LastAttempt:   now,
		TransferBytes: bytes,
	}
	if !Zwrite(newcfg) {
		dnsLogPrintln("TRANSFER ERROR:", zone, "received AXFR without a valid SOA")
		scheduleTransferRetryWithReason(zone, zinfo, "AXFR: no valid SOA in the answer")
		return
	}

	stopTimer(zone, "retry")
	if err := WriteZoneFile(zone, filename, records); err != nil {
		dnsLogPrintln("STORAGE ERROR: failed to persist", zone, "-", err)
	}

	dnsLogPrintf("TRANSFER: %s primary %s transfer finished; %d records (%d bytes) received in %s",
		zone, master, len(records), bytes, time.Since(begin))
}

// RFC 1982 serial arithmetic also handles the 2^32-1 -> 0 rollover.
func dnsSerialNewer(remote, local uint32) bool { return int32(remote-local) > 0 }

// AXFR includes DNSSEC records irrespective of DO. No records are filtered or
// re-signed. Check both SOA delimiters before removing the closing duplicate.
func fetchAXFR(zone, address string) ([]dns.RR, error) {
	message := new(dns.Msg)
	message.SetAxfr(zone)
	transfer := &dns.Transfer{DialTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Second}
	answer, err := transfer.In(message, address)
	if err != nil {
		if transfer.Conn != nil {
			_ = transfer.Close()
		}
		return nil, err
	}
	var records []dns.RR
	for envelope := range answer {
		if envelope.Error != nil {
			return nil, envelope.Error
		}
		records = append(records, envelope.RR...)
	}
	return validateAXFRRecords(zone, records)
}

func validateAXFRRecords(zone string, records []dns.RR) ([]dns.RR, error) {
	if len(records) < 2 {
		return nil, errors.New("missing AXFR SOA delimiters")
	}
	first, firstOK := records[0].(*dns.SOA)
	last, lastOK := records[len(records)-1].(*dns.SOA)
	if !firstOK || !lastOK || dnsName(first.Hdr.Name) != dnsName(zone) ||
		first.Hdr.Class != dns.ClassINET || !dns.IsDuplicate(first, last) || first.Hdr.Ttl != last.Hdr.Ttl {
		return nil, errors.New("AXFR SOA delimiters do not match the requested zone")
	}
	for _, rr := range records[1 : len(records)-1] {
		if rr == nil || rr.Header().Rrtype == dns.TypeSOA {
			return nil, errors.New("unexpected SOA/nil record inside AXFR")
		}
		if rr.Header().Class != dns.ClassINET || !dns.IsSubDomain(dnsName(zone), dnsName(rr.Header().Name)) {
			return nil, errors.New("AXFR contains a record outside the requested IN zone")
		}
	}
	return dns.Dedup(records[:len(records)-1], nil), nil
}

func GetSerial(message string, resolver string) (uint32, error) {
	serial, err := getSerialAt(dnsCanonicalZone(message), net.JoinHostPort(resolver, "53"))
	if err == nil {
		dnsLogPrintf("CLIENT: Found serial for %s on %s: %d", message, resolver, serial)
	}
	return serial, err
}

func getSerialAt(zone, address string) (uint32, error) {
	client := &dns.Client{Net: "udp", Timeout: 10 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion(zone, dns.TypeSOA)
	m.RecursionDesired = false
	r, _, err := client.Exchange(m, address)
	if err == nil && r != nil && r.Truncated {
		client.Net = "tcp"
		r, _, err = client.Exchange(m, address)
	}
	if err != nil {
		return 0, fmt.Errorf("connection error: %w", err)
	}
	if r == nil {
		return 0, errors.New("empty response")
	}
	if r.Rcode != dns.RcodeSuccess {
		return 0, errors.New(RcodeToString(r.Rcode))
	}
	if !r.Authoritative || r.Truncated {
		return 0, errors.New("SOA response is not complete and authoritative")
	}
	for _, rr := range r.Answer {
		if soa, ok := rr.(*dns.SOA); ok && soa.Hdr.Class == dns.ClassINET && dnsName(soa.Hdr.Name) == dnsName(zone) {
			return soa.Serial, nil
		}
	}
	return 0, errors.New("requested apex SOA record not found")
}
