package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
)

// Exit codes for the --domain family.
// 0   - action done
// 1   - action refused for a logical reason (not found / already exists)
// 200 - malformed or missing arguments, same class as --password/--delete
// 255 - control file or zone directory is unusable
const (
	exitDomainOK       = 0
	exitDomainRefused  = 1
	exitDomainBadInput = 200
	exitDomainIOError  = 255
)

// namedConfEntry is one parsed slave zone line of Config.named_conf.
type namedConfEntry struct {
	Name   string
	Master string
	File   string
}

// cliZoneLineRe is kept identical to the pattern used by the web dashboard so
// that the CLI always reports exactly the same set of zones the UI shows.
var cliZoneLineRe = regexp.MustCompile(`^zone "(.+?)" .+masters \{ (.+?); \}.+?`)

// cliZoneFileRe pulls the zone file out of the same line when it is present.
var cliZoneFileRe = regexp.MustCompile(`file "(.+?)"`)

// validSearchPattern limits a search pattern to characters that may legally
// appear in a domain name, in any script. The pattern is matched as a plain
// substring, never as a regular expression, so no user input ever reaches the
// regexp engine.
func validSearchPattern(pattern string) bool {
	if pattern == "" {
		return false
	}

	for _, value := range pattern {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			continue
		}
		if value == '.' || value == '-' || value == '_' {
			continue
		}
		return false
	}

	return true
}

func PrintDomainUsage() {
	fmt.Println(VERSION + " " + COPYRIGHT)
	fmt.Println("Usage:")
	fmt.Println("--domain search <domain>           - Search zones by full or partial name, prints domain:primary")
	fmt.Println("--domain add    <domain> <primary> - Add zone with primary IP address")
	fmt.Println("--domain delete <domain>           - Delete zone and its zone file")
	fmt.Println("--domain reload <domain>           - Force a transfer of the zone from its primary")
	os.Exit(exitDomainBadInput)
}

// DomainCommand implements "--domain <search|add|delete> ...".
// All changes are written straight into the control file; the running instance
// is then asked to reload through its own internal mechanism.
func DomainCommand() {
	if len(os.Args) < 3 {
		PrintDomainUsage()
	}

	switch strings.ToLower(os.Args[2]) {
	case "search":
		DomainSearchCLI(domainArg(3, "No domain name, please input a full or partial domain name."))
	case "add":
		domain := domainArg(3, "No domain name, please input a domain name.")
		master := domainArg(4, "No primary, please input the primary IP address.")
		DomainAddCLI(domain, master)
	case "delete":
		DomainDeleteCLI(domainArg(3, "No domain name, please input a domain name."))
	case "reload":
		DomainReloadCLI(domainArg(3, "No domain name, please input a domain name."))
	default:
		PrintDomainUsage()
	}
}

func domainArg(index int, message string) string {
	if len(os.Args) <= index {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(exitDomainBadInput)
	}

	value := strings.TrimSpace(os.Args[index])
	if value == "" {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(exitDomainBadInput)
	}

	return value
}

// DomainSearchCLI prints every zone whose name contains the pattern, one
// "domain:primary" pair per line. Output is streamed, so a control file with
// hundreds of thousands of zones costs no extra memory.
func DomainSearchCLI(pattern string) {
	if len(pattern) > 253 || validSearchPattern(pattern) == false {
		fmt.Fprintln(os.Stderr, "Malformed search pattern.")
		os.Exit(exitDomainBadInput)
	}

	needle := strings.ToLower(strings.TrimSuffix(pattern, "."))

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	file, err := os.Open(Config.named_conf)
	if err != nil {
		// A control file that was never created simply holds no zones.
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No domains found for "+pattern+".")
			os.Exit(exitDomainRefused)
		}
		fmt.Fprintln(os.Stderr, "Can't read "+Config.named_conf+":", err)
		os.Exit(exitDomainIOError)
	}
	defer file.Close()

	found := 0
	writer := bufio.NewWriter(os.Stdout)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		ret := cliZoneLineRe.FindStringSubmatch(scanner.Text())
		if len(ret) == 0 {
			continue
		}

		// Any spelling of the name matches any spelling of the term.
		if domainNameContains(ret[1], needle) == false {
			continue
		}

		fmt.Fprintf(writer, "%s:%s\n", ret[1], ret[2])
		found++
	}

	writer.Flush()

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Can't read "+Config.named_conf+":", err)
		os.Exit(exitDomainIOError)
	}

	if found == 0 {
		fmt.Fprintln(os.Stderr, "No domains found for "+pattern+".")
		os.Exit(exitDomainRefused)
	}

	os.Exit(exitDomainOK)
}

func DomainAddCLI(domain string, master string) {
	// A name in its native script is accepted and stored in punycode, which is
	// how the primary serves it and how every query asks for it.
	typed := domain

	domain, ok := validateDomainInput(domain)
	if ok == false {
		fmt.Fprintln(os.Stderr, "Malformed domain name.")
		os.Exit(exitDomainBadInput)
	}

	if domain != strings.ToLower(strings.TrimSuffix(typed, ".")) {
		fmt.Println("Converted " + typed + " to " + domain + ".")
	}

	if canonical, ok := canonicalIP(master); ok {
		master = canonical
	} else {
		fmt.Fprintln(os.Stderr, "Malformed primary IP address.")
		os.Exit(exitDomainBadInput)
	}

	entry, exist := lookupDomainEntryAnyForm(domain)
	if exist == true {
		fmt.Fprintln(os.Stderr, "Domain already exist with "+entry.Master)
		if entry.Name != domain {
			fmt.Fprintln(os.Stderr, "It is declared as "+entry.Name+".")
		}
		os.Exit(exitDomainRefused)
	}

	setupCLILogging()

	if err := AddToFile(domain, master); err != nil {
		fmt.Fprintln(os.Stderr, "Can't write "+Config.named_conf+":", err)
		LogError.Println("Direct changes: Domain " + domain + " not added: " + err.Error())
		os.Exit(exitDomainIOError)
	}

	confirmDomainState(domain, true)

	LogAction.Println("Direct changes: Domain " + domain + " added directly with primary " + master)
	fmt.Println("Domain " + domain + " added with primary " + master + ".")

	reloadAfterDirectChange()
	os.Exit(exitDomainOK)
}

func DomainDeleteCLI(domain string) {
	// Never converted, and never traded for the other spelling: the operator
	// deletes the declaration they typed. A zone written in its native script
	// is deleted by that name, one written in punycode by that one, and
	// "--domain search" prints which of the two the control file holds.
	domain = normalizeDomainSpelling(domain)

	if validateDomainName(domain) == false {
		fmt.Fprintln(os.Stderr, "Malformed domain name.")
		os.Exit(exitDomainBadInput)
	}

	entry, exist := lookupDomainEntry(domain)
	if exist == false {
		fmt.Fprintln(os.Stderr, "Domain "+domain+" does not exist.")
		os.Exit(exitDomainRefused)
	}

	setupCLILogging()

	// Exactly the declaration that was found, and nothing else: the same zone
	// may also be declared in its other spelling, and that one is not touched.
	removed, err := DeleteFromFileExact(entry.Name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Can't write "+Config.named_conf+":", err)
		LogError.Println("Direct changes: Domain " + entry.Name + " not removed: " + err.Error())
		os.Exit(exitDomainIOError)
	}

	if removed == 0 {
		// The zone was there a moment ago, so someone else is writing the file.
		fmt.Fprintln(os.Stderr, "Domain "+entry.Name+" is no longer declared in "+Config.named_conf+", nothing removed.")
		os.Exit(exitDomainRefused)
	}

	confirmDomainState(entry.Name, false)

	// Only once the declaration is really gone; otherwise a failed rewrite would
	// leave a zone that is declared but has no data.
	deleteZoneFileForEntry(entry)

	LogAction.Println("Direct changes: Domain " + entry.Name + " removed directly from " + entry.Master)
	fmt.Println("Domain " + entry.Name + " removed.")

	reloadAfterDirectChange()
	os.Exit(exitDomainOK)
}

// confirmDomainState re-reads the control file after a write and checks that it
// really says what the command just claimed. A rewrite that cannot be completed
// - an unwritable directory, a rename that fails, another process putting the
// entry back - used to be reported as success, so the operator learned about it
// only from the next reload.
func confirmDomainState(domain string, wantDeclared bool) {
	entry, declared := lookupDomainEntry(domain)
	if declared == wantDeclared {
		return
	}

	if wantDeclared {
		fmt.Fprintln(os.Stderr, "Domain "+domain+" is not declared in "+Config.named_conf+" after the write.")
		LogError.Println("Direct changes: Domain " + domain + " did not appear in " + Config.named_conf)
	} else {
		fmt.Fprintln(os.Stderr, "Domain "+domain+" is STILL declared in "+Config.named_conf+" with primary "+entry.Master+".")
		LogError.Println("Direct changes: Domain " + domain + " is still declared in " + Config.named_conf + " after the rewrite")
	}

	fmt.Fprintln(os.Stderr, "The control file was not rewritten. Check "+Config.error_log+
		", the write permissions on "+filepath.Dir(Config.named_conf)+", and run --check.")

	os.Exit(exitDomainIOError)
}

// DomainReloadCLI forces a fresh transfer of one zone from its primary,
// regardless of the serial that primary advertises.
//
// The CLI runs in its own process and has no access to the zone map of the
// running instance, so the request is handed over instead of being executed
// here: the integrated server picks it up from a spool file after a SIGHUP,
// an external BIND is told through "rndc retransfer".
func DomainReloadCLI(domain string) {
	// Literal, like delete: the zone is looked up by the name as it is declared.
	domain = normalizeDomainSpelling(domain)

	if validateDomainName(domain) == false {
		fmt.Fprintln(os.Stderr, "Malformed domain name.")
		os.Exit(exitDomainBadInput)
	}

	entry, exist := lookupDomainEntry(domain)
	if exist == false {
		fmt.Fprintln(os.Stderr, "Domain "+domain+" does not exist.")
		os.Exit(exitDomainRefused)
	}

	setupCLILogging()

	if integratedDNSEnabled() {
		alive, pid := checkPid()
		if alive == false {
			// Nothing is serving the zone right now, and the startup check
			// compares it with the primary on the next start anyway.
			fmt.Println("DirectSlave is not running, zone will be checked against the primary on next start.")
			os.Exit(exitDomainOK)
		}

		if err := QueueZoneRefetch(entry.Name); err != nil {
			fmt.Fprintln(os.Stderr, "Can't queue the refetch request:", err)
			LogError.Println("Direct changes: refetch request for " + entry.Name + " failed: " + err.Error())
			os.Exit(exitDomainIOError)
		}

		if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
			fmt.Fprintln(os.Stderr, "Can't send reload signal to pid", pid, "-", err)
			LogError.Println("Direct changes: reload signal to pid", pid, "failed:", err)
			os.Exit(exitDomainIOError)
		}

		LogAction.Println("Direct changes: Domain " + entry.Name + " refetch requested from primary " + entry.Master)
		fmt.Println("Refetch of " + entry.Name + " from " + entry.Master + " requested, pid " + fmt.Sprint(pid) + ".")
		os.Exit(exitDomainOK)
	}

	if Config.rndc_path == "none" {
		fmt.Fprintln(os.Stderr, "RNDC is disabled, can't request a transfer.")
		os.Exit(exitDomainRefused)
	}

	runRndcCommand("retransfer", entry.Name)
	LogAction.Println("Direct changes: Domain " + entry.Name + " retransfer requested from primary " + entry.Master)
	fmt.Println("RNDC retransfer of " + entry.Name + " requested.")
	os.Exit(exitDomainOK)
}

// lookupDomainEntry finds the declaration that is spelled exactly the way the
// caller wrote it. The "--domain" commands work on the control file literally,
// so an internationalized name is never silently traded for its other form:
// "--domain search" prints the name as it is stored and that is the name the
// other commands take.
func lookupDomainEntry(domain string) (namedConfEntry, bool) {
	return lookupDomainForms([]string{normalizeDomainSpelling(domain)})
}

// lookupDomainEntryAnyForm also accepts the other spelling of the same zone.
// Only "--domain add" uses it: adding a name whose punycode twin is already
// declared would put the same zone in the control file twice.
func lookupDomainEntryAnyForm(domain string) (namedConfEntry, bool) {
	return lookupDomainForms(domainMatchForms(domain))
}

func lookupDomainForms(wanted []string) (entry namedConfEntry, exist bool) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	if len(wanted) == 0 {
		return entry, false
	}

	file, err := os.Open(Config.named_conf)
	if err != nil {
		// A control file that was never created simply holds no zones; AddToFile
		// creates it on the first --domain add.
		if os.IsNotExist(err) {
			return entry, false
		}
		fmt.Fprintln(os.Stderr, "Can't read "+Config.named_conf+":", err)
		os.Exit(exitDomainIOError)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		ret := cliZoneLineRe.FindStringSubmatch(line)
		if len(ret) == 0 {
			continue
		}

		name := strings.ToLower(strings.TrimSuffix(ret[1], "."))

		matched := false
		for _, form := range wanted {
			if name == form {
				matched = true
				break
			}
		}
		if matched == false {
			continue
		}

		entry = namedConfEntry{Name: ret[1], Master: ret[2]}

		if path := cliZoneFileRe.FindStringSubmatch(line); len(path) > 1 {
			entry.File = path[1]
		}

		return entry, true
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "Can't read "+Config.named_conf+":", err)
		os.Exit(exitDomainIOError)
	}

	return entry, false
}

// deleteZoneFileForEntry removes the stored zone data. The path recorded in the
// control file wins; the legacy named_workdir/<domain>.db layout is the fallback.
func deleteZoneFileForEntry(entry namedConfEntry) {
	if entry.File == "" {
		DeleteDomainFile(entry.Name)
		return
	}

	path := normalizeZoneFilePath(entry.File)

	if err := os.Remove(path); err != nil && os.IsNotExist(err) == false {
		LogError.Println("Cant' delete zone file,", path, "-", err)
	}
}

// setupCLILogging opens the regular log files so a direct change is recorded in
// action.log exactly like a change made through the API or the dashboard.
// Read-only commands never call it and therefore never require writable logs.
func setupCLILogging() {
	LogAccess, LogAction, LogError = setupLogging()
}

// reloadAfterDirectChange re-applies the control file using the daemon's own
// reload path: SIGHUP for the integrated DNS server, rndc for an external one.
func reloadAfterDirectChange() {
	if integratedDNSEnabled() {
		alive, pid := checkPid()

		if alive == false {
			fmt.Println("DirectSlave is not running, zones will be loaded on next start.")
			return
		}

		if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
			fmt.Fprintln(os.Stderr, "Can't send reload signal to pid", pid, "-", err)
			LogError.Println("Direct changes: reload signal to pid", pid, "failed:", err)
			return
		}

		fmt.Println("Reload signal sent to pid", pid)
		LogAction.Println("Direct changes: reload signal sent to pid", pid)
		return
	}

	if Config.rndc_path == "none" {
		fmt.Println("RNDC is disabled, reload skipped.")
		return
	}

	runRndc()
	fmt.Println("RNDC reload done.")
}
