package main

import "net/http"

func wNotFound(w http.ResponseWriter, r *http.Request) {
	loggerAccess(r)

	w.WriteHeader(404)

	replyString(w, "error=1&details=Page not found")

	if Config.debug == "1" {
		// DEBUG all post/get/request env
		LogError.Printf("DEBUG: %+v", r)
	}

	return
}
