package service

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// cliHandler answers one forwarded request on a fake CLI. br reads the rest
// of the stream after the request head.
type cliHandler func(stream net.Conn, br *bufio.Reader, req *http.Request)

// fakeTunnel registers a live session for subdomain whose CLI end runs
// handler for every stream the server opens, and returns a test server that
// routes requests through ServeTunnel.
func fakeTunnel(t *testing.T, service *tcpService, subdomain, userID string, handler cliHandler) (*httptest.Server, *yamux.Session) {
	t.Helper()
	serverEnd, cliEnd := net.Pipe()
	cfg := tunnelSessionConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(serverEnd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	clientCfg := yamux.DefaultConfig()
	clientCfg.EnableKeepAlive = false
	clientCfg.LogOutput = io.Discard
	client, err := yamux.Client(cliEnd, clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			stream, err := client.Accept()
			if err != nil {
				return
			}
			go func() {
				defer stream.Close()
				br := bufio.NewReader(stream)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				handler(stream, br, req)
			}()
		}
	}()

	service.mu.Lock()
	service.sessions[subdomain] = session
	service.owners[subdomain] = serverEnd
	service.tunnelIDs[subdomain] = "tunnel_" + subdomain
	service.sessionUsers[subdomain] = userID
	service.sessionTypes[subdomain] = "http"
	service.nextSession++
	service.sessionOrder[subdomain] = service.nextSession
	service.mu.Unlock()
	service.quotaMu.Lock()
	service.quotaCache[userID] = trafficQuotaCacheEntry{limits: model.ProPlanLimits(), expiresAt: time.Now().Add(time.Hour)}
	service.quotaMu.Unlock()

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !service.ServeTunnel(w, r) {
			w.Header().Set("X-Fell-Through", "1")
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(func() {
		front.Close()
		session.Close()
		client.Close()
	})
	return front, client
}

// echoCLI answers 200 and reports what it received in X-Got-* headers.
func echoCLI(stream net.Conn, _ *bufio.Reader, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{
			"X-Got-Method": {req.Method},
			"X-Got-Path":   {req.URL.Path},
			"X-Got-Hop":    {req.Header.Get(tunnelHopHeader)},
			"X-Got-Body":   {string(body)},
		},
		Body:          io.NopCloser(strings.NewReader("ok")),
		ContentLength: 2,
		Close:         true,
	}
	_ = resp.Write(stream)
}

func tunnelRequest(t *testing.T, front *httptest.Server, method, host, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	return req
}

func doTunnel(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestServeTunnelLeavesOtherHostsToPocketBase(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoCLI)

	for _, host := range []string{"goport.uz", "back.goport.uz", "www.goport.uz", "a.b.goport.uz", "example.com"} {
		resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, host, "/", nil))
		if resp.Header.Get("X-Fell-Through") != "1" {
			t.Errorf("%s should fall through to PocketBase", host)
		}
	}
}

func TestHopHeaderIsIgnoredWithoutSecret(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoCLI)

	req := tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/_/", nil)
	req.Header.Set(tunnelHopHeader, "1")
	resp := doTunnel(t, req)
	if resp.Header.Get("X-Fell-Through") != "" {
		t.Fatal("a visitor-sent hop header must not reach PocketBase when no secret is set")
	}
	if got := resp.Header.Get("X-Got-Hop"); got != "" {
		t.Fatalf("the hop header reached the CLI: %q", got)
	}
}

func TestHopHeaderNeedsTheConfiguredSecret(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.hopSecret = "s3cret-value"
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoCLI)

	wrong := tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/", nil)
	wrong.Header.Set(tunnelHopHeader, "guess")
	resp := doTunnel(t, wrong)
	if resp.Header.Get("X-Fell-Through") != "" || resp.Header.Get("X-Got-Hop") != "" {
		t.Fatal("a wrong hop value must be ignored and not forwarded")
	}

	right := tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/", nil)
	right.Header.Set(tunnelHopHeader, "s3cret-value")
	if resp := doTunnel(t, right); resp.Header.Get("X-Fell-Through") != "1" {
		t.Fatal("the configured secret should let the request skip tunnel routing")
	}
}

func TestPlainTunnelForwardsSharePathsAndPreflights(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoCLI)

	resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/s/anything", nil))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Got-Path") != "/s/anything" {
		t.Fatalf("/s/ on an http tunnel: status %d, path %q", resp.StatusCode, resp.Header.Get("X-Got-Path"))
	}

	preflight := tunnelRequest(t, front, http.MethodOptions, "myapp.goport.uz", "/api/collections/x/records", nil)
	preflight.Header.Set("Origin", "https://web.telegram.org")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	resp = doTunnel(t, preflight)
	if resp.Header.Get("X-Got-Method") != http.MethodOptions {
		t.Fatal("an OPTIONS preflight should reach the CLI")
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "X-Frame-Options", "X-Content-Type-Options"} {
		if v := resp.Header.Get(name); v != "" {
			t.Errorf("%s = %q was added to a tunneled response", name, v)
		}
	}
}

func TestShareTunnelOnlyServesItsTokenPath(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	front, _ := fakeTunnel(t, service, "files", "user_1", echoCLI)
	token := strings.Repeat("t", 32)
	service.mu.Lock()
	service.sessionTypes["files"] = "share"
	service.shareTokens["files"] = token
	service.mu.Unlock()

	if resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "files.goport.uz", "/s/"+token+"/a.txt", nil)); resp.StatusCode != http.StatusOK {
		t.Fatalf("share token path: status %d", resp.StatusCode)
	}
	for _, path := range []string{"/", "/s/wrong", "/other"} {
		if resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "files.goport.uz", path, nil)); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s on a share tunnel: status %d, want 404", path, resp.StatusCode)
		}
	}
}
