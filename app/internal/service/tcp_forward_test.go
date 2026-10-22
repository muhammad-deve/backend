package service

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestRequestBodyOverLimitGets413(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.maxRequestBody = 1024
	reached := make(chan struct{}, 4)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, br *bufio.Reader, req *http.Request) {
		reached <- struct{}{}
		echoCLI(stream, br, req)
	})

	// Declared too large: refused before the CLI is involved.
	resp := doTunnel(t, tunnelRequest(t, front, http.MethodPost, "myapp.goport.uz", "/upload", strings.NewReader(strings.Repeat("a", 2048))))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversized body: status %d, want 413", resp.StatusCode)
	}
	select {
	case <-reached:
		t.Fatal("a body declared over the limit must not reach the CLI")
	default:
	}

	// Chunked, so the size is only known while streaming.
	chunked := tunnelRequest(t, front, http.MethodPost, "myapp.goport.uz", "/upload", io.MultiReader(strings.NewReader(strings.Repeat("b", 4096))))
	chunked.ContentLength = -1
	if resp := doTunnel(t, chunked); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("streamed oversized body: status %d, want 413", resp.StatusCode)
	}

	// Under the limit the body arrives whole.
	small := tunnelRequest(t, front, http.MethodPost, "myapp.goport.uz", "/upload", io.MultiReader(strings.NewReader(strings.Repeat("c", 1000))))
	small.ContentLength = -1
	resp = doTunnel(t, small)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Got-Body") != strings.Repeat("c", 1000) {
		t.Fatalf("body under the limit: status %d, got %d bytes", resp.StatusCode, len(resp.Header.Get("X-Got-Body")))
	}
}

func TestOversizedResponseHeadIsRefused(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.maxResponseHead = 2048
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, _ *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\nX-Big: "+strings.Repeat("x", 8192)+"\r\nContent-Length: 0\r\n\r\n")
	})
	if resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/", nil)); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}

func TestSlowTunnelGets504(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.responseTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(net.Conn, *bufio.Reader, *http.Request) { <-release })
	if resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/", nil)); resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504", resp.StatusCode)
	}
}

func TestEventStreamIsFlushedAsItArrives(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	second := make(chan struct{})
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, _ *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nConnection: close\r\n\r\ndata: one\n\n")
		<-second
		_, _ = io.WriteString(stream, "data: two\n\n")
	})

	resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/events", nil))
	reader := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "data: one\n" {
			t.Fatalf("first event = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first event was held back until the response ended")
	}
	close(second)
}

func TestVisitorLeavingClosesTheStream(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	streamEnded := make(chan struct{})
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, br *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\ndata: hello\n\n")
		// Waits for the server to give up on the stream.
		_, _ = io.Copy(io.Discard, br)
		close(streamEnded)
	})

	req := tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
	resp.Body.Close()
	http.DefaultClient.CloseIdleConnections()

	select {
	case <-streamEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel stream stayed open after the visitor left")
	}
}

func TestTunnelCookiesStayOnTheTunnelHost(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, _ *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\n"+
			"Set-Cookie: a=1; Domain=goport.uz; Path=/\r\n"+
			"Set-Cookie: b=2; Path=/; domain=.GOPORT.UZ; Secure\r\n"+
			"Set-Cookie: c=3; Domain=myapp.goport.uz\r\n"+
			"Set-Cookie: d=4\r\n"+
			"Content-Length: 0\r\nConnection: close\r\n\r\n")
	})
	resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/", nil))
	want := []string{"a=1; Path=/", "b=2; Path=/; Secure", "c=3; Domain=myapp.goport.uz", "d=4"}
	got := resp.Header.Values("Set-Cookie")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Set-Cookie = %q, want %q", got, want)
	}
}

func TestHostOnlyCookie(t *testing.T) {
	cases := map[string]string{
		"s=1; Domain=goport.uz":         "s=1",
		"s=1; Domain=uz; HttpOnly":      "s=1; HttpOnly",
		"s=1;Domain=.goport.uz.;Path=/": "s=1;Path=/",
		"s=1; Domain=app.goport.uz":     "s=1; Domain=app.goport.uz",
		"s=1; Domain=evilgoport.uz":     "s=1; Domain=evilgoport.uz",
		"s=1; Path=/":                   "s=1; Path=/",
	}
	for in, want := range cases {
		if got := hostOnlyCookie(in, "goport.uz"); got != want {
			t.Errorf("hostOnlyCookie(%q) = %q, want %q", in, got, want)
		}
	}
}

// dialUpgrade sends a raw WebSocket upgrade request to the test server.
func dialUpgrade(t *testing.T, frontURL, host string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(frontURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(conn, "GET /socket HTTP/1.1\r\nHost: "+host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, reader, resp
}

// echoUpgradeCLI switches protocols and echoes what the visitor sends.
func echoUpgradeCLI(stream net.Conn, br *bufio.Reader, _ *http.Request) {
	_, _ = io.WriteString(stream, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSet-Cookie: ws=1; Domain=goport.uz\r\n\r\n")
	_, _ = io.Copy(stream, br)
}

func TestWebSocketUpgradeIsRelayed(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoUpgradeCLI)

	conn, reader, resp := dialUpgrade(t, front.URL, "myapp.goport.uz")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("Set-Cookie"); got != "ws=1" {
		t.Fatalf("101 Set-Cookie = %q, want the Domain removed", got)
	}
	_, _ = io.WriteString(conn, "ping")
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, %v", echo, err)
	}
}

func TestIdleWebSocketIsClosed(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.upgradeIdleTimeout = 300 * time.Millisecond
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoUpgradeCLI)

	_, reader, resp := dialUpgrade(t, front.URL, "myapp.goport.uz")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("expected the idle connection to be closed")
	} else if isTimeout(err) {
		t.Fatal("the idle connection was never closed")
	}
}

func TestUpgradeRefusedByTheAppIsAnOrdinaryResponse(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, _ *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 403 Forbidden\r\nContent-Length: 4\r\n\r\nnope")
	})
	req := tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/socket", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	resp := doTunnel(t, req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || string(body) != "nope" {
		t.Fatalf("status %d body %q, want 403 nope", resp.StatusCode, body)
	}
}

func TestCLIOpenedStreamsAreClosed(t *testing.T) {
	serverEnd, cliEnd := net.Pipe()
	cfg := tunnelSessionConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(serverEnd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	clientCfg := yamux.DefaultConfig()
	clientCfg.EnableKeepAlive = false
	clientCfg.LogOutput = io.Discard
	client, err := yamux.Client(cliEnd, clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	go refuseCLIStreams(session, "myapp")

	stream, err := client.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stream.Write([]byte("hello"))
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := stream.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("a CLI-opened stream should be closed by the server, read err = %v", err)
	}

	for i := 1; i < maxCLIOpenedStreams; i++ {
		s, err := client.Open()
		if err != nil {
			break
		}
		_ = s.Close()
	}
	select {
	case <-session.CloseChan():
	case <-time.After(3 * time.Second):
		t.Fatal("a CLI that keeps opening streams should lose its session")
	}
}
