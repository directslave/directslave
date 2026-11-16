package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

func startLatestVersionChecker() {
	LogAction.Println("Statrting version check processor... OK")

	for {
		time.Sleep(time.Hour * 12)
		getLatestVersion()
	}
}

func getLatestVersion() {
	url := "https://directslave.com/version"

	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("Failed to fetch latest version from https://directslave.com/version: %s\n", err)
		return
	}
	defer resp.Body.Close()

	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Failed to read latest version from https://directslave.com/version: %s\n", err)
		return
	}

	remote := strings.TrimSpace(string(buf))
	fmt.Printf("Checking for updates at https://directslave.com/version: %s\n", remote)

	// The public v3 endpoint may still advertise 3.x while a local v4 build is
	// being used. Never present an older branch as a new version.
	if versionNewer(remote, CURRENT) {
		updateAvailable = remote

		LogAction.Printf("New version available: %s\n Get it on https://directslave.com/download", updateAvailable)

		if Config.debug == "1" || Config.background == "1" {
			fmt.Printf("New version available: %s\nGet it on https://directslave.com/download\n", updateAvailable)
		}
	} else {
		updateAvailable = ""
		fmt.Printf("No updates available, next check in 12 hours\n")
	}
}

func versionNewer(remote, current string) bool {
	parse := func(v string) ([]int, bool) {
		parts := strings.Split(strings.TrimSpace(v), ".")
		if len(parts) == 0 {
			return nil, false
		}
		out := make([]int, 0, len(parts))
		for _, part := range parts {
			// Keep comparison intentionally simple: DirectSlave versions are
			// numeric dotted versions. Ignore a suffix after the numeric part.
			n := 0
			seen := false
			for _, r := range part {
				if r < '0' || r > '9' {
					break
				}
				seen = true
				n = n*10 + int(r-'0')
			}
			if !seen {
				return nil, false
			}
			out = append(out, n)
		}
		return out, true
	}

	rv, rok := parse(remote)
	cv, cok := parse(current)
	if !rok || !cok {
		return false
	}

	max := len(rv)
	if len(cv) > max {
		max = len(cv)
	}
	for i := 0; i < max; i++ {
		r, c := 0, 0
		if i < len(rv) {
			r = rv[i]
		}
		if i < len(cv) {
			c = cv[i]
		}
		if r != c {
			return r > c
		}
	}
	return false
}
