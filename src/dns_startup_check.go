package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Number of consecutive unanswered SOA probes after which a primary is treated
// as unreachable. The rest of its zones are released immediately instead of
// spending one query timeout each.
const startupCheckMaxFailures = 5

type startupCheckTarget struct {
	zone   string
	master string
}

type startupCheckCounters struct {
	verified   int64
	refetching int64
	unreached  int64
}

// VerifyStoredZonesAtStartup confirms every zone restored from local storage
// against its primary before that zone is answered.
//
// DirectSlave may have been down while a zone changed on the primary; the
// refresh timer of a large zone can be hours away, so without this check the
// secondary would serve stale data for exactly as long. The check runs once,
// at start, and never on a SIGHUP reload of a running instance.
//
// The primaries are not flooded: zones are grouped by primary, every primary is
// probed by a single worker at no more than startup_check_rate queries per
// second, and startup_check_workers caps the number of probes in flight over
// all primaries. One cheap SOA query per zone is sent; an AXFR follows only for
// the zones that really drifted.
func VerifyStoredZonesAtStartup() {
	if startupCheckEnabled() == false {
		return
	}

	begin := time.Now()
	targets := collectUncheckedZones()

	total := 0
	for _, list := range targets {
		total += len(list)
	}

	if total == 0 {
		dnsLogPrintln("STARTUP CHECK: no stored zones to verify")
		return
	}

	// An absurd rate would turn the pacing interval into zero, which no ticker
	// accepts; a million probes per second is well past any real primary.
	rate := Config.startup_check_rate
	if rate <= 0 {
		rate = 20
	}
	if rate > 1000000 {
		rate = 1000000
	}

	workers := Config.startup_check_workers
	if workers <= 0 {
		workers = 20
	}

	slowest := 0
	for _, list := range targets {
		if len(list) > slowest {
			slowest = len(list)
		}
	}

	dnsLogPrintf("STARTUP CHECK: verifying %d stored zones on %d primaries; %d queries/sec per primary, %d probes in flight, ETA %s",
		total, len(targets), rate, workers, (time.Duration(slowest) * time.Second / time.Duration(rate)).Round(time.Second))

	if Config.startup_check_grace > 0 {
		grace := time.Duration(Config.startup_check_grace) * time.Second
		dnsLogPrintf("STARTUP CHECK: unverified zones will be answered from stored data after %s", grace)
		time.AfterFunc(grace, func() {
			releaseUncheckedZones("startup_check_grace reached")
		})
	}

	counters := new(startupCheckCounters)
	inFlight := make(chan struct{}, workers)
	group := new(sync.WaitGroup)

	for master, list := range targets {
		group.Add(1)
		go func(master string, list []startupCheckTarget) {
			defer group.Done()
			verifyPrimaryZones(master, list, rate, inFlight, counters)
		}(master, list)
	}

	group.Wait()

	dnsLogPrintf("STARTUP CHECK: finished in %s; %d zones in sync, %d refetching, %d left on stored data (primary unreachable)",
		time.Since(begin).Round(time.Millisecond),
		atomic.LoadInt64(&counters.verified),
		atomic.LoadInt64(&counters.refetching),
		atomic.LoadInt64(&counters.unreached))

	CallDNSProfiler()
}

// collectUncheckedZones groups every withheld zone by its primary.
func collectUncheckedZones() map[string][]startupCheckTarget {
	targets := make(map[string][]startupCheckTarget)

	zones.Range(func(key, value interface{}) bool {
		zinfo, ok := value.(*ZoneConfig)
		if ok == false || zinfo.Unchecked == false {
			return true
		}

		// A brand new zone is already waiting for its first AXFR and is
		// withheld by New == 1; probing its serial would be a wasted query.
		if zinfo.New == 1 || len(zinfo.Masters) == 0 {
			return true
		}

		master := zinfo.Masters[0]
		targets[master] = append(targets[master], startupCheckTarget{zone: zinfo.Zone, master: master})
		return true
	})

	return targets
}

// verifyPrimaryZones walks one primary's zones sequentially, so that primary
// never sees more than one probe at a time and no more than rate per second.
func verifyPrimaryZones(master string, list []startupCheckTarget, rate int, inFlight chan struct{}, counters *startupCheckCounters) {
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	failures := 0

	for index, target := range list {
		if failures >= startupCheckMaxFailures {
			remaining := list[index:]
			dnsLogPrintf("STARTUP CHECK WARNING: primary %s did not answer %d probes in a row; %d zones keep serving stored data",
				master, failures, len(remaining))
			reason := fmt.Sprintf("startup check: primary %s did not answer %d probes in a row", master, failures)
			for _, rest := range remaining {
				releaseAndRetry(rest.zone, reason)
				atomic.AddInt64(&counters.unreached, 1)
			}
			return
		}

		<-ticker.C

		inFlight <- struct{}{}
		outcome := verifyStoredZone(target.zone, target.master)
		<-inFlight

		switch outcome {
		case startupZoneInSync:
			failures = 0
			atomic.AddInt64(&counters.verified, 1)
		case startupZoneStale:
			failures = 0
			atomic.AddInt64(&counters.refetching, 1)
		default:
			failures++
			atomic.AddInt64(&counters.unreached, 1)
		}
	}
}

const (
	startupZoneInSync = iota
	startupZoneStale
	startupZoneUnreachable
)

// verifyStoredZone probes one zone. The stored copy is released for answering
// as soon as the primary confirms the serial; when the primary holds a newer
// one the zone stays withheld until the AXFR that is started here completes.
func verifyStoredZone(zone string, master string) int {
	value, ok := zones.Load(zone)
	if ok == false {
		return startupZoneInSync
	}

	zinfo := value.(*ZoneConfig)
	if zinfo.Unchecked == false {
		return startupZoneInSync
	}

	received, err := GetSerial(zone, master)
	if err != nil {
		dnsLogPrintf("STARTUP CHECK WARNING: %s primary %s - %s; answering the stored copy and scheduling a retry",
			zone, master, err)
		releaseAndRetry(zone, "startup check: "+err.Error())
		return startupZoneUnreachable
	}

	if received <= zinfo.Serial {
		dnsLogPrintf("STARTUP CHECK: %s primary %s - stored serial %d confirmed", zone, master, zinfo.Serial)
		markZoneChecked(zone)
		// The primary agreed with what we hold, which is what makes the zone
		// synced rather than merely answered.
		recordZoneProbe(zone)
		return startupZoneInSync
	}

	dnsLogPrintf("STARTUP CHECK: %s primary %s - stored serial %d, primary serial %d; zone drifted, refetching",
		zone, master, zinfo.Serial, received)

	// The serial is already known, so the AXFR skips its own probe. It runs in
	// the background under the transfer_queue limit while this worker moves on
	// to the next zone of the same primary.
	go ForceZoneTransfer(zone, master)

	return startupZoneStale
}

// releaseAndRetry answers a zone that could not be verified and arms the retry
// timer, so the check is repeated without blocking the rest of the startup.
func releaseAndRetry(zone string, reason string) {
	// Canonicalise like every other entry point: the callers pass names that are
	// already canonical, but a raw name here would silently do nothing at all.
	zone = dnsCanonicalZone(zone)

	value, ok := zones.Load(zone)
	if ok == false {
		return
	}

	// The reason has to be passed on: a startup check that could not confirm a
	// zone is exactly the case where the zone is served on trust, and it must
	// be counted as such rather than silently looking healthy.
	scheduleTransferRetryWithReason(zone, value.(*ZoneConfig), reason)
}
