package main

import (
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// ZoneConfig is the in-memory representation of an authoritative secondary zone.
// RRs is immutable after the structure is published in zones; a successful AXFR
// builds a new ZoneConfig and atomically replaces the old pointer.
type ZoneConfig struct {
	Zone    string
	Masters []string
	File    string
	Serial  uint32
	Refresh uint32
	Retry   uint32
	Expire  uint32
	MinTTL  uint32
	New     int
	RRs     []dns.RR
	index   *zoneIndex

	// Transfer bookkeeping. Without it "cannot get updates" is indistinguishable
	// from "refresh is simply not due yet": a zone that has been failing for a
	// week and one transferred a minute ago look identical otherwise.
	// LastConfirmed is the last time the PRIMARY agreed with what we hold:
	// either a successful AXFR, or a SOA probe that matched. A zone answered
	// from stored data that was never confirmed is not the same thing as a
	// zone in sync, and only this field can tell them apart.
	LastConfirmed time.Time
	LastTransfer  time.Time
	LastAttempt   time.Time
	LastError     string
	Failures      int
	TransferBytes int

	// Unchecked marks a zone that was loaded from local storage at startup and
	// whose serial has not been compared with the primary yet. Such a zone is
	// NOT answered: the stored copy may have gone stale while DirectSlave was
	// down. Only the startup check sets this flag, so every other code path
	// keeps the zero value and serves immediately, as before.
	Unchecked bool
}

var zones sync.Map

// Zread finds the longest served zone suffix for key.
func Zread(key string) (zinfo *ZoneConfig, ok bool) {
	name := dnsName(key)
	labels := dns.SplitDomainName(name)

	for len(labels) > 0 {
		domain := strings.Join(labels, ".") + "."
		if value, found := zones.Load(domain); found {
			return value.(*ZoneConfig), true
		}
		labels = labels[1:]
	}

	if value, found := zones.Load("."); found {
		return value.(*ZoneConfig), true
	}
	return nil, false
}

func Zwrite(zinfo *ZoneConfig) bool {
	zinfo.Zone = dnsName(dnsCanonicalZone(zinfo.Zone))
	zinfo, ok := ImportSOA(zinfo)
	if !ok {
		return false
	}

	if zinfo.New == 0 {
		idx, err := buildZoneIndex(zinfo)
		if err != nil {
			dnsLogPrintf("IMPORTER ERROR: Zone %s not imported: %s", zinfo.Zone, err)
			return false
		}
		zinfo.index = idx
	}

	// Publish first, then arm timers. This also makes very short test timers safe.
	zones.Store(zinfo.Zone, zinfo)

	refreshTime := zinfo.Refresh
	if refreshTime == 0 {
		refreshTime = uint32(Config.dns_new_refresh)
	}
	if zinfo.MinTTL > 0 && refreshTime < zinfo.MinTTL {
		refreshTime = zinfo.MinTTL
	}

	expireTime := zinfo.Expire
	if expireTime == 0 {
		expireTime = uint32(Config.dns_new_expire)
	}

	setupTimer(zinfo.Zone, int(refreshTime), "refresh")
	setupTimer(zinfo.Zone, int(expireTime), "expire")
	return true
}

// markZoneChecked publishes a zone that has been verified against its primary,
// or that may no longer be withheld. The record is replaced by an updated copy
// with a single compare-and-swap, so a concurrent transfer that already stored
// a newer ZoneConfig is never overwritten.
func markZoneChecked(zone string) bool {
	zone = dnsCanonicalZone(zone)

	value, ok := zones.Load(zone)
	if !ok {
		return false
	}

	zinfo := value.(*ZoneConfig)
	if zinfo.Unchecked == false {
		return false
	}

	updated := *zinfo
	updated.Unchecked = false

	return zones.CompareAndSwap(zone, zinfo, &updated)
}

// releaseUncheckedZones starts answering every zone that is still waiting for
// its startup check. It is used by the startup_check_grace deadline.
func releaseUncheckedZones(reason string) {
	released := 0

	zones.Range(func(key, value interface{}) bool {
		zinfo, ok := value.(*ZoneConfig)
		if ok == false || zinfo.Unchecked == false {
			return true
		}
		if markZoneChecked(zinfo.Zone) {
			released++
		}
		return true
	})

	if released > 0 {
		dnsLogPrintf("STARTUP CHECK: %s - %d unverified zones are now answered from stored data", reason, released)
	}
}

// updateZoneRecord applies a change to a zone's bookkeeping without touching
// its records. The pointer is swapped atomically, exactly like markZoneChecked,
// so a transfer that publishes a newer ZoneConfig is never overwritten.
func updateZoneRecord(zone string, change func(*ZoneConfig)) bool {
	zone = dnsCanonicalZone(zone)

	value, ok := zones.Load(zone)
	if ok == false {
		return false
	}

	current := value.(*ZoneConfig)
	updated := *current
	change(&updated)

	return zones.CompareAndSwap(zone, current, &updated)
}

// recordZoneFailure notes that the primary could not be reached or did not
// deliver a usable zone.
func recordZoneFailure(zone string, reason string) {
	updateZoneRecord(zone, func(zinfo *ZoneConfig) {
		zinfo.LastAttempt = time.Now()
		zinfo.LastError = reason
		zinfo.Failures++
	})
}

// recordZoneProbe notes a successful check that found nothing to transfer: the
// primary confirmed the serial we already hold.
func recordZoneProbe(zone string) {
	updateZoneRecord(zone, func(zinfo *ZoneConfig) {
		now := time.Now()
		zinfo.LastAttempt = now
		zinfo.LastConfirmed = now
		zinfo.LastError = ""
		zinfo.Failures = 0
	})
}

func Zdelete(domain string) bool {
	zone := dns.Fqdn(strings.ToLower(strings.TrimSpace(domain)))
	if _, ok := zones.Load(zone); ok {
		zones.Delete(zone)
		return true
	}
	return false
}

func unloadAllZones() {
	zones.Range(func(key, _ interface{}) bool {
		zone, _ := key.(string)
		stopAllZoneTimers(zone)
		zones.Delete(key)
		return true
	})
}
