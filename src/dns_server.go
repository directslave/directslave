package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"syscall"

	"github.com/miekg/dns"
)

// dnsEndpoint is one bound address: a UDP socket and a TCP listener on the same
// address and port, each with its own server.
type dnsEndpoint struct {
	addr    string
	udpConn net.PacketConn
	tcpLn   net.Listener
	udpSrv  *dns.Server
	tcpSrv  *dns.Server
}

func (endpoint *dnsEndpoint) close() {
	if endpoint.udpConn != nil {
		_ = endpoint.udpConn.Close()
	}
	if endpoint.tcpLn != nil {
		_ = endpoint.tcpLn.Close()
	}
}

type integratedDNSRuntime struct {
	endpoints []*dnsEndpoint
}

var (
	dnsRuntime   *integratedDNSRuntime
	dnsRuntimeMu sync.Mutex
)

// prepareIntegratedDNSServer binds UDP and TCP before privileges are dropped.
// This is required for the default port 53 on native Unix installations.
//
// Every configured address gets its own pair of sockets, IPv4 on udp4/tcp4 and
// IPv6 on udp6/tcp6. Binding is all or nothing: if one address the operator
// named cannot be bound, the ones already bound are closed and start-up fails,
// because a secondary that silently answers on half of its NS addresses is
// worse than one that does not start.
func prepareIntegratedDNSServer() error {
	if !integratedDNSEnabled() {
		return nil
	}

	dnsRuntimeMu.Lock()
	defer dnsRuntimeMu.Unlock()
	if dnsRuntime != nil {
		return nil
	}

	ip4, ip6, err := dnsListenFamilies()
	if err != nil {
		return err
	}

	port := dnsListenPort()
	handler := dns.HandlerFunc(handleDnsRequest)
	runtime := &integratedDNSRuntime{}

	fail := func(err error) error {
		for _, endpoint := range runtime.endpoints {
			endpoint.close()
		}
		return err
	}

	for _, family := range []dnsListenFamily{ip4, ip6} {
		for _, host := range family.hosts() {
			addr := net.JoinHostPort(host, port)

			endpoint, err := bindDNSEndpoint(family.network, addr, handler)
			if err != nil {
				// The one forgiving case: IPv6 was never asked for, only
				// defaulted to "*", and this host has no IPv6 at all. The old
				// single dual-stack socket started fine on such a host, so the
				// default must too.
				if family.network == "6" && family.any && family.explicit == false && noIPv6Here(err) {
					dnsLogPrintf("STARTUP: IPv6 is not available on this host (%s); DNS listens on IPv4 only", err)
					continue
				}
				return fail(err)
			}

			runtime.endpoints = append(runtime.endpoints, endpoint)
		}
	}

	if len(runtime.endpoints) == 0 {
		return fmt.Errorf("DNS: no listen address could be bound")
	}

	dnsRuntime = runtime
	return nil
}

func bindDNSEndpoint(family string, addr string, handler dns.Handler) (*dnsEndpoint, error) {
	udpConn, err := net.ListenPacket("udp"+family, addr)
	if err != nil {
		return nil, fmt.Errorf("DNS UDP bind %s failed: %w", addr, err)
	}

	tcpLn, err := net.Listen("tcp"+family, addr)
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("DNS TCP bind %s failed: %w", addr, err)
	}

	return &dnsEndpoint{
		addr:    addr,
		udpConn: udpConn,
		tcpLn:   tcpLn,
		udpSrv:  &dns.Server{PacketConn: udpConn, Handler: handler},
		tcpSrv:  &dns.Server{Listener: tcpLn, Handler: handler},
	}, nil
}

// noIPv6Here recognises "this kernel or jail has no IPv6", as opposed to an
// address that is in use or not configured on this host.
func noIPv6Here(err error) bool {
	return errors.Is(err, syscall.EAFNOSUPPORT) ||
		errors.Is(err, syscall.EPROTONOSUPPORT) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}

func startIntegratedDNSServer() {
	if !integratedDNSEnabled() {
		return
	}

	dnsRuntimeMu.Lock()
	runtime := dnsRuntime
	dnsRuntimeMu.Unlock()
	if runtime == nil {
		dnsLogPrintln("SERVER: integrated DNS was not prepared")
		return
	}

	initTransferQueue()

	// Load stored zones BEFORE the listeners start serving. The sockets are
	// already bound, so early queries wait in the kernel buffer instead of
	// getting REFUSED for zones that simply have not been parsed yet.
	ImportStoredZones()

	bound := make([]string, 0, len(runtime.endpoints))
	for _, endpoint := range runtime.endpoints {
		bound = append(bound, endpoint.addr)
	}
	dnsLogPrintf("STARTUP: Integrated authoritative DNS enabled on %s (UDP/TCP)", strings.Join(bound, ", "))
	if Config.dns_listen_legacy != "" {
		dnsLogPrintf("STARTUP WARNING: dns_listen is deprecated; replace \"dns_listen %s\" with dns_listen_ip4 / dns_listen_ip6",
			Config.dns_listen_legacy)
	}
	if Config.dns_expunge_expired == 1 {
		dnsLogPrintln("STARTUP: Expired zones will be expunged from the control file")
	}

	for _, endpoint := range runtime.endpoints {
		endpoint := endpoint
		go func() {
			if err := endpoint.udpSrv.ActivateAndServe(); err != nil {
				dnsLogPrintln("SERVER: UDP DNS on", endpoint.addr, "stopped:", err)
			}
		}()
		go func() {
			if err := endpoint.tcpSrv.ActivateAndServe(); err != nil {
				dnsLogPrintln("SERVER: TCP DNS on", endpoint.addr, "stopped:", err)
			}
		}()
	}

	// Zones restored from disk are withheld until their serial is confirmed
	// with the primary. The check runs in the background so the listeners are
	// responsive right away; a zone that is still unverified answers SERVFAIL.
	go VerifyStoredZonesAtStartup()
}

func reloadIntegratedDNSZones() {
	if !integratedDNSEnabled() {
		return
	}
	dnsLogPrintln("RELOAD: reloading integrated DNS zones from", Config.named_conf)
	ReloadStoredZones()
}

func shutdownIntegratedDNSServer() {
	if !integratedDNSEnabled() {
		return
	}

	dnsRuntimeMu.Lock()
	runtime := dnsRuntime
	dnsRuntime = nil
	dnsRuntimeMu.Unlock()
	if runtime == nil {
		return
	}

	unloadAllZones()
	for _, endpoint := range runtime.endpoints {
		_ = endpoint.udpSrv.Shutdown()
		_ = endpoint.tcpSrv.Shutdown()
		endpoint.close()
	}
}

func integratedDNSAddZone(domain, master string) {
	if !integratedDNSEnabled() {
		return
	}

	master = canonicalIPOrSame(master)
	zone := dnsCanonicalZone(domain)
	filename := Config.named_workdir + "/" + strings.TrimSuffix(zone, ".") + ".db"
	cfg := &ZoneConfig{
		Zone:    zone,
		Masters: []string{master},
		File:    filename,
		Refresh: uint32(Config.dns_new_refresh),
		Retry:   uint32(Config.dns_new_retry),
		Expire:  uint32(Config.dns_new_expire),
		New:     1,
	}
	Zwrite(cfg)
	go DoZoneTransfer(zone, master)
}

func integratedDNSReloadZone(domain, master string) {
	if !integratedDNSEnabled() {
		return
	}

	master = canonicalIPOrSame(master)
	zone := dnsCanonicalZone(domain)
	if value, ok := zones.Load(zone); ok {
		cfg := value.(*ZoneConfig)
		cfgCopy := *cfg
		cfgCopy.Masters = []string{master}
		zones.Store(zone, &cfgCopy)
		go DoZoneTransfer(zone, master)
		return
	}
	integratedDNSAddZone(domain, master)
}

func integratedDNSRemoveZone(domain string) {
	if !integratedDNSEnabled() {
		return
	}
	zone := dnsCanonicalZone(domain)
	filename := Config.named_workdir + "/" + strings.TrimSuffix(zone, ".") + ".db"
	if value, ok := zones.Load(zone); ok && value.(*ZoneConfig).File != "" {
		filename = value.(*ZoneConfig).File
	}
	stopAllZoneTimers(zone)
	Zdelete(zone)
	if filename != "" {
		if err := osRemoveZoneFile(filename); err != nil {
			dnsLogPrintln("STORAGE ERROR: cannot remove", filename, "-", err)
		}
	}
	dnsLogPrintln("MEMMAP: Unloaded zone", zone)
}
