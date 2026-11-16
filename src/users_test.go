package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useTempAuthFile(t *testing.T, content string) string {
	t.Helper()

	dir, err := ioutil.TempDir("", "ds-auth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "passwd")
	if content != "" {
		if err := ioutil.WriteFile(path, []byte(content), 0640); err != nil {
			t.Fatal(err)
		}
	}

	previous := Config.authfile
	Config.authfile = path
	t.Cleanup(func() { Config.authfile = previous })

	return path
}

func readFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAuthLineUser(t *testing.T) {
	cases := []struct {
		line string
		user string
		ok   bool
	}{
		{"admin:$1$salt$hash", "admin", true},
		{"a.c:$1$salt$hash", "a.c", true},
		{"# admin:$1$salt$hash", "", false},
		{"   # comment", "", false},
		{"", "", false},
		{"no-colon-here", "", false},
		{":orphan", "", false},
	}

	for _, c := range cases {
		user, ok := authLineUser(c.line)
		if user != c.user || ok != c.ok {
			t.Errorf("authLineUser(%q) = %q, %v; want %q, %v", c.line, user, ok, c.user, c.ok)
		}
	}
}

// A user name is data, never a pattern: "a.c" must not match "abc".
func TestSetAuthEntryMatchesNameExactly(t *testing.T) {
	path := useTempAuthFile(t, "abc:OLD-ABC\na.c:OLD-DOT\n# keep me\na(b:OLD-PAREN\n")

	created, err := setAuthEntry("a.c", "a.c:NEW-DOT")
	if err != nil || created {
		t.Fatalf("update of a.c: created=%v err=%v", created, err)
	}

	created, err = setAuthEntry("a(b", "a(b:NEW-PAREN")
	if err != nil || created {
		t.Fatalf("update of a(b: created=%v err=%v", created, err)
	}

	want := "abc:OLD-ABC\na.c:NEW-DOT\n# keep me\na(b:NEW-PAREN\n"
	if got := readFileString(t, path); got != want {
		t.Fatalf("authfile:\n%s\nwant:\n%s", got, want)
	}
}

func TestSetAuthEntryCreatesFileAndUser(t *testing.T) {
	path := useTempAuthFile(t, "")

	created, err := setAuthEntry("admin", "admin:HASH")
	if err != nil || created == false {
		t.Fatalf("created=%v err=%v", created, err)
	}

	if got := readFileString(t, path); got != "admin:HASH\n" {
		t.Fatalf("authfile = %q", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new authfile mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSetAuthEntryKeepsModeAndCollapsesDuplicates(t *testing.T) {
	path := useTempAuthFile(t, "dup:ONE\nother:X\ndup:TWO\n")

	if _, err := setAuthEntry("dup", "dup:NEW"); err != nil {
		t.Fatal(err)
	}

	if got := readFileString(t, path); got != "dup:NEW\nother:X\n" {
		t.Fatalf("authfile = %q", got)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("existing authfile mode changed to %o", info.Mode().Perm())
	}
}

func TestRemoveAuthEntry(t *testing.T) {
	path := useTempAuthFile(t, "abc:1\na.c:2\n")

	removed, err := removeAuthEntry("a.c")
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}

	removed, err = removeAuthEntry("nobody")
	if err != nil || removed != 0 {
		t.Fatalf("unknown user: removed=%d err=%v", removed, err)
	}

	if got := readFileString(t, path); got != "abc:1\n" {
		t.Fatalf("authfile = %q", got)
	}
}

func TestUserExistAndList(t *testing.T) {
	useTempAuthFile(t, "abc:1\r\n# x:1\n\na.c:2\n")

	if UserExist("abc") == false || UserExist("a.c") == false {
		t.Fatal("existing users not found")
	}
	if UserExist("x") || UserExist("ab") || UserExist("a") {
		t.Fatal("a user that is not in the file was found")
	}
	if got := strings.Join(listAuthUsers(), ","); got != "abc,a.c" {
		t.Fatalf("listAuthUsers = %q", got)
	}
}

func TestPasswordProblem(t *testing.T) {
	for _, pass := range []string{"pa:ss:word", "p@ss w0rd!#$%^&*()", " lead and trail ", "пароль-密码", `q"uo'te\back`} {
		if problem := passwordProblem(pass); problem != "" {
			t.Errorf("passwordProblem(%q) = %q, want none", pass, problem)
		}
	}
	for _, pass := range []string{"", "tab\there", "esc\x1b[A", "del\x7f"} {
		if passwordProblem(pass) == "" {
			t.Errorf("passwordProblem(%q) accepted", pass)
		}
	}
}
