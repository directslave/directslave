package main

import (
	"github.com/asaskevich/govalidator"
)

// validateDomainInput is the gate every domain that arrives from the API, the
// dashboard or the CLI goes through. It returns the ASCII (punycode) form,
// which is what gets stored, queried and transferred; validation runs on that
// form, because a name in its native script never passes IsDNSName.
func validateDomainInput(domain string) (string, bool) {
	converted, ok := NormalizeDomain(domain)
	if ok == false {
		return "", false
	}

	if validateData(converted, "domain") == false {
		return "", false
	}

	return converted, true
}

// validateDomainName checks a name without converting it. Delete and reload
// accept a zone exactly as it is declared, which for anything added before
// DirectSlave normalized names is the native script.
func validateDomainName(domain string) bool {
	_, ok := validateDomainInput(domain)
	return ok
}

func validateData(data string, datatype string) bool {
	switch datatype {
	case "user":
		return govalidator.IsAlphanumeric(data)
	case "domain":
		return govalidator.IsDNSName(data)
	case "ip":
		return govalidator.IsIP(data)
	case "file":
		f, _ := govalidator.IsFilePath(data)
		return f
	default:
		return false
	}
}
