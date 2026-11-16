package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

type ZoneFile struct {
	Zone   string
	File   string
	Master string
}

type ZoneFiles map[string]*ZoneFile

func ImportStoredZones() {
	controlFile := LoadZones(Config.named_conf)
	ImportZoneFiles(controlFile)
}

func LoadZones(filename string) ZoneFiles {
	files, _ := loadZonesChecked(filename)
	return files
}

// loadZonesChecked is LoadZones that also reports read errors, so a reload can
// tell "control file is empty" apart from "control file is unreadable".
func loadZonesChecked(filename string) (ZoneFiles, error) {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	lock := lockControlFile(false)
	defer unlockControlFile(lock)

	stream, err := os.Open(filename)
	if err != nil {
		dnsLogPrintln("STORAGE ERROR: cannot open control file", filename, "-", err)
		return make(ZoneFiles), err
	}
	defer stream.Close()

	// Compatible with the one-line entries written by legacy AddToFile().
	re := regexp.MustCompile(`^zone "(.+?)".+file "(.+?)".+masters \{ (.+?); \};.+$`)
	files := make(ZoneFiles)

	lines := 0
	ignored := 0

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		lines++

		ret := re.FindStringSubmatch(line)
		if len(ret) > 3 {
			files[ret[1]] = &ZoneFile{Zone: ret[1], File: ret[2], Master: canonicalIPOrSame(ret[3])}
			continue
		}

		if strings.TrimSpace(line) != "" {
			ignored++
		}
	}
	if err := scanner.Err(); err != nil {
		dnsLogPrintln("STORAGE ERROR: scanning control file", filename, "-", err)
		return files, err
	}

	// Without these numbers a reload that removes nothing is impossible to tell
	// apart from a control file that still holds the zone.
	dnsLogPrintf("STORAGE: control file %s: %d lines, %d zone declarations, %d lines ignored",
		filename, lines, len(files), ignored)

	return files, nil
}

func ImportZoneFiles(files ZoneFiles) {
	all := len(files)
	awaiting := 0
	served := 0

	// Zones restored from disk at startup are withheld until their serial has
	// been confirmed with the primary. A reload of a running instance keeps the
	// old behavior: it never withholds a zone that is already being answered.
	unchecked := startupCheckEnabled()

	for _, content := range files {
		if importOrScheduleZone(content, unchecked) {
			served++
		} else {
			awaiting++
		}
	}

	dnsLogPrintf("STORAGE: Done loading zones. Total: %d, served: %d, awaiting transfer: %d\n", all, served, awaiting)
	CallDNSProfiler()
}

// importOrScheduleZone loads one zone from its stored file. If the file is
// missing or invalid the zone is registered as "new" and an AXFR is started.
// It returns true when the zone is served from disk immediately.
func importOrScheduleZone(content *ZoneFile, unchecked bool) bool {
	zone := dnsCanonicalZone(content.Zone)

	// A zone declared in its native script is served under its ASCII name, but
	// the stored file was written under the old one and its records still carry
	// the old owner names. Importing it would publish a zone that answers
	// nothing, so it is refetched instead.
	native := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(content.Zone), "."))
	if native+"." != zone {
		dnsLogPrintln("STORAGE: Zone", content.Zone, "is declared in its native script; serving it as",
			zone, "and refetching from", content.Master)
	} else if ImportZoneFile(content.Zone, content.File, content.Master, unchecked) {
		return true
	}

	dnscfg := &ZoneConfig{
		Zone:    zone,
		Masters: []string{content.Master},
		Refresh: uint32(Config.dns_new_refresh),
		Retry:   uint32(Config.dns_new_retry),
		Expire:  uint32(Config.dns_new_expire),
		New:     1,
		File:    normalizeZoneFilePath(content.File),
	}

	dnsLogPrintln("STORAGE WARNING: Zone", content.Zone, "file", content.File,
		"not found/invalid; scheduling transfer from", content.Master)
	Zwrite(dnscfg)
	go DoZoneTransfer(zone, content.Master)
	return false
}

var reloadMu sync.Mutex

// ReloadStoredZones re-reads the control file and reconciles the live zone
// registry with it WITHOUT ever emptying it: zones that are unchanged are not
// touched (their data, timers and in-flight transfers stay as they are), new
// zones are loaded, zones whose primary changed get a fresh transfer, and only
// zones that disappeared from the control file are unloaded. Each per-zone
// change is a single atomic pointer swap, so queries never see a gap.
func ReloadStoredZones() {
	reloadMu.Lock()
	defer reloadMu.Unlock()

	desired, err := loadZonesChecked(Config.named_conf)
	if err != nil {
		dnsLogPrintln("RELOAD ERROR: control file unreadable, keeping current zones -", err)
		return
	}

	added, changed, unchanged := 0, 0, 0
	for _, content := range desired {
		zone := dnsCanonicalZone(content.Zone)
		value, ok := zones.Load(zone)
		if !ok {
			importOrScheduleZone(content, false)
			added++
			continue
		}
		cfg := value.(*ZoneConfig)
		if len(cfg.Masters) == 1 && sameIP(cfg.Masters[0], content.Master) {
			unchanged++
			continue
		}
		integratedDNSReloadZone(zone, content.Master)
		changed++
	}

	// Re-read right before removing so a zone added through the API while we
	// were loading is not mistaken for a stale one.
	final, err := loadZonesChecked(Config.named_conf)
	if err != nil {
		dnsLogPrintln("RELOAD ERROR: control file unreadable, skipping removals -", err)
		return
	}
	keep := make(map[string]struct{}, len(final))
	for _, content := range final {
		keep[dnsCanonicalZone(content.Zone)] = struct{}{}
	}

	removed := 0
	zones.Range(func(key, _ interface{}) bool {
		zone, _ := key.(string)
		if _, ok := keep[zone]; !ok {
			stopAllZoneTimers(zone)
			Zdelete(zone)
			dnsLogPrintln("MEMMAP: Unloaded zone", zone, "(removed from control file)")
			removed++
		}
		return true
	})

	served := 0
	zones.Range(func(_, _ interface{}) bool {
		served++
		return true
	})

	dnsLogPrintf("RELOAD: zones added %d, primary changed %d, unchanged %d, removed %d; %d declared in %s, %d in memory",
		added, changed, unchanged, removed, len(final), Config.named_conf, served)
	CallDNSProfiler()
}

func normalizeZoneFilePath(filename string) string {
	if filepath.IsAbs(filename) {
		return filename
	}
	return filepath.Join(Config.named_workdir, filename)
}

func ImportZoneFile(zone string, filename string, master string, unchecked bool) bool {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	filename = normalizeZoneFilePath(filename)
	dnsLogPrintln("STORAGE: Loading", zone, "from", filename)

	stream, err := os.Open(filename)
	if err != nil {
		return false
	}
	defer stream.Close()

	var records []dns.RR
	// Use the zone parser rather than parsing one physical line at a time. This
	// keeps existing BIND text-format files usable ($ORIGIN/$TTL, multiline SOA,
	// relative owner names, etc.). BIND raw-format files simply fail here and
	// are re-fetched from the configured primary by ImportZoneFiles().
	parser := dns.NewZoneParser(stream, dnsCanonicalZone(zone), filename)
	for rr, ok := parser.Next(); ok; rr, ok = parser.Next() {
		if rr != nil {
			records = append(records, rr)
		}
	}
	if err := parser.Err(); err != nil {
		dnsLogPrintln("STORAGE ERROR: Import failed for", zone, "from", filename, "-", err)
		return false
	}

	records = dns.Dedup(records, nil)
	if len(records) == 0 {
		dnsLogPrintln("STORAGE ERROR: Import failed for", zone, "from", filename, "- no records found")
		return false
	}

	dnscfg := &ZoneConfig{
		Zone:      dnsCanonicalZone(zone),
		Masters:   []string{master},
		File:      filename,
		RRs:       records,
		Unchecked: unchecked,
	}

	if !Zwrite(dnscfg) {
		dnsLogPrintln("STORAGE ERROR: Import failed for", zone, "from", filename, "- no usable SOA")
		return false
	}
	return true
}

func WriteZoneFile(domain string, filename string, records []dns.RR) error {
	fileMutex.Lock()
	defer fileMutex.Unlock()

	filename = normalizeZoneFilePath(filename)
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(filename), ".directsecondary-zone-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	writer := bufio.NewWriter(tmp)
	for _, rr := range records {
		if rr == nil {
			continue
		}
		if _, err := fmt.Fprintln(writer, rr.String()); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filename); err != nil {
		return err
	}

	dnsLogPrintln("STORAGE: Zone", domain, "stored as", filename)
	return nil
}

func DeleteZoneFile(domain string) {
	zone := strings.TrimSuffix(dnsCanonicalZone(domain), ".")
	path := filepath.Join(Config.named_workdir, zone+".db")
	if zinfo, ok := Zread(domain); ok && zinfo.Zone == dnsCanonicalZone(domain) && zinfo.File != "" {
		path = zinfo.File
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		dnsLogPrintln("STORAGE ERROR: cannot remove", path, "-", err)
		return
	}
	dnsLogPrintln("STORAGE: Removed zone file", path)
}
