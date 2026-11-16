package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func initSignalChannel() (sig chan os.Signal) {
	sig = make(chan os.Signal, 8)
	signal.Notify(sig, os.Interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTRAP, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGALRM, syscall.SIGTERM)

	return sig

}

func dropPrivileges(userName string, groupName string) (success bool) {

	success = false

	if os.Getuid() == 0 {

		if Config.debug == "1" {
			fmt.Println("\nDEBUG: Running as root (0), dropping privileges to", Config.uid+":"+Config.gid)
		}

		user, err := strconv.Atoi(userName)
		crashErrorString("Cant' use plain username as UID:", err)

		group, err := strconv.Atoi(groupName)
		crashErrorString("Cant' use plain groupname as GID:", err)

		if err := syscall.Setgroups([]int{group}); err != nil {
			crashError(err)
		}

		if err := syscall.Setgid(group); err != nil {
			crashError(err)
		}

		if err := syscall.Setuid(user); err != nil {
			crashError(err)
		}

		crashError(err)

		success = true
	}

	return
}

func detachSelf(nochdir, noclose int) (int, error) {

	curPid := syscall.Getpid()

	// Detaching mechanics:
	// Run process from itself as a child process
	// Then detach it from parent
	// Then exit parent, leaving child working in background

	if syscall.Getppid() == 1 {

		/* Change the file mode mask */
		syscall.Umask(0)

		if nochdir == 0 {
			os.Chdir("/")
		}

		fmt.Println("Spawning main() server with pid", curPid)
		WritePidFile(curPid)

		return 0, nil
	}

	// Before spawning new child, we must check existing pid
	// Read Config.pid file then check if proc with this pid is still alive
	// This is not fully guaranty if this pid is taken by another process
	// so I must build some checks with internal listener() and os/signal, of course

	alive, pid := checkPid()
	if alive == true {
		if Config.debug == "1" {
			if LogError != nil {
				LogError.Println("[from checkPid] checkPid returns:", alive, "pid", pid)
			} else {
				fmt.Println("DEBUG: checkPid returns:", alive, "pid", pid)
			}
		}

		if LogError != nil {
			LogError.Println("I'm alive at", pid, ", exiting!")
		} else {
			fmt.Println("I'm alive at", pid, ", exiting!")
		}
		os.Exit(0)
	}

	execSpec := &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{},
	}

	_, err := syscall.ForkExec(os.Args[0], os.Args, execSpec)

	if err != nil {
		if LogError != nil {
			LogError.Println("Can't create process"+os.Args[0], err)
		} else {
			fmt.Println("Can't create process"+os.Args[0], err)
		}
	}

	os.Exit(0)

	return 0, nil
}

func checkPid() (alive bool, intPid int) {
	alive = false
	intPid = 0

	file, err := os.Open(Config.pid)

	if err != nil {
		// LogError.Println(err)
		return
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(scanner.Text()) >= 1 {
			intPid, _ = strconv.Atoi(scanner.Text())
		}
	}
	file.Close()
	if intPid == 0 {
		return
	}
	err = syscall.Kill(intPid, 0)
	if err == nil {
		alive = true
		return
	}
	return
}

func WritePidFile(myPid int) {

	// Write Pidfile
	file, err := os.OpenFile(Config.pid, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		crashErrorString("Couldn't create pid file:", err)
	} else {
		_, _ = file.Write([]byte(strconv.Itoa(myPid)))
		_ = file.Close()
		if os.Getuid() == 0 {
			uid, uerr := strconv.Atoi(Config.uid)
			gid, gerr := strconv.Atoi(Config.gid)
			if uerr == nil && gerr == nil {
				_ = os.Chown(Config.pid, uid, gid)
			}
		}
	}
	fmt.Println("Pidfile writen as ", Config.pid)
	return
}

func PrintStatus() {
	alive, pid := checkPid()

	// fmt.Printf("%d\n", pid)

	if alive == true {
		fmt.Println(os.Args[0]+" running as pid", pid, ".")
	} else {
		fmt.Println(os.Args[0] + " is not running.")
	}
	os.Exit(0)
}

func catchSignal(c chan os.Signal) {

	s := map[string]string{
		"hangup":      "SIGHUP",
		"interrupt":   "SIGINT",
		"quit":        "SIGQUIT",
		"trace trap":  "SIGTRAP",
		"abort trap":  "SIGABRT",
		"alarm clock": "SIGALRM",
		"terminated":  "SIGTERM",
	}

	for {
		sig := <-c

		recv := s[sig.String()]

		if recv == "SIGHUP" {
			LogAccess, LogAction, LogError = setupLogging()

			LogError.Printf("RELOAD: Signal %s received, reloading\n", sig.String())
			if integratedDNSEnabled() {
				reloadIntegratedDNSZones()
				// Forced refetches requested by "--domain reload" are queued in
				// a spool file and applied after the control file is re-read.
				ProcessZoneRefetchQueue()
			}
			if Config.debug == "1" || Config.background == "1" {
				fmt.Printf("RELOAD: Signal %s received, reloading\n", sig.String())
			}
		} else {

			LogError.Printf("SHUTDOWN: %s received, exiting...\n", recv)

			doCleanup()
			os.Exit(0)
		}
	}
}

func doCleanup() {
	stopControlSocket()
	shutdownIntegratedDNSServer()
	stopDNSQueryLogger()
	err := os.Remove(Config.pid)
	if err != nil {
		LogError.Println("Cant' delete pidfile:", err)
	} else {
		LogError.Println("Pidfile removed, clean shutdown.")
	}

}
