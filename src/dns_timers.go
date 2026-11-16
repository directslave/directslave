package main

import (
	"strings"
	"sync"
	"time"
)

type zoneTimer struct {
	timer *time.Timer
	// deadline is kept because time.Timer does not expose when it will fire,
	// and "next refresh in 3h12m" is the single most useful line in a dump.
	deadline time.Time
}

var timers sync.Map

func timerKey(zone, purpose string) string {
	return zone + "|" + purpose
}

func setupTimer(zone string, timeout int, purpose string) {
	if timeout <= 0 {
		return
	}

	key := timerKey(zone, purpose)
	if old, ok := timers.LoadAndDelete(key); ok {
		old.(*zoneTimer).timer.Stop()
	}

	entry := &zoneTimer{deadline: time.Now().Add(time.Duration(timeout) * time.Second)}
	entry.timer = time.AfterFunc(time.Duration(timeout)*time.Second, func() {
		// A timer being replaced may already have become runnable. Only the
		// currently registered timer is allowed to execute the callback. Keep
		// the fired entry registered until the callback replaces/stops it; a
		// separate Delete here could race with a newly installed timer.
		current, ok := timers.Load(key)
		if !ok || current != entry {
			return
		}
		callRefresh(zone, purpose)
	})
	timers.Store(key, entry)

	dnsLogPrintln("TIMER:", strings.ToUpper(purpose), "timer started for", zone, "with", timeout, "secs")
}

func stopTimer(zone string, purpose string) {
	key := timerKey(zone, purpose)
	if value, ok := timers.LoadAndDelete(key); ok {
		value.(*zoneTimer).timer.Stop()
		dnsLogPrintln("TIMER:", strings.ToUpper(purpose), "timer stopped for", zone)
	}
}

// zoneTimerDeadline reports when a zone's timer of that purpose fires.
func zoneTimerDeadline(zone string, purpose string) (time.Time, bool) {
	if value, ok := timers.Load(timerKey(zone, purpose)); ok {
		return value.(*zoneTimer).deadline, true
	}
	return time.Time{}, false
}

func stopAllZoneTimers(zone string) {
	stopTimer(zone, "refresh")
	stopTimer(zone, "retry")
	stopTimer(zone, "expire")
}

func callRefresh(zone string, purpose string) {
	switch purpose {
	case "expire":
		dnsLogPrintln("TIMER: EXPIRE", zone, "zone expired")
		DropZone(zone)
	case "refresh", "retry":
		rec, ok := Zread(zone)
		if !ok || rec.Zone != dnsCanonicalZone(zone) || len(rec.Masters) == 0 {
			stopTimer(zone, purpose)
			return
		}
		dnsLogPrintln("TIMER:", strings.ToUpper(purpose), "called transfer for", zone)
		go DoZoneTransfer(zone, rec.Masters[0])
	}
}

func DropZone(zone string) {
	zone = dnsCanonicalZone(zone)
	dnsLogPrintln("EXPIRE: Zone", zone, "expired")

	// Capture file path before removing the in-memory record.
	var zoneFile string
	if zinfo, ok := zones.Load(zone); ok {
		zoneFile = zinfo.(*ZoneConfig).File
	}

	stopAllZoneTimers(zone)
	if Zdelete(zone) {
		dnsLogPrintln("MEMMAP: Unloaded zone", zone)
	}
	if zoneFile != "" {
		if err := osRemoveZoneFile(zoneFile); err != nil {
			dnsLogPrintln("STORAGE ERROR: cannot remove expired zone file", zoneFile, "-", err)
		}
	}

	if Config.dns_expunge_expired == 1 {
		if _, err := DeleteFromFile(strings.TrimSuffix(zone, ".")); err != nil {
			dnsLogPrintln("EXPIRE ERROR: cannot remove", zone, "from", Config.named_conf, "-", err)
		}
	}
}
