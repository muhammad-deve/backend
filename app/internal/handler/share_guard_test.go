package handler

import (
	"net/http/httptest"
	"testing"
	"time"
)

type guardClock struct{ now time.Time }

func (c *guardClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestGuard() (*sharePasswordGuard, *guardClock) {
	clock := &guardClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	guard := newSharePasswordGuard()
	guard.now = func() time.Time { return clock.now }
	return guard, clock
}

// wrong makes one wrong attempt and returns the pause it caused.
func wrong(t *testing.T, guard *sharePasswordGuard, link, visitor string) (time.Duration, int) {
	t.Helper()
	if wait, reason := guard.begin(link, visitor); wait > 0 {
		t.Fatalf("attempt refused before it started: %v (%s)", wait, reason)
	}
	wait, _, left := guard.finish(link, visitor, shareAttemptWrong)
	return wait, left
}

func TestSharePasswordGuardEscalates(t *testing.T) {
	guard, clock := newTestGuard()
	steps := []struct {
		pause time.Duration
		left  int
	}{
		{0, 2}, {0, 1}, {time.Minute, 2},
		{0, 1}, {30 * time.Minute, 2},
		{0, 1}, {24 * time.Hour, 1},
	}
	for index, step := range steps {
		pause, left := wrong(t, guard, "link", "1.2.3.4")
		if pause != step.pause || left != step.left {
			t.Fatalf("failure %d: pause %v left %d, want %v and %d", index+1, pause, left, step.pause, step.left)
		}
		if pause > 0 {
			if wait, reason := guard.begin("link", "1.2.3.4"); wait != pause || reason != shareLockVisitor {
				t.Fatalf("failure %d: begin during pause = %v %s", index+1, wait, reason)
			}
			if status := guard.status("link", "1.2.3.4"); status != pause {
				t.Fatalf("failure %d: status = %v", index+1, status)
			}
			clock.advance(pause)
		}
	}
	// Other visitors and other links are unaffected.
	if wait, _ := guard.begin("link", "5.6.7.8"); wait != 0 {
		t.Fatalf("another visitor was paused: %v", wait)
	}
	guard.finish("link", "5.6.7.8", shareAttemptOther)
	if wait, _ := guard.begin("other-link", "1.2.3.4"); wait != 0 {
		t.Fatalf("another link was paused: %v", wait)
	}
}

func TestSharePasswordGuardResetsOnSuccess(t *testing.T) {
	guard, _ := newTestGuard()
	wrong(t, guard, "link", "1.2.3.4")
	wrong(t, guard, "link", "1.2.3.4")
	guard.begin("link", "1.2.3.4")
	guard.finish("link", "1.2.3.4", shareAttemptCorrect)
	if _, left := wrong(t, guard, "link", "1.2.3.4"); left != 2 {
		t.Fatalf("attempts left after a success = %d, want 2", left)
	}
}

func TestSharePasswordGuardForgetsAfterADay(t *testing.T) {
	guard, clock := newTestGuard()
	wrong(t, guard, "link", "1.2.3.4")
	wrong(t, guard, "link", "1.2.3.4")
	clock.advance(25 * time.Hour)
	if pause, left := wrong(t, guard, "link", "1.2.3.4"); pause != 0 || left != 2 {
		t.Fatalf("old failures still counted: pause %v left %d", pause, left)
	}
}

func TestSharePasswordGuardPausesLinkUnderDistributedGuessing(t *testing.T) {
	guard, clock := newTestGuard()
	for index := 0; index < shareLinkMaxFailures; index++ {
		visitor := "10.0." + string(rune('a'+index/26)) + "." + string(rune('a'+index%26))
		wrong(t, guard, "link", visitor)
		clock.advance(time.Second)
	}
	wait, reason := guard.begin("link", "9.9.9.9")
	if reason != shareLockLink || wait <= 0 || wait > shareLinkWindow {
		t.Fatalf("link not paused: %v %s", wait, reason)
	}
	clock.advance(wait)
	if wait, _ := guard.begin("link", "9.9.9.9"); wait != 0 {
		t.Fatalf("link still paused after the window: %v", wait)
	}
}

func TestSharePasswordGuardAllowsOneCheckAtATime(t *testing.T) {
	guard, _ := newTestGuard()
	guard.begin("link", "1.2.3.4")
	if wait, reason := guard.begin("link", "1.2.3.4"); wait == 0 || reason != shareLockBusy {
		t.Fatalf("parallel attempt allowed: %v %s", wait, reason)
	}
	guard.finish("link", "1.2.3.4", shareAttemptOther)
	if wait, _ := guard.begin("link", "1.2.3.4"); wait != 0 {
		t.Fatalf("attempt refused after release: %v", wait)
	}
}

func TestShareVisitorKeyTrustsOnlyTheProxy(t *testing.T) {
	viaProxy := httptest.NewRequest("POST", "/", nil)
	viaProxy.RemoteAddr = "172.17.0.1:50000"
	viaProxy.Header.Set("X-Forwarded-For", "6.6.6.6, 84.54.1.2")
	if got := shareVisitorKey(viaProxy); got != "84.54.1.2" {
		t.Fatalf("proxied visitor = %q, want the address nginx appended", got)
	}
	viaProxy.Header.Set("X-Real-IP", "84.54.1.3")
	if got := shareVisitorKey(viaProxy); got != "84.54.1.3" {
		t.Fatalf("proxied visitor with X-Real-IP = %q", got)
	}

	direct := httptest.NewRequest("POST", "/", nil)
	direct.RemoteAddr = "84.54.1.2:40000"
	direct.Header.Set("X-Real-IP", "1.1.1.1")
	direct.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := shareVisitorKey(direct); got != "84.54.1.2" {
		t.Fatalf("direct visitor = %q, forged headers were trusted", got)
	}

	ipv6 := httptest.NewRequest("POST", "/", nil)
	ipv6.RemoteAddr = "[2a05:45c2:1:2:3:4:5:6]:40000"
	other := httptest.NewRequest("POST", "/", nil)
	other.RemoteAddr = "[2a05:45c2:1:2:ffff:4:5:6]:40000"
	if shareVisitorKey(ipv6) != shareVisitorKey(other) {
		t.Fatal("addresses in one IPv6 /64 were treated as different visitors")
	}
}

func TestHumanWait(t *testing.T) {
	cases := map[time.Duration]string{
		time.Minute: "1 minute", 30 * time.Minute: "30 minutes", 24 * time.Hour: "24 hours",
		45 * time.Second: "45 seconds", 61 * time.Second: "2 minutes", 59*time.Second + 500*time.Millisecond: "1 minute",
		time.Hour: "1 hour", 90 * time.Minute: "2 hours",
	}
	for wait, want := range cases {
		if got := humanWait(wait); got != want {
			t.Errorf("humanWait(%v) = %q, want %q", wait, got, want)
		}
	}
}

func TestShareUploaderLimit(t *testing.T) {
	clock := &guardClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	limit := newShareUploaderLimit(2)
	limit.now = func() time.Time { return clock.now }
	if !limit.allow("1.2.3.4", "a") || !limit.allow("1.2.3.4", "b") {
		t.Fatal("first two accounts refused")
	}
	if !limit.allow("1.2.3.4", "a") {
		t.Fatal("an account already counted was refused")
	}
	if limit.allow("1.2.3.4", "c") {
		t.Fatal("third account from one address allowed")
	}
	if !limit.allow("5.6.7.8", "c") {
		t.Fatal("another address was limited")
	}
	clock.advance(shareUploaderWindow + time.Minute)
	if !limit.allow("1.2.3.4", "c") {
		t.Fatal("limit did not reset after a day")
	}
}
