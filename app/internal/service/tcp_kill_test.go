package service

import (
	"net"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

func TestStoppedTunnelIsNotResumedForAMinute(t *testing.T) {
	service, app, users := dbService(t, "alice")
	saveRow(t, app, users["alice"], "shop", true, time.Now())
	session, cliEnd := heldSession(t, service, "shop", users["alice"])
	answeringCLI(t, cliEnd)

	if err := service.StopTunnel(users["alice"], "shop"); err != nil {
		t.Fatal(err)
	}
	if !session.IsClosed() {
		t.Fatal("stop should close the session")
	}
	service.mu.Lock()
	service.forgetSessionLocked("shop")
	service.mu.Unlock()

	// A 0.1.9+ CLI resumes; 0.1.8 reconnects without resume and gets its
	// latest tunnel. Both are refused for good.
	for _, req := range []tunnelRegistrationRequest{
		{Type: "http", Port: "3000", Token: "gp_alice", Resume: "shop"},
		{Type: "http", Port: "3000", Token: "gp_alice"},
		{Type: "http", Port: "3000", Token: "gp_alice", Subdomain: "shop"},
	} {
		_, err := service.resolveTunnel(req)
		if got := refusalText(err); got != stoppedMessage {
			t.Errorf("%+v: err = %v, want %q", req, err, stoppedMessage)
		}
	}

	// A fresh name still works, and the hold ends after a minute.
	if claim, err := service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_alice", Reset: true}); err != nil || claim.subdomain == "shop" {
		t.Fatalf("reset: %q, %v", claim.subdomain, err)
	}
	service.mu.Lock()
	service.stopped[stopKey(users["alice"], "shop")] = time.Now().Add(-time.Second)
	service.mu.Unlock()
	if claim, err := service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_alice", Resume: "shop"}); err != nil || claim.subdomain != "shop" {
		t.Fatalf("after the hold: %q, %v", claim.subdomain, err)
	}
}

func TestRegistrationRecordsTheTokenRow(t *testing.T) {
	service, app, users := dbService(t, "alice")
	tokens, err := app.FindCollectionByNameOrId(model.TokensCollection)
	if err != nil {
		t.Fatal(err)
	}
	token := core.NewRecord(tokens)
	token.Set("user_id", users["alice"])
	token.Set("name", "laptop")
	token.Set("token", "gp_alice")
	if err := app.Save(token); err != nil {
		t.Fatal(err)
	}

	claim, err := service.resolveTunnel(tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_alice"})
	if err != nil {
		t.Fatal(err)
	}
	if claim.tokenID != token.Id {
		t.Fatalf("tokenID = %q, want %q", claim.tokenID, token.Id)
	}
}

func TestDeletingATokenClosesItsTunnels(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	first, firstCLI := heldSession(t, service, "first", "user_1")
	second, secondCLI := heldSession(t, service, "second", "user_1")
	other, otherCLI := heldSession(t, service, "other", "user_1")
	for _, conn := range []net.Conn{firstCLI, secondCLI, otherCLI} {
		answeringCLI(t, conn)
	}
	service.sessionTokens["first"] = "token_a"
	service.sessionTokens["second"] = "token_a"
	service.sessionTokens["other"] = "token_b"

	// A registration with the token still in its handshake.
	pendingServer, pendingCLI := net.Pipe()
	t.Cleanup(func() { pendingCLI.Close() })
	service.sessions["pending"] = nil
	service.owners["pending"] = pendingServer
	service.sessionUsers["pending"] = "user_1"
	service.sessionTokens["pending"] = "token_a"

	if closed := service.CloseSessionsForToken("token_a"); closed != 3 {
		t.Fatalf("closed %d, want 3", closed)
	}
	if !first.IsClosed() || !second.IsClosed() {
		t.Fatal("the token's sessions should be closed")
	}
	if other.IsClosed() {
		t.Fatal("another token's session must stay open")
	}
	_ = pendingCLI.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := pendingCLI.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("the pending registration should be cut, read err = %v", err)
	}

	// A registration that verified the token just before it was deleted.
	err := service.reserveSession(tunnelClaim{subdomain: "late", tunnelID: "t", userID: "user_1", tokenID: "token_a", maxActive: 10}, tunnelRegistrationRequest{Type: "http"}, nil)
	if err == nil || refusalText(err) == "" {
		t.Fatalf("err = %v, want an authentication refusal", err)
	}
}
