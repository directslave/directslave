package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

var LogAccess, LogAction, LogError, LogDns, LogDnsQuery *log.Logger

var dnsQueryLoggerMu sync.RWMutex

func setupLogging() (LogAccess, LogAction, LogError *log.Logger) {
	accessFile, err := os.OpenFile(Config.access_log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	crashError(err)

	actionFile, err := os.OpenFile(Config.action_log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	crashError(err)

	errorFile, err := os.OpenFile(Config.error_log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	crashError(err)

	// no defer.close, stay opened
	accessWriter := io.Writer(accessFile)
	actionWriter := io.Writer(actionFile)
	errorWriter := io.Writer(errorFile)

	LogAction = log.New(actionWriter, "", log.Ldate|log.Ltime)
	LogAccess = log.New(accessWriter, "", log.Ldate|log.Ltime)
	LogError = log.New(errorWriter, "", log.Ldate|log.Ltime)

	// DNS logging is optional. With an old config it aliases action.log and
	// therefore introduces no new mandatory path or permission requirement.
	LogDns = LogAction
	if integratedDNSEnabled() && Config.dns_server_log != "" && Config.dns_server_log != Config.action_log {
		dnsFile, dnsErr := os.OpenFile(Config.dns_server_log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if dnsErr != nil {
			LogError.Printf("DNS: cannot open dns_server_log %s: %v; using action log", Config.dns_server_log, dnsErr)
		} else {
			LogDns = log.New(io.Writer(dnsFile), "", log.Ldate|log.Ltime)
		}
	}

	dnsQueryLoggerMu.Lock()
	LogDnsQuery = nil
	if integratedDNSEnabled() && dnsQueryLogRequested() && Config.dns_query_log_file != "" {
		queryFile, queryErr := os.OpenFile(Config.dns_query_log_file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if queryErr != nil {
			LogError.Printf("DNS QUERY LOG: cannot open %s: %v; query logging disabled", Config.dns_query_log_file, queryErr)
		} else {
			LogDnsQuery = log.New(io.Writer(queryFile), "", log.Ldate|log.Ltime)
		}
	}
	dnsQueryLoggerMu.Unlock()

	return
}

func getDNSQueryLogger() *log.Logger {
	dnsQueryLoggerMu.RLock()
	defer dnsQueryLoggerMu.RUnlock()
	return LogDnsQuery
}

func loggerAccess(r *http.Request) {
	LogAccess.Printf("[%s] \"%s %s %s\"", ClientIP(r.RemoteAddr), r.Method, r.URL, r.Proto)
	return
}

func checkError(err error) (ok bool) {
	ok = false
	if err != nil {
		ok = true
		fmt.Println("\nERROR:", err)
	}
	return
}

func crashErrorString(str string, err error) {
	if err != nil {
		fmt.Println(str, err.Error())
		os.Exit(250)
	}
	return
}

func crashError(err error) {
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(255)
	}
	return
}
