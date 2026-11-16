package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A local control socket, opened next to the pid file when the integrated DNS
// server is enabled. The zone registry lives in the memory of the running
// instance, so a separate CLI process has no other way to see it.
//
// The socket is a plain line protocol, one command per line:
//
//	stats          counters only
//	dump           counters plus one line per zone
//	zone <name>    everything known about one zone
//	ping           "pong", for a liveness check
//	quit           close
//
// It is a unix socket with mode 0600 owned by the DirectSlave uid, so the file
// permissions are the access control: nothing is exposed on the network, and
// no credentials have to be shared with the CLI.

const controlSocketTimeout = 30 * time.Second

var controlListener net.Listener

func controlSocketPath() string {
	pid := strings.TrimSpace(Config.pid)
	if pid == "" {
		return ""
	}

	directory := filepath.Dir(pid)
	name := strings.TrimSuffix(filepath.Base(pid), ".pid")

	return filepath.Join(directory, name+".sock")
}

// startControlSocket serves the socket until shutdown. A leftover socket from a
// crashed instance is removed first: the pid check has already established that
// no other instance is running.
func startControlSocket() {
	if integratedDNSEnabled() == false {
		return
	}

	path := controlSocketPath()
	if path == "" {
		dnsLogPrintln("CONTROL: no pid path configured; control socket disabled")
		return
	}

	if err := os.Remove(path); err != nil && os.IsNotExist(err) == false {
		dnsLogPrintln("CONTROL ERROR: cannot remove the stale socket", path, "-", err)
		return
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		dnsLogPrintln("CONTROL ERROR: cannot open", path, "-", err)
		return
	}

	if err := os.Chmod(path, 0600); err != nil {
		dnsLogPrintln("CONTROL ERROR: cannot set permissions on", path, "-", err)
	}

	controlListener = listener
	dnsLogPrintln("CONTROL: control socket listening on", path)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				// Accept fails for good on shutdown; nothing to report.
				return
			}
			go serveControlConn(conn)
		}
	}()
}

func stopControlSocket() {
	if controlListener == nil {
		return
	}

	_ = controlListener.Close()
	controlListener = nil

	if path := controlSocketPath(); path != "" {
		_ = os.Remove(path)
	}
}

func serveControlConn(conn net.Conn) {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(controlSocketTimeout))

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	defer writer.Flush()

	for {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return
		}

		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			if err != nil {
				return
			}
			continue
		}

		switch strings.ToLower(fields[0]) {
		case "ping":
			fmt.Fprintln(writer, "pong")
		case "stats":
			var out strings.Builder
			RenderStatsSummary(&out, CollectZoneStats(false))
			writer.WriteString(out.String())
		case "dump":
			var out strings.Builder
			stats := CollectZoneStats(true)
			RenderStatsSummary(&out, stats)
			out.WriteString("\n")
			RenderStatsZones(&out, stats)
			writer.WriteString(out.String())
		case "zone":
			if len(fields) < 2 {
				fmt.Fprintln(writer, "ERROR: zone <name>")
				break
			}
			writer.WriteString(renderOneZone(fields[1]))
		case "quit", "exit":
			writer.Flush()
			return
		default:
			fmt.Fprintf(writer, "ERROR: unknown command %q; try stats, dump, zone <name>, ping, quit\n", fields[0])
		}

		writer.Flush()

		if err != nil {
			return
		}
	}
}

func renderOneZone(name string) string {
	zone := registryKey(name)

	value, ok := zones.Load(zone)
	if ok == false {
		return "ERROR: " + zone + " is not loaded\n"
	}

	now := time.Now()
	report := describeZone(value.(*ZoneConfig), now)

	var out strings.Builder
	fmt.Fprintf(&out, "zone          %s\n", report.Zone)
	fmt.Fprintf(&out, "state         %s (%s)\n", report.State, stateMeaning(report.State))
	fmt.Fprintf(&out, "primary       %s\n", report.Master)
	fmt.Fprintf(&out, "serial        %d\n", report.Serial)
	fmt.Fprintf(&out, "records       %d\n", report.Records)
	switch {
	case report.Signed:
		fmt.Fprintln(&out, "dnssec        signed (apex DNSKEY and a signature over the SOA)")
	case report.SignedPartial:
		fmt.Fprintln(&out, "dnssec        HALF SIGNED: one of the apex DNSKEY set and the SOA signature is missing")
	default:
		fmt.Fprintln(&out, "dnssec        unsigned")
	}
	if report.Bytes > 0 {
		fmt.Fprintf(&out, "last payload  %s\n", humanBytes(uint64(report.Bytes)))
	}
	fmt.Fprintf(&out, "soa timers    refresh %d, retry %d, expire %d, minttl %d\n",
		report.Refresh, report.Retry, report.Expire, report.MinTTL)
	fmt.Fprintf(&out, "confirmed     %s\n", sinceText(report.LastConfirmed, now))
	fmt.Fprintf(&out, "last transfer %s\n", sinceText(report.LastTransfer, now))
	fmt.Fprintf(&out, "last attempt  %s\n", sinceText(report.LastAttempt, now))
	fmt.Fprintf(&out, "failures      %d\n", report.Failures)
	if report.LastError != "" {
		fmt.Fprintf(&out, "last error    %s\n", report.LastError)
	}
	fmt.Fprintf(&out, "next refresh  %s\n", untilOrNone(report.NextRefresh, now))
	fmt.Fprintf(&out, "next retry    %s\n", untilOrNone(report.NextRetry, now))
	fmt.Fprintf(&out, "expires in    %s\n", untilOrNone(report.NextExpire, now))
	fmt.Fprintf(&out, "zone file     %s\n", report.File)

	return out.String()
}

// askControlSocket sends one command and copies the answer back. It is the CLI
// side: a separate process cannot read the zone registry any other way.
func askControlSocket(command string) (string, error) {
	path := controlSocketPath()
	if path == "" {
		return "", fmt.Errorf("no pid path configured, so no control socket path")
	}

	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(controlSocketTimeout))

	if _, err := conn.Write([]byte(command + "\nquit\n")); err != nil {
		return "", err
	}

	var out strings.Builder
	buffer := make([]byte, 32*1024)
	for {
		read, err := conn.Read(buffer)
		if read > 0 {
			out.Write(buffer[:read])
		}
		if err != nil {
			break
		}
	}

	return out.String(), nil
}
