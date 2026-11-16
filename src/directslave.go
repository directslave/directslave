package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/context"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/mmcloughlin/professor"
)

const (
	CURRENT    = "4.0.18"
	VERSION    = "DirectSlave GO/" + CURRENT + " Advanced/SecDNS"
	COPYRIGHT  = "(c) Roman Mazur <roman.mazur@gmail.com> 2012-2026"
	CONFIGFILE = "/usr/local/directslave/etc/directslave.conf"

	webroot   = "/usr/local/directslave/www/webroot"
	templates = "/usr/local/directslave/www/templates"

	DEMO = 0
)

type Domain struct {
	Name   string
	Master string
}

type User struct {
	UserName string
	Password string
}

type LogFile struct {
	FileName string
	Size     string
}

type Out struct {
	Product         string
	Domains         []Domain
	Users           []User
	LogFiles        []LogFile
	Pager           string
	ErrorMessage    string
	CustomAction    string
	CustomRow       string
	CustomName      string
	CustomValue     string
	NewVersionAvail string

	// The zone registry lives in this process, so the dashboard can show what
	// every zone is actually doing instead of only what the control file says.
	// Zones and Stats are nil wherever that data does not apply, and the
	// templates fall back to the plain listing.
	Zones      *DomainsView
	Stats      *StatsView
	Integrated bool

	// The current view, carried through pagination and the search form so a
	// filter is not lost on the next click.
	Search string
	State  string
	Master string
	DNSSEC string
	Sort   string
	Order  string
}

var (
	updateAvailable string
	err             error
	fileMutex       sync.Mutex
	rchan           chan int
)

func main() {
	Config = ReadConfigFile(CONFIGFILE)

	if len(os.Args) <= 1 {
		PrintUsage()
		return
	}
	option := os.Args[1]

	// Administrative commands preserve the legacy privilege model: they run
	// after switching to the configured DirectSlave uid/gid.
	if option != "--run" && option != "--debug" {
		dropPrivileges(Config.uid, Config.gid)
		RunWithOption(option)
		return
	}

	// --debug may override background/debug before we decide how to start.
	RunWithOption(option)

	if integratedDNSEnabled() {
		// Native port 53 must be bound before privileges are dropped. In daemon
		// mode the detach must happen first, otherwise the re-exec child would
		// inherit the already-dropped uid and would not be able to bind :53.
		if Config.background == "1" {
			PrintStartValues()
			if _, err = detachSelf(0, 0); err != nil {
				crashErrorString("Server start error:", err)
			}
		} else {
			PrintStartValues()
			alive, pid := checkPid()
			if alive {
				fmt.Println("I'm already alive at pid", pid, "! Exiting now...")
				os.Exit(0)
			}
		}

		if err = prepareIntegratedDNSServer(); err != nil {
			crashErrorString("Integrated DNS start error:", err)
		}
		dropPrivileges(Config.uid, Config.gid)
		LogAccess, LogAction, LogError = setupLogging()
		startDNSQueryLogger()

		if Config.background != "1" {
			WritePidFile(os.Getpid())
		}
	} else {
		// Unchanged legacy path: DirectSlave controls an external DNS daemon.
		dropPrivileges(Config.uid, Config.gid)
		LogAccess, LogAction, LogError = setupLogging()

		if Config.background == "1" {
			PrintStartValues()
			if _, err = detachSelf(0, 0); err != nil {
				LogError.Println("Server start error:", err)
				crashErrorString("Server start error:", err)
			}
		} else {
			PrintStartValues()
			alive, pid := checkPid()
			if alive {
				fmt.Println("I'm already alive at pid", pid, "! Exiting now...")
				os.Exit(0)
			}
			WritePidFile(os.Getpid())
		}
	}

	updateAvailable = ""
	getLatestVersion()

	router := mux.NewRouter()
	router.HandleFunc("/CMD_API_MEMORY_MONITOR", use(MonitorMemoryAllocation))
	router.HandleFunc("/CMD_API_LOGIN_TEST", use(wLoginTest, AuthBasic))
	router.HandleFunc("/CMD_API_DNS_ADMIN", use(wDnsAdmin, AuthBasic))
	router.HandleFunc("/CMD_API_POP", use(wEmailAdmin, AuthBasic))
	router.NotFoundHandler = http.HandlerFunc(wNotFound)

	router.HandleFunc("/dashboard/domains", use(getDomains, checkWebLoggedIn))
	router.HandleFunc("/dashboard/domains/edit", use(getDomainsEdit, checkWebLoggedIn))
	router.HandleFunc("/dashboard/domains/add", use(getDomainsAdd, checkWebLoggedIn))
	router.HandleFunc("/dashboard/domains/save", use(getDomainsSave, checkWebLoggedIn))
	router.HandleFunc("/dashboard/domains/remove", use(getDomainsRemove, checkWebLoggedIn))
	router.HandleFunc("/dashboard/domains/reload", use(getDomainsReload, checkWebLoggedIn))
	router.HandleFunc("/dashboard/stats", use(getStats, checkWebLoggedIn))

	router.HandleFunc("/dashboard/users", use(getUsers, checkWebLoggedIn))
	router.HandleFunc("/dashboard/users/edit", use(getUsersEdit, checkWebLoggedIn))
	router.HandleFunc("/dashboard/users/add", use(getUsersAdd, checkWebLoggedIn))
	router.HandleFunc("/dashboard/users/save", use(getUsersSave, checkWebLoggedIn))
	router.HandleFunc("/dashboard/users/remove", use(getUsersRemove, checkWebLoggedIn))

	router.HandleFunc("/dashboard/logs", use(getLogs, checkWebLoggedIn))
	router.HandleFunc("/dashboard/logs/view", use(getLogsView, checkWebLoggedIn))
	router.HandleFunc("/login", use(webLogin, checkWebLoggedIn))
	router.HandleFunc("/logout", use(webLogout, checkWebLoggedIn))
	router.HandleFunc("/", use(getRoot, checkWebLoggedIn))
	router.PathPrefix("/").Handler(http.FileServer(http.Dir(webroot)))

	http.Handle("/", router)

	sig := initSignalChannel()
	if !integratedDNSEnabled() && Config.rndc_path != "none" {
		rchan = make(chan int, 2048)
		go startRndcProcessor(rchan)
	}

	go startLatestVersionChecker()
	go catchSignal(sig)

	if Config.pprof_enable == "1" {
		professor.Launch("localhost:" + Config.pprof_port)
	}
	if integratedDNSEnabled() {
		StartDNSProfiler()
	}

	sessionstore = sessions.NewCookieStore([]byte(Config.cookie_auth_key))

	if integratedDNSEnabled() {
		startIntegratedDNSServer()
		// After the privileges are dropped, so the socket belongs to the
		// DirectSlave uid and its permissions are the access control.
		startControlSocket()
	}

	if Config.ssl == "on" {
		go func() {
			err = http.ListenAndServe(joinListenAddr(Config.host, Config.port), context.ClearHandler(http.DefaultServeMux))
			LogError.Println("Server "+Config.host+":"+Config.port+" start error!", err)
			crashErrorString("Server "+Config.host+":"+Config.port+" start error!", err)
		}()

		tlsCfg := &tls.Config{
			// Preserve the v3/v4 TLS range for legacy compatibility. Security
			// hardening can be done as a separate compatibility-breaking change.
			MinVersion: tls.VersionSSL30,
			MaxVersion: tls.VersionTLS12,
		}
		srv := &http.Server{
			Addr:      joinListenAddr(Config.host, Config.sslport),
			Handler:   router,
			TLSConfig: tlsCfg,
		}

		kpr, keyErr := NewKeypairReloader(Config.ssl_cert, Config.ssl_key)
		if keyErr != nil {
			crashErrorString("Server "+Config.host+":"+Config.sslport+" start error!", keyErr)
		}
		srv.TLSConfig.GetCertificate = kpr.GetCertificateFunc()

		err = srv.ListenAndServeTLS("", "")
		if err != nil {
			LogError.Println("Server "+Config.host+":"+Config.sslport+" start error!", err)
			crashErrorString("Server "+Config.host+":"+Config.sslport+" start error!", err)
		}
	} else {
		err = http.ListenAndServe(joinListenAddr(Config.host, Config.port), context.ClearHandler(http.DefaultServeMux))
		if err != nil {
			LogError.Println("Server "+Config.host+":"+Config.port+" start error!", err)
			crashErrorString("Server "+Config.host+":"+Config.port+" start error!", err)
		}
	}
}

func use(h http.HandlerFunc, middleware ...func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
	for _, m := range middleware {
		h = m(h)
	}
	return h
}

func getRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dashboard/domains", 302)
}
