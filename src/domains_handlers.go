package main

import (
	"bufio"
	"fmt"
	"html"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func getDomains(w http.ResponseWriter, r *http.Request) {
	renderDomainList(w, r, "")
}

// renderDomainList draws the zone list. With the integrated DNS server it joins
// the control file with the live zone registry and sorts and filters the whole
// set before paging it; against an external BIND none of that is knowable in
// this process, so it falls back to the plain domain/primary listing.
func renderDomainList(w http.ResponseWriter, r *http.Request, message string) {

	r.ParseForm()

	page, _ := strconv.ParseInt(r.FormValue("page")[0:], 10, 64)

	if page == 0 {
		page = 1
	}

	search := strings.TrimSpace(r.FormValue("search"))

	if integratedDNSEnabled() == false {
		Domains, pages := scanAllDomains(page, search)

		var Output = Out{
			Domains:      Domains,
			Product:      VERSION,
			Pager:        buildPager(page, pages),
			Search:       html.EscapeString(search),
			ErrorMessage: message,
		}

		outputTemplate(w, "domains", Output)
		return
	}

	sortKey := validDomainSortKey(r.FormValue("sort"))
	order := "asc"
	if r.FormValue("order") == "desc" {
		order = "desc"
	}

	filters := domainFilters{
		Search: search,
		State:  validStateFilter(strings.ToLower(strings.TrimSpace(r.FormValue("state")))),
		Master: strings.TrimSpace(r.FormValue("master")),
		DNSSEC: validDNSSECFilter(strings.ToLower(strings.TrimSpace(r.FormValue("dnssec")))),
	}

	if filters.Master != "" {
		if canonical, ok := canonicalIP(filters.Master); ok {
			filters.Master = canonical
		} else {
			filters.Master = ""
		}
	}

	now := time.Now()

	rows := collectDomainRows(now)
	all := len(rows)

	rows = filterDomainRows(rows, filters)
	sortDomainRows(rows, sortKey, order == "desc", now)

	total := len(rows)
	first, last, pages := domainPageBounds(total, &page)

	view := DomainsView{
		Rows:    renderDomainRows(rows[first:last], now, filters),
		Columns: buildDomainColumns(sortKey, order, filters),
		Total:   total,
		All:     all,
		Filters: describeFilters(filters),
		ClearAt: domainListURL(sortKey, order, domainFilters{}),
		SelfURL: domainListURL(sortKey, order, filters),
		Sort:    sortKey,
		Order:   order,
	}

	var Output = Out{
		Product:      VERSION,
		Zones:        &view,
		Pager:        buildPager(page, pages),
		Search:       html.EscapeString(filters.Search),
		State:        html.EscapeString(filters.State),
		Master:       html.EscapeString(filters.Master),
		DNSSEC:       html.EscapeString(filters.DNSSEC),
		Sort:         sortKey,
		Order:        order,
		ErrorMessage: message,
	}

	outputTemplate(w, "domains", Output)
}

func buildPager(page int64, pages int) string {
	if pages <= 1 {
		return ""
	}

	var pager string

	for i := 1; i <= pages; i++ {
		selected := ""
		if int64(i) == page {
			selected = `selected="selected"`
		}
		pager += fmt.Sprintf(`<option %s value="%d">Page %d of %d</option>`, selected, i, i, pages)
	}

	return pager
}

func scanAllDomains(page int64, search string) (Domains []Domain, pages int) {

	// fmt.Println("Searching for", search)

	Output := new(Out)

	// items per page
	items := int64(50)

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	file, err := os.Open(Config.named_conf)
	if err != nil {
		controlWarn("can't read "+Config.named_conf, err)
		return Output.Domains, 0
	}
	defer file.Close()

	// One fixed pattern parses the line; the search term is then compared as a
	// plain substring. The term used to be compiled into the pattern, so a
	// dashboard search could alter or break the expression, and it could never
	// match a name in its native script.
	re := regexp.MustCompile(`^zone "(.+?)" .+masters \{ (.+?); \}.+?`)

	needle := strings.ToLower(strings.TrimSpace(search))

	i := int64(0)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {

		ret := re.FindStringSubmatch(scanner.Text())
		if len(ret) == 0 {
			continue
		}

		if domainNameContains(ret[1], needle) == false {
			continue
		}

		i++
		if i <= items*page && i >= (items*page)-items {
			Output.AddItemDomain(Domain{ret[1], ret[2]})
		}
	}

	if err := scanner.Err(); err != nil {
		controlWarn("can't scan "+Config.named_conf, err)
	}

	ppages := float64(i) / float64(items)

	// fmt.Printf("%d %.5f %d\n", i, Round(ppages, .0, 0), page)

	return Output.Domains, int(Round(ppages, .0, 0))

}

func (domain *Out) AddItemDomain(item Domain) []Domain {
	domain.Domains = append(domain.Domains, item)
	return domain.Domains
}

func getDomainsEdit(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	domain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))

	if validateDomainName(domain) == false {
		var Output = Out{Product: VERSION, CustomAction: "", CustomRow: "", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Input is not a domain name.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	if domain == "" {
		var Output = Out{Product: VERSION, CustomAction: "", CustomRow: "", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Empty domain name.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	_, master := DomainExist(domain)

	if master == "none" {
		var Output = Out{Product: VERSION, CustomAction: "", CustomRow: "", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">This domain name does not exist.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	var Output = Out{Product: VERSION, CustomAction: "edit", CustomRow: "yes", CustomName: domain, CustomValue: master, ErrorMessage: "Edit domain " + domain}

	outputTemplate(w, "domains", Output)

	return
}

func getDomainsAdd(w http.ResponseWriter, r *http.Request) {

	var Output = Out{Product: VERSION, CustomAction: "add", CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "Add new domain"}

	outputTemplate(w, "domains", Output)

	return
}

func getDomainsSave(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	domain := r.FormValue("CustomName")
	master := r.FormValue("CustomValue")
	action := r.FormValue("CustomAction")

	var ErrorString string

	// An added or edited zone is always stored in punycode; a name typed in its
	// native script is converted here.
	if converted, ok := validateDomainInput(domain); ok {
		domain = converted
	}

	if validateData(domain, "domain") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed Domain name.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	if canonical, ok := canonicalIP(master); ok {
		master = canonical
	} else {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed Master IP address.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	if validateData(action, "user") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed Action name.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	if domain == "" {
		ErrorString += "<b>Error</b>: Empty domain value<br>"
	}

	if master == "" {
		ErrorString += "<b>Error</b>: Empty master value<br>"
	}

	if action == "" {
		ErrorString += "<b>Error</b>: Empty action value<br>"
	}

	if ErrorString == "" {
		exists, emaster := DomainExist(domain)

		if exists == true && action == "add" {
			ErrorString = "<b>Error</b>: Add imposible. Domain <u>" + domain + "</u> already exist with primary <u>" + emaster + "</u>. Consider edit it."
		}

		if exists == false && action == "edit" {
			ErrorString = "<b>Error</b>: Edit impossible. Domain <u>" + domain + "</u> does not exist. Consider adding it."
		}

		if ErrorString == "" {
			switch action {
			case "add":
				AddDomain(domain, master)
				LogAction.Println("Domain " + domain + " added by " + getCurrentUser(r) + " with primary " + master + " [" + ClientIP(r.RemoteAddr) + "].")
				ErrorString = "Domain <u>" + domain + "</u> added with primary <u>" + master + "</u>."
			case "edit":
				DomainEdit(domain, master)
				LogAction.Println("Domain " + domain + " edited by " + getCurrentUser(r) + ", set primary " + master + " [" + ClientIP(r.RemoteAddr) + "].")
				ErrorString = "Domain <u>" + domain + "</u> saved with primary <u>" + master + "</u>."
			default:
				ErrorString = "<b>Error</b>: Unknown action."
			}

		}
	}

	var Output = Out{Product: VERSION, CustomAction: action, CustomRow: "yes", CustomName: domain, CustomValue: master, ErrorMessage: ErrorString}

	outputTemplate(w, "domains", Output)

	return

}

func getDomainsRemove(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	// Removal accepts either spelling so a legacy zone can be deleted as it is
	// declared; DomainExist() and DeleteFromFile() match both.
	domain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))

	if validateDomainName(domain) == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed Domain name.</div>"}
		outputTemplate(w, "domains", Output)
		return
	}

	var ErrorString string

	if domain == "" {
		ErrorString += "<b>Error</b>: Empty domain value<br>"
	}

	if ErrorString == "" {
		exists, master := DomainExist(domain)

		if exists == false {
			ErrorString = "<b>Error</b>: Remove impossible. Domain <u>" + domain + "</u> does not exist."
		}

		if ErrorString == "" {
			DomainDelete(domain, master)
			LogAction.Println("Domain " + domain + " removed with master " + master + " by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "]")
			ErrorString = "Domain <u>" + html.EscapeString(domain) + "</u> removed."
		}
	}

	// Back to the list rather than to an empty table: after a removal the next
	// thing anyone wants to see is what is left.
	renderDomainList(w, r, ErrorString)
}

// getDomainsReload forces a fresh transfer of one zone from its primary,
// regardless of the serial that primary advertises.
//
// The dashboard runs inside the daemon, so unlike "--domain reload" there is
// nothing to hand over: the zone registry is right here and the transfer is
// started directly.
func getDomainsReload(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	domain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))

	if validateDomainName(domain) == false {
		renderDomainList(w, r, "<div class=\"alert alert-danger\">Malformed domain name.</div>")
		return
	}

	exists, master := DomainExist(domain)
	if exists == false {
		renderDomainList(w, r, "<div class=\"alert alert-danger\">Reload impossible. Domain <u>"+
			html.EscapeString(domain)+"</u> does not exist.</div>")
		return
	}

	safe := html.EscapeString(domain)

	if integratedDNSEnabled() {
		zone := registryKey(domain)
		if _, loaded := zones.Load(zone); loaded {
			// Forced: a zone whose primary still advertises the same serial is
			// exactly the zone someone presses this button for.
			go ForceZoneTransfer(zone, master)
		} else {
			// Declared but never loaded. Publishing it first is what makes the
			// transfer possible at all.
			integratedDNSAddZone(domain, master)
		}

		LogAction.Println("Domain " + domain + " refetch requested from primary " + master +
			" by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "]")

		renderDomainList(w, r, "<div class=\"alert alert-info\">Refetch of <u>"+safe+
			"</u> from <u>"+html.EscapeString(master)+"</u> started. The table shows the result once the transfer ends.</div>")
		return
	}

	if Config.rndc_path == "none" {
		renderDomainList(w, r, "<div class=\"alert alert-danger\">RNDC is disabled, can't request a transfer.</div>")
		return
	}

	runRndcCommand("retransfer", domain)
	LogAction.Println("Domain " + domain + " retransfer requested from primary " + master +
		" by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "]")

	renderDomainList(w, r, "<div class=\"alert alert-info\">RNDC retransfer of <u>"+safe+"</u> requested.</div>")
}
