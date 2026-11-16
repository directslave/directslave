package main

import (
	"fmt"
	"net/http"
	"runtime"
)

func MonitorMemoryAllocation(w http.ResponseWriter, r *http.Request) {

	loggerAccess(r)

	if Config.debug == "1" {
		// DEBUG all post/get/request env
		LogError.Printf("DEBUG: %+v", r)
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if Config.debug == "1" {
		LogAction.Println("*** Memory usage requested ***")
		LogAction.Println("Alloc:", mem.Alloc)
		LogAction.Println("TotalAlloc:", mem.TotalAlloc)
		LogAction.Println("HeapAlloc:", mem.HeapAlloc)
		LogAction.Println("HeapSys:", mem.HeapSys)
	}

	r.Close = true

	reply := fmt.Sprintf("alloc=%d&total alloc=%d&heap alloc=%d&sys heap=%d", mem.Alloc, mem.TotalAlloc, mem.HeapAlloc, mem.HeapSys)

	replyString(w, reply)

	return
}
