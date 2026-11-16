package main

import (
	"bufio"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
)

func AddDomain(domain string, master string) {
	if err := AddToFile(domain, master); err != nil {
		LogError.Println("Domain", domain, "not added:", err)
		return
	}

	if integratedDNSEnabled() {
		integratedDNSAddZone(domain, master)
	} else {
		useRndc()
	}

	LogAction.Println("Domain", domain, "added with primary", master)
	return
}

func DomainDelete(domain string, master string) {
	removed, err := DeleteFromFile(domain)
	if err != nil {
		LogError.Println("Domain", domain, "not removed from", Config.named_conf+":", err)
		return
	}
	if removed == 0 {
		LogError.Println("Domain", domain, "had no declaration in", Config.named_conf)
	}

	if integratedDNSEnabled() {
		integratedDNSRemoveZone(domain)
	} else {
		useRndc()
		DeleteDomainFile(domain)
	}

	LogAction.Println("Domain", domain, "removed from", master)
	return
}

func DeleteDomainFile(domain string) {

	path := Config.named_workdir + "/" + domain + ".db"

	err := os.Remove(path)
	if err != nil {
		LogError.Println("Cant' delete zone file,", path, "not found.")
	}

	return
}

func DomainReload(domain string, master string) {
	if integratedDNSEnabled() {
		integratedDNSReloadZone(domain, master)
	} else {
		useRndc()
	}

	LogAction.Println("Reloaded", domain, "from master", master)
	return
}

func DomainEdit(domain string, master string) {
	if _, err := ReplaceInFile(domain, master); err != nil {
		LogError.Println("Domain", domain, "not changed:", err)
		return
	}

	if integratedDNSEnabled() {
		integratedDNSReloadZone(domain, master)
	} else {
		useRndc()
	}
}

func DomainExist(domain string) (exist bool, master string) {

	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	master = "none"
	exist = false

	file, err := os.Open(Config.named_conf)
	if err != nil {
		// LogError.Fatal() used to take the whole daemon down whenever the
		// control file was momentarily unreadable.
		controlWarn("can't read "+Config.named_conf, err)
		return
	}
	defer file.Close()

	// Either spelling, for the same reason as in rewriteControlFile().
	forms := domainMatchForms(domain)
	if len(forms) == 0 {
		return
	}

	quoted := make([]string, 0, len(forms))
	for _, form := range forms {
		quoted = append(quoted, regexp.QuoteMeta(form))
	}

	re := regexp.MustCompile(`^zone "(?:` + strings.Join(quoted, "|") + `)" .+masters \{ (.+?); \}.+?`)

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {

		ret := re.FindStringSubmatch(scanner.Text())

		if len(ret) > 0 {
			return true, canonicalIPOrSame(ret[1])
		}
	}

	if err := scanner.Err(); err != nil {
		controlWarn("can't scan "+Config.named_conf, err)
	}

	return

}

func zoneTemplate(domain string, master string) string {
	var format string

	// One spelling per address in the control file, so the primary written
	// here compares equal to the address a NOTIFY or an API call arrives from.
	master = canonicalIPOrSame(master)

	if Config.named_format == "text" {
		format = "masterfile-format text;"
	} else {
		format = ""
	}

	return "zone \"" + domain + "\" { type slave; file \"" + Config.named_workdir + "/" + domain + ".db\"; masters { " + master + "; }; max-retry-time " + Config.retry_time + "; min-retry-time " + Config.retry_time + "; " + format + " };\n"
}

func AddToFile(domain string, master string) error {

	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(true)
	defer unlockControlFile(lock)

	// A zone is always declared in its ASCII form: that is what the primary
	// serves it under and what every query asks for.
	if converted, ok := NormalizeDomain(domain); ok {
		domain = converted
	}

	text, err := ioutil.ReadFile(Config.named_conf)
	if err != nil && os.IsNotExist(err) == false {
		// The control file must never be rewritten from a failed read: the old
		// code logged the error and then wrote back the empty buffer plus one
		// zone, wiping every other zone in the file.
		controlWarn("can't read "+Config.named_conf+", zone "+domain+" not added", err)
		return err
	}

	// A file whose last line has no newline would otherwise get the new zone
	// glued onto it, and both entries would stop parsing.
	if len(text) > 0 && text[len(text)-1] != '\n' {
		text = append(text, '\n')
	}

	return writeControlFile(append(text, []byte(zoneTemplate(domain, master))...))
}

// DeleteFromFile removes the zone by any spelling of its name and reports how
// many lines were dropped, so a caller can tell a real removal from a no-op.
//
// DirectAdmin sends an internationalized name in its native script while the
// declaration is written in punycode, so the API and the dashboard have to
// accept both. The "--domain" CLI does not: see DeleteFromFileExact.
func DeleteFromFile(domain string) (int, error) {

	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(true)
	defer unlockControlFile(lock)

	return rewriteControlFile(domainMatchForms(domain), "")
}

// DeleteFromFileExact removes only the declaration that is spelled exactly the
// way the caller wrote it. It backs "--domain delete", which works on the
// control file literally: what the operator types is what is removed, and
// nothing else, even when the same zone is also declared in another form.
func DeleteFromFileExact(domain string) (int, error) {

	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(true)
	defer unlockControlFile(lock)

	return rewriteControlFile([]string{normalizeDomainSpelling(domain)}, "")
}

// ReplaceInFile points the zone at another primary in a single rewrite, so the
// zone is never briefly absent from the control file the way a delete followed
// by an add would leave it.
func ReplaceInFile(domain string, master string) (int, error) {

	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(true)
	defer unlockControlFile(lock)

	// An edit is also how a zone stored under its native script migrates to the
	// ASCII form: it is matched by both spellings and rewritten as one.
	declared := domain
	if converted, ok := NormalizeDomain(domain); ok {
		declared = converted
	}

	return rewriteControlFile(domainMatchForms(domain), zoneTemplate(declared, master))
}

// rewriteControlFile streams the control file, dropping every line that
// declares the zone and putting replacement in place of the first one. The
// caller must already hold fileMutex and the control-file lock.
func rewriteControlFile(forms []string, replacement string) (int, error) {

	// QuoteMeta keeps a dot in the name a dot: an unescaped name matched other
	// zones too, and a name with regex characters could match nothing at all.
	quoted := make([]string, 0, len(forms))
	for _, form := range forms {
		if form == "" {
			continue
		}
		quoted = append(quoted, regexp.QuoteMeta(form))
	}

	if len(quoted) == 0 {
		return 0, errDomainEmpty
	}

	source, err := os.Open(Config.named_conf)
	if err != nil {
		controlWarn("can't read "+Config.named_conf, err)
		return 0, err
	}
	defer source.Close()

	re := regexp.MustCompile(`^zone "(?:` + strings.Join(quoted, "|") + `)" .+masters.+$`)

	var output []byte
	matched := 0

	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if re.MatchString(line) {
			matched++
			if matched == 1 && replacement != "" {
				output = append(output, []byte(replacement)...)
			}
			continue
		}

		output = append(output, []byte(line+"\n")...)
	}

	// Without this check a scan that stopped early (an over-long line, an I/O
	// error) used to be renamed over the control file, silently truncating it.
	if err := scanner.Err(); err != nil {
		controlWarn("can't scan "+Config.named_conf+", the file is left untouched", err)
		return 0, err
	}

	if matched == 0 {
		if replacement == "" {
			return 0, nil
		}
		output = append(output, []byte(replacement)...)
	}

	if err := writeControlFile(output); err != nil {
		return 0, err
	}

	return matched, nil
}
