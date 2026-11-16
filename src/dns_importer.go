package main

import "github.com/miekg/dns"

func ImportRR(input string) dns.RR {
	rr, err := dns.NewRR(input)
	if err != nil {
		dnsLogPrintln("IMPORTER ERROR: Error importing record", input, "-", err)
		return nil
	}
	return rr
}

func ImportSOA(zinfo *ZoneConfig) (*ZoneConfig, bool) {
	if zinfo.New == 1 {
		dnsLogPrintf("MEMMAP: Loaded empty zone %s; awaiting transfer\n", zinfo.Zone)
		return zinfo, true
	}

	soaFound := 0
	for _, rr := range zinfo.RRs {
		if rr == nil || rr.Header().Rrtype != dns.TypeSOA {
			continue
		}

		soa, ok := rr.(*dns.SOA)
		if !ok || dnsName(soa.Hdr.Name) != dnsName(zinfo.Zone) || soa.Hdr.Class != dns.ClassINET {
			continue
		}

		zinfo.Serial = soa.Serial
		zinfo.Refresh = soa.Refresh
		zinfo.Retry = soa.Retry
		zinfo.Expire = soa.Expire
		zinfo.MinTTL = soa.Minttl
		zinfo.New = 0
		soaFound++
	}

	if soaFound != 1 {
		dnsLogPrintln("IMPORTER ERROR: Zone", zinfo.Zone, "not imported - expected exactly one IN apex SOA record")
		return nil, false
	}

	dnsLogPrintf("MEMMAP: Loaded zone %s with serial %d; refresh %d sec; retry %d sec; expire %d sec; minttl %d",
		zinfo.Zone, zinfo.Serial, zinfo.Refresh, zinfo.Retry, zinfo.Expire, zinfo.MinTTL)
	return zinfo, true
}
