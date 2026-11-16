package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Exit codes of --password and --delete.
const (
	exitAuthUsage = 200 // wrong arguments, as before
	exitAuthError = 1   // nothing was changed
)

// validNewUserName is the rule for a user that does not exist yet, the same
// one the dashboard applies. A name that is already in the authfile is always
// accepted, whatever it looks like, so an entry created by an older version
// can still get a new password or be deleted.
func validNewUserName(user string) bool {
	return user != "" && len(user) <= 64 && validateData(user, "user")
}

// passwordProblem returns why a password cannot be used, or "".
func passwordProblem(pass string) string {
	if pass == "" {
		return "Empty password."
	}

	for _, c := range pass {
		if c < 0x20 || c == 0x7f {
			return "Password contains a control character (tab, escape or similar)."
		}
	}

	return ""
}

// readPasswordLine reads one line from reader. When in is a terminal the
// prompt is shown and the typed characters are not echoed.
func readPasswordLine(in *os.File, reader *bufio.Reader, interactive bool, prompt string) (string, error) {
	if interactive {
		fmt.Print(prompt)

		restore, err := disableEcho(in)
		if err != nil {
			// /dev/null and other devices that are not a terminal end up
			// here. Reading still works, it just cannot be hidden.
			fmt.Fprintln(os.Stderr, "\nWarning: cannot hide the input ("+err.Error()+").")
		} else {
			defer restore()
		}
	}

	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}

	return strings.TrimRight(line, "\r\n"), nil
}

// readNewPassword asks for the password twice on a terminal. With standard
// input redirected it reads the password from the first line; a second line,
// if there is one, has to repeat it.
func readNewPassword(user string) (string, error) {
	in := os.Stdin
	reader := bufio.NewReader(in)
	interactive := isTerminal(in)

	first, err := readPasswordLine(in, reader, interactive, "Enter new password for "+user+": ")
	if err != nil {
		if err == io.EOF {
			return "", errors.New("No password given.")
		}
		return "", err
	}

	second, err := readPasswordLine(in, reader, interactive, "Repeat new password for "+user+": ")
	if err != nil {
		if err == io.EOF && interactive == false {
			return first, nil
		}
		if err == io.EOF {
			return "", errors.New("Password mismatch.")
		}
		return "", err
	}

	if first != second {
		return "", errors.New("Password mismatch.")
	}

	return first, nil
}

func UpdatePasswordFile() {
	if len(os.Args) < 3 || os.Args[2] == "" {
		fmt.Println("No username, please input username: --password <user>")
		os.Exit(exitAuthUsage)
	}

	if len(os.Args) > 3 {
		fmt.Println("Too many arguments. Usage: --password <user>")
		fmt.Println("The password is asked for, it is not given on the command line.")
		os.Exit(exitAuthUsage)
	}

	user := os.Args[2]

	if strings.Contains(user, ":") {
		fmt.Println("The password is no longer accepted on the command line, where it stays")
		fmt.Println("in the shell history and is visible in the process list.")
		fmt.Println("Usage: --password <user>")
		os.Exit(exitAuthUsage)
	}

	exists := UserExist(user)

	if exists == false && validNewUserName(user) == false {
		fmt.Println("Invalid username. Use latin letters and digits only (64 characters at most).")
		os.Exit(exitAuthUsage)
	}

	pass, err := readNewPassword(user)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(exitAuthError)
	}

	if problem := passwordProblem(pass); problem != "" {
		fmt.Println(problem, "Nothing changed.")
		os.Exit(exitAuthError)
	}

	created, err := setAuthEntry(user, GenerateHashString(user, pass))
	if err != nil {
		reportAuthError(err)
		os.Exit(exitAuthError)
	}

	if created {
		fmt.Println("User " + user + " created (added to " + Config.authfile + ").")
	} else {
		fmt.Println("Password for " + user + " changed.")
	}

	os.Exit(0)
}

func DeleteFromPasswordFile() {
	if len(os.Args) < 3 || os.Args[2] == "" {
		fmt.Println("No username, please input username.")
		os.Exit(exitAuthUsage)
	}

	user := os.Args[2]

	removed, err := removeAuthEntry(user)
	if err != nil {
		reportAuthError(err)
		os.Exit(exitAuthError)
	}

	if removed == 0 {
		fmt.Println("No entry for user " + user + " found in authfile.")
		os.Exit(exitAuthError)
	}

	fmt.Println("User " + user + " entry removed from authfile.")
	os.Exit(0)
}
