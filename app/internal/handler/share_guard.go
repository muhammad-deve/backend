package handler

import (
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// sharePasswordGuard slows down password guessing on protected share links.
//
// Each visitor (link + IP) gets a short pause after 3 wrong passwords, a long
// one after 5, and a day after 7. Separately, a link that collects too many
// wrong passwords from all visitors in an hour pauses for everyone, which
// stops guessing spread across many IP addresses. State is in memory, so a
// backend restart clears it.
type sharePasswordGuard struct {
	mu       sync.Mutex
	now      func() time.Time
	visitors map[string]*shareVisitorAttempts
	links    map[string]*shareLinkAttempts
}

type shareVisitorAttempts struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
	checking    bool
}

type shareLinkAttempts struct {
	failures []time.Time
	checking int
}

type shareLockReason string

const (
	shareLockNone    shareLockReason = ""
	shareLockVisitor shareLockReason = "visitor"
	shareLockLink    shareLockReason = "link"
	shareLockBusy    shareLockReason = "busy"
)

type shareAttemptOutcome int

const (
	shareAttemptWrong shareAttemptOutcome = iota
	shareAttemptCorrect
	// shareAttemptOther covers outcomes that say nothing about the password,
	// such as a link that was deleted or revoked meanwhile.
	shareAttemptOther
)

const (
	shareFailureMemory   = 24 * time.Hour
	shareLinkWindow      = time.Hour
	shareLinkMaxFailures = 30
	shareLinkMaxChecking = 4
	shareGuardPruneAt    = 10000
)

func newSharePasswordGuard() *sharePasswordGuard {
	return &sharePasswordGuard{
		now:      time.Now,
		visitors: make(map[string]*shareVisitorAttempts),
		links:    make(map[string]*shareLinkAttempts),
	}
}

// shareLockAfter is how long a visitor waits after their nth wrong password.
func shareLockAfter(failures int) time.Duration {
	switch {
	case failures >= 7:
		return 24 * time.Hour
	case failures == 5:
		return 30 * time.Minute
	case failures == 3:
		return time.Minute
	}
	return 0
}

// shareAttemptsLeft is how many more wrong passwords a visitor can enter
// before the next pause.
func shareAttemptsLeft(failures int) int {
	for next := failures + 1; ; next++ {
		if shareLockAfter(next) > 0 {
			return next - failures
		}
	}
}

// begin reserves one password check. When the visitor or the link is paused,
// or the visitor already has a check running, it returns how long to wait.
func (g *sharePasswordGuard) begin(link, visitor string) (time.Duration, shareLockReason) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.pruneLocked(now)
	key := link + "|" + visitor
	attempts := g.visitors[key]
	if attempts != nil && attempts.lockedUntil.After(now) {
		return attempts.lockedUntil.Sub(now), shareLockVisitor
	}
	linkAttempts := g.linkLocked(link, now)
	if wait := linkAttempts.wait(now); wait > 0 {
		return wait, shareLockLink
	}
	if (attempts != nil && attempts.checking) || linkAttempts.checking >= shareLinkMaxChecking {
		return time.Second, shareLockBusy
	}
	if attempts == nil {
		attempts = &shareVisitorAttempts{}
		g.visitors[key] = attempts
	}
	attempts.checking = true
	linkAttempts.checking++
	return 0, shareLockNone
}

// finish records the result of a check reserved with begin. For a wrong
// password it returns the pause that now applies (zero when none) and how many
// attempts remain before the next pause.
func (g *sharePasswordGuard) finish(link, visitor string, outcome shareAttemptOutcome) (time.Duration, shareLockReason, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	key := link + "|" + visitor
	attempts := g.visitors[key]
	linkAttempts := g.linkLocked(link, now)
	if linkAttempts.checking > 0 {
		linkAttempts.checking--
	}
	if attempts == nil {
		return 0, shareLockNone, 0
	}
	attempts.checking = false
	switch outcome {
	case shareAttemptCorrect:
		delete(g.visitors, key)
		return 0, shareLockNone, 0
	case shareAttemptOther:
		if attempts.failures == 0 {
			delete(g.visitors, key)
		}
		return 0, shareLockNone, 0
	}
	if now.Sub(attempts.lastFailure) > shareFailureMemory {
		attempts.failures = 0
	}
	attempts.failures++
	attempts.lastFailure = now
	linkAttempts.failures = append(linkAttempts.failures, now)
	if lock := shareLockAfter(attempts.failures); lock > 0 {
		attempts.lockedUntil = now.Add(lock)
		return lock, shareLockVisitor, shareAttemptsLeft(attempts.failures)
	}
	if wait := linkAttempts.wait(now); wait > 0 {
		return wait, shareLockLink, 0
	}
	return 0, shareLockNone, shareAttemptsLeft(attempts.failures)
}

// status reports how long this visitor must still wait, without reserving a
// check. The share page uses it to resume a countdown after a reload.
func (g *sharePasswordGuard) status(link, visitor string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	wait := time.Duration(0)
	if attempts := g.visitors[link+"|"+visitor]; attempts != nil && attempts.lockedUntil.After(now) {
		wait = attempts.lockedUntil.Sub(now)
	}
	if linkAttempts := g.links[link]; linkAttempts != nil {
		linkAttempts.trim(now)
		wait = max(wait, linkAttempts.wait(now))
	}
	return wait
}

func (g *sharePasswordGuard) linkLocked(link string, now time.Time) *shareLinkAttempts {
	attempts := g.links[link]
	if attempts == nil {
		attempts = &shareLinkAttempts{}
		g.links[link] = attempts
	}
	attempts.trim(now)
	return attempts
}

func (a *shareLinkAttempts) trim(now time.Time) {
	cutoff := now.Add(-shareLinkWindow)
	index := sort.Search(len(a.failures), func(i int) bool { return a.failures[i].After(cutoff) })
	if index > 0 {
		a.failures = append(a.failures[:0], a.failures[index:]...)
	}
}

// wait is how long until the link drops below the failure ceiling again.
func (a *shareLinkAttempts) wait(now time.Time) time.Duration {
	if len(a.failures) < shareLinkMaxFailures {
		return 0
	}
	oldest := a.failures[len(a.failures)-shareLinkMaxFailures]
	return max(oldest.Add(shareLinkWindow).Sub(now), time.Second)
}

func (g *sharePasswordGuard) pruneLocked(now time.Time) {
	if len(g.visitors)+len(g.links) < shareGuardPruneAt {
		return
	}
	for key, attempts := range g.visitors {
		if !attempts.checking && !attempts.lockedUntil.After(now) && now.Sub(attempts.lastFailure) > shareFailureMemory {
			delete(g.visitors, key)
		}
	}
	for key, attempts := range g.links {
		attempts.trim(now)
		if attempts.checking == 0 && len(attempts.failures) == 0 {
			delete(g.links, key)
		}
	}
}

// shareVisitorKey identifies a visitor for the password guard. Headers set by
// a proxy are trusted only when the request came from a private address (the
// nginx container); the API port is also reachable directly, and there anyone
// could forge them. IPv6 addresses are grouped by /64, the block one
// subscriber normally holds.
func shareVisitorKey(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	address := net.ParseIP(remote)
	if address != nil && (address.IsLoopback() || address.IsPrivate()) {
		if forwarded := forwardedClientIP(r); forwarded != nil {
			address = forwarded
		}
	}
	if address == nil {
		return remote
	}
	if address.To4() == nil {
		return address.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return address.String()
}

// forwardedClientIP reads the client address nginx passes on. X-Real-IP is
// overwritten by nginx; for X-Forwarded-For only the right-most entry, the
// one nginx appended, can be trusted.
func forwardedClientIP(r *http.Request) net.IP {
	if address := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); address != nil {
		return address
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	return net.ParseIP(strings.TrimSpace(parts[len(parts)-1]))
}

// shareUploaderLimit caps how many different Free accounts may upload cloud
// files from one network address per day, so one person can't spread uploads
// over many throwaway accounts. Pro accounts are not counted. Many people can
// share one mobile-carrier address, so the cap counts accounts, not files.
type shareUploaderLimit struct {
	mu   sync.Mutex
	now  func() time.Time
	max  int
	seen map[string]map[string]time.Time
}

const shareUploaderWindow = 24 * time.Hour

func newShareUploaderLimit(max int) *shareUploaderLimit {
	return &shareUploaderLimit{now: time.Now, max: max, seen: make(map[string]map[string]time.Time)}
}

// allow records that userID uploads from address and reports whether that
// stays within the daily cap.
func (l *shareUploaderLimit) allow(address, userID string) bool {
	if l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-shareUploaderWindow)
	if len(l.seen) > shareGuardPruneAt {
		for key, users := range l.seen {
			for id, at := range users {
				if at.Before(cutoff) {
					delete(users, id)
				}
			}
			if len(users) == 0 {
				delete(l.seen, key)
			}
		}
	}
	users := l.seen[address]
	for id, at := range users {
		if at.Before(cutoff) {
			delete(users, id)
		}
	}
	if _, known := users[userID]; known {
		return true
	}
	if len(users) >= l.max {
		return false
	}
	if users == nil {
		users = make(map[string]time.Time)
		l.seen[address] = users
	}
	users[userID] = now
	return true
}
