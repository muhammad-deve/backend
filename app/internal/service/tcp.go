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
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
)

type TCPI interface {
	Start() error
	Stop()
	HandleTunnelRequest(e *core.RequestEvent) (bool, error)
	StopTunnel(userID, subdomain string) error
	DeleteTunnel(userID, subdomain string) error
}

var (
	ErrTunnelNotFound = errors.New("tunnel not found")
	ErrTunnelInactive = errors.New("tunnel is not active")
	ErrTunnelActive   = errors.New("stop the tunnel before deleting it")
)

type tunnelRegistrationRequest struct {
	Type      string `json:"type"`
	Port      string `json:"port"`
	Subdomain string `json:"subdomain,omitempty"`
	Reset     bool   `json:"reset,omitempty"`
	Token     string `json:"token,omitempty"`
}

type tunnelRegistrationResponse struct {
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
	Error     string `json:"error,omitempty"`
}

type tcpService struct {
	listener  net.Listener
	addr      string
	mu        sync.Mutex
	conns     map[string]net.Conn
	sessions  map[string]*yamux.Session
	owners    map[string]net.Conn
	tunnelIDs map[string]string
	app       core.App
	domain    string
	statsMu   sync.Mutex
	stats     map[tunnelStatKey]*tunnelStat
	stopFlush chan struct{}
	tunnels   repository.TunnelsI
	usage     repository.UsageI
}

type tunnelStatKey struct {
	tunnelID    string
	subdomain   string
	bucketStart time.Time
}

type tunnelStat struct {
	requests   int64
	bytes      int64
	lastActive time.Time
}

func NewTCPService(app core.App, tunnels repository.TunnelsI, usage repository.UsageI) TCPI {
	port := os.Getenv("TCP_PORT")
	if port == "" {
		port = "7000"
	}

	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}

	return &tcpService{
		addr:      ":" + port,
		conns:     make(map[string]net.Conn),
		sessions:  make(map[string]*yamux.Session),
		owners:    make(map[string]net.Conn),
		tunnelIDs: make(map[string]string),
		app:       app,
		domain:    domain,
		stats:     make(map[tunnelStatKey]*tunnelStat),
		stopFlush: make(chan struct{}),
		tunnels:   tunnels,
		usage:     usage,
	}
}

func (t *tcpService) Start() error {
	listener, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("failed to start TCP listener on %s: %w", t.addr, err)
	}
	t.listener = listener

	log.Printf("TCP tunnel listener started on %s", t.addr)

	// On startup, clear any stale is_current flags left over from a previous
	// crash or unclean shutdown so subdomains aren't permanently locked.
	t.clearAllCurrentFlags()

	go t.acceptLoop()
	go t.flushLoop()
	return nil
}

func (t *tcpService) Stop() {
	if t.listener != nil {
		t.listener.Close()
	}

	select {
	case <-t.stopFlush:
		// already closed
	default:
		close(t.stopFlush)
	}

	t.mu.Lock()
	for addr, conn := range t.conns {
		conn.Close()
		delete(t.conns, addr)
	}
	for subdomain, session := range t.sessions {
		if session != nil {
			session.Close()
		}
		delete(t.sessions, subdomain)
		delete(t.owners, subdomain)
		delete(t.tunnelIDs, subdomain)
	}
	t.mu.Unlock()

	// Persist any traffic accumulated since the last flush.
	t.flushStats()

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
	var sessionReserved bool

	defer func() {
		remoteAddr := conn.RemoteAddr().String()
		conn.Close()

		t.mu.Lock()
		delete(t.conns, remoteAddr)
		ownedSession := sessionReserved && subdomain != "" && t.owners[subdomain] == conn
		if ownedSession {
			delete(t.sessions, subdomain)
			delete(t.owners, subdomain)
			delete(t.tunnelIDs, subdomain)
		}
		t.mu.Unlock()

		// Only the connection that still holds the subdomain may mark it offline.
		// A rejected duplicate registration, or a reconnect that already handed the
		// subdomain to a newer connection, would otherwise flag a tunnel that is
		// still serving traffic as offline in the dashboard.
		if ownedSession {
			t.setTunnelCurrent(subdomain, false)
		}

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

	createdSubdomain, tunnelID, err := t.resolveTunnel(req)
	if err != nil {
		log.Printf("failed to register tunnel: %v", err)
		t.sendRegistrationError(conn, fmt.Sprintf("failed to register tunnel: %v", err))
		return
	}
	subdomain = createdSubdomain
	if !t.reserveSession(subdomain, tunnelID, conn) {
		t.sendRegistrationError(conn, fmt.Sprintf("subdomain %q is already connected", subdomain))
		return
	}
	sessionReserved = true

	resp := tunnelRegistrationResponse{
		Subdomain: subdomain,
		URL:       fmt.Sprintf("https://%s.%s", subdomain, t.domain),
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("failed to send tunnel registration response: %v", err)
		return
	}

	log.Printf("registered %s tunnel %s -> localhost:%s", req.Type, resp.URL, req.Port)

	// Mark the subdomain as actively in use.
	t.setTunnelCurrent(subdomain, true)

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

const tunnelUserID = "5743847505m28jb"

// errInvalidToken signals that a CLI either supplied no token or a token that
// doesn't match any account. Tunnels now require authentication, so the
// connection is refused with this message instead of falling back to a shared
// anonymous account.
var errInvalidToken = errors.New("authentication required: run `goport auth <token>` with a valid token from your GoPort dashboard")

// resolveUserID maps a CLI token to the owning user. A token is now mandatory:
// an empty token, or one that doesn't resolve to an account, is rejected with
// errInvalidToken rather than being lumped onto a shared account.
func (t *tcpService) resolveUserID(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errInvalidToken
	}
	rec, err := t.app.FindFirstRecordByFilter(model.TokensCollection, "token = {:token}", dbx.Params{
		"token": token,
	})
	if err != nil || rec == nil {
		return "", errInvalidToken
	}
	owner := rec.GetString("user_id")
	if owner == "" {
		return "", errInvalidToken
	}
	return owner, nil
}

func (t *tcpService) resolveTunnel(req tunnelRegistrationRequest) (string, string, error) {
	if req.Reset && strings.TrimSpace(req.Subdomain) != "" {
		return "", "", fmt.Errorf("reset and custom subdomain cannot be used together")
	}

	userID, err := t.resolveUserID(req.Token)
	if err != nil {
		return "", "", err
	}

	if strings.TrimSpace(req.Subdomain) != "" {
		subdomain, err := normalizeRequestedSubdomain(req.Subdomain, t.domain)
		if err != nil {
			return "", "", err
		}
		return t.saveTunnel(subdomain, true, userID, req.Port, req.Type)
	}

	if !req.Reset {
		record, err := t.findCurrentTunnel(userID)
		if err == nil {
			subdomain := record.GetString("subdomain")
			if subdomain != "" {
				return t.saveTunnel(subdomain, record.GetBool("is_custom"), userID, req.Port, req.Type)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
	}

	subdomain, err := t.generateUniqueSubdomain()
	if err != nil {
		return "", "", err
	}
	return t.saveTunnel(subdomain, false, userID, req.Port, req.Type)
}

func (t *tcpService) saveTunnel(subdomain string, isCustom bool, userID, localPort, protocol string) (string, string, error) {
	collection, err := t.app.FindCollectionByNameOrId(model.TunnelsCollection)
	if err != nil {
		return "", "", err
	}

	if existing, err := t.findTunnelBySubdomain(subdomain); err == nil {
		// The live session map decides whether a subdomain is in use, not the
		// is_current column, which can be left stale by an unclean shutdown.
		// Checking it before any write also stops a second CLI from rewriting the
		// connection metadata of a tunnel that is still online.
		if t.hasLiveSession(subdomain) {
			return "", "", fmt.Errorf("subdomain %q is currently in use", subdomain)
		}

		owner := existing.GetString("user")
		if owner != "" && owner != userID {
			if err := t.tunnels.DeleteWithLogs(existing.Id); err != nil {
				return "", "", err
			}
		} else {
			existing.Set("user", userID)
			existing.Set("is_custom", isCustom)
			existing.Set("is_current", false)
			existing.Set("local_port", strings.TrimSpace(localPort))
			existing.Set("protocol", normalizedProtocol(protocol))
			if err := t.app.Save(existing); err != nil {
				return "", "", err
			}
			return subdomain, existing.Id, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}

	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("subdomain", subdomain)
	record.Set("is_custom", isCustom)
	record.Set("is_current", false)
	record.Set("local_port", strings.TrimSpace(localPort))
	record.Set("protocol", normalizedProtocol(protocol))

	if err := t.app.Save(record); err != nil {
		return "", "", err
	}

	return subdomain, record.Id, nil
}

func normalizedProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return "http"
	}
	return protocol
}

func (t *tcpService) StopTunnel(userID, subdomain string) error {
	if _, err := t.tunnels.GetOwned(userID, subdomain); err != nil {
		return ErrTunnelNotFound
	}

	t.mu.Lock()
	session := t.sessions[subdomain]
	t.mu.Unlock()
	if session == nil || session.IsClosed() {
		return ErrTunnelInactive
	}

	if err := session.Close(); err != nil {
		return err
	}
	t.setTunnelCurrent(subdomain, false)
	return nil
}

func (t *tcpService) DeleteTunnel(userID, subdomain string) error {
	record, err := t.tunnels.GetOwned(userID, subdomain)
	if err != nil {
		return ErrTunnelNotFound
	}

	t.mu.Lock()
	session := t.sessions[subdomain]
	t.mu.Unlock()
	if record.IsCurrent || (session != nil && !session.IsClosed()) {
		return ErrTunnelActive
	}

	return t.tunnels.DeleteWithLogs(record.ID)
}

func (t *tcpService) findCurrentTunnel(userID string) (*core.Record, error) {
	records, err := t.app.FindRecordsByFilter("tunnels", "user = {:user}", "-updated", 1, 0, dbx.Params{
		"user": userID,
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

func (t *tcpService) findTunnelBySubdomain(subdomain string) (*core.Record, error) {
	return t.app.FindFirstRecordByFilter("tunnels", "subdomain = {:subdomain}", dbx.Params{
		"subdomain": subdomain,
	})
}

// setTunnelCurrent updates the is_current flag for a subdomain in the database.
// This tracks whether a tunnel is actively connected so other users know if a
// subdomain is available to claim.
func (t *tcpService) setTunnelCurrent(subdomain string, current bool) {
	rec, err := t.findTunnelBySubdomain(subdomain)
	if err != nil {
		return
	}
	rec.Set("is_current", current)
	if err := t.app.Save(rec); err != nil {
		log.Printf("failed to update is_current for %s: %v", subdomain, err)
	}
}

// clearAllCurrentFlags resets is_current=false for all tunnels. Called on
// startup to recover from crashes where flags were left stale.
func (t *tcpService) clearAllCurrentFlags() {
	records, err := t.app.FindRecordsByFilter(model.TunnelsCollection, "is_current = true", "", 0, 0)
	if err != nil {
		log.Printf("failed to query stale is_current tunnels: %v", err)
		return
	}
	for _, rec := range records {
		rec.Set("is_current", false)
		if err := t.app.Save(rec); err != nil {
			log.Printf("failed to clear is_current for tunnel %s: %v", rec.Id, err)
		}
	}
	if len(records) > 0 {
		log.Printf("cleared is_current flag on %d stale tunnel(s)", len(records))
	}
}

// hasLiveSession reports whether a subdomain is currently claimed by a connected
// CLI. A reserved-but-nil session is a registration that is still completing its
// yamux handshake and counts as live.
func (t *tcpService) hasLiveSession(subdomain string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, reserved := t.owners[subdomain]; !reserved {
		return false
	}
	session, ok := t.sessions[subdomain]
	if !ok {
		return false
	}
	return session == nil || !session.IsClosed()
}

func (t *tcpService) reserveSession(subdomain, tunnelID string, conn net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.sessions[subdomain]; ok {
		if existing == nil || !existing.IsClosed() {
			return false
		}
		delete(t.sessions, subdomain)
		delete(t.owners, subdomain)
		delete(t.tunnelIDs, subdomain)
	}

	t.sessions[subdomain] = nil
	t.owners[subdomain] = conn
	t.tunnelIDs[subdomain] = tunnelID
	return true
}

func normalizeRequestedSubdomain(value, domain string) (string, error) {
	subdomain := strings.ToLower(strings.TrimSpace(value))
	domain = strings.ToLower(strings.TrimSpace(domain))

	subdomain = strings.TrimPrefix(subdomain, "https://")
	subdomain = strings.TrimPrefix(subdomain, "http://")
	if i := strings.IndexAny(subdomain, "/:"); i >= 0 {
		subdomain = subdomain[:i]
	}
	subdomain = strings.TrimSuffix(subdomain, ".")
	if domain != "" {
		subdomain = strings.TrimSuffix(subdomain, "."+domain)
	}

	if err := validateSubdomain(subdomain); err != nil {
		return "", err
	}
	return subdomain, nil
}

func validateSubdomain(subdomain string) error {
	if len(subdomain) == 0 || len(subdomain) > 63 {
		return fmt.Errorf("subdomain must be 1-63 characters")
	}
	if subdomain[0] == '-' || subdomain[len(subdomain)-1] == '-' {
		return fmt.Errorf("subdomain cannot start or end with '-'")
	}
	if isReservedSubdomain(subdomain) {
		return fmt.Errorf("subdomain %q is reserved", subdomain)
	}

	for _, ch := range subdomain {
		if ch >= 'a' && ch <= 'z' {
			continue
		}
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '-' {
			continue
		}
		return fmt.Errorf("subdomain can only contain lowercase letters, numbers, and '-'")
	}
	return nil
}

func isReservedSubdomain(subdomain string) bool {
	switch subdomain {
	case "api", "admin", "back", "dashboard", "www":
		return true
	default:
		return false
	}
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

// tunnelForwardedHeader marks a request that has already been routed through a
// tunnel hop. The target of a tunnel may itself be a GoPort backend (for example
// when exposing this very service for testing). Because the original public Host
// header is preserved end-to-end, without this marker the forwarded request would
// match the subdomain rule again on the target backend, find no session in its own
// map, and fail with "tunnel is not connected".
const tunnelForwardedHeader = "X-Goport-Tunnel"

func (t *tcpService) HandleTunnelRequest(e *core.RequestEvent) (bool, error) {
	// Already came out of a tunnel hop, so don't intercept it again.
	if e.Request.Header.Get(tunnelForwardedHeader) != "" {
		return false, nil
	}

	subdomain, ok := t.subdomainFromHost(e.Request.Host)
	if !ok {
		return false, nil
	}

	t.mu.Lock()
	session := t.sessions[subdomain]
	tunnelID := t.tunnelIDs[subdomain]
	t.mu.Unlock()

	if session == nil || session.IsClosed() || tunnelID == "" {
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

	// Upgrade requests must keep their Connection and Upgrade headers all the
	// way to the local application. Next.js uses this for the dev HMR socket.
	upgradeRequest := isUpgradeRequest(e.Request)
	outReq, err := buildForwardRequest(e.Request, upgradeRequest)
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

	if upgradeRequest {
		clientConn, _, err := http.NewResponseController(e.Response).Hijack()
		if err != nil {
			log.Printf("tunnel %s: failed to hijack upgrade connection: %v", subdomain, err)
			_ = stream.Close()
			return true, err
		}
		defer clientConn.Close()

		// An upgraded connection is counted once, when it closes, together with
		// everything relayed over it.
		t.recordTraffic(tunnelID, subdomain, copyTunnelStream(clientConn, stream))
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
	written, err := io.Copy(e.Response, resp.Body)
	if err != nil && !isClosedErr(err) {
		log.Printf("tunnel %s: error copying response body: %v", subdomain, err)
		t.recordTraffic(tunnelID, subdomain, written+maxInt64(outReq.ContentLength, 0))
		return true, nil
	}
	t.recordTraffic(tunnelID, subdomain, written+maxInt64(outReq.ContentLength, 0))
	return true, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// recordTraffic keeps the completion timestamp with each delta so retries and
// flushes around a bucket boundary cannot move traffic into the wrong period.
func (t *tcpService) recordTraffic(tunnelID, subdomain string, bytesTransferred int64) {
	if tunnelID == "" || subdomain == "" {
		return
	}

	completedAt := time.Now().UTC()
	key := tunnelStatKey{
		tunnelID:    tunnelID,
		subdomain:   subdomain,
		bucketStart: completedAt.Truncate(model.UsageBucketDuration),
	}
	t.statsMu.Lock()
	stat := t.stats[key]
	if stat == nil {
		stat = &tunnelStat{}
		t.stats[key] = stat
	}
	stat.requests++
	if bytesTransferred > 0 {
		stat.bytes += bytesTransferred
	}
	if completedAt.After(stat.lastActive) {
		stat.lastActive = completedAt
	}
	t.statsMu.Unlock()
}

func (t *tcpService) flushLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopFlush:
			return
		case <-ticker.C:
			t.flushStats()
		}
	}
}

// flushStats drains the in-memory counters. Failed deltas retain their original
// bucket and completion time when they are added back for the next flush.
func (t *tcpService) flushStats() {
	t.statsMu.Lock()
	pending := make(map[tunnelStatKey]tunnelStat, len(t.stats))
	for key, stat := range t.stats {
		if stat.requests == 0 && stat.bytes == 0 {
			continue
		}
		pending[key] = *stat
		delete(t.stats, key)
	}
	t.statsMu.Unlock()

	for key, delta := range pending {
		err := t.usage.AddTraffic(key.tunnelID, key.bucketStart, model.UsageDelta{
			Requests:   delta.requests,
			Bytes:      delta.bytes,
			LastActive: delta.lastActive,
		})
		if err == nil {
			continue
		}
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("discarding stale tunnel stats for %s", key.subdomain)
			continue
		}

		log.Printf("failed to persist tunnel stats for %s: %v", key.subdomain, err)
		t.statsMu.Lock()
		stat := t.stats[key]
		if stat == nil {
			stat = &tunnelStat{}
			t.stats[key] = stat
		}
		stat.requests += delta.requests
		stat.bytes += delta.bytes
		if delta.lastActive.After(stat.lastActive) {
			stat.lastActive = delta.lastActive
		}
		t.statsMu.Unlock()
	}
}

// buildForwardRequest reads the incoming request body and creates a fresh outbound
// http.Request suitable for serializing over the tunnel stream with Request.Write.
func buildForwardRequest(in *http.Request, preserveUpgradeHeaders bool) (*http.Request, error) {
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

	// Copy request headers. Upgrade headers are only forwarded when this is an
	// upgrade request; they are invalid on ordinary proxied requests.
	for key, values := range in.Header {
		if isHopByHopHeader(key) && !preserveUpgradeHeaders {
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
	// Mark the request as tunnel-forwarded so a target that is itself a GoPort
	// backend serves it normally instead of re-intercepting it.
	out.Header.Set(tunnelForwardedHeader, "1")
	if contentLength >= 0 {
		out.ContentLength = contentLength
	}
	if !preserveUpgradeHeaders {
		out.Close = true
		out.Header.Set("Connection", "close")
	}

	return out, nil
}

func isUpgradeRequest(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}

// copyTunnelStream relays an upgraded connection in both directions and reports
// how many bytes moved. It returns as soon as either direction ends, so bytes
// still in flight the other way can be missed; the total only feeds aggregate
// usage figures.
func copyTunnelStream(a, b net.Conn) int64 {
	var transferred atomic.Int64

	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(a, b)
		transferred.Add(n)
		_ = closeWrite(a)
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(b, a)
		transferred.Add(n)
		_ = closeWrite(b)
		done <- struct{}{}
	}()
	<-done

	return transferred.Load()
}

func closeWrite(conn net.Conn) error {
	if writer, ok := conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return nil
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
