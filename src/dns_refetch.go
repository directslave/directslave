package main

import (
	"os"
	"path/filepath"
	"strings"
)

// The CLI has no access to the zone map of the running daemon, so "--domain
// reload" leaves a request in a small spool file next to the zone files and
// signals the instance. The SIGHUP handler picks the file up and starts a
// forced transfer for every zone listed in it. No extra port, no credentials.
const refetchSpoolName = ".directslave.refetch"

func refetchSpoolPath() string {
	return filepath.Join(Config.named_workdir, refetchSpoolName)
}

// QueueZoneRefetch appends one refetch request. Writes are short and opened
// with O_APPEND, so several CLI calls never interleave inside a line.
func QueueZoneRefetch(zone string) error {
	file, err := os.OpenFile(refetchSpoolPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	if _, err := file.WriteString(dnsCanonicalZone(zone) + "\n"); err != nil {
		file.Close()
		return err
	}

	return file.Close()
}

// ProcessZoneRefetchQueue drains the spool and starts a forced transfer for
// every zone in it. The file is renamed before it is read, so a request that
// arrives while this runs lands in a fresh spool and is handled by the next
// signal instead of being dropped.
func ProcessZoneRefetchQueue() {
	if integratedDNSEnabled() == false {
		return
	}

	path := refetchSpoolPath()
	pending := path + ".processing"

	fileMutex.Lock()
	if err := os.Rename(path, pending); err != nil {
		fileMutex.Unlock()
		if os.IsNotExist(err) == false {
			dnsLogPrintln("REFETCH ERROR: cannot take", path, "-", err)
		}
		return
	}
	fileMutex.Unlock()

	data, err := os.ReadFile(pending)
	if err != nil {
		dnsLogPrintln("REFETCH ERROR: cannot read", pending, "-", err)
		return
	}

	if err := os.Remove(pending); err != nil {
		dnsLogPrintln("REFETCH ERROR: cannot remove", pending, "-", err)
	}

	requested := 0

	for _, line := range strings.Split(string(data), "\n") {
		zone := dnsCanonicalZone(line)
		if zone == "" || zone == "." {
			continue
		}

		value, ok := zones.Load(zone)
		if ok == false {
			dnsLogPrintln("REFETCH ERROR:", zone, "is not served by us; request ignored")
			continue
		}

		zinfo := value.(*ZoneConfig)
		if len(zinfo.Masters) == 0 {
			dnsLogPrintln("REFETCH ERROR:", zone, "has no primary; request ignored")
			continue
		}

		master := zinfo.Masters[0]
		dnsLogPrintln("REFETCH:", zone, "forced transfer requested from primary", master)

		go ForceZoneTransfer(zone, master)
		requested++
	}

	if requested > 0 {
		dnsLogPrintf("REFETCH: %d forced transfers started", requested)
	}
}
