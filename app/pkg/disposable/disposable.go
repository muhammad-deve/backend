// Package disposable recognises throwaway ("temp mail") email domains.
//
// domains.txt is the community list from
// https://github.com/disposable-email-domains/disposable-email-domains
// (CC0, snapshot of 2026-10-19). Refresh it by downloading
// disposable_email_blocklist.conf over this file.
package disposable

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed domains.txt
var list string

var domains = sync.OnceValue(func() map[string]struct{} {
	set := make(map[string]struct{}, 10000)
	for _, line := range strings.Split(list, "\n") {
		domain := strings.ToLower(strings.TrimSpace(line))
		if domain != "" && !strings.HasPrefix(domain, "#") {
			set[domain] = struct{}{}
		}
	}
	return set
})

// Email reports whether the address uses a disposable domain. Subdomains of a
// listed domain count too, so "x.mailinator.com" is caught by
// "mailinator.com".
func Email(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.Trim(strings.ToLower(strings.TrimSpace(email[at+1:])), ".")
	set := domains()
	for domain != "" {
		if _, found := set[domain]; found {
			return true
		}
		dot := strings.IndexByte(domain, '.')
		if dot < 0 {
			return false
		}
		domain = domain[dot+1:]
	}
	return false
}
