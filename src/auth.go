package main

// mem leak
// https://stackoverflow.com/questions/48199178/how-to-avoid-memory-leak-with-gorilla-sessions-on-google-app-engine-standard-env

import (
	"bufio"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"text/template"

	"github.com/GehirnInc/crypt"
	_ "github.com/GehirnInc/crypt/md5_crypt"
	"github.com/gorilla/context"
	"github.com/gorilla/sessions"
)

var sessionstore = sessions.NewCookieStore() // initialize cookie store

func AuthBasic(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer context.Clear(r)

		if checkLoggedIn(r) == false {

			auth := r.Header.Get("Authorization")

			s := strings.SplitN(auth, " ", 2)
			if len(s) != 2 {
				replyString(w, "error=1&text=Login FAIL&details=Login failed")
				LogAction.Println("Authorization failed from", ClientIP(r.RemoteAddr))
				return
			}

			b, err := base64.StdEncoding.DecodeString(s[1])
			if err != nil {
				replyString(w, "error=1&text=Login FAIL&details=Login failed")
				LogAction.Println("Authorization failed from", ClientIP(r.RemoteAddr))
				return
			}

			pair := strings.SplitN(string(b), ":", 2)
			if len(pair) != 2 {
				replyString(w, "error=1&text=Login FAIL&details=Login failed")
				LogAction.Println("Authorization failed from", ClientIP(r.RemoteAddr))
				return
			}

			if strings.Contains(pair[0], "|") {
				npair := strings.Split(pair[0], "|")
				pair[0] = npair[0]
			}

			ok := checkPassword(pair[0], pair[1])

			if ok == false {
				replyString(w, "error=1&text=Login FAIL&details=Login failed")
				LogAction.Println("Authorization failed from", ClientIP(r.RemoteAddr))
				return
			}

			LogAction.Println("Authorization passed for", pair[0], "from", ClientIP(r.RemoteAddr))
		}

		h.ServeHTTP(w, r)

	}
}

func checkLoggedIn(r *http.Request) bool {
	defer context.Clear(r)

	// store := sessions.NewCookieStore([]byte(Config.cookie_auth_key + ClientIP(r.RemoteAddr))) // тут добавить в конфиг ключ, пусть сами задают

	session, _ := sessionstore.Get(r, Config.cookie_sess_id)

	if session.IsNew == true {
		return false
	}

	if session.Values["auth"] == false {
		return false
	}

	return true

}

func checkWebLoggedIn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer context.Clear(r)

		if r.URL.String() == "/login" {
			h.ServeHTTP(w, r)
			return
		}

		// store := sessions.NewCookieStore([]byte(Config.cookie_auth_key + ClientIP(r.RemoteAddr)))

		session, _ := sessionstore.Get(r, Config.cookie_sess_id)

		if session.IsNew == true {
			http.Redirect(w, r, "/login", 302)
			return
		}

		if session.Values["auth"] == false {
			http.Redirect(w, r, "/login", 302)
			return
		}

		h.ServeHTTP(w, r)

	}

}

func wLoginTest(w http.ResponseWriter, r *http.Request) {
	defer context.Clear(r)

	loggerAccess(r)

	if Config.debug == "1" {
		// DEBUG all post/get/request env
		LogError.Printf("DEBUG: %+v", r)
	}

	replyString(w, "error=0&text=Login OK&details=none")
	return
}

func webLogin(w http.ResponseWriter, r *http.Request) {
	defer context.Clear(r)

	r.ParseForm()

	user := ""
	pass := ""
	message := ""

	if r.FormValue("action") == "Login" {
		user = r.FormValue("user")
		pass = r.FormValue("pass")

		if validateData(user, "user") == false {
			LogError.Println("Malicious user request from  " + ClientIP(r.RemoteAddr) + ".")
			message = "Invalid input."
			showLoginForm(w, r, "", "", message)
			return
		}

		ok := checkPassword(user, pass)

		if ok == true {

			session, _ := sessionstore.Get(r, Config.cookie_sess_id)

			// Set some session values.
			session.Values["auth"] = true
			session.Values["username"] = user
			session.Values["userid"] = 1

			// Save it before we write to the response/return from the handler.
			session.Save(r, w)

			// Sucessfully logged in
			http.Redirect(w, r, "/dashboard/domains", 302)
			LogAction.Println("User " + user + " logged in from " + ClientIP(r.RemoteAddr) + ".")
			return
		} else {
			LogError.Println("User " + user + " login FAILED from " + ClientIP(r.RemoteAddr) + ".")
			message = "Invalid credentials."
		}
	}
	showLoginForm(w, r, "", "", message)
	return
	// }
	// return
}

func webLogout(w http.ResponseWriter, r *http.Request) {
	defer context.Clear(r)

	session, _ := sessionstore.Get(r, Config.cookie_sess_id)

	user := fmt.Sprintf("%s", session.Values["username"])

	// Set some session values.
	session.Values["auth"] = false
	session.Values["username"] = ""
	session.Values["userid"] = 0

	// Save it before we write to the response/return from the handler.
	session.Save(r, w)

	message := "Logged out. Bye-bye!"

	LogAction.Println("User " + user + " logged out from " + ClientIP(r.RemoteAddr) + ". Bye!")
	showLoginForm(w, r, "", "", message)
	return
}

func checkPassword(user string, pass string) (ok bool) {
	ok = false

	pairString := GenerateHashString(user, pass)

	file, err := os.Open(Config.authfile)
	if err != nil {
		LogError.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {

		if pairString == scanner.Text() {
			file.Close()
			return true
		}
	}

	return false
}

func GenerateHashString(user string, pass string) (hashString string) {

	hashString = ""

	binHash := md5.New()

	io.WriteString(binHash, user)

	enc := base64.StdEncoding.EncodeToString(binHash.Sum(nil))

	str := strings.Split(enc, "=")

	crypt := crypt.MD5.New()
	passString, _ := crypt.Generate([]byte(pass), []byte("$1$"+str[0]+"$"))

	hashString = fmt.Sprintf("%s:%s", user, passString)

	return

}

func showLoginForm(w http.ResponseWriter, r *http.Request, user string, pass string, message string) {
	defer context.Clear(r)

	type temp struct {
		User    string
		Pass    string
		Message string
		Product string
	}

	Output := new(temp)

	Output.User = user
	Output.Pass = pass
	Output.Message = message
	Output.Product = VERSION

	tpl, _ := ioutil.ReadFile(templates + "/login.tpl")

	// Parse raw template
	raw, err := template.New("response").Parse(string(tpl))

	// Create a new template and parse the temp into it.
	t := template.Must(raw, err)

	// Execute the template
	_ = t.Execute(w, Output)
	return
}
