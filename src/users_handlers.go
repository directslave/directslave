package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/sessions"
)

func getUsers(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	page, _ := strconv.ParseInt(r.FormValue("page")[0:], 10, 64)

	if page == 0 {
		page = 1
	}

	search := r.FormValue("search")

	Users, pages := scanAllUsers(page, search)

	var pager string

	if pages > 1 {
		for i := 1; i <= pages; i++ {
			selected := ""
			if int64(i) == page {
				selected = `selected="selected"`
			}
			pager += fmt.Sprintf(`<option %s value="%d">Page %d of %d</option>`, selected, i, i, pages)
		}
	}

	var Output = Out{
		Users:   Users,
		Product: VERSION,
		Pager:   pager,
	}

	outputTemplate(w, "users", Output)

	return
}

func getUsersAdd(w http.ResponseWriter, r *http.Request) {

	var Output = Out{Product: VERSION, CustomAction: "add", CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "Add new user"}

	outputTemplate(w, "users", Output)

	return
}

func getUsersEdit(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	user := r.FormValue("user")

	if validateData(user, "user") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed User name.</div>"}
		outputTemplate(w, "users", Output)
		return
	}

	if user == "" {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Empty User name.</div>"}
		outputTemplate(w, "users", Output)
		return
	}

	var Output = Out{Product: VERSION, CustomAction: "edit", CustomRow: "yes", CustomName: user, CustomValue: "", ErrorMessage: "Edit user " + user}

	outputTemplate(w, "users", Output)

	return
}

func getUsersSave(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	user := r.FormValue("CustomName")
	password := r.FormValue("CustomValue")
	action := r.FormValue("CustomAction")

	if validateData(user, "user") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed User name.</div>"}
		outputTemplate(w, "users", Output)
		return
	}

	if validateData(action, "user") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed Action name.</div>"}
		outputTemplate(w, "users", Output)
		return
	}

	var ErrorString string

	if user == "" {
		ErrorString += "<b>Error</b>: Empty user value<br>"
	}

	if password == "" {
		ErrorString += "<b>Error</b>: Empty password value<br>"
	}

	if action == "" {
		ErrorString += "<b>Error</b>: Empty action value<br>"
	}

	if ErrorString == "" {
		exists := UserExist(user)

		// fmt.Println(exists)

		if exists == true && action == "add" {
			ErrorString = "<b>Error</b>: Add imposible. User <u>" + user + "</u> already exist. Consider edit it."
		}

		if exists == false && action == "edit" {
			ErrorString = "<b>Error</b>: Edit impossible. User <u>" + user + "</u> does not exist. Consider adding it."
		}

		if ErrorString == "" {
			switch action {
			case "add":
				PasswordUserAddToFile(user, GenerateHashString(user, password))
				LogAction.Println("User " + user + " added by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "].")
				ErrorString = "User <u>" + user + "</u> added."
			case "edit":
				PasswordUserUpdateFile(user, GenerateHashString(user, password))
				LogAction.Println("User " + user + " changed by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "].")
				ErrorString = "User <u>" + user + "</u> saved."
			default:
				ErrorString = "<b>Error</b>: Unknown action."
			}

		}
	}

	var Output = Out{Product: VERSION, CustomRow: "yes", CustomAction: action, CustomName: user, CustomValue: "", ErrorMessage: ErrorString}

	outputTemplate(w, "users", Output)

	return

}

func getUsersRemove(w http.ResponseWriter, r *http.Request) {

	r.ParseForm()

	user := r.FormValue("user")

	if validateData(user, "user") == false {
		var Output = Out{Product: VERSION, CustomRow: "yes", CustomName: "", CustomValue: "", ErrorMessage: "<div style=\"text-align: center; font-size: 15pt; color: #E12F2F; font-weight: bold;\">Malformed User name.</div>"}
		outputTemplate(w, "users", Output)
		return
	}

	var ErrorString string

	if user == "" {
		ErrorString += "<b>Error</b>: Empty user value<br>"
	}

	if ErrorString == "" {
		exists := UserExist(user)

		if exists == false {
			ErrorString = "<b>Error</b>: Remove impossible. User <u>" + user + "</u> does not exist."
		}

		if ErrorString == "" {
			DeleteUserFromFile(user)
			LogAction.Println("User " + user + " removed by " + getCurrentUser(r) + " [" + ClientIP(r.RemoteAddr) + "].")
			ErrorString = "User <u>" + user + "</u> removed."
		}
	}

	var Output = Out{Product: VERSION, ErrorMessage: ErrorString}

	outputTemplate(w, "users", Output)

	return

}

func scanAllUsers(page int64, search string) (Users []User, pages int) {

	Output := new(Out)

	// items per page
	items := int64(50)

	// The search box is a plain substring match. It used to be compiled as a
	// regular expression, and an unbalanced bracket crashed the request.
	i := int64(0)

	for _, name := range listAuthUsers() {
		if search != "" && strings.Contains(name, search) == false {
			continue
		}

		i++
		if i <= items*page && i >= (items*page)-items {
			Output.AddItemUser(User{name, "*****************"})
		}
	}

	ppages := float64(i) / float64(items)

	return Output.Users, int(Round(ppages, .0, 0))

}

func (user *Out) AddItemUser(item User) []User {
	user.Users = append(user.Users, item)
	return user.Users
}

func getCurrentUser(r *http.Request) string {

	store := sessions.NewCookieStore([]byte(Config.cookie_auth_key + ClientIP(r.RemoteAddr)))

	session, _ := store.Get(r, Config.cookie_sess_id)

	user := fmt.Sprintf("%s", session.Values["username"])

	return user

}
