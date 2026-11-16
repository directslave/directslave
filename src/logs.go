package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

func getLogsView(w http.ResponseWriter, r *http.Request) {

	Files := map[string]string{
		"access.log": Config.access_log,
		"action.log": Config.action_log,
		"error.log":  Config.error_log,
	}
	if integratedDNSEnabled() {
		Files["dns.log"] = Config.dns_server_log
		if dnsQueryLogRequested() {
			Files["dns-query.log"] = Config.dns_query_log_file
		}
	}

	file := r.FormValue("file")

	if Files[file] == "" {
		var Output = Out{Product: VERSION, CustomAction: "", CustomRow: "", CustomName: "", CustomValue: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Invalid file name requested.</div>"}
		outputTemplate(w, "logs", Output)
		return
	}

	length, err := strconv.Atoi(r.FormValue("length"))

	if length == 0 || length >= 1001 {
		length = 1000
	}

	content, err := GoTail(Files[file], length)
	if err != nil {
		LogError.Printf("ERROR: %s", err)
		content = "File is 0 lines long."
	}

	var Output = Out{
		CustomValue: fmt.Sprintf("%s", strings.Replace(content, "\n", "<br>", -1)),
		CustomName:  file,
		Product:     VERSION,
	}

	outputTemplate(w, "logs", Output)

	return

}

func getLogs(w http.ResponseWriter, r *http.Request) {
	files := []struct {
		name string
		path string
	}{
		{"access.log", Config.access_log},
		{"action.log", Config.action_log},
		{"error.log", Config.error_log},
	}
	if integratedDNSEnabled() && Config.dns_server_log != "" && Config.dns_server_log != Config.action_log {
		files = append(files, struct {
			name string
			path string
		}{"dns.log", Config.dns_server_log})
	}
	if integratedDNSEnabled() && dnsQueryLogRequested() && Config.dns_query_log_file != "" {
		files = append(files, struct {
			name string
			path string
		}{"dns-query.log", Config.dns_query_log_file})
	}

	logFiles := make([]LogFile, 0, len(files))
	for _, item := range files {
		size := ""
		info, err := os.Stat(item.path)
		if err != nil {
			size = fmt.Sprintln(err)
		} else {
			size = fmt.Sprintf("%d", info.Size())
		}
		logFiles = append(logFiles, LogFile{item.name, size})
	}

	var Output = Out{LogFiles: logFiles, Product: VERSION}
	outputTemplate(w, "logs", Output)
}
