package service

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// heldSession registers a server-side yamux session for subdomain, as
// handleConnection does, and returns the CLI end of the connection.
func heldSession(t *testing.T, service *tcpService, subdomain, userID string) (*yamux.Session, net.Conn) {
	t.Helper()
	serverEnd, cliEnd := net.Pipe()
	cfg := tunnelSessionConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(serverEnd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		cliEnd.Close()
	})
	service.sessions[subdomain] = session
	service.owners[subdomain] = serverEnd
	service.sessionUsers[subdomain] = userID
	return session, cliEnd
}

func answeringCLI(t *testing.T, conn net.Conn) {
	t.Helper()
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	client, err := yamux.Client(conn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
}

func shortProbe(t *testing.T) {
	t.Helper()
	previous := staleSessionProbeTimeout
	staleSessionProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { staleSessionProbeTimeout = previous })
}

func TestReleaseDeadSessionFreesUnansweredSession(t *testing.T) {
	shortProbe(t)
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	// Nothing reads the CLI end, like a link that died without a clean close.
	session, _ := heldSession(t, service, "myapp", "user_1")

	if !service.releaseDeadSession("myapp", "user_1") {
		t.Fatal("a session that never answers should be released for the reconnect")
	}
	if !session.IsClosed() {
		t.Fatal("the dead session should be closed")
	}
	if service.hasLiveSession("myapp") {
		t.Fatal("the subdomain should no longer count as live")
	}
}

func TestReleaseDeadSessionKeepsAnsweringSession(t *testing.T) {
	shortProbe(t)
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	session, cliEnd := heldSession(t, service, "myapp", "user_1")
	answeringCLI(t, cliEnd)

	if service.releaseDeadSession("myapp", "user_1") {
		t.Fatal("a session that answers must not be evicted")
	}
	if session.IsClosed() {
		t.Fatal("the live session must stay open")
	}
}

func TestReleaseDeadSessionIgnoresOtherAccounts(t *testing.T) {
	shortProbe(t)
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	session, _ := heldSession(t, service, "myapp", "user_1")

	if service.releaseDeadSession("myapp", "user_2") {
		t.Fatal("another account must not be able to evict the session")
	}
	if session.IsClosed() {
		t.Fatal("the session must stay open")
	}
}

func TestReleaseDeadSessionSkipsPendingHandshake(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.sessions["myapp"] = nil
	service.owners["myapp"] = nil
	service.sessionUsers["myapp"] = "user_1"

	if service.releaseDeadSession("myapp", "user_1") {
		t.Fatal("a registration still completing its handshake must not be released")
	}
}
