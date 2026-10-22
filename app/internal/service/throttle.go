package service

import (
	"strings"
	"sync"
	"time"
)

// Limits for the sign-in and code endpoints. The send limits come from config.
const (
	codeChecksPerIP     = 30
	codeCheckWindow     = 15 * time.Minute
	loginFailuresPerIP  = 30
	loginFailuresPerID  = 10
	loginFailureWindow  = 15 * time.Minute
	codeSendWindow      = time.Hour
	defaultSendsPerIP   = 10
	defaultSendsGlobal  = 300
	maxWrongCodesPerOTP = 5
)

// ThrottleI holds the per-IP and server-wide limits for the account endpoints.
// Every method returns a rate-limit error (see IsRateLimited) when blocked.
// State is kept in memory and resets on restart.
type ThrottleI interface {
	// CodeSend limits requests that email a code, per IP and server-wide.
	CodeSend(ip string) error
	// CodeCheck limits attempts at a 6-digit code per IP.
	CodeCheck(ip string) error
	// LoginAllowed reports whether a password attempt may run.
	LoginAllowed(ip, identity string) error
	// LoginFailed records a wrong password.
	LoginFailed(ip, identity string)
	// LoginSucceeded clears the failures for identity.
	LoginSucceeded(identity string)
}

type throttle struct {
	sendIP     *rateLimiter
	sendGlobal *rateLimiter
	checkIP    *rateLimiter
	loginIP    *rateLimiter
	loginID    *rateLimiter
}

// NewThrottle builds the limiter set. Zero or negative send limits use the
// defaults.
func NewThrottle(sendsPerIP, sendsGlobal int) ThrottleI {
	if sendsPerIP <= 0 {
		sendsPerIP = defaultSendsPerIP
	}
	if sendsGlobal <= 0 {
		sendsGlobal = defaultSendsGlobal
	}
	return &throttle{
		sendIP:     newRateLimiter(codeSendWindow, sendsPerIP, 0),
		sendGlobal: newRateLimiter(codeSendWindow, sendsGlobal, 0),
		checkIP:    newRateLimiter(codeCheckWindow, codeChecksPerIP, 0),
		loginIP:    newRateLimiter(loginFailureWindow, loginFailuresPerIP, 0),
		loginID:    newRateLimiter(loginFailureWindow, loginFailuresPerID, 0),
	}
}

func (t *throttle) CodeSend(ip string) error {
	if blocked, wait := t.sendIP.Blocked(ip); blocked {
		return &errRateLimited{retryAfter: wait}
	}
	if ok, wait := t.sendGlobal.Allow("all"); !ok {
		return &errRateLimited{retryAfter: wait}
	}
	t.sendIP.Add(ip)
	return nil
}

func (t *throttle) CodeCheck(ip string) error {
	if ok, wait := t.checkIP.Allow(ip); !ok {
		return &errRateLimited{retryAfter: wait}
	}
	return nil
}

func (t *throttle) LoginAllowed(ip, identity string) error {
	if blocked, wait := t.loginIP.Blocked(ip); blocked {
		return &errRateLimited{retryAfter: wait}
	}
	if blocked, wait := t.loginID.Blocked(identityKey(identity)); blocked {
		return &errRateLimited{retryAfter: wait}
	}
	return nil
}

func (t *throttle) LoginFailed(ip, identity string) {
	t.loginIP.Add(ip)
	t.loginID.Add(identityKey(identity))
}

func (t *throttle) LoginSucceeded(identity string) {
	t.loginID.Reset(identityKey(identity))
}

func identityKey(identity string) string {
	return strings.ToLower(strings.TrimSpace(identity))
}

// otpAttempts counts tries per OTP id so a 6-digit code can't be guessed.
// A try is reserved before the code is checked, so parallel requests can't
// get past the limit either. In memory only.
type otpAttempts struct {
	mu    sync.Mutex
	tries map[string]otpTries
	max   int
	ttl   time.Duration
	now   func() time.Time
}

type otpTries struct {
	count int
	first time.Time
}

func newOTPAttempts(max int, ttl time.Duration) *otpAttempts {
	return &otpAttempts{tries: make(map[string]otpTries), max: max, ttl: ttl, now: time.Now}
}

// begin reserves a try for id. It returns false once every try is used.
func (a *otpAttempts) begin(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	for key, entry := range a.tries {
		if now.Sub(entry.first) > a.ttl {
			delete(a.tries, key)
		}
	}
	entry, ok := a.tries[id]
	if !ok {
		entry = otpTries{first: now}
	}
	if entry.count >= a.max {
		return false
	}
	entry.count++
	a.tries[id] = entry
	return true
}

// release returns a reserved try after a correct code.
func (a *otpAttempts) release(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry, ok := a.tries[id]; ok && entry.count > 0 {
		entry.count--
		a.tries[id] = entry
	}
}

// exhausted reports whether id has used every try.
func (a *otpAttempts) exhausted(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tries[id].count >= a.max
}

// forget drops the counter for id.
func (a *otpAttempts) forget(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tries, id)
}
