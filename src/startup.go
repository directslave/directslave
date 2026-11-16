package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
)

func PrintUsage() {
	fmt.Println(VERSION + " " + COPYRIGHT)
	fmt.Println("Usage:")
	fmt.Println("--check    - Check config file parameters permission set")
	fmt.Println("--debug    - Run foreground in debug mode [override config debug options]")
	fmt.Println("--run      - Run background in normal mode")
	fmt.Println("--status   - Check and print service status")
	fmt.Println("--password <user> - Create a user or change its password (asks for the password)")
	fmt.Println("--delete   <user> - Delete user passwd file entry")
	fmt.Println("--domain search <domain>           - Search zones by full or partial name")
	fmt.Println("--domain add    <domain> <primary> - Add zone with primary IP address")
	fmt.Println("--domain delete <domain>           - Delete zone and its zone file")
	fmt.Println("--domain reload <domain>           - Force a transfer of the zone from its primary")
	fmt.Println("--dump-db [stats|<domain>]         - Show the live zone registry of the running instance")
	os.Exit(0)
}

func RunWithOption(option string) {
	switch option {
	case "--run":
		return
	case "--check":
		DoChecks()
	case "--status":
		PrintStatus()
	case "--password":
		UpdatePasswordFile()
	case "--delete":
		DeleteFromPasswordFile()
	case "--domain":
		DomainCommand()
	case "--dump-db":
		DumpDatabaseCLI()
	case "--debug":
		Config.debug = "1"
		Config.background = "0"
		return
	default:
		PrintUsage()
	}
	return
}

func PrintStartValues() {

	if Config.background != "1" {

		fmt.Println("\n*** Starting " + VERSION + " server ***\n*** " + COPYRIGHT + " ***")

		fmt.Printf("Using user UID:%d, GID:%d\n\n", syscall.Getuid(), syscall.Getgid())
		fmt.Printf("Using secondary conf: %s\n", Config.named_conf)
		if integratedDNSEnabled() {
			fmt.Printf("Integrated authoritative DNS: %s UDP/TCP\n", dnsListenDescription())
			if Config.dns_listen_legacy != "" {
				fmt.Println("WARNING: dns_listen is deprecated, use dns_listen_ip4 / dns_listen_ip6")
			}
			if dnsQueryLogRequested() {
				fmt.Printf("DNS query log: %s -> %s (buffer %d)\n", Config.dns_query_log, Config.dns_query_log_file, Config.dns_query_log_buffer)
			}
		} else {
			fmt.Printf("DNS backend: external (rndc: %s)\n", Config.rndc_path)
		}

		if Config.ssl == "on" {
			fmt.Printf("Creating HTTP PLAIN socket on: %s:%s\n", Config.host, Config.port)
			fmt.Printf("Creating HTTP/2 TLS socket on: %s:%s\n", Config.host, Config.sslport)
			fmt.Printf("Using certificate: %s\n", Config.ssl_cert)
			fmt.Printf("Using certificate key: %s\n\n", Config.ssl_key)
		} else {
			fmt.Printf("Creating HTTP PLAIN socket on: %s:%s\n\n", Config.host, Config.port)
		}
		fmt.Println("Staying foreground.")
	} else {
		fmt.Println("\n*** Starting " + VERSION + " server ***\n*** " + COPYRIGHT + " ***")
		fmt.Println("Going background.")
	}

	return
}

func DoChecks() {

	failcount := 0

	fmt.Println(VERSION + " " + COPYRIGHT)
	fmt.Println("\nHere we do some test to check if your config ... \n", CONFIGFILE, "is good.")

	fmt.Printf("Reading %s ... ", CONFIGFILE)
	Config = ReadConfigFile(CONFIGFILE)
	fmt.Println("OK")

	fmt.Printf("\nTrying to drop privileges to %s %s ... ", Config.uid, Config.gid)
	dropPrivileges(Config.uid, Config.gid)
	fmt.Println("OK")

	fmt.Printf("Running tests as UID:%d, GID:%d\n\n", syscall.Getuid(), syscall.Getgid())

	options := map[string]string{
		"access_log": "",
		"error_log":  "",
		"action_log": "",
		"named_conf": "",
		"pid":        "",
	}

	for option := range options {

		file := reflect.ValueOf(&Config).Elem().FieldByName(option)

		fmt.Printf("Opening/creating file '%s' -> %s ... ", option, file)

		f, err := os.OpenFile(fmt.Sprintf("%s", file), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if checkError(err) == false {
			fmt.Println("OK")
			f.Close()
		} else {
			failcount++
			fmt.Println("!!! FAIL !")
			fmt.Println("Check file permissions and set it to", Config.uid+":", Config.gid)
			fmt.Println("Set directory writable flag for user", Config.uid)
		}
	}

	f, err := os.OpenFile(Config.authfile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if checkError(err) == false {
		f.Close()
	} else {
		failcount++
		fmt.Println("!!! No \"authfile\" -> ", Config.authfile)
		fmt.Println("Check permissions or use --password flag to create one.")
	}

	if Config.ssl == "on" {

		fmt.Println("SSL is on! Testing CERT and KEY accessibility:")

		sslopts := map[string]string{
			"ssl_cert": "",
			"ssl_key":  "",
		}

		for opt := range sslopts {

			file := reflect.ValueOf(&Config).Elem().FieldByName(opt)

			fmt.Printf("Opening file '%s' -> %s ... ", opt, file)

			f, err := os.Open(fmt.Sprintf("%s", file))
			if checkError(err) == false {
				fmt.Println("OK")
				f.Close()
			} else {
				failcount++
				fmt.Println("!!! FAIL !")
				fmt.Println("Cant' read", opt+".", "Please check path & permissions.")
			}

		}

	}

	// Appending to named_conf is not what a zone add/delete does: it rewrites
	// the file through a temporary copy and a rename, which needs the directory
	// to be writable. Without this test a delete can fail while looking fine.
	fmt.Printf("Testing atomic rewrite of '%s' ... ", Config.named_conf)
	if err := CheckControlFileWritable(); err != nil {
		failcount++
		fmt.Println("!!! FAIL !")
		fmt.Println("Can't create and rename a file in", filepath.Dir(Config.named_conf), "-", err)
		fmt.Println("Zone add/delete will silently keep the old file. Set the directory writable for user", Config.uid)
	} else {
		fmt.Println("OK")
	}

	fmt.Printf("Testing control file lock '%s' ... ", controlLockPath())
	if err := CheckControlLock(); err != nil {
		fmt.Println("!!! WARNING !")
		fmt.Println("Can't lock", controlLockPath(), "-", err)
		fmt.Println("DirectSlave will keep working, but the daemon and the --domain CLI won't be serialized.")
	} else {
		fmt.Println("OK")
	}

	if path, size, found := LeftoverTmpFile(); found {
		failcount++
		fmt.Println("\n!!! Leftover", path, "-", size, "bytes.")
		fmt.Println("It is a remnant of an interrupted zone delete and can resurrect deleted zones. Remove it.")
	}

	fmt.Printf("Testing named_workdir acesssibility %s ... ", Config.named_workdir)

	_, err = ioutil.ReadDir(Config.named_workdir)
	if err == nil {
		fmt.Println("OK")
	} else {
		failcount++
		fmt.Println("!!! FAIL!\nDirectory", Config.named_workdir, "unaccessible.\nPlease, check if it exist and owned by", Config.uid, ".")
	}

	if integratedDNSEnabled() {
		fmt.Print("Testing integrated DNS listen addresses ... ")
		if _, _, err := dnsListenFamilies(); err != nil {
			failcount++
			fmt.Println("!!! FAIL!\n" + err.Error())
		} else {
			fmt.Println("OK")
			fmt.Printf("Integrated DNS will listen on %s; RNDC is not required.\n", dnsListenDescription())
		}
		if Config.dns_listen_legacy != "" {
			fmt.Printf("WARNING: dns_listen is deprecated; replace \"dns_listen %s\" with dns_listen_ip4 / dns_listen_ip6.\n",
				Config.dns_listen_legacy)
		}
	} else if Config.rndc_path != "none" {
		fmt.Printf("Testing RNDC util accessibility at path %s ... ", Config.rndc_path)

		cmd := exec.Command(Config.rndc_path, "")
		err := cmd.Run()

		if err != nil && err.Error() != "exit status 1" {
			failcount++
			fmt.Println("!!! FAIL! \nrndc not found or cant' exec", Config.rndc_path)
		} else {
			fmt.Println("OK")
		}

	} else {
		fmt.Println("RNDC util disabled. It's better to have RNDC enabled & configured.")
	}

	if failcount == 0 {
		fmt.Println("\n*** All OK! You can safely run with --run flag.")
		os.Exit(0)
	} else {
		fmt.Println("\n***", failcount, "errors detected!\nPlease, recheck your config and run with --check flag again.")
		os.Exit(255)
	}

}
