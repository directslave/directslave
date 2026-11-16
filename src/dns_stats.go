package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The zone registry lives in the memory of the running instance, so these
// numbers cannot be derived from the control file or from the zone files on
// disk. dns_control.go exposes them over a local socket; the web dashboard
// will call the same collector in-process.

// A zone is either in sync with its primary or it is not, and that is the
// distinction an operator acts on. "Answered" is not the same thing: a copy
// stored in 2017 whose primary now answers REFUSED is answered, and is not in
// sync with anything.
const (
	StateSynced      = "synced"      // the primary confirmed what we hold
	StateUnsynced    = "unsynced"    // answered from stored data, never confirmed
	StateAwaiting    = "awaiting"    // declared but never transferred; not answered
	StateUnverified  = "unverified"  // startup check still pending; not answered
	StateTransfering = "transfering" // an AXFR is running right now
	StateEmpty       = "empty"       // published with no records: should not happen
)

type ZoneReport struct {
	Zone          string
	State         string
	Master        string
	Serial        uint32
	Records       int
	Bytes         int
	Refresh       uint32
	Retry         uint32
	Expire        uint32
	MinTTL        uint32
	LastConfirmed time.Time
	LastTransfer  time.Time
	LastAttempt   time.Time
	Failures      int
	LastError     string
	NextRefresh   time.Time
	NextRetry     time.Time
	NextExpire    time.Time
	Transferring  bool
	Unchecked     bool
	New           bool

	// DNSSEC, as the zone was actually received: Signed means the apex carries
	// both a DNSKEY set and a signature over its SOA. SignedPartial means it
	// carries one of the two, which is a zone that cannot work.
	Signed        bool
	SignedPartial bool
	File          string
	ExpiresWithin time.Duration
}

type MasterReport struct {
	Master   string
	Zones    int
	Unsynced int
	Awaiting int
	Records  int
}

type ZoneStats struct {
	Taken         time.Time
	Uptime        time.Duration
	Integrated    bool
	Declared      int
	Loaded        int
	Signed        int
	SignedPartial int
	ByState       map[string]int
	Records       int
	Bytes         int
	Masters       []MasterReport
	QueueDepth    int
	QueueLimit    int
	ActiveNow     int
	Timers        map[string]int
	OldestSync    time.Duration
	Zones         []ZoneReport
	HeapAlloc     uint64
	HeapSys       uint64
	TotalAlloc    uint64
	Goroutines    int
	QueryLogged   uint64
}

// CollectZoneStats walks the live registry. It takes no lock beyond what
// sync.Map provides, so it never blocks a query or a transfer.
func CollectZoneStats(withZones bool) ZoneStats {
	now := time.Now()

	stats := ZoneStats{
		Taken:      now,
		Uptime:     time.Since(dnsStartTime),
		Integrated: integratedDNSEnabled(),
		ByState:    make(map[string]int),
		Timers:     make(map[string]int),
		QueueLimit: Config.transfer_queue,
	}

	if TransferQueue != nil {
		stats.QueueDepth = len(TransferQueue)
	}

	activeTransfers.Range(func(_, _ interface{}) bool {
		stats.ActiveNow++
		return true
	})

	timers.Range(func(key, _ interface{}) bool {
		name, _ := key.(string)
		if index := strings.LastIndexByte(name, '|'); index >= 0 {
			stats.Timers[name[index+1:]]++
		}
		return true
	})

	perMaster := make(map[string]*MasterReport)

	zones.Range(func(key, value interface{}) bool {
		zinfo, ok := value.(*ZoneConfig)
		if ok == false {
			return true
		}

		report := describeZone(zinfo, now)

		stats.Loaded++
		stats.ByState[report.State]++
		if report.Signed {
			stats.Signed++
		}
		if report.SignedPartial {
			stats.SignedPartial++
		}
		stats.Records += report.Records
		stats.Bytes += report.Bytes

		if report.Master != "" {
			entry := perMaster[report.Master]
			if entry == nil {
				entry = &MasterReport{Master: report.Master}
				perMaster[report.Master] = entry
			}
			entry.Zones++
			entry.Records += report.Records
			if report.State == StateUnsynced {
				entry.Unsynced++
			}
			if report.State == StateAwaiting {
				entry.Awaiting++
			}
		}

		if report.LastConfirmed.IsZero() == false {
			if age := now.Sub(report.LastConfirmed); age > stats.OldestSync {
				stats.OldestSync = age
			}
		}

		if withZones {
			stats.Zones = append(stats.Zones, report)
		}
		return true
	})

	for _, entry := range perMaster {
		stats.Masters = append(stats.Masters, *entry)
	}
	sort.Slice(stats.Masters, func(i, j int) bool {
		if stats.Masters[i].Zones != stats.Masters[j].Zones {
			return stats.Masters[i].Zones > stats.Masters[j].Zones
		}
		return stats.Masters[i].Master < stats.Masters[j].Master
	})

	sort.Slice(stats.Zones, func(i, j int) bool { return stats.Zones[i].Zone < stats.Zones[j].Zone })

	stats.Declared = countDeclaredZones()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	stats.HeapAlloc = mem.HeapAlloc
	stats.HeapSys = mem.HeapSys
	stats.TotalAlloc = mem.TotalAlloc
	stats.Goroutines = runtime.NumGoroutine()
	stats.QueryLogged = droppedQueryLogLines()

	return stats
}

// countDeclaredZones counts the zone lines of the control file without the
// logging and without the cross-process lock that loadZonesChecked takes: a
// dump must never write to the DNS log or wait on a writer, and a count that
// is off by one during a rewrite does not matter.
func countDeclaredZones() int {
	file, err := os.Open(Config.named_conf)
	if err != nil {
		return 0
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		if declaredZoneRe.MatchString(scanner.Text()) {
			count++
		}
	}

	return count
}

var declaredZoneRe = regexp.MustCompile(`^zone "(.+?)".+file "(.+?)".+masters \{ (.+?); \};.+$`)

func describeZone(zinfo *ZoneConfig, now time.Time) ZoneReport {
	report := ZoneReport{
		Zone:          zinfo.Zone,
		Serial:        zinfo.Serial,
		Records:       len(zinfo.RRs),
		Bytes:         zinfo.TransferBytes,
		Refresh:       zinfo.Refresh,
		Retry:         zinfo.Retry,
		Expire:        zinfo.Expire,
		MinTTL:        zinfo.MinTTL,
		LastConfirmed: zinfo.LastConfirmed,
		LastTransfer:  zinfo.LastTransfer,
		LastAttempt:   zinfo.LastAttempt,
		Failures:      zinfo.Failures,
		LastError:     zinfo.LastError,
		Unchecked:     zinfo.Unchecked,
		New:           zinfo.New == 1,
		File:          zinfo.File,
	}

	if len(zinfo.Masters) > 0 {
		report.Master = zinfo.Masters[0]
	}

	if _, running := activeTransfers.Load(zinfo.Zone); running {
		report.Transferring = true
	}

	keys, sigs := zoneSigningState(zinfo)
	report.Signed = keys && sigs
	report.SignedPartial = keys != sigs

	if deadline, ok := zoneTimerDeadline(zinfo.Zone, "refresh"); ok {
		report.NextRefresh = deadline
	}
	if deadline, ok := zoneTimerDeadline(zinfo.Zone, "retry"); ok {
		report.NextRetry = deadline
	}
	if deadline, ok := zoneTimerDeadline(zinfo.Zone, "expire"); ok {
		report.NextExpire = deadline
		report.ExpiresWithin = deadline.Sub(now)
	}

	// One primary classification, most urgent first, so the counters add up to
	// the number of zones instead of double counting.
	switch {
	case report.Transferring:
		report.State = StateTransfering
	case report.New:
		report.State = StateAwaiting
	case report.Unchecked:
		report.State = StateUnverified
	case report.Records == 0:
		report.State = StateEmpty
	case report.LastConfirmed.IsZero() || report.Failures > 0:
		// Either the primary never confirmed this copy, or it has stopped
		// confirming it. Both mean the same thing to whoever is on call.
		report.State = StateUnsynced
	default:
		report.State = StateSynced
	}

	return report
}

func shortDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

func agoOrNever(moment time.Time, now time.Time) string {
	if moment.IsZero() {
		return "never"
	}
	return shortDuration(now.Sub(moment))
}

// sinceText is agoOrNever for prose, where "never ago" would read badly.
func sinceText(moment time.Time, now time.Time) string {
	if moment.IsZero() {
		return "never"
	}
	return shortDuration(now.Sub(moment)) + " ago"
}

func untilOrNone(moment time.Time, now time.Time) string {
	if moment.IsZero() {
		return "-"
	}
	return shortDuration(moment.Sub(now))
}

func humanBytes(value uint64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(value)/(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(value)/(1<<10))
	}
	return fmt.Sprintf("%d B", value)
}

// RenderStatsSummary writes the counters that answer "what is going on".
func RenderStatsSummary(out *strings.Builder, stats ZoneStats) {
	fmt.Fprintf(out, "DirectSlave %s\n", CURRENT)
	fmt.Fprintf(out, "taken        %s (uptime %s)\n", stats.Taken.Format("2006-01-02 15:04:05"), shortDuration(stats.Uptime))
	fmt.Fprintf(out, "backend      %s\n", backendName(stats.Integrated))
	fmt.Fprintln(out)

	fmt.Fprintf(out, "zones declared in control file  %d\n", stats.Declared)
	fmt.Fprintf(out, "zones loaded in memory          %d\n", stats.Loaded)
	fmt.Fprintf(out, "zones DNSSEC signed             %d\n", stats.Signed)
	if stats.SignedPartial > 0 {
		fmt.Fprintf(out, "  !! %d zones carry only half of their DNSSEC material (DNSKEY without an apex RRSIG, or the other way round)\n",
			stats.SignedPartial)
	}
	if stats.Declared != stats.Loaded {
		fmt.Fprintf(out, "  !! %d declared zones are not loaded; a reload is pending or they failed to load\n",
			stats.Declared-stats.Loaded)
	}
	fmt.Fprintln(out)

	for _, state := range []string{StateSynced, StateUnsynced, StateAwaiting, StateUnverified, StateTransfering, StateEmpty} {
		fmt.Fprintf(out, "  %-12s %6d   %s\n", state, stats.ByState[state], stateMeaning(state))
	}
	fmt.Fprintln(out)

	answered := stats.ByState[StateSynced] + stats.ByState[StateUnsynced]
	fmt.Fprintf(out, "answered     %d of %d loaded zones (%d in sync, %d not confirmed)\n",
		answered, stats.Loaded, stats.ByState[StateSynced], stats.ByState[StateUnsynced])
	fmt.Fprintf(out, "records held %d\n", stats.Records)
	if stats.Bytes > 0 {
		fmt.Fprintf(out, "last transfers %s received in total\n", humanBytes(uint64(stats.Bytes)))
	}
	if stats.OldestSync > 0 {
		fmt.Fprintf(out, "oldest confirmation %s ago\n", shortDuration(stats.OldestSync))
	} else {
		fmt.Fprintln(out, "oldest confirmation never (no primary has confirmed anything since start)")
	}
	fmt.Fprintf(out, "transfers    %d running, queue %d of %d\n", stats.ActiveNow, stats.QueueDepth, stats.QueueLimit)
	fmt.Fprintf(out, "timers       refresh %d, retry %d, expire %d\n",
		stats.Timers["refresh"], stats.Timers["retry"], stats.Timers["expire"])
	fmt.Fprintf(out, "memory       heap %s in use, %s from OS, %s allocated since start\n",
		humanBytes(stats.HeapAlloc), humanBytes(stats.HeapSys), humanBytes(stats.TotalAlloc))
	fmt.Fprintf(out, "goroutines   %d\n", stats.Goroutines)
	if stats.QueryLogged > 0 {
		fmt.Fprintf(out, "query log    %d lines dropped (buffer full)\n", stats.QueryLogged)
	}

	if len(stats.Masters) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "%-40s %8s %8s %8s %10s\n", "PRIMARY", "ZONES", "UNSYNCED", "AWAITING", "RECORDS")
		for _, master := range stats.Masters {
			fmt.Fprintf(out, "%-40s %8d %8d %8d %10d\n",
				master.Master, master.Zones, master.Unsynced, master.Awaiting, master.Records)
		}
	}
}

func backendName(integrated bool) string {
	if integrated {
		return "integrated DNS"
	}
	return "external (BIND/rndc)"
}

func stateMeaning(state string) string {
	switch state {
	case StateSynced:
		return "the primary confirmed this copy"
	case StateUnsynced:
		return "answered from stored data the primary has not confirmed"
	case StateAwaiting:
		return "declared, never transferred; not answered"
	case StateUnverified:
		return "restored from disk, serial not confirmed yet; not answered"
	case StateTransfering:
		return "an AXFR is running right now"
	case StateEmpty:
		return "published with no records"
	}
	return ""
}

// RenderStatsZones writes one line per zone, greppable and column aligned.
func RenderStatsZones(out *strings.Builder, stats ZoneStats) {
	now := stats.Taken

	fmt.Fprintf(out, "%-45s %-11s %-16s %-12s %7s %9s %9s %8s %8s %6s %s\n",
		"ZONE", "STATE", "PRIMARY", "SERIAL", "RECORDS", "CONFIRMED", "TRANSFER", "REFRESH", "EXPIRES", "FAILS", "LAST ERROR")

	for _, zone := range stats.Zones {
		next := untilOrNone(zone.NextRefresh, now)
		if zone.NextRetry.IsZero() == false {
			next = "r" + untilOrNone(zone.NextRetry, now)
		}

		fmt.Fprintf(out, "%-45s %-11s %-16s %-12d %7d %9s %9s %8s %8s %6d %s\n",
			strings.TrimSuffix(zone.Zone, "."),
			zone.State,
			zone.Master,
			zone.Serial,
			zone.Records,
			agoOrNever(zone.LastConfirmed, now),
			agoOrNever(zone.LastTransfer, now),
			next,
			untilOrNone(zone.NextExpire, now),
			zone.Failures,
			zone.LastError)
	}
}
