package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

var (
	dnsQueryLogCh      chan string
	dnsQueryLogStop    chan struct{}
	dnsQueryLogDone    chan struct{}
	dnsQueryLogDropped uint64
)

func dnsQueryLogMode() string {
	return strings.ToLower(strings.TrimSpace(Config.dns_query_log))
}

func dnsQueryLogRequested() bool {
	switch dnsQueryLogMode() {
	case "all", "errors":
		return true
	default:
		return false
	}
}

func startDNSQueryLogger() {
	if !integratedDNSEnabled() {
		return
	}

	mode := dnsQueryLogMode()
	switch mode {
	case "", "off":
		return
	case "all", "errors":
		// valid
	default:
		if LogError != nil {
			LogError.Printf("DNS QUERY LOG: invalid mode %q; expected off, errors or all; disabled", Config.dns_query_log)
		}
		return
	}

	if getDNSQueryLogger() == nil {
		if LogError != nil {
			LogError.Printf("DNS QUERY LOG: %s requested, but log file is unavailable; disabled", mode)
		}
		return
	}

	if Config.dns_query_log_buffer <= 0 {
		Config.dns_query_log_buffer = 8192
	}

	dnsQueryLogCh = make(chan string, Config.dns_query_log_buffer)
	dnsQueryLogStop = make(chan struct{})
	dnsQueryLogDone = make(chan struct{})
	atomic.StoreUint64(&dnsQueryLogDropped, 0)

	go func() {
		defer close(dnsQueryLogDone)
		for {
			select {
			case line := <-dnsQueryLogCh:
				writeDNSQueryLogLine(line)
			case <-dnsQueryLogStop:
				for {
					select {
					case line := <-dnsQueryLogCh:
						writeDNSQueryLogLine(line)
					default:
						writeDNSQueryDropSummary()
						return
					}
				}
			}
		}
	}()

	dnsLogPrintf("STARTUP: DNS query log enabled: mode=%s file=%s buffer=%d", mode, Config.dns_query_log_file, Config.dns_query_log_buffer)
}

func stopDNSQueryLogger() {
	if dnsQueryLogStop == nil || dnsQueryLogDone == nil {
		return
	}

	close(dnsQueryLogStop)
	<-dnsQueryLogDone
	dnsQueryLogStop = nil
	dnsQueryLogDone = nil
	dnsQueryLogCh = nil
}

func writeDNSQueryLogLine(line string) {
	writeDNSQueryDropSummary()
	logger := getDNSQueryLogger()
	if logger != nil {
		logger.Println(line)
	}
}

func writeDNSQueryDropSummary() {
	dropped := atomic.SwapUint64(&dnsQueryLogDropped, 0)
	if dropped == 0 {
		return
	}
	logger := getDNSQueryLogger()
	if logger != nil {
		logger.Printf("QUERY_LOG: dropped=%d reason=buffer_full", dropped)
	}
}

func queueDNSQueryLog(w dns.ResponseWriter, r, response *dns.Msg, elapsed time.Duration, writeErr error) {
	mode := dnsQueryLogMode()
	if mode == "off" || mode == "" || dnsQueryLogCh == nil {
		return
	}

	isError := response.Rcode != dns.RcodeSuccess || writeErr != nil
	if mode == "errors" && !isError {
		return
	}
	if mode != "all" && mode != "errors" {
		return
	}

	peer := GetPeer(w)
	proto := "unknown"
	if addr := w.RemoteAddr(); addr != nil {
		proto = strings.ToLower(addr.Network())
		if strings.HasPrefix(proto, "udp") {
			proto = "udp"
		} else if strings.HasPrefix(proto, "tcp") {
			proto = "tcp"
		}
	}

	rcode := RcodeToString(response.Rcode)
	if rcode == "" {
		rcode = fmt.Sprintf("RCODE%d", response.Rcode)
	}
	aa := 0
	if response.Authoritative {
		aa = 1
	}
	writeStatus := "ok"
	if writeErr != nil {
		writeStatus = "error"
	}

	latencyUS := elapsed.Microseconds()
	if latencyUS < 0 {
		latencyUS = 0
	}

	if len(r.Question) == 0 {
		queueDNSQueryLogLine(fmt.Sprintf(
			"QUERY: proto=%s client=%s id=%d qname=- qtype=- qclass=- rcode=%s aa=%d zone=- answers=%d authority=%d latency_us=%d write=%s",
			proto, peer.SrcIP, r.Id, rcode, aa, len(response.Answer), len(response.Ns), latencyUS, writeStatus,
		))
		return
	}

	for _, q := range r.Question {
		name := dns.Fqdn(strings.ToLower(q.Name))
		qtype := TypeToString(q.Qtype)
		if qtype == "" {
			qtype = fmt.Sprintf("TYPE%d", q.Qtype)
		}
		qclass := ClassToString(q.Qclass)
		if qclass == "" {
			qclass = fmt.Sprintf("CLASS%d", q.Qclass)
		}

		zoneName := "-"
		if zone, ok := Zread(name); ok {
			zoneName = zone.Zone
		}

		queueDNSQueryLogLine(fmt.Sprintf(
			"QUERY: proto=%s client=%s id=%d qname=%s qtype=%s qclass=%s rcode=%s aa=%d zone=%s answers=%d authority=%d latency_us=%d write=%s",
			proto, peer.SrcIP, r.Id, name, qtype, qclass, rcode, aa, zoneName, len(response.Answer), len(response.Ns), latencyUS, writeStatus,
		))
	}
}

func queueDNSQueryLogLine(line string) {
	select {
	case dnsQueryLogCh <- line:
	default:
		atomic.AddUint64(&dnsQueryLogDropped, 1)
	}
}

// droppedQueryLogLines reports the query-log lines dropped since the last
// flush, for the statistics collector.
func droppedQueryLogLines() uint64 {
	return atomic.LoadUint64(&dnsQueryLogDropped)
}
