package main

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Punycode (RFC 3492) and the IDNA label conversion DirectSlave needs.
//
// A primary stores an internationalized zone in its ASCII form, and every DNS
// query arrives in that form too, but DirectAdmin hands the name over exactly
// as the user typed it. A zone declared as "книга.pp.ua" therefore never
// matched an incoming query for "xn--80afohp.pp.ua." and its AXFR could not be
// built at all.
//
// This is deliberately not a full IDNA2008 implementation: it lowercases and
// punycode-encodes, without Unicode NFC normalization or the IDNA mapping
// table, which would pull golang.org/x/text into the build. For the names that
// reach a secondary - already normalized by the primary and by the registry -
// the result is identical; a name that needs more than that is rejected by the
// DNS name validation instead of being silently mangled.

const (
	punycodeBase        = 36
	punycodeTmin        = 1
	punycodeTmax        = 26
	punycodeSkew        = 38
	punycodeDamp        = 700
	punycodeInitialBias = 72
	punycodeInitialN    = 128
	punycodeDelimiter   = '-'
	punycodePrefix      = "xn--"

	maxInt32 = int(^uint32(0) >> 1)
)

var (
	errPunycodeOverflow = errors.New("punycode: arithmetic overflow")
	errPunycodeInput    = errors.New("punycode: malformed input")
	errDomainEmpty      = errors.New("domain: empty name")
	errDomainLabel      = errors.New("domain: empty or oversized label")
	errDomainLength     = errors.New("domain: name is longer than 253 characters")
)

// isASCIIName reports whether every byte is plain ASCII, which is the common
// case and lets the conversion be skipped entirely.
func isASCIIName(name string) bool {
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// ToPunycode lowercases the name and converts every non-ASCII label to its
// "xn--" form. A name that is already ASCII is only lowercased, so an input
// that is punycode already passes through unchanged.
func ToPunycode(domain string) (string, error) {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if domain == "" {
		return "", errDomainEmpty
	}

	domain = strings.ToLower(domain)

	labels := strings.Split(domain, ".")
	for index, label := range labels {
		// An empty label means a name like "a..b" or a bare ".", which is not a
		// domain and must not reach the control file or a DNS message.
		if label == "" || len(label) > 63 {
			return "", errDomainLabel
		}
		if isASCIIName(label) {
			continue
		}

		encoded, err := punycodeEncode(label)
		if err != nil {
			return "", err
		}

		labels[index] = punycodePrefix + encoded
		if len(labels[index]) > 63 {
			return "", errDomainLabel
		}
	}

	converted := strings.Join(labels, ".")
	if len(converted) > 253 {
		return "", errDomainLength
	}

	return converted, nil
}

// ToUnicodeName decodes every "xn--" label back for display and for matching a
// name an operator typed in its native script. A label that does not decode is
// left as it is, so the function never fails.
func ToUnicodeName(domain string) string {
	trimmed := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if strings.Contains(trimmed, punycodePrefix) == false {
		return trimmed
	}

	labels := strings.Split(trimmed, ".")
	for index, label := range labels {
		if strings.HasPrefix(label, punycodePrefix) == false {
			continue
		}

		decoded, err := punycodeDecode(label[len(punycodePrefix):])
		if err != nil {
			return trimmed
		}

		labels[index] = decoded
	}

	return strings.Join(labels, ".")
}

// NormalizeDomain is the single entry point for anything that arrives from the
// API, the dashboard or the CLI: it returns the ASCII form the zone must be
// stored and queried under.
func NormalizeDomain(domain string) (string, bool) {
	converted, err := ToPunycode(domain)
	if err != nil {
		return "", false
	}
	return converted, true
}

// normalizeDomainSpelling trims and lowercases a name without changing the
// script it is written in. It is what "spelled exactly the same way" means for
// the parts of DirectSlave that match a declaration literally.
func normalizeDomainSpelling(domain string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
}

// domainMatchForms returns every spelling of the name that may appear in the
// control file: what the caller typed, its ASCII form and its native form.
// Zones added before DirectSlave converted names are stored in UTF-8, so a
// delete has to find them by either spelling.
func domainMatchForms(domain string) []string {
	trimmed := normalizeDomainSpelling(domain)
	if trimmed == "" {
		return nil
	}

	forms := []string{trimmed}

	add := func(candidate string) {
		if candidate == "" {
			return
		}
		for _, known := range forms {
			if known == candidate {
				return
			}
		}
		forms = append(forms, candidate)
	}

	if converted, err := ToPunycode(trimmed); err == nil {
		add(converted)
	}
	add(ToUnicodeName(trimmed))

	return forms
}

// domainNameContains reports whether a search term appears in the zone name in
// any of its spellings. A search must find a zone whichever way it is declared
// and whichever way the term was typed: "книга" and "xn--80afohp" both have to
// find the zone, whether the control file holds the native or the ASCII form.
func domainNameContains(name string, needle string) bool {
	if needle == "" {
		return true
	}

	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if strings.Contains(name, needle) {
		return true
	}

	if native := ToUnicodeName(name); native != name && strings.Contains(native, needle) {
		return true
	}

	if converted, err := ToPunycode(name); err == nil && converted != name {
		if strings.Contains(converted, needle) {
			return true
		}
	}

	return false
}

func punycodeAdapt(delta int, numPoints int, firstTime bool) int {
	if firstTime {
		delta /= punycodeDamp
	} else {
		delta /= 2
	}

	delta += delta / numPoints

	k := 0
	for delta > ((punycodeBase-punycodeTmin)*punycodeTmax)/2 {
		delta /= punycodeBase - punycodeTmin
		k += punycodeBase
	}

	return k + (punycodeBase-punycodeTmin+1)*delta/(delta+punycodeSkew)
}

func punycodeThreshold(k int, bias int) int {
	t := k - bias
	if t < punycodeTmin {
		return punycodeTmin
	}
	if t > punycodeTmax {
		return punycodeTmax
	}
	return t
}

func punycodeDigit(value int) byte {
	if value < 26 {
		return byte('a' + value)
	}
	return byte('0' + value - 26)
}

func punycodeValue(digit byte) (int, error) {
	switch {
	case digit >= 'a' && digit <= 'z':
		return int(digit - 'a'), nil
	case digit >= 'A' && digit <= 'Z':
		return int(digit - 'A'), nil
	case digit >= '0' && digit <= '9':
		return int(digit-'0') + 26, nil
	}
	return 0, errPunycodeInput
}

func punycodeEncode(label string) (string, error) {
	input := []rune(label)

	var output []byte
	basic := 0

	for _, value := range input {
		if value < punycodeInitialN {
			output = append(output, byte(value))
			basic++
		}
	}

	handled := basic
	if basic > 0 {
		output = append(output, punycodeDelimiter)
	}

	n := rune(punycodeInitialN)
	delta := 0
	bias := punycodeInitialBias

	for handled < len(input) {
		next := rune(maxInt32)
		for _, value := range input {
			if value >= n && value < next {
				next = value
			}
		}

		if int(next-n) > (maxInt32-delta)/(handled+1) {
			return "", errPunycodeOverflow
		}
		delta += int(next-n) * (handled + 1)
		n = next

		for _, value := range input {
			if value < n {
				delta++
				if delta < 0 {
					return "", errPunycodeOverflow
				}
				continue
			}
			if value > n {
				continue
			}

			q := delta
			for k := punycodeBase; ; k += punycodeBase {
				t := punycodeThreshold(k, bias)
				if q < t {
					break
				}
				output = append(output, punycodeDigit(t+(q-t)%(punycodeBase-t)))
				q = (q - t) / (punycodeBase - t)
			}

			output = append(output, punycodeDigit(q))
			bias = punycodeAdapt(delta, handled+1, handled == basic)
			delta = 0
			handled++
		}

		delta++
		n++
	}

	return string(output), nil
}

func punycodeDecode(encoded string) (string, error) {
	if encoded == "" {
		return "", errPunycodeInput
	}

	var output []rune

	position := 0
	if split := strings.LastIndexByte(encoded, punycodeDelimiter); split >= 0 {
		for index := 0; index < split; index++ {
			if encoded[index] >= utf8.RuneSelf {
				return "", errPunycodeInput
			}
			output = append(output, rune(encoded[index]))
		}
		position = split + 1
	}

	n := rune(punycodeInitialN)
	i := 0
	bias := punycodeInitialBias

	for position < len(encoded) {
		previous := i
		weight := 1

		for k := punycodeBase; ; k += punycodeBase {
			if position >= len(encoded) {
				return "", errPunycodeInput
			}

			digit, err := punycodeValue(encoded[position])
			if err != nil {
				return "", err
			}
			position++

			if digit > (maxInt32-i)/weight {
				return "", errPunycodeOverflow
			}
			i += digit * weight

			t := punycodeThreshold(k, bias)
			if digit < t {
				break
			}

			if weight > maxInt32/(punycodeBase-t) {
				return "", errPunycodeOverflow
			}
			weight *= punycodeBase - t
		}

		bias = punycodeAdapt(i-previous, len(output)+1, previous == 0)

		if i/(len(output)+1) > maxInt32-int(n) {
			return "", errPunycodeOverflow
		}
		n += rune(i / (len(output) + 1))
		i %= len(output) + 1

		if n < punycodeInitialN || n > utf8.MaxRune || (n >= 0xD800 && n <= 0xDFFF) {
			return "", errPunycodeInput
		}

		output = append(output, 0)
		copy(output[i+1:], output[i:])
		output[i] = n
		i++
	}

	// An "xn--" label that decodes to nothing, or to plain ASCII, was never
	// produced by an encoder. Returning it would invent a name that does not
	// exist, so it is rejected and the label is kept as it is.
	if len(output) == 0 {
		return "", errPunycodeInput
	}

	decoded := string(output)
	if isASCIIName(decoded) {
		return "", errPunycodeInput
	}

	return decoded, nil
}
