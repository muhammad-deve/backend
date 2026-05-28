package service

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type TCPI interface {
	Start() error
	Stop()
	HandleTunnelRequest(e *core.RequestEvent) (bool, error)
}

type tunnelRegistrationRequest struct {
	Type string `json:"type"`
	Port string `json:"port"`
}

type tunnelRegistrationResponse struct {
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
	Error     string `json:"error,omitempty"`
}

type tcpService struct {
	listener net.Listener
	addr     string
	mu       sync.Mutex
	conns    map[string]net.Conn
	sessions map[string]*yamux.Session
	app      core.App
	domain   string
}

func NewTCPService(app core.App) TCPI {
	port := os.Getenv("TCP_PORT")
	if port == "" {
		port = "7000"
	}

	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}

	return &tcpService{
		addr:     ":" + port,
		conns:    make(map[string]net.Conn),
		sessions: make(map[string]*yamux.Session),
		app:      app,
		domain:   domain,
	}
}

func (t *tcpService) Start() error {
	listener, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("failed to start TCP listener on %s: %w", t.addr, err)
	}
	t.listener = listener

	log.Printf("TCP tunnel listener started on %s", t.addr)

	go t.acceptLoop()
	return nil
}

func (t *tcpService) Stop() {
	if t.listener != nil {
		t.listener.Close()
	}

	t.mu.Lock()
	for addr, conn := range t.conns {
		conn.Close()
		delete(t.conns, addr)
	}
	for subdomain, session := range t.sessions {
		session.Close()
		delete(t.sessions, subdomain)
	}
	t.mu.Unlock()

	log.Printf("TCP tunnel listener stopped")
}

func (t *tcpService) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			log.Printf("TCP accept error: %v", err)
			return
		}

		remoteAddr := conn.RemoteAddr().String()
		log.Printf("new CLI connection from %s", remoteAddr)

		t.mu.Lock()
		t.conns[remoteAddr] = conn
		t.mu.Unlock()

		go t.handleConnection(conn)
	}
}

func (t *tcpService) handleConnection(conn net.Conn) {
	var subdomain string

	defer func() {
		remoteAddr := conn.RemoteAddr().String()
		conn.Close()

		t.mu.Lock()
		delete(t.conns, remoteAddr)
		if subdomain != "" {
			delete(t.sessions, subdomain)
		}
		t.mu.Unlock()

		log.Printf("CLI disconnected: %s", remoteAddr)
	}()

	var req tunnelRegistrationRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("failed to read tunnel registration: %v", err)
		return
	}
	if req.Port == "" {
		log.Printf("tunnel registration missing port")
		t.sendRegistrationError(conn, "missing port")
		return
	}

	createdSubdomain, err := t.createTunnel()
	if err != nil {
		log.Printf("failed to create tunnel: %v", err)
		t.sendRegistrationError(conn, fmt.Sprintf("failed to create tunnel: %v", err))
		return
	}
	subdomain = createdSubdomain

	resp := tunnelRegistrationResponse{
		Subdomain: subdomain,
		URL:       fmt.Sprintf("https://%s.%s", subdomain, t.domain),
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("failed to send tunnel registration response: %v", err)
		return
	}

	log.Printf("registered %s tunnel %s -> localhost:%s", req.Type, resp.URL, req.Port)

	session, err := yamux.Server(conn, nil)
	if err != nil {
		log.Printf("failed to start yamux server: %v", err)
		return
	}
	defer session.Close()

	t.mu.Lock()
	t.sessions[subdomain] = session
	t.mu.Unlock()

	<-session.CloseChan()
}

func (t *tcpService) sendRegistrationError(conn net.Conn, msg string) {
	if err := json.NewEncoder(conn).Encode(tunnelRegistrationResponse{Error: msg}); err != nil {
		log.Printf("failed to send tunnel registration error: %v", err)
	}
}

func (t *tcpService) createTunnel() (string, error) {
	collection, err := t.app.FindCollectionByNameOrId("tunnels")
	if err != nil {
		return "", err
	}

	const userID = "5743847505m28jb"
	subdomain, err := t.generateUniqueSubdomain()
	if err != nil {
		return "", err
	}

	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("subdomain", subdomain)
	record.Set("is_custom", false)

	if err := t.app.Save(record); err != nil {
		return "", err
	}

	return subdomain, nil
}

func (t *tcpService) generateUniqueSubdomain() (string, error) {
	for i := 0; i < 10; i++ {
		subdomain, err := randomSubdomain(6)
		if err != nil {
			return "", err
		}

		_, err = t.app.FindFirstRecordByFilter("tunnels", "subdomain = {:subdomain}", dbx.Params{
			"subdomain": subdomain,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return subdomain, nil
		}
		if err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("failed to generate unique subdomain")
}

func randomSubdomain(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = alphabet[int(bytes[i])%len(alphabet)]
	}

	return string(bytes), nil
}

func (t *tcpService) HandleTunnelRequest(e *core.RequestEvent) (bool, error) {
	subdomain, ok := t.subdomainFromHost(e.Request.Host)
	if !ok {
		return false, nil
	}

	t.mu.Lock()
	session := t.sessions[subdomain]
	t.mu.Unlock()

	if session == nil || session.IsClosed() {
		http.Error(e.Response, "tunnel is not connected", http.StatusBadGateway)
		return true, nil
	}

	stream, err := session.Open()
	if err != nil {
		log.Printf("tunnel %s: failed to open stream: %v", subdomain, err)
		http.Error(e.Response, "failed to open tunnel stream", http.StatusBadGateway)
		return true, nil
	}
	defer stream.Close()

	// Build a clean outgoing request so Request.Write produces a valid wire-format request
	// regardless of how the incoming request was processed by PocketBase's router.
	outReq, err := buildForwardRequest(e.Request)
	if err != nil {
		log.Printf("tunnel %s: failed to build forward request: %v", subdomain, err)
		http.Error(e.Response, "failed to prepare tunnel request", http.StatusBadGateway)
		return true, nil
	}

	if err := outReq.Write(stream); err != nil {
		log.Printf("tunnel %s: failed to write request to stream: %v", subdomain, err)
		http.Error(e.Response, "failed to send request to tunnel", http.StatusBadGateway)
		return true, nil
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), outReq)
	if err != nil {
		log.Printf("tunnel %s: failed to read response from stream: %v", subdomain, err)
		http.Error(e.Response, "failed to read tunnel response", http.StatusBadGateway)
		return true, nil
	}
	defer resp.Body.Close()

	copyHeaders(e.Response.Header(), resp.Header)
	e.Response.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(e.Response, resp.Body); err != nil && !isClosedErr(err) {
		log.Printf("tunnel %s: error copying response body: %v", subdomain, err)
		return true, nil
	}
	return true, nil
}

// buildForwardRequest reads the incoming request body and creates a fresh outbound
// http.Request suitable for serializing over the tunnel stream with Request.Write.
func buildForwardRequest(in *http.Request) (*http.Request, error) {
	var body io.Reader
	var contentLength int64 = -1

	if in.Body != nil && in.Body != http.NoBody {
		buf, err := io.ReadAll(in.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		_ = in.Body.Close()
		body = bytes.NewReader(buf)
		contentLength = int64(len(buf))
	}

	out, err := http.NewRequest(in.Method, in.URL.RequestURI(), body)
	if err != nil {
		return nil, err
	}

	// Copy non-hop headers; the local server should see the original headers.
	for key, values := range in.Header {
		if isHopByHopHeader(key) {
			continue
		}
		// http.NewRequest may have set Content-Length from the body; skip duplicate.
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, v := range values {
			out.Header.Add(key, v)
		}
	}
	out.Host = in.Host
	if contentLength >= 0 {
		out.ContentLength = contentLength
	}
	out.Close = true
	out.Header.Set("Connection", "close")

	return out, nil
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

func (t *tcpService) subdomainFromHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	domain := strings.ToLower(strings.TrimSpace(t.domain))
	suffix := "." + domain
	if host == domain || host == "www."+domain || host == "back."+domain || !strings.HasSuffix(host, suffix) {
		return "", false
	}

	subdomain := strings.TrimSuffix(host, suffix)
	if subdomain == "" || strings.Contains(subdomain, ".") {
		return "", false
	}

	return subdomain, true
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
