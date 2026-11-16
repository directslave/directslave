package main

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// The dashboard side of "--dump-db stats". The numbers come from the same
// collector, so a screenshot of this page and a paste of the CLI output can
// never disagree; what the page adds is that every counter is a link into the
// zone list filtered by that counter.

type StatsState struct {
	Name    string
	Count   int
	Meaning string
	URL     string
	Class   string
	Zero    bool
}

type StatsMaster struct {
	Master    string
	MasterURL string
	Zones     int
	Unsynced  int
	Awaiting  int
	Records   string
}

type StatsLine struct {
	Label string
	Value string
}

type StatsView struct {
	Version     string
	Taken       string
	Uptime      string
	Backend     string
	Declared    int
	Loaded      int
	Missing     int
	MissingText string
	MissingURL  string
	Signed      int
	SignedURL   string
	Partial     int
	PartialText string
	PartialURL  string
	AllURL      string
	States      []StatsState
	Lines       []StatsLine
	Masters     []StatsMaster
	Raw         string
}

func getStats(w http.ResponseWriter, r *http.Request) {
	if integratedDNSEnabled() == false {
		// Without the integrated server there is no zone registry in this
		// process, and BIND's own statistics channel is the right place to look.
		var Output = Out{
			Product: VERSION,
			ErrorMessage: "<div class=\"alert alert-info\">Zone statistics are collected by the integrated DNS server. " +
				"This instance runs against an external BIND, so the numbers live in BIND itself.</div>",
		}
		outputTemplate(w, "stats", Output)
		return
	}

	stats := CollectZoneStats(false)
	view := buildStatsView(stats)

	var Output = Out{Product: VERSION, Stats: &view}

	outputTemplate(w, "stats", Output)
}

func buildStatsView(stats ZoneStats) StatsView {
	view := StatsView{
		Version:  html.EscapeString(CURRENT),
		Taken:    stats.Taken.Format("2006-01-02 15:04:05"),
		Uptime:   shortDuration(stats.Uptime),
		Backend:  backendName(stats.Integrated),
		Declared: stats.Declared,
		Loaded:   stats.Loaded,
		Signed:   stats.Signed,
		AllURL:   domainListURL("", "", domainFilters{}),

		// Sorted so the signed zones are at the top of the list the counter
		// opens: on an estate that is mostly unsigned, an alphabetical list
		// would open on ten pages of zones the counter did not count.
		SignedURL: domainListURL("dnssec", "desc", domainFilters{DNSSEC: DNSSECSigned}),
	}

	if stats.SignedPartial > 0 {
		view.Partial = stats.SignedPartial
		view.PartialText = fmt.Sprintf("%d zone carries only half of its DNSSEC material", view.Partial)
		if view.Partial != 1 {
			view.PartialText = fmt.Sprintf("%d zones carry only half of their DNSSEC material", view.Partial)
		}
		view.PartialURL = domainListURL("", "", domainFilters{DNSSEC: DNSSECPartial})
	}

	if stats.Declared > stats.Loaded {
		view.Missing = stats.Declared - stats.Loaded
		view.MissingText = fmt.Sprintf("%d declared zone is not loaded", view.Missing)
		if view.Missing != 1 {
			view.MissingText = fmt.Sprintf("%d declared zones are not loaded", view.Missing)
		}
		view.MissingURL = domainListURL("", "", domainFilters{State: StateNotLoaded})
	}

	for _, state := range []string{StateSynced, StateUnsynced, StateAwaiting, StateUnverified, StateTransfering, StateEmpty} {
		count := stats.ByState[state]
		view.States = append(view.States, StatsState{
			Name:    state,
			Count:   count,
			Meaning: stateMeaning(state),
			URL:     domainListURL("", "", domainFilters{State: state}),
			Class:   statsStateClass(state, count),
			Zero:    count == 0,
		})
	}

	answered := stats.ByState[StateSynced] + stats.ByState[StateUnsynced]

	view.Lines = append(view.Lines,
		StatsLine{"answered", fmt.Sprintf("%d of %d loaded zones (%d in sync, %d not confirmed)",
			answered, stats.Loaded, stats.ByState[StateSynced], stats.ByState[StateUnsynced])},
		StatsLine{"records held", strconv.Itoa(stats.Records)},
	)

	if stats.Bytes > 0 {
		view.Lines = append(view.Lines, StatsLine{"last transfers", humanBytes(uint64(stats.Bytes)) + " received in total"})
	}

	if stats.OldestSync > 0 {
		view.Lines = append(view.Lines, StatsLine{"oldest confirmation", shortDuration(stats.OldestSync) + " ago"})
	} else {
		view.Lines = append(view.Lines, StatsLine{"oldest confirmation", "never (no primary has confirmed anything since start)"})
	}

	view.Lines = append(view.Lines,
		StatsLine{"transfers", fmt.Sprintf("%d running, queue %d of %d", stats.ActiveNow, stats.QueueDepth, stats.QueueLimit)},
		StatsLine{"timers", fmt.Sprintf("refresh %d, retry %d, expire %d",
			stats.Timers["refresh"], stats.Timers["retry"], stats.Timers["expire"])},
		StatsLine{"memory", fmt.Sprintf("heap %s in use, %s from OS, %s allocated since start",
			humanBytes(stats.HeapAlloc), humanBytes(stats.HeapSys), humanBytes(stats.TotalAlloc))},
		StatsLine{"goroutines", strconv.Itoa(stats.Goroutines)},
	)

	if stats.QueryLogged > 0 {
		view.Lines = append(view.Lines, StatsLine{"query log", fmt.Sprintf("%d lines dropped (buffer full)", stats.QueryLogged)})
	}

	for index := range view.Lines {
		view.Lines[index].Label = html.EscapeString(view.Lines[index].Label)
		view.Lines[index].Value = html.EscapeString(view.Lines[index].Value)
	}

	for _, master := range stats.Masters {
		view.Masters = append(view.Masters, StatsMaster{
			Master:    html.EscapeString(master.Master),
			MasterURL: domainListURL("", "", domainFilters{Master: master.Master}),
			Zones:     master.Zones,
			Unsynced:  master.Unsynced,
			Awaiting:  master.Awaiting,
			Records:   strconv.Itoa(master.Records),
		})
	}

	// The same text the CLI prints, for pasting into a ticket without having to
	// retype what the page already shows.
	var raw strings.Builder
	RenderStatsSummary(&raw, stats)
	view.Raw = html.EscapeString(raw.String())

	return view
}

// statsStateClass colours a counter only when it is non-zero: a page where
// every line is red teaches an operator to ignore red.
func statsStateClass(state string, count int) string {
	if count == 0 {
		return "ds-zero"
	}

	switch state {
	case StateUnsynced, StateEmpty:
		return "ds-bad"
	case StateAwaiting, StateUnverified:
		return "ds-warn"
	case StateTransfering:
		return "ds-busy"
	}

	return "ds-good"
}
