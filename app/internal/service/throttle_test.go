package service

import (
	"errors"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) add(d time.Duration)     { c.t = c.t.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)} }
func withClock(l *rateLimiter, c *fakeClock) { l.now = c.now }

func TestRateLimiterWindow(t *testing.T) {
	clock := newFakeClock()
	limiter := newRateLimiter(time.Hour, 3, 0)
	withClock(limiter, clock)

	for i := 0; i < 3; i++ {
		if ok, _ := limiter.Allow("a"); !ok {
			t.Fatalf("hit %d blocked", i+1)
		}
		clock.add(time.Minute)
	}
	ok, wait := limiter.Allow("a")
	if ok {
		t.Fatal("fourth hit in the window must be blocked")
	}
	if wait != 57*time.Minute {
		t.Fatalf("retry after = %s, want 57m", wait)
	}
	if ok, _ := limiter.Allow("b"); !ok {
		t.Fatal("other keys must not be affected")
	}
	clock.add(wait)
	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("hit must be allowed once the oldest leaves the window")
	}
}

func TestRateLimiterCooldownBlockedAndReset(t *testing.T) {
	clock := newFakeClock()
	limiter := newRateLimiter(time.Hour, 5, time.Minute)
	withClock(limiter, clock)

	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("first hit blocked")
	}
	if ok, wait := limiter.Allow("a"); ok || wait != time.Minute {
		t.Fatalf("cooldown: ok=%v wait=%s", ok, wait)
	}

	failures := newRateLimiter(time.Hour, 2, 0)
	withClock(failures, clock)
	failures.Add("x")
	if blocked, _ := failures.Blocked("x"); blocked {
		t.Fatal("one failure must not block")
	}
	failures.Add("x")
	if blocked, _ := failures.Blocked("x"); !blocked {
		t.Fatal("two failures must block")
	}
	failures.Reset("x")
	if blocked, _ := failures.Blocked("x"); blocked {
		t.Fatal("reset must clear the key")
	}
}

func TestThrottleCodeSendLimits(t *testing.T) {
	th := NewThrottle(2, 3).(*throttle)

	for i := 0; i < 2; i++ {
		if err := th.CodeSend("1.1.1.1"); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	err := th.CodeSend("1.1.1.1")
	if _, ok := IsRateLimited(err); !ok {
		t.Fatalf("third send from one IP: err = %v, want rate limit", err)
	}
	if err := th.CodeSend("2.2.2.2"); err != nil {
		t.Fatalf("send from another IP: %v", err)
	}
	if _, ok := IsRateLimited(th.CodeSend("3.3.3.3")); !ok {
		t.Fatal("server-wide limit must apply across IPs")
	}
}

func TestThrottleLoginFailures(t *testing.T) {
	th := NewThrottle(0, 0)
	for i := 0; i < loginFailuresPerID; i++ {
		if err := th.LoginAllowed("1.1.1.1", "Dev@Example.com"); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		th.LoginFailed("1.1.1.1", "Dev@Example.com")
	}
	if _, ok := IsRateLimited(th.LoginAllowed("9.9.9.9", "dev@example.com")); !ok {
		t.Fatal("identity must be limited across IPs and letter case")
	}
	th.LoginSucceeded("dev@example.com")
	if err := th.LoginAllowed("9.9.9.9", "dev@example.com"); err != nil {
		t.Fatalf("success must clear the identity: %v", err)
	}
}

func TestOTPAttemptsReserveTries(t *testing.T) {
	attempts := newOTPAttempts(2, time.Hour)
	if !attempts.begin("otp") || !attempts.begin("otp") {
		t.Fatal("first two tries must be allowed")
	}
	if attempts.begin("otp") {
		t.Fatal("third try must be refused while two are reserved")
	}
	attempts.release("otp")
	if !attempts.begin("otp") {
		t.Fatal("a released try must be usable again")
	}
	if !attempts.exhausted("otp") {
		t.Fatal("two used tries must exhaust the OTP")
	}
}

func TestCheckOTPCodeDeletesAfterTooManyWrongCodes(t *testing.T) {
	app := newTestApp(t)
	user := newTestUser(t, app, "dev@example.com", false)
	otp := newTestOTP(t, app, user, user.Email(), "123456")
	attempts := newOTPAttempts(maxWrongCodesPerOTP, time.Hour)

	if _, err := checkOTPCode(app, attempts, otp.Id, "123456", nil); err != nil {
		t.Fatalf("right code: %v", err)
	}
	for i := 0; i < maxWrongCodesPerOTP; i++ {
		if _, err := checkOTPCode(app, attempts, otp.Id, "000000", nil); err == nil {
			t.Fatalf("wrong code %d accepted", i+1)
		}
	}
	if _, err := app.FindOTPById(otp.Id); err == nil {
		t.Fatal("OTP must be deleted after too many wrong codes")
	}
	if _, err := checkOTPCode(app, attempts, otp.Id, "123456", nil); err == nil {
		t.Fatal("right code must fail once the OTP is gone")
	}
}

func TestCheckOTPCodeRefusesWhenTriesAreUsed(t *testing.T) {
	app := newTestApp(t)
	user := newTestUser(t, app, "dev@example.com", false)
	otp := newTestOTP(t, app, user, user.Email(), "123456")
	attempts := newOTPAttempts(maxWrongCodesPerOTP, time.Hour)
	for i := 0; i < maxWrongCodesPerOTP; i++ {
		attempts.begin(otp.Id)
	}

	_, err := checkOTPCode(app, attempts, otp.Id, "123456", nil)
	if !errors.Is(err, ErrTooManyCodeAttempts) {
		t.Fatalf("err = %v, want ErrTooManyCodeAttempts", err)
	}
}
