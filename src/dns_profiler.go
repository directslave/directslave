package main

import (
	"runtime"
	"time"
)

var dnsStartTime = time.Now()

func StartDNSProfiler() {
	if Config.pprof_enable == "1" {
		dnsLogPrintf("STARTUP: Pprof profiler enabled on localhost:%s", Config.pprof_port)
	} else {
		dnsLogPrintln("STARTUP: Pprof profiler disabled")
	}
}

func CallDNSProfiler() {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	dnsLogPrintf("PROFILER: Uptime %s. Allocated memory map: %d bytes; task queue: %d.",
		time.Since(dnsStartTime), mem.TotalAlloc, runtime.NumGoroutine())
}
