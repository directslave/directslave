package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type ctxKey string

const ctxRawBodyKey ctxKey = "rawBody"

func wDnsAdmin(w http.ResponseWriter, r *http.Request) {

	loggerAccess(r)

	// DEBUG: загальна структура реквеста (без body)
	if Config.debug == "1" {
		LogError.Printf("DEBUG: %+v", r)
	}

	// Читаємо body ОДИН раз для POST/PUT, зберігаємо в context, відновлюємо r.Body.
	// Логуємо body лише якщо debug=1.
	if r.Method == "POST" || r.Method == "PUT" {
		const maxBody = 2 << 20 // 2MB
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			LogError.Printf("Error reading body: %v", err)
			http.Error(w, "can't read body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		r = r.WithContext(context.WithValue(r.Context(), ctxRawBodyKey, b))

		if Config.debug == "1" {
			if len(b) > 0 {
				LogError.Printf("DEBUG: BODY: %s", string(b))
			} else {
				LogError.Printf("DEBUG: BODY: <empty>")
			}
		}
	}

	// Парсимо query + form-body (якщо Content-Type = x-www-form-urlencoded)
	if err := r.ParseForm(); err != nil {
		LogError.Printf("Error parsing form: %v", err)
		http.Error(w, "can't parse form", http.StatusBadRequest)
		return
	}

	action := r.Form.Get("action")

	// Fallback: деякі версії/виклики можуть прислати form-urlencoded body без Content-Type.
	// Стару сумісність не ламаємо: query вже в r.Form; тут лише додаємо з body, якщо action порожній.
	if action == "" {
		if v := r.Context().Value(ctxRawBodyKey); v != nil {
			if b, ok := v.([]byte); ok && len(b) > 0 {
				m, err := url.ParseQuery(string(b))
				if err == nil && len(m) > 0 {
					if r.PostForm == nil {
						r.PostForm = make(url.Values)
					}
					if r.Form == nil {
						r.Form = make(url.Values)
					}
					for k, vv := range m {
						for _, val := range vv {
							r.PostForm.Add(k, val)
							r.Form.Add(k, val)
						}
					}
					action = r.Form.Get("action")
				}
			}
		}
	}

	switch action {

	case "exists":
		wDomainExist(w, r)
		return

	case "rawsave":
		wDomainAdd(w, r)
		return

	case "delete":
		// DA може прислати:
		// - старий формат: query action=delete&select0=...
		// - новий формат: body action=delete&select0=...
		// - домен як domain=...
		// - або select0/select1/... для масового видалення
		domains := make([]string, 0, 4)

		if d := r.Form.Get("domain"); d != "" {
			domains = append(domains, d)
		}

		for k, vv := range r.Form {
			if !strings.HasPrefix(k, "select") {
				continue
			}
			for _, v := range vv {
				if v != "" {
					domains = append(domains, v)
				}
			}
		}

		if len(domains) == 0 {
			replyString(w, "error=1&details=No domain specified")
			return
		}

		denied := make([]string, 0, 2)

		for _, domain := range domains {
			if ok := domainDeleteOne(r, domain); !ok {
				denied = append(denied, domain)
			}
		}

		if len(denied) > 0 {
			replyString(w, "error=1&details=No permission to remove domain(s): "+strings.Join(denied, ", "))
			return
		}

		replyString(w, "error=0&text=Zone record removed")
		return
	}

	replyString(w, "error=1&details=No action taken")
}

// Повертає true якщо:
// - домен існує і master = поточний клієнт => видалили
// - домен не існує => ідемпотентно вважаємо OK
// Повертає false лише якщо домен існує, але master інший (нема прав).
func domainDeleteOne(r *http.Request, domain string) bool {

	loggerAccess(r)

	if Config.debug == "1" {
		LogError.Printf("DEBUG: %+v", r)
	}

	// Not converted: DomainExist() matches both spellings, so a zone declared
	// in its native script before this version is still found and removed.
	exist, master := DomainExist(domain)

	if exist == true {
		if sameIP(master, ClientIP(r.RemoteAddr)) {
			DomainDelete(domain, master)
			return true
		}

		LogAction.Println("Domain", domain, "owned by", master, ", delete forbidden for", ClientIP(r.RemoteAddr))
		return false
	}

	// Ідемпотентність: якщо немає named/зони/файла — delete все одно успішний
	LogAction.Println("Domain", domain, "delete: nothing to delete for", ClientIP(r.RemoteAddr))
	return true
}

func wDomainDelete(w http.ResponseWriter, r *http.Request, domain string) {
	// Wrapper для сумісності, якщо десь ще викликається.
	if domainDeleteOne(r, domain) {
		replyString(w, "error=0&text=Zone record removed")
		return
	}
	replyString(w, "error=1&details=No permission to remove domain "+domain)
}

func wDomainAdd(w http.ResponseWriter, r *http.Request) {

	loggerAccess(r)

	param := make(map[string]string)

	// Для rawsave body = повний zonefile. Беремо з context (ParseForm міг з'їсти r.Body).
	if v := r.Context().Value(ctxRawBodyKey); v != nil {
		if b, ok := v.([]byte); ok {
			param["body"] = string(b)
		}
	}
	if _, ok := param["body"]; !ok {
		param["body"] = ""
	}

	if Config.debug == "1" {
		LogError.Printf("DEBUG: %+v", r)
	}

	// DirectAdmin sends the name the way the user typed it, which for an
	// internationalized domain is the native script. The primary serves it in
	// punycode and every query asks for punycode, so it is converted here.
	domain, valid := validateDomainInput(r.FormValue("domain"))
	if valid == false {
		replyString(w, "error=1&details=invalid domain or not called through API")
		return
	}

	exist, master := DomainExist(domain)
	username := r.FormValue("username")

	param["zonedir"] = fmt.Sprintf("%s", Config.named_workdir)
	param["domain"] = fmt.Sprintf("%s", domain)
	param["master"] = fmt.Sprintf("%s", master)
	param["username"] = fmt.Sprintf("%s", username)

	if exist == true {
		if sameIP(master, ClientIP(r.RemoteAddr)) {
			DomainReload(domain, master)
			RunExternalScript("DNSADM", param, "dns_write_post.sh")
			replyString(w, "error=0&text=Zone record reloaded")
			return
		} else {
			LogAction.Println("Domain", domain, "reload forbidden for", master)
			replyString(w, "error=1&details=Domain already exist on master"+master)
			return
		}
	} else {
		param["master"] = fmt.Sprintf("%s", ClientIP(r.RemoteAddr))
		RunExternalScript("DNSADM", param, "dns_write_post.sh")
	}

	AddDomain(domain, ClientIP(r.RemoteAddr))
	replyString(w, "error=0&details=Zone record added")
	return
}

func wDomainExist(w http.ResponseWriter, r *http.Request) {

	domain, valid := validateDomainInput(r.FormValue("domain"))
	if valid == false {
		replyString(w, "error=1&details=invalid domain or not called through API")
		return
	}

	LogAction.Println("Checking", domain, "from", ClientIP(r.RemoteAddr))

	exists, master := DomainExist(domain)
	if exists == true {
		replyString(w, "error=0&exists=1&master="+master+"&details=Domain exists on "+master)
		LogAction.Println("Domain", domain, "exists on primary", master)
	} else {
		replyString(w, "error=0&exists=0")
		LogAction.Println("Domain", domain, "not exists")
	}

	return
}
