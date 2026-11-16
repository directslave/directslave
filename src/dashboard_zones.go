package main

import (
	"bufio"
	"html"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The dashboard list of zones. It joins the two things that together describe a
// secondary: the control file, which says what this server is supposed to hold,
// and the zone registry in memory, which says what it actually holds and how
// well that is going. Neither source alone can answer "is this zone healthy",
// so the page merges them.
//
// Everything here runs in the daemon's own process, so the registry is a map
// lookup away; no socket, no extra copy of the data.

// StateNotLoaded is not a state of a zone in memory: it is the absence of one.
// A zone is declared in the control file and the registry has never heard of
// it, which is what a pending reload or a failed load looks like.
const StateNotLoaded = "notloaded"

// A zone that is loaded but no longer declared. It still answers queries until
// the next reload, so hiding it would be the worst of both worlds.
const StateUndeclared = "undeclared"

var dashboardZoneRe = regexp.MustCompile(`^zone "(.+?)" .+masters \{ (.+?); \}.+?`)

// zoneRow carries the raw values, because sorting has to compare serials and
// instants rather than the strings the table shows.
type zoneRow struct {
	name     string
	master   string
	declared bool
	loaded   bool
	report   ZoneReport
}

// DomainRow is what the template prints. Every field is already escaped: the
// dashboard renders through text/template, which does not escape anything by
// itself, and a zone name or a primary's error text is not ours to trust.
type DomainRow struct {
	Name      string
	NameURL   string
	Master    string
	MasterURL string
	State     string
	StateURL  string
	StateHint string
	RowClass  string
	Serial    string
	Records   string
	DNSSEC    string
	DNSSECURL string
	DNSSECCls string
	Confirmed string
	Transfer  string
	Refresh   string
	Expires   string
	Fails     string
	FailClass string
	LastError string
	Loaded    bool
}

// DomainColumn is one sortable heading.
type DomainColumn struct {
	Key    string
	Title  string
	Href   string
	Arrow  string
	Active bool
	Align  string
}

// DomainsView is the whole enriched table. A nil pointer means the dashboard is
// running against an external BIND, where none of this is knowable, and the
// template falls back to the plain domain/master list.
type DomainsView struct {
	Rows    []DomainRow
	Columns []DomainColumn
	Total   int
	All     int
	Filters []DomainFilter
	ClearAt string
	SelfURL string
	Sort    string
	Order   string
}

// DomainFilter describes one active filter, so the page can say what it is
// showing and offer a way out of it.
type DomainFilter struct {
	Label string
	Value string
}

// domainFilters is the view an operator is currently looking at. It travels as
// one value through filtering, link building and the template, so adding an
// axis does not mean threading another argument through six functions.
type domainFilters struct {
	Search string
	State  string
	Master string
	DNSSEC string
}

// DNSSEC filter values. Signed and unsigned are the two halves of the estate;
// partial is the pathological case worth a link of its own.
const (
	DNSSECSigned   = "signed"
	DNSSECUnsigned = "unsigned"
	DNSSECPartial  = "partial"
)

// domainSortColumns is the column order of the table and, at the same time, the
// whitelist of sort keys: a key that is not here falls back to the zone name.
var domainSortColumns = []DomainColumn{
	{Key: "zone", Title: "Zone"},
	{Key: "state", Title: "State"},
	{Key: "master", Title: "Primary"},
	{Key: "serial", Title: "Serial", Align: "text-right"},
	{Key: "records", Title: "Records", Align: "text-right"},
	{Key: "dnssec", Title: "DNSSEC"},
	{Key: "confirmed", Title: "Confirmed", Align: "text-right"},
	{Key: "transfer", Title: "Transfer", Align: "text-right"},
	{Key: "refresh", Title: "Refresh", Align: "text-right"},
	{Key: "expires", Title: "Expires", Align: "text-right"},
	{Key: "fails", Title: "Fails", Align: "text-right"},
	{Key: "error", Title: "Last error"},
}

// maxDuration stands in for "no such moment". A zone that was never confirmed
// is infinitely stale, and a timer that is not armed fires infinitely far away;
// both then sort to the end of an ascending column instead of to the top.
const maxDuration = time.Duration(math.MaxInt64)

const domainsPerPage = 50

// domainPageBounds turns a requested page number into a slice of the result
// set. It clamps rather than trusts: the page number arrives in the query
// string, and a filter that shrinks the result can leave a bookmarked page
// number pointing past the end. page is adjusted to the page actually shown, so
// the pager highlights the right entry.
func domainPageBounds(total int, page *int64) (first int, last int, pages int) {
	pages = (total + domainsPerPage - 1) / domainsPerPage

	if *page < 1 || pages == 0 {
		*page = 1
	}
	if pages > 0 && *page > int64(pages) {
		*page = int64(pages)
	}

	first = int((*page - 1) * domainsPerPage)
	if first < 0 || first > total {
		first = 0
	}

	last = first + domainsPerPage
	if last > total {
		last = total
	}

	return first, last, pages
}

// collectDomainRows merges the control file with the zone registry.
func collectDomainRows(now time.Time) []zoneRow {
	reports := loadedZoneReports(now)
	rows := make([]zoneRow, 0, len(reports)+8)
	seen := make(map[string]bool, len(reports))

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	file, err := os.Open(Config.named_conf)
	if err != nil {
		controlWarn("can't read "+Config.named_conf, err)
	} else {
		defer file.Close()

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

		for scanner.Scan() {
			ret := dashboardZoneRe.FindStringSubmatch(scanner.Text())
			if len(ret) == 0 {
				continue
			}

			canonical := registryKey(ret[1])
			row := zoneRow{
				name:     strings.TrimSuffix(ret[1], "."),
				master:   ret[2],
				declared: true,
			}

			if report, ok := reports[canonical]; ok {
				row.report = report
				row.loaded = true
				if row.master == "" {
					row.master = report.Master
				}
			} else {
				row.report.State = StateNotLoaded
			}

			seen[canonical] = true
			rows = append(rows, row)
		}

		if err := scanner.Err(); err != nil {
			controlWarn("can't scan "+Config.named_conf, err)
		}
	}

	// Zones held in memory that the control file no longer declares. They keep
	// answering queries until the next reload, so leaving them out of the list
	// would hide exactly the zones an operator is looking for after an edit.
	for canonical, report := range reports {
		if seen[canonical] {
			continue
		}
		rows = append(rows, zoneRow{
			name:   strings.TrimSuffix(report.Zone, "."),
			master: report.Master,
			loaded: true,
			report: report,
		})
	}

	return rows
}

// registryKey is the exact spelling a zone is stored under in the registry.
// Zwrite runs both steps before publishing, so a lookup that skipped dnsName
// would miss any zone whose declared name needs escaping on the wire.
func registryKey(name string) string {
	return dnsName(dnsCanonicalZone(name))
}

func loadedZoneReports(now time.Time) map[string]ZoneReport {
	reports := make(map[string]ZoneReport)

	zones.Range(func(_, value interface{}) bool {
		zinfo, ok := value.(*ZoneConfig)
		if ok == false {
			return true
		}
		reports[zinfo.Zone] = describeZone(zinfo, now)
		return true
	})

	return reports
}

// rowState is the state the table shows, which for an undeclared zone is not
// the state of the copy in memory but the fact that it should not be there.
func rowState(row zoneRow) string {
	if row.declared == false && row.loaded {
		return StateUndeclared
	}
	return row.report.State
}

// rowDNSSEC is the DNSSEC axis of one row. A zone that is not loaded has no
// records here at all, so nothing can be said about it; it is left out of both
// the signed and the unsigned half rather than counted as unsigned.
func rowDNSSEC(row zoneRow) string {
	switch {
	case row.loaded == false:
		return ""
	case row.report.Signed:
		return DNSSECSigned
	case row.report.SignedPartial:
		return DNSSECPartial
	}
	return DNSSECUnsigned
}

func filterDomainRows(rows []zoneRow, filters domainFilters) []zoneRow {
	needle := strings.ToLower(strings.TrimSpace(filters.Search))
	state := strings.ToLower(strings.TrimSpace(filters.State))
	master := canonicalIPOrSame(strings.TrimSpace(filters.Master))
	dnssec := filters.DNSSEC

	if needle == "" && state == "" && master == "" && dnssec == "" {
		return rows
	}

	// A fresh slice rather than rows[:0]: filtering must not quietly rewrite the
	// caller's backing array, which a later reuse of the full list would then
	// read back as garbage.
	kept := make([]zoneRow, 0, len(rows))

	for _, row := range rows {
		if needle != "" && domainNameContains(row.name, needle) == false {
			continue
		}
		if state != "" && rowState(row) != state {
			continue
		}
		// Either source: the control file is the declaration and memory is what
		// the last transfer actually used, and after an edit that has not been
		// reloaded the two disagree. A filter that saw only one of them would
		// return nothing for exactly the zone worth looking at.
		if master != "" && sameIP(row.master, master) == false && sameIP(row.report.Master, master) == false {
			continue
		}
		if dnssec != "" && rowDNSSEC(row) != dnssec {
			continue
		}
		kept = append(kept, row)
	}

	return kept
}

// sortDomainRows orders the whole result set, not the visible page. Sorting one
// page of fifty would answer "which of these fifty zones fails most", and the
// question an operator actually has is "which zones fail most", so the sort has
// to happen before the slice into pages.
func sortDomainRows(rows []zoneRow, key string, desc bool, now time.Time) {
	compare := domainRowComparator(key, now)

	sort.SliceStable(rows, func(i, j int) bool {
		verdict := compare(rows[i], rows[j])
		if verdict == 0 {
			// The name is the tie break and stays ascending in both directions,
			// so a descending column does not scramble equal rows.
			return rows[i].name < rows[j].name
		}
		if desc {
			return verdict > 0
		}
		return verdict < 0
	})
}

func domainRowComparator(key string, now time.Time) func(a zoneRow, b zoneRow) int {
	switch key {
	case "state":
		return func(a, b zoneRow) int { return strings.Compare(rowState(a), rowState(b)) }
	case "master":
		return func(a, b zoneRow) int { return compareMasters(a.master, b.master) }
	case "serial":
		return func(a, b zoneRow) int { return compareUint(uint64(a.report.Serial), uint64(b.report.Serial)) }
	case "records":
		return func(a, b zoneRow) int { return compareInt(a.report.Records, b.report.Records) }
	case "dnssec":
		return func(a, b zoneRow) int { return compareInt(dnssecRank(a), dnssecRank(b)) }
	case "confirmed":
		return func(a, b zoneRow) int {
			return compareDuration(ageOf(a.report.LastConfirmed, now), ageOf(b.report.LastConfirmed, now))
		}
	case "transfer":
		return func(a, b zoneRow) int {
			return compareDuration(ageOf(a.report.LastTransfer, now), ageOf(b.report.LastTransfer, now))
		}
	case "refresh":
		return func(a, b zoneRow) int {
			return compareDuration(nextWakeup(a.report, now), nextWakeup(b.report, now))
		}
	case "expires":
		return func(a, b zoneRow) int {
			return compareDuration(untilOf(a.report.NextExpire, now), untilOf(b.report.NextExpire, now))
		}
	case "fails":
		return func(a, b zoneRow) int { return compareInt(a.report.Failures, b.report.Failures) }
	case "error":
		return func(a, b zoneRow) int { return strings.Compare(a.report.LastError, b.report.LastError) }
	}

	return func(a, b zoneRow) int { return strings.Compare(a.name, b.name) }
}

// compareMasters keeps primaries in numeric order, IPv4 before IPv6, so
// 192.0.2.9 comes before 192.0.2.154 instead of after it.
func compareMasters(a string, b string) int {
	left, leftOK := ipSortKey(a)
	right, rightOK := ipSortKey(b)

	if leftOK && rightOK {
		return strings.Compare(left, right)
	}

	return strings.Compare(a, b)
}

func ageOf(moment time.Time, now time.Time) time.Duration {
	if moment.IsZero() {
		return maxDuration
	}
	return now.Sub(moment)
}

func untilOf(moment time.Time, now time.Time) time.Duration {
	if moment.IsZero() {
		return maxDuration
	}
	return moment.Sub(now)
}

// nextWakeup is what the Refresh column shows: the retry timer when the zone is
// in trouble, the refresh timer otherwise.
func nextWakeup(report ZoneReport, now time.Time) time.Duration {
	if report.NextRetry.IsZero() == false {
		return report.NextRetry.Sub(now)
	}
	return untilOf(report.NextRefresh, now)
}

// dnssecRank orders the column: unknown, unsigned, half signed, signed.
func dnssecRank(row zoneRow) int {
	switch rowDNSSEC(row) {
	case DNSSECUnsigned:
		return 1
	case DNSSECPartial:
		return 2
	case DNSSECSigned:
		return 3
	}
	return 0
}

func compareInt(a int, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareUint(a uint64, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareDuration(a time.Duration, b time.Duration) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// renderDomainRows turns the page's slice of rows into printable, escaped text.
func renderDomainRows(rows []zoneRow, now time.Time, filters domainFilters) []DomainRow {
	out := make([]DomainRow, 0, len(rows))

	for _, row := range rows {
		current := rowState(row)
		dnssec := rowDNSSEC(row)

		// Each cell that is also a filter links to this same view with that one
		// axis set, so narrowing never costs the operator the rest of the view.
		byState := filters
		byState.State = current

		byMaster := filters
		byMaster.Master = row.master

		byDNSSEC := filters
		byDNSSEC.DNSSEC = dnssec

		item := DomainRow{
			Name:      html.EscapeString(row.name),
			NameURL:   url.QueryEscape(row.name),
			Master:    html.EscapeString(row.master),
			MasterURL: domainListURL("", "", byMaster),
			State:     html.EscapeString(current),
			StateURL:  domainListURL("", "", byState),
			StateHint: html.EscapeString(rowStateMeaning(current)),
			RowClass:  rowStateClass(current),
			LastError: html.EscapeString(row.report.LastError),
			Loaded:    row.loaded,
		}

		if row.loaded {
			item.Serial = strconv.FormatUint(uint64(row.report.Serial), 10)
			item.Records = strconv.Itoa(row.report.Records)
			item.Confirmed = agoOrNever(row.report.LastConfirmed, now)
			item.Transfer = agoOrNever(row.report.LastTransfer, now)
			item.Refresh = untilOrNone(row.report.NextRefresh, now)
			if row.report.NextRetry.IsZero() == false {
				// A retry pending is the more important of the two, and the "r"
				// says which timer the number belongs to.
				item.Refresh = "r" + untilOrNone(row.report.NextRetry, now)
			}
			item.Expires = untilOrNone(row.report.NextExpire, now)
			item.Fails = strconv.Itoa(row.report.Failures)
			if row.report.Failures > 0 {
				item.FailClass = "ds-fails"
			}

			item.DNSSEC = dnssecLabel(dnssec)
			item.DNSSECURL = domainListURL("", "", byDNSSEC)
			item.DNSSECCls = dnssecClass(dnssec)
		} else {
			item.Serial = "-"
			item.Records = "-"
			item.DNSSEC = "-"
			item.Confirmed = "-"
			item.Transfer = "-"
			item.Refresh = "-"
			item.Expires = "-"
			item.Fails = "-"
		}

		out = append(out, item)
	}

	return out
}

// dnssecLabel keeps the common case quiet: most zones are unsigned, and a
// column that repeats "unsigned" ten thousand times hides the ones that are not.
func dnssecLabel(dnssec string) string {
	switch dnssec {
	case DNSSECSigned:
		return "signed"
	case DNSSECPartial:
		return "half signed"
	}
	return "-"
}

func dnssecClass(dnssec string) string {
	switch dnssec {
	case DNSSECSigned:
		return "ds-signed"
	case DNSSECPartial:
		return "ds-fails"
	}
	return "ds-muted"
}

func rowStateMeaning(state string) string {
	switch state {
	case StateNotLoaded:
		return "declared in the control file but not present in memory; a reload is pending or it failed to load"
	case StateUndeclared:
		return "still answered from memory but no longer declared in the control file"
	}
	return stateMeaning(state)
}

// rowStateClass paints the row by how much attention it needs. Bootstrap's
// contextual classes are already in the stylesheet the dashboard ships.
func rowStateClass(state string) string {
	switch state {
	case StateUnsynced, StateEmpty, StateUndeclared:
		return "danger"
	case StateAwaiting, StateUnverified, StateNotLoaded:
		return "warning"
	case StateTransfering:
		return "info"
	}
	return ""
}

// domainListURL builds a link to the list with one thing changed and the rest
// of the current view preserved, so a state filter does not silently drop the
// search term the operator typed.
func domainListURL(sortKey string, order string, filters domainFilters) string {
	query := url.Values{}

	if sortKey != "" {
		query.Set("sort", sortKey)
	}
	if order != "" {
		query.Set("order", order)
	}
	if filters.Search != "" {
		query.Set("search", filters.Search)
	}
	if filters.State != "" {
		query.Set("state", filters.State)
	}
	if filters.Master != "" {
		query.Set("master", filters.Master)
	}
	if filters.DNSSEC != "" {
		query.Set("dnssec", filters.DNSSEC)
	}

	if len(query) == 0 {
		return "/dashboard/domains"
	}

	return "/dashboard/domains?" + query.Encode()
}

// buildDomainColumns makes every heading a link that sorts by that column,
// flipping the direction when the column is already the active one.
func buildDomainColumns(sortKey string, order string, filters domainFilters) []DomainColumn {
	columns := make([]DomainColumn, 0, len(domainSortColumns))

	for _, column := range domainSortColumns {
		next := "asc"
		column.Arrow = ""

		if column.Key == sortKey {
			column.Active = true
			if order == "desc" {
				column.Arrow = " ▼"
			} else {
				column.Arrow = " ▲"
				next = "desc"
			}
		} else {
			// Counters and ages are most useful largest first: nobody opens the
			// Fails column to find the zones that never failed.
			switch column.Key {
			case "records", "fails", "serial", "confirmed", "transfer", "dnssec":
				next = "desc"
			}
		}

		column.Href = domainListURL(column.Key, next, filters)
		column.Title = html.EscapeString(column.Title)
		columns = append(columns, column)
	}

	return columns
}

func validDomainSortKey(key string) string {
	for _, column := range domainSortColumns {
		if column.Key == key {
			return key
		}
	}
	return "zone"
}

// validStateFilter keeps an arbitrary query string out of the comparison and
// out of the page.
func validStateFilter(state string) string {
	switch state {
	case StateSynced, StateUnsynced, StateAwaiting, StateUnverified,
		StateTransfering, StateEmpty, StateNotLoaded, StateUndeclared:
		return state
	}
	return ""
}

// validDNSSECFilter keeps an arbitrary query string out of the comparison.
func validDNSSECFilter(dnssec string) string {
	switch dnssec {
	case DNSSECSigned, DNSSECUnsigned, DNSSECPartial:
		return dnssec
	}
	return ""
}

func describeFilters(filters domainFilters) []DomainFilter {
	described := make([]DomainFilter, 0, 4)

	if filters.Search != "" {
		described = append(described, DomainFilter{Label: "name contains", Value: html.EscapeString(filters.Search)})
	}
	if filters.State != "" {
		described = append(described, DomainFilter{Label: "state", Value: html.EscapeString(filters.State)})
	}
	if filters.Master != "" {
		described = append(described, DomainFilter{Label: "primary", Value: html.EscapeString(filters.Master)})
	}
	if filters.DNSSEC != "" {
		described = append(described, DomainFilter{Label: "dnssec", Value: html.EscapeString(filters.DNSSEC)})
	}

	return described
}
