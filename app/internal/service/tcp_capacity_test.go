package service

import (
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func countTunnelRows(t *testing.T, service *tcpService, userID string) int {
	t.Helper()
	rows, err := service.app.FindRecordsByFilter(model.TunnelsCollection, "user = {:user}", "", 0, 0, dbx.Params{"user": userID})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestFullPlanWritesNoRows(t *testing.T) {
	shortProbe(t)
	service, app, users := dbService(t, "alice")
	service.quotaCache[users["alice"]] = trafficQuotaCacheEntry{limits: model.FreePlanLimits(), expiresAt: time.Now().Add(time.Hour)}
	saveRow(t, app, users["alice"], "first1", false, time.Now())
	// Held, but its link died without a clean close.
	heldSession(t, service, "first1", users["alice"])
	before := countTunnelRows(t, service, users["alice"])

	for _, req := range []tunnelRegistrationRequest{
		{Type: "http", Port: "3000", Token: "gp_alice", Reset: true},
		{Type: "share", Port: "3000", Token: "gp_alice", ShareToken: "abcdefghijklmnopqrstuvwxyz"},
	} {
		_, err := service.resolveTunnel(req)
		if got := refusalText(err); got != planLimitMessage(1) {
			t.Errorf("%s: err = %v, want the plan limit", req.Type, err)
		}
	}
	if after := countTunnelRows(t, service, users["alice"]); after != before {
		t.Fatalf("refused registrations wrote %d tunnel rows", after-before)
	}

	// Resuming the held subdomain replaces that session, so it doesn't count.
	claim, err := service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_alice", Resume: "first1"})
	if err != nil || claim.subdomain != "first1" {
		t.Fatalf("resume: %q, %v", claim.subdomain, err)
	}
}

func TestRegistrationRateLimits(t *testing.T) {
	deny := fakeTokens{verify: func(string) (*model.TokenOwner, error) { return nil, ErrInvalidToken }}

	t.Run("per token", func(t *testing.T) {
		service := NewTCPService(nil, nil, nil, nil, deny).(*tcpService)
		service.registrationsByToken = newRateLimiter(time.Minute, 2, 0)
		for i := 0; i < 2; i++ {
			if resp := registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_same"}); resp.Error == tooManyRegistrationsMessage {
				t.Fatalf("attempt %d was rate limited", i+1)
			}
		}
		if resp := registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_same"}); resp.Error != tooManyRegistrationsMessage {
			t.Fatalf("third attempt: %q", resp.Error)
		}
		if resp := registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_other"}); resp.Error == tooManyRegistrationsMessage {
			t.Fatal("another token must not be limited")
		}
	})

	t.Run("per IP", func(t *testing.T) {
		service := NewTCPService(nil, nil, nil, nil, deny).(*tcpService)
		service.registrationsByIP = newRateLimiter(time.Minute, 2, 0)
		for _, token := range []string{"gp_a", "gp_b"} {
			registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: token})
		}
		if resp := registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_c"}); resp.Error != tooManyRegistrationsMessage {
			t.Fatalf("third attempt from one IP: %q", resp.Error)
		}
	})
}

func TestRateLimitMessageIsNotFinal(t *testing.T) {
	// CLIs stop retrying on these phrases; a rate limit must not contain any.
	for _, phrase := range []string{"authentication required", "available on pro", "traffic limit", "outside your plan allowance", "cannot be used together", "subdomain must", "subdomain cannot", "subdomain can only", "is reserved", "missing port"} {
		for _, message := range []string{tooManyRegistrationsMessage, planLimitMessage(1), internalRegistrationError} {
			if containsFold(message, phrase) {
				t.Errorf("%q contains the permanent phrase %q", message, phrase)
			}
		}
	}
	for _, message := range []string{nameTakenMessage, stoppedMessage} {
		if !containsFold(message, "subdomain cannot") {
			t.Errorf("%q should stop the CLI", message)
		}
	}
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
