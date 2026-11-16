package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
)

// The authfile holds one "user:hash" entry per line. Every function here
// matches the user name as a plain string up to the first ':'. It used to be
// spliced into a regular expression, so updating "a.c" also rewrote "abc", and
// a name with a bracket in it crashed the command.

// authMutex serializes the read-modify-write cycles of the dashboard handlers.
var authMutex sync.Mutex

// authLineUser returns the user name of an authfile line. Blank lines, comment
// lines and lines without a ':' have none.
func authLineUser(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}

	i := strings.IndexByte(line, ':')
	if i <= 0 {
		return "", false
	}

	return line[:i], true
}

// readAuthFile returns the lines of the authfile. A file that does not exist
// yet is an empty one: the first --password creates it.
func readAuthFile() ([]string, error) {
	data, err := ioutil.ReadFile(Config.authfile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	text := strings.Replace(string(data), "\r\n", "\n", -1)
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil, nil
	}

	return strings.Split(text, "\n"), nil
}

// writeAuthFile rewrites the authfile. An existing file keeps its mode; a new
// one is created readable by the DirectSlave user only, it holds password
// hashes.
func writeAuthFile(lines []string) error {
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}

	return ioutil.WriteFile(Config.authfile, []byte(text), 0600)
}

// listAuthUsers returns the user names in file order.
func listAuthUsers() []string {
	lines, err := readAuthFile()
	if err != nil {
		reportAuthError(err)
		return nil
	}

	var users []string
	for _, line := range lines {
		if user, ok := authLineUser(line); ok {
			users = append(users, user)
		}
	}

	return users
}

func UserExist(user string) (exist bool) {
	for _, name := range listAuthUsers() {
		if name == user {
			return true
		}
	}

	return false
}

// setAuthEntry stores entry ("user:hash") for user. It replaces the existing
// line of that user, or appends a new one, and reports which of the two
// happened. Duplicate lines of the same user, which older versions could
// leave behind, collapse into one.
func setAuthEntry(user string, entry string) (created bool, err error) {
	authMutex.Lock()
	defer authMutex.Unlock()

	lines, err := readAuthFile()
	if err != nil {
		return false, err
	}

	out := make([]string, 0, len(lines)+1)
	replaced := false

	for _, line := range lines {
		name, ok := authLineUser(line)
		if ok && name == user {
			if replaced == false {
				out = append(out, entry)
				replaced = true
			}
			continue
		}
		out = append(out, line)
	}

	if replaced == false {
		out = append(out, entry)
	}

	return replaced == false, writeAuthFile(out)
}

// removeAuthEntry deletes every line of user and returns how many there were.
func removeAuthEntry(user string) (removed int, err error) {
	authMutex.Lock()
	defer authMutex.Unlock()

	lines, err := readAuthFile()
	if err != nil {
		return 0, err
	}

	out := make([]string, 0, len(lines))

	for _, line := range lines {
		name, ok := authLineUser(line)
		if ok && name == user {
			removed++
			continue
		}
		out = append(out, line)
	}

	if removed == 0 {
		return 0, nil
	}

	return removed, writeAuthFile(out)
}

// reportAuthError writes to the error log when the daemon is running and to
// stderr from the command line, where the loggers are not set up.
func reportAuthError(err error) {
	if err == nil {
		return
	}

	if LogError != nil {
		LogError.Println("FATAL: authfile " + Config.authfile + ": " + err.Error())
		return
	}

	fmt.Fprintln(os.Stderr, "ERROR: authfile "+Config.authfile+": "+err.Error())
}

// The three functions below are what the dashboard calls.

func DeleteUserFromFile(str string) {
	_, err := removeAuthEntry(str)
	reportAuthError(err)
}

func PasswordUserUpdateFile(login string, pair string) {
	_, err := setAuthEntry(login, pair)
	reportAuthError(err)
}

func PasswordUserAddToFile(login string, pair string) (err error) {
	_, err = setAuthEntry(login, pair)
	reportAuthError(err)
	return
}
