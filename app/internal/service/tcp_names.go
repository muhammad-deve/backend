package service

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// nameTakenMessage refuses a name another account owns. It contains
// "subdomain cannot", so CLIs 0.1.8+ stop instead of retrying.
const nameTakenMessage = "subdomain cannot be used: it belongs to another account"

// nameReclaimAfter is how long a name must go unused before another account
// may claim it. Until then it stays with its owner, whether it is a custom,
// grandfathered or generated name.
const nameReclaimAfter = 90 * 24 * time.Hour

// legacyReservedSubdomains were refused from the start. Names saved before the
// list below grew are checked against these only, so they keep working.
var legacyReservedSubdomains = map[string]bool{
	"api": true, "admin": true, "back": true, "dashboard": true, "www": true,
}

// reservedSubdomains can't be claimed as new names. Tunnels share goport.uz's
// site, so names like login or billing would make convincing phishing pages.
var reservedSubdomains = func() map[string]bool {
	names := map[string]bool{}
	for name := range legacyReservedSubdomains {
		names[name] = true
	}
	for _, name := range []string{
		"login", "auth", "account", "accounts", "billing", "pay", "payment",
		"checkout", "support", "status", "docs", "mail", "email", "app",
		"goport", "help", "secure", "security", "verify", "signin", "signup",
		"oauth", "sso", "id", "my", "static", "cdn", "assets", "blog", "news",
		"abuse",
	} {
		names[name] = true
	}
	return names
}()

// cleanRequestedSubdomain turns what a user typed (a name, a host or a URL)
// into a bare lowercase name.
func cleanRequestedSubdomain(value, domain string) string {
	subdomain := strings.ToLower(strings.TrimSpace(value))
	domain = strings.ToLower(strings.TrimSpace(domain))

	subdomain = strings.TrimPrefix(subdomain, "https://")
	subdomain = strings.TrimPrefix(subdomain, "http://")
	if i := strings.IndexAny(subdomain, "/:"); i >= 0 {
		subdomain = subdomain[:i]
	}
	subdomain = strings.TrimSuffix(subdomain, ".")
	if domain != "" {
		subdomain = strings.TrimSuffix(subdomain, "."+domain)
	}
	return subdomain
}

// validateSubdomain checks a name for a new claim.
func validateSubdomain(subdomain string) error {
	if err := checkSubdomainFormat(subdomain, reservedSubdomains); err != nil {
		return err
	}
	// Punycode names can imitate other names with look-alike characters.
	if strings.HasPrefix(subdomain, "xn--") {
		return fmt.Errorf("subdomain cannot start with 'xn--'")
	}
	return nil
}

// validateLegacySubdomain checks a saved name against the rules it was saved
// under, which rows written straight through the old REST API may still
// break.
func validateLegacySubdomain(subdomain string) error {
	return checkSubdomainFormat(subdomain, legacyReservedSubdomains)
}

func checkSubdomainFormat(subdomain string, reserved map[string]bool) error {
	if len(subdomain) == 0 || len(subdomain) > 63 {
		return fmt.Errorf("subdomain must be 1-63 characters")
	}
	if subdomain[0] == '-' || subdomain[len(subdomain)-1] == '-' {
		return fmt.Errorf("subdomain cannot start or end with '-'")
	}
	if reserved[subdomain] {
		return fmt.Errorf("subdomain %q is reserved", subdomain)
	}

	for _, ch := range subdomain {
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '-' {
			continue
		}
		return fmt.Errorf("subdomain can only contain lowercase letters, numbers, and '-'")
	}
	return nil
}

func isReservedSubdomain(subdomain string) bool {
	return reservedSubdomains[subdomain]
}

// saveTunnel claims subdomain for userID and saves the tunnel row. A name
// another account owns is refused unless it has gone unused for
// nameReclaimAfter.
func (t *tcpService) saveTunnel(subdomain string, isCustom bool, userID, localPort, protocol string) (string, string, error) {
	// Every way a custom name is handed out (--custom, a reconnect, or a
	// restart reusing the latest tunnel) comes through here, so a word added
	// to blocked_words also stops names that were allowed before.
	if isCustom && containsBlockedWord(subdomain, t.blockedWords()) {
		return "", "", refuse(errBlockedSubdomain)
	}

	// Refuse another account's name before probing its session.
	if existing, err := t.findTunnelBySubdomain(subdomain); err == nil {
		if !t.mayClaim(existing, userID) {
			return "", "", refuseWith(nameTakenMessage)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}

	// The live session map decides whether a subdomain is in use, not the
	// is_current column, which can be left stale by an unclean shutdown.
	// Checking it before any write also stops a second CLI from rewriting the
	// connection metadata of a tunnel that is still online. The probe can
	// take seconds, so it runs before claimMu is taken.
	if t.hasLiveSession(subdomain) && !t.releaseDeadSession(subdomain, userID) {
		return "", "", refuse(fmt.Errorf("subdomain %q is currently in use", subdomain))
	}

	// Find, decide and save under one lock, so two registrations can't both
	// pass the check for the same name.
	t.claimMu.Lock()
	defer t.claimMu.Unlock()

	existing, err := t.findTunnelBySubdomain(subdomain)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if err == nil {
		owner := existing.GetString("user")
		if owner == "" || owner == userID {
			return t.updateTunnel(existing, isCustom, userID, localPort, protocol)
		}
		if !t.mayClaim(existing, userID) {
			return "", "", refuseWith(nameTakenMessage)
		}
		// The old row's lifetime totals go with it; its usage buckets stay
		// with the old owner's monthly usage.
		log.Printf("subdomain %s: unused for %v; giving it to another account", subdomain, nameReclaimAfter)
		if err := t.tunnels.DeleteWithLogs(existing.Id); err != nil {
			return "", "", err
		}
	}
	return t.insertTunnel(subdomain, isCustom, userID, localPort, protocol)
}

// createRandomTunnel saves a tunnel under a fresh generated name. The name is
// drawn and saved under claimMu, so it can't collide with any row.
func (t *tcpService) createRandomTunnel(userID, localPort, protocol string) (string, string, error) {
	blocked := t.blockedWords()
	t.claimMu.Lock()
	defer t.claimMu.Unlock()

	for i := 0; i < 10; i++ {
		subdomain, err := t.randomName()
		if err != nil {
			return "", "", err
		}
		// Random letters can spell a blocked word or a reserved name too;
		// just draw again.
		if containsBlockedWord(subdomain, blocked) || isReservedSubdomain(subdomain) || t.isReserved(subdomain) {
			continue
		}
		_, err = t.findTunnelBySubdomain(subdomain)
		if errors.Is(err, sql.ErrNoRows) {
			return t.insertTunnel(subdomain, false, userID, localPort, protocol)
		}
		if err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("failed to generate unique subdomain")
}

func (t *tcpService) updateTunnel(existing *core.Record, isCustom bool, userID, localPort, protocol string) (string, string, error) {
	t.recordMu.Lock()
	defer t.recordMu.Unlock()
	// Re-read under the lock: the liveness probe can take seconds, and saving
	// an old copy would undo minutes added meanwhile.
	subdomain := existing.GetString("subdomain")
	if fresh, err := t.findTunnelBySubdomain(subdomain); err == nil {
		existing = fresh
	}
	existing.Set("user", userID)
	existing.Set("is_custom", isCustom)
	existing.Set("is_current", false)
	existing.Set("local_port", strings.TrimSpace(localPort))
	existing.Set("protocol", normalizedProtocol(protocol))
	if err := t.app.Save(existing); err != nil {
		return "", "", err
	}
	return subdomain, existing.Id, nil
}

func (t *tcpService) insertTunnel(subdomain string, isCustom bool, userID, localPort, protocol string) (string, string, error) {
	collection, err := t.app.FindCollectionByNameOrId(model.TunnelsCollection)
	if err != nil {
		return "", "", err
	}
	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("subdomain", subdomain)
	record.Set("is_custom", isCustom)
	record.Set("is_current", false)
	record.Set("local_port", strings.TrimSpace(localPort))
	record.Set("protocol", normalizedProtocol(protocol))
	if err := t.app.Save(record); err != nil {
		return "", "", err
	}
	return subdomain, record.Id, nil
}

// mayClaim reports whether userID may use the name saved in record: it is
// theirs or nobody's, or its owner hasn't used it for nameReclaimAfter.
func (t *tcpService) mayClaim(record *core.Record, userID string) bool {
	owner := record.GetString("user")
	if owner == "" || owner == userID {
		return true
	}
	if t.isReserved(record.GetString("subdomain")) {
		return false
	}
	return time.Since(t.lastUsed(record)) >= nameReclaimAfter
}

// lastUsed is when a tunnel was last online or carried traffic. Its row is
// saved on every registration and disconnect (and on startup if it was left
// online), which moves `updated`; tunnel_logs has its last request. A failed
// lookup counts as used now, so a name is never given away by mistake.
func (t *tcpService) lastUsed(record *core.Record) time.Time {
	last := record.GetDateTime("updated").Time()
	if created := record.GetDateTime("created").Time(); created.After(last) {
		last = created
	}
	row, err := t.app.FindFirstRecordByFilter(model.TunnelLogsCollection, "tunnel_id = {:id}", dbx.Params{"id": record.Id})
	switch {
	case err == nil:
		if active := row.GetDateTime("last_active").Time(); active.After(last) {
			last = active
		}
	case !errors.Is(err, sql.ErrNoRows):
		log.Printf("could not read last activity of tunnel %s: %v", record.Id, err)
		return time.Now()
	}
	return last
}

// ownsName reports whether userID owns the saved name subdomain.
func (t *tcpService) ownsName(subdomain, userID string) bool {
	record, err := t.findTunnelBySubdomain(subdomain)
	return err == nil && record.GetString("user") == userID
}

func randomSubdomain(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = alphabet[int(bytes[i])%len(alphabet)]
	}

	return string(bytes), nil
}
