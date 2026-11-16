package main

import (
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// DirectAdmin posts the mailbox form to CMD_API_POP without a Content-Type and
// without percent-encoding the values: a body carries raw spaces, raw UTF-8 and
// whatever characters the user put in the password. url.ParseQuery cannot read
// that - it fails on a password containing '%', splits one containing '&' into
// two fields, and would decode escape sequences that were never escapes.
//
// The body is therefore split on field boundaries instead, and the values are
// taken verbatim. A boundary is an '&' followed by a field name and '=', where
// the name has not been seen yet; "&v1x!*" inside a password is not a boundary
// because "v1x!*" is not a name followed by '='.
//
// A client that declares "application/x-www-form-urlencoded" is still read the
// standard way, so anything that does encode its bodies is unaffected.

var daFieldRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}=`)

var jsonSecretRe = regexp.MustCompile(`(?i)"(passwd|password|pass|newpass|oldpass)"\s*:\s*"(?:[^"\\]|\\.)*"`)

// secretFields are never written to the log in clear: DirectAdmin sends the
// mailbox password in the same body.
var secretFields = []string{"passwd", "passwd2", "password", "pass", "newpass", "oldpass"}

func isSecretField(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, secret := range secretFields {
		if name == secret {
			return true
		}
	}
	return false
}

// daKnownFields is the field set DirectAdmin sends to CMD_API_POP. Inside a
// password value only these start a new field, so a password containing
// "&s=w" stays one value. Outside a password any syntactically valid name is
// accepted, so a field added by a future DirectAdmin is still picked up.
var daKnownFields = map[string]bool{
	"action": true, "cluster": true, "domain": true, "user": true,
	"newuser": true, "passwd": true, "passwd2": true, "quota": true,
	"quota_limit": true, "suspended": true, "create": true, "update": true,
	"delete": true, "modify": true, "email": true, "name": true,
	"value": true, "limit": true, "usage": true, "json": true, "api": true,
	"fakepasswordremembered": true, "fakeusernameremembered": true,
}

func isKnownField(name string) bool {
	if daKnownFields[name] {
		return true
	}

	// select0, select1, ... for a multi-mailbox delete.
	if strings.HasPrefix(name, "select") && len(name) > 6 {
		for i := 6; i < len(name); i++ {
			if name[i] < '0' || name[i] > '9' {
				return false
			}
		}
		return true
	}

	return false
}

type daPair struct {
	Name  string
	Value string
}

// fieldStart returns where the value of a top-level field begins, or -1.
func fieldStart(body string, name string) int {
	if strings.HasPrefix(body, name+"=") {
		return len(name) + 1
	}
	if index := strings.Index(body, "&"+name+"="); index >= 0 {
		return index + len(name) + 2
	}
	return -1
}

// passwordSpans returns the byte ranges of the passwd and passwd2 values.
//
// DirectAdmin sends the password twice, and that makes the boundary provable
// rather than guessed: the split is at the "&passwd2=" whose following bytes
// repeat the preceding value exactly and end the field. A password containing
// "&user=", "&passwd2=" or any other field-looking text is therefore kept
// whole, and it cannot decide which mailbox the relay will accept.
func passwordSpans(body string) [][2]int {
	valueStart := fieldStart(body, "passwd")
	if valueStart < 0 {
		return nil
	}

	const marker = "&passwd2="

	// Every place the pair could split, longest first: a password that itself
	// contains "&passwd2=" makes several splits arithmetically valid, and the
	// real value is always the longest one that still leaves a proper field
	// behind it.
	var markers []int
	for offset := valueStart; offset <= len(body)-len(marker); {
		found := strings.Index(body[offset:], marker)
		if found < 0 {
			break
		}
		markers = append(markers, offset+found)
		offset += found + 1
	}

	for index := len(markers) - 1; index >= 0; index-- {
		found := markers[index]

		value := body[valueStart:found]
		secondStart := found + len(marker)
		secondEnd := secondStart + len(value)

		if secondEnd <= len(body) && body[secondStart:secondEnd] == value &&
			pairIsFollowedByAField(body, secondEnd) {
			return [][2]int{{valueStart, found}, {secondStart, secondEnd}}
		}
	}

	return nil
}

// pairIsFollowedByAField tells a real end of the password pair from a split
// that only looks self-consistent. A password of "&passwd2=" makes two splits
// arithmetically valid; only one of them is followed by the next DirectAdmin
// field rather than by another "passwd2".
func pairIsFollowedByAField(body string, at int) bool {
	if at == len(body) {
		return true
	}
	if body[at] != '&' {
		return false
	}

	match := daFieldRe.FindString(body[at+1:])
	if match == "" {
		return false
	}

	name := match[:len(match)-1]
	if name == "passwd" || name == "passwd2" {
		return false
	}

	return isKnownField(name)
}

func insideSpans(spans [][2]int, index int) bool {
	for _, span := range spans {
		// span[0] is the first byte of the value, so a password that starts
		// with '&' is covered too; span[1] is the '&' that ends the field and
		// must stay a boundary.
		if index >= span[0] && index < span[1] {
			return true
		}
	}
	return false
}

// splitDirectAdminBody cuts the body into fields without decoding anything.
func splitDirectAdminBody(body string) []daPair {
	var pairs []daPair

	seen := make(map[string]bool)
	start := 0

	closeSegment := func(end int) {
		segment := body[start:end]
		name, value, found := strings.Cut(segment, "=")
		if found {
			pairs = append(pairs, daPair{Name: name, Value: value})
			seen[name] = true
			return
		}
		if segment != "" {
			// A fragment with no '=' at all: keep it visible for the log.
			pairs = append(pairs, daPair{Name: "", Value: segment})
		}
	}

	// currentField is the field whose value is being accumulated.
	currentField := func() string {
		name, _, found := strings.Cut(body[start:], "=")
		if found == false {
			return ""
		}
		return name
	}

	// The password pair is located first, so no '&' inside it is ever mistaken
	// for the start of a field.
	spans := passwordSpans(body)

	for i := 0; i < len(body); i++ {
		if body[i] != '&' {
			continue
		}

		if insideSpans(spans, i) {
			continue
		}

		match := daFieldRe.FindString(body[i+1:])
		if match == "" {
			continue
		}

		name := match[:len(match)-1]

		if seen[name] {
			// The same field twice is far more likely to be part of a value
			// than a real repetition.
			continue
		}

		// A value that may hold free text, and whose span could not be proved,
		// ends only at a field DirectAdmin actually sends.
		if spans == nil && isSecretField(currentField()) && isKnownField(name) == false {
			continue
		}

		closeSegment(i)
		start = i + 1
	}

	closeSegment(len(body))
	return pairs
}

// parseMailBody returns the fields and how they were read, for the log.
//
// The Content-Type decides. DirectAdmin sends none and does not encode, so its
// values are taken verbatim; a client that declares the standard form encoding
// is decoded the standard way. Guessing from the body itself does not work: a
// raw password of plain ASCII is indistinguishable from an encoded one, and
// treating it as encoded turns "b++a1" into "b  a1" and cuts everything after
// an "&".
func parseMailBody(contentType string, body string) (map[string]string, string) {
	param := make(map[string]string)

	if strings.Contains(strings.ToLower(contentType), "application/x-www-form-urlencoded") {
		if m, err := url.ParseQuery(body); err == nil {
			for name := range m {
				if len(m[name]) > 0 {
					param[name] = m[name][0]
				}
			}
			return param, "percent-decoded"
		}
	}

	for _, pair := range splitDirectAdminBody(body) {
		if pair.Name == "" {
			continue
		}
		if _, exists := param[pair.Name]; exists {
			continue
		}
		param[pair.Name] = pair.Value
	}

	return param, "verbatim"
}

// describeBody renders a body for the log with every secret value replaced by
// its length, using the same field boundaries as the parser.
func describeBody(body string) string {
	var out []string

	for _, pair := range splitDirectAdminBody(body) {
		if pair.Name == "" {
			out = append(out, pair.Value)
			continue
		}
		if isSecretField(pair.Name) {
			out = append(out, pair.Name+"=<"+itoaInt(len(pair.Value))+" chars hidden>")
			continue
		}
		out = append(out, pair.Name+"="+pair.Value)
	}

	rendered := jsonSecretRe.ReplaceAllString(strings.Join(out, "&"), `"$1":"<hidden>"`)

	for i := 0; i < len(rendered); i++ {
		if rendered[i] < 0x20 || rendered[i] == 0x7f {
			return quote(rendered)
		}
	}

	return rendered
}

func itoaInt(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func quote(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		case '"':
			builder.WriteString("\\\"")
		default:
			builder.WriteByte(value[i])
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func wEmailAdmin(w http.ResponseWriter, r *http.Request) {
	loggerAccess(r)

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		LogError.Printf("MAILADM: error reading body: %v", err)
		http.Error(w, "can't read body", http.StatusBadRequest)
		return
	}

	param, mode := parseMailBody(r.Header.Get("Content-Type"), string(body))

	if Config.debug == "1" {
		LogError.Printf("MAILADM: %s from %s; Content-Type=%q, Content-Length=%d, read %d bytes, %d fields, %s",
			r.Method, ClientIP(r.RemoteAddr), r.Header.Get("Content-Type"),
			r.ContentLength, len(body), len(param), mode)
		LogError.Printf("MAILADM: body=%s", describeBody(string(body)))
	}

	// DirectAdmin always sends the password twice. If the two differ, the body
	// was split in the wrong place - worth knowing before a mailbox is created
	// with a password nobody can reproduce.
	if first, second := param["passwd"], param["passwd2"]; first != "" && second != "" && first != second {
		LogError.Printf("MAILADM: passwd and passwd2 differ (%d and %d bytes); the body may have been split wrongly: %s",
			len(first), len(second), describeBody(string(body)))
	}

	action := param["action"]
	if action == "" {
		LogError.Printf("MAILADM: no action in body; Content-Type=%q, %d fields, body=%s",
			r.Header.Get("Content-Type"), len(param), describeBody(string(body)))
		replyString(w, "error=1&details=No action given")
		return
	}

	switch action {
	case "create":
		RunExternalScript("MAILADM", param, "pop_create.sh")
		replyString(w, "error=0&text=Mailbox created")
	case "modify":
		RunExternalScript("MAILADM", param, "pop_modify.sh")
		replyString(w, "error=0&text=Mailbox modified")
	case "delete":
		RunExternalScript("MAILADM", param, "pop_delete.sh")
		replyString(w, "error=0&text=Mailbox removed")
	default:
		LogAction.Println("MAILADM: No email action taken for", action)
		replyString(w, "error=0&details=No action taken")
	}

	return
}
