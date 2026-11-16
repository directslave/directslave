package main

import (
	"fmt"
	"net/http"
	"text/template"
)

func replyString(w http.ResponseWriter, reply string) {

	w.Header().Set("Server", VERSION)
	w.Header().Set("Connection", "close")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Pragma", "no-cache")

	reply, _ = UrlEncode(reply)

	fmt.Fprintln(w, reply)
	return

}

func outputTemplate(w http.ResponseWriter, tmpl string, Output Out) {

	t, err := template.ParseFiles(templates + "/" + tmpl + ".tpl")
	if err != nil {
		// fmt.Println(err)
	}

	Output.NewVersionAvail = updateAvailable

	// The statistics page and the enriched zone table only exist when the zone
	// registry does, so the navigation has to know which backend is running.
	Output.Integrated = integratedDNSEnabled()

	w.Header().Set("Server", VERSION)
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Pragma", "no-cache")

	err = t.Execute(w, Output)
	if err != nil {
		LogError.Println(err)
	}

	return

}
