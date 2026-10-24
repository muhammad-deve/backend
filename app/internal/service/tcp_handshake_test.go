package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// scriptedListener hands out queued errors and connections, then reports
// net.ErrClosed once closed.
type scriptedListener struct {
	results chan acceptResult
	closed  chan struct{}
}

type acceptResult struct {
	conn net.Conn
	err  error
}

func newScriptedListener() *scriptedListener {
	return &scriptedListener{results: make(chan acceptResult, 8), closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	select {
	case r := <-l.results:
		return r.conn, r.err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *scriptedListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{} }

func readRegistrationReply(t *testing.T, conn net.Conn) tunnelRegistrationResponse {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading registration reply: %v", err)
	}
	var resp tunnelRegistrationResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("bad reply %q: %v", line, err)
	}
	return resp
}

func TestAcceptLoopSurvivesAcceptErrors(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	listener := newScriptedListener()
	service.listener = listener

	done := make(chan struct{})
	go func() {
		service.acceptLoop()
		close(done)
	}()

	// A failed Accept used to end the loop for good.
	listener.results <- acceptResult{err: errors.New("accept: too many open files")}

	serverEnd, cliEnd := net.Pipe()
	defer cliEnd.Close()
	listener.results <- acceptResult{conn: serverEnd}

	// A registration without a port is refused, which proves the connection
	// accepted after the error was served.
	go func() { _, _ = cliEnd.Write([]byte(`{"type":"http"}` + "\n")) }()
	if resp := readRegistrationReply(t, cliEnd); resp.Error != "missing port" {
		t.Fatalf("reply error = %q, want %q", resp.Error, "missing port")
	}

	listener.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop should stop once the listener is closed")
	}
}

func TestOversizedRegistrationIsRefused(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	serverEnd, cliEnd := net.Pipe()
	defer cliEnd.Close()

	service.handshakes.begin("pipe")
	go service.handleConnection(serverEnd, "pipe")
	go func() {
		_, _ = cliEnd.Write([]byte(`{"type":"http","port":"` + strings.Repeat("9", maxRegistrationBytes) + `"}` + "\n"))
	}()

	if resp := readRegistrationReply(t, cliEnd); resp.Error != "registration request is too large" {
		t.Fatalf("reply error = %q", resp.Error)
	}
}

func TestHandshakeLimiterCapsTotalAndPerIP(t *testing.T) {
	limiter := newHandshakeLimiter(3, 2)
	if !limiter.begin("1.1.1.1") || !limiter.begin("1.1.1.1") {
		t.Fatal("the first two handshakes from one IP should be allowed")
	}
	if limiter.begin("1.1.1.1") {
		t.Fatal("a third pending handshake from the same IP must be refused")
	}
	if !limiter.begin("2.2.2.2") {
		t.Fatal("another IP should still get a slot")
	}
	if limiter.begin("3.3.3.3") {
		t.Fatal("the global cap must apply across IPs")
	}

	limiter.end("1.1.1.1")
	if !limiter.begin("3.3.3.3") {
		t.Fatal("a finished handshake should free its slot")
	}
	limiter.end("1.1.1.1")
	limiter.end("2.2.2.2")
	limiter.end("3.3.3.3")
	if limiter.total != 0 || len(limiter.byIP) != 0 {
		t.Fatalf("limiter should be empty, got total=%d byIP=%v", limiter.total, limiter.byIP)
	}
}

func TestHandshakeSlotIsFreedAfterRefusal(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	serverEnd, cliEnd := net.Pipe()
	defer cliEnd.Close()

	service.handshakes.begin("pipe")
	finished := make(chan struct{})
	go func() {
		service.handleConnection(serverEnd, "pipe")
		close(finished)
	}()
	go func() { _, _ = cliEnd.Write([]byte(`{"type":"http"}` + "\n")) }()
	readRegistrationReply(t, cliEnd)
	<-finished

	if service.handshakes.total != 0 {
		t.Fatalf("pending handshakes = %d after the connection ended, want 0", service.handshakes.total)
	}
}
