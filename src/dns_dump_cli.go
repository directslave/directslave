package main

import (
	"fmt"
	"os"
	"strings"
)

// DumpDatabaseCLI backs "--dump-db". The zone registry is in the memory of the
// running instance, so this asks it over the local control socket rather than
// guessing from the control file and the zone files on disk.
//
//	--dump-db              counters plus one line per zone
//	--dump-db stats        counters only
//	--dump-db <zone>       everything known about one zone
func DumpDatabaseCLI() {
	if integratedDNSEnabled() == false {
		fmt.Fprintln(os.Stderr, "The integrated DNS server is off (dns_server = "+Config.dns_server+").")
		fmt.Fprintln(os.Stderr, "There is no in-memory zone registry to dump; the zones are BIND's.")
		os.Exit(exitDomainRefused)
	}

	command := "dump"

	if len(os.Args) > 2 {
		argument := strings.ToLower(strings.TrimSpace(os.Args[2]))
		switch argument {
		case "", "dump", "all":
			command = "dump"
		case "stats", "summary":
			command = "stats"
		default:
			name := strings.TrimSpace(os.Args[2])
			if validateDomainName(name) == false {
				fmt.Fprintln(os.Stderr, "Usage: --dump-db [stats|<domain>]")
				os.Exit(exitDomainBadInput)
			}
			command = "zone " + name
		}
	}

	answer, err := askControlSocket(command)
	if err != nil {
		alive, pid := checkPid()
		if alive == false {
			fmt.Fprintln(os.Stderr, "DirectSlave is not running, so there is no zone registry to read.")
			os.Exit(exitDomainRefused)
		}

		fmt.Fprintln(os.Stderr, "Can't reach the control socket at "+controlSocketPath()+":", err)
		fmt.Fprintln(os.Stderr, "The instance at pid", pid, "may predate the control socket; restart it.")
		os.Exit(exitDomainIOError)
	}

	answer = strings.TrimRight(answer, "\n")
	if answer == "" {
		fmt.Fprintln(os.Stderr, "The control socket returned nothing.")
		os.Exit(exitDomainIOError)
	}

	fmt.Println(answer)

	if strings.HasPrefix(answer, "ERROR:") {
		os.Exit(exitDomainRefused)
	}

	os.Exit(exitDomainOK)
}
