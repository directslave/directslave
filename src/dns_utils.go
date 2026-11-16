package main

import (
	"log"
	"net"
	"strconv"

	"github.com/miekg/dns"
)

// Peer describes a DNS transport peer.
type Peer struct {
	SrcIP   string
	SrcPort uint
}

func GetPeer(w dns.ResponseWriter) *Peer {
	peer := new(Peer)

	switch addr := w.RemoteAddr().(type) {
	case *net.UDPAddr:
		peer.SrcIP = addr.IP.String()
		peer.SrcPort = uint(addr.Port)
	case *net.TCPAddr:
		peer.SrcIP = addr.IP.String()
		peer.SrcPort = uint(addr.Port)
	default:
		if host, port, err := net.SplitHostPort(w.RemoteAddr().String()); err == nil {
			peer.SrcIP = host
			if p, err := strconv.Atoi(port); err == nil && p >= 0 {
				peer.SrcPort = uint(p)
			}
		}
	}

	return peer
}

func TypeToString(key uint16) string {
	if value, ok := dns.TypeToString[key]; ok {
		return value
	}
	return ""
}

func ClassToString(key uint16) string {
	if value, ok := dns.ClassToString[key]; ok {
		return value
	}
	return ""
}

func RcodeToString(key int) string {
	if value, ok := dns.RcodeToString[key]; ok {
		return value
	}
	return ""
}

func dnsLogPrintln(v ...interface{}) {
	log.Println(v...)
	if LogDns != nil {
		LogDns.Println(v...)
	}
}

func dnsLogPrintf(format string, v ...interface{}) {
	log.Printf(format, v...)
	if LogDns != nil {
		LogDns.Printf(format, v...)
	}
}
