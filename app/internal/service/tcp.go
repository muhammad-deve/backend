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
	"math"
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
	"gitlab.yurtal.tech/company/pocketbase-app-template/pkg/geoip"
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
	// Resume is the subdomain a reconnecting CLI held before its connection
	// dropped. It is honoured only for a tunnel the same account already owns,
	// so it never grants a name the plan would not allow on a fresh start.
	Resume string `json:"resume,omitempty"`

	// What the CLI reports about itself (0.1.9 and newer). Older CLIs send
	// none of these, so blank means "0.1.8 or older".
	Version         string `json:"version,omitempty"`
	OS              string `json:"os,omitempty"`
	Arch            string `json:"arch,omitempty"`
	Install         string `json:"install,omitempty"`
	ReconnectReason string `json:"reconnect_reason,omitempty"`
}

// registrationTimeout bounds the JSON handshake so a client that connects and
// never sends a request cannot hold a goroutine forever.
const registrationTimeout = 15 * time.Second

// staleSessionProbeTimeout is how long a held session gets to answer a ping
// before a reconnect from the same account replaces it. A var so tests can
// shorten it.
var staleSessionProbeTimeout = 5 * time.Second

// tunnelSessionConfig tolerates slow uplinks. yamux sends pings on the same TCP
// connection as data, so with the default 10s timeout a CLI pushing a large
// response over a slow link missed its keepalive and the whole tunnel dropped.
func tunnelSessionConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = log.Writer()
	return cfg
}

type tunnelRegistrationResponse struct {
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
	Error     string `json:"error,omitempty"`
}

type tcpService struct {
	listener      net.Listener
	addr          string
	mu            sync.Mutex
	conns         map[string]net.Conn
	sessions      map[string]*yamux.Session
	owners        map[string]net.Conn
	tunnelIDs     map[string]string
	sessionUsers  map[string]string
	sessionCustom map[string]bool
	sessionOrder  map[string]uint64
	nextSession   uint64
	app           core.App
	domain        string
	statsMu       sync.Mutex
	stats         map[tunnelStatKey]*tunnelStat
	stopFlush     chan struct{}
	tunnels       repository.TunnelsI
	usage         repository.UsageI
	billing       BillingI
	tokens        TokensI
	quotaMu       sync.Mutex
	quotaCache    map[string]trafficQuotaCacheEntry
	// onlineSince is when each live session's connected time was last
	// counted; pendingOnline holds time from sessions that ended since the
	// last write. Both are guarded by mu.
	onlineSince   map[string]time.Time
	pendingOnline []onlineDelta
	// recordMu serialises read-modify-write saves of tunnel rows with the
	// minutes_used increment, so a save from a stale copy can't undo it.
	recordMu sync.Mutex
	geo      *geoip.DB
}

// onlineDelta is connected time to add to one tunnel and its owner.
type onlineDelta struct {
	tunnelID string
	userID   string
	seconds  float64
}

// onlineAccrualInterval is how often connected time is written. A crash
// loses at most this much; a clean shutdown loses none.
var onlineAccrualInterval = time.Minute

type tunnelStatKey struct {
	tunnelID    string
	subdomain   string
	userID      string
	bucketStart time.Time
}

type tunnelStat struct {
	requests   int64
	bytes      int64
	lastActive time.Time
}

type trafficQuotaCacheEntry struct {
	limits    model.PlanLimits
	usedBytes int64
	expiresAt time.Time
}

func NewTCPService(app core.App, tunnels repository.TunnelsI, usage repository.UsageI, billing BillingI, tokens TokensI) TCPI {
	port := os.Getenv("TCP_PORT")
	if port == "" {
		port = "7000"
	}

	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}

	return &tcpService{
		addr:          ":" + port,
		conns:         make(map[string]net.Conn),
		sessions:      make(map[string]*yamux.Session),
		owners:        make(map[string]net.Conn),
		tunnelIDs:     make(map[string]string),
		sessionUsers:  make(map[string]string),
		sessionCustom: make(map[string]bool),
		sessionOrder:  make(map[string]uint64),
		app:           app,
		domain:        domain,
		stats:         make(map[tunnelStatKey]*tunnelStat),
		stopFlush:     make(chan struct{}),
		tunnels:       tunnels,
		usage:         usage,
		billing:       billing,
		tokens:        tokens,
		quotaCache:    make(map[string]trafficQuotaCacheEntry),
		onlineSince:   make(map[string]time.Time),
		geo:           geoip.Open(""),
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

	now := time.Now()
	t.mu.Lock()
	for subdomain := range t.onlineSince {
		t.endOnlineLocked(subdomain, now)
	}
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
		delete(t.sessionUsers, subdomain)
		delete(t.sessionCustom, subdomain)
		delete(t.sessionOrder, subdomain)
	}
	t.mu.Unlock()

	// Persist any traffic and connected time accumulated since the last flush.
	t.flushStats()
	t.flushOnline(now)

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
			t.endOnlineLocked(subdomain, time.Now())
			delete(t.sessions, subdomain)
			delete(t.owners, subdomain)
			delete(t.tunnelIDs, subdomain)
			delete(t.sessionUsers, subdomain)
			delete(t.sessionCustom, subdomain)
			delete(t.sessionOrder, subdomain)
		}
		t.mu.Unlock()

		// Only the connection that still holds the subdomain may mark it offline.
		// A rejected duplicate registration, or a reconnect that already handed the
		// subdomain to a newer connection, would otherwise flag a tunnel that is
		// still serving traffic as offline in the dashboard. Check again right
		// before the write: a reconnecting CLI may have claimed it meanwhile.
		if ownedSession && !t.isReserved(subdomain) {
			t.setTunnelCurrent(subdomain, false)
		}

		log.Printf("CLI disconnected: %s", remoteAddr)
	}()

	_ = conn.SetDeadline(time.Now().Add(registrationTimeout))

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

	createdSubdomain, tunnelID, userID, maxActiveTunnels, isCustom, err := t.resolveTunnel(req)
	if err != nil {
		log.Printf("failed to register tunnel: %v", err)
		t.sendRegistrationError(conn, fmt.Sprintf("failed to register tunnel: %v", err))
		return
	}
	subdomain = createdSubdomain
	if err := t.reserveSession(subdomain, tunnelID, userID, maxActiveTunnels, isCustom, conn); err != nil {
		t.sendRegistrationError(conn, err.Error())
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

	// The handshake is done; from here yamux keepalives detect a dead link.
	_ = conn.SetDeadline(time.Time{})

	log.Printf("registered %s tunnel %s -> localhost:%s", req.Type, resp.URL, req.Port)

	// Mark the subdomain as actively in use.
	t.setTunnelCurrent(subdomain, true)
	t.recordDevice(req, conn.RemoteAddr().String())

	session, err := yamux.Server(conn, tunnelSessionConfig())
	if err != nil {
		log.Printf("failed to start yamux server: %v", err)
		return
	}
	defer session.Close()

	t.mu.Lock()
	t.sessions[subdomain] = session
	t.onlineSince[subdomain] = time.Now()
	t.mu.Unlock()

	<-session.CloseChan()
}

// deviceField keeps a value the CLI reported short and plain before it is
// stored. Anything outside letters, digits and "._-+" is dropped.
func deviceField(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 {
		value = value[:32]
	}
	for _, r := range value {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-+", r)
		if !ok {
			return ""
		}
	}
	return value
}

// recordDevice stores what the CLI reported about itself on its token row,
// since each token is one device, plus the country its IP belongs to (the IP
// itself is not stored). It is one UPDATE, so tunnels opening at once from one
// device can't lose a reconnect, and blank values from older CLIs keep what
// was already there.
func (t *tcpService) recordDevice(req tunnelRegistrationRequest, remoteAddr string) {
	token := strings.TrimSpace(req.Token)
	if token == "" || t.app == nil {
		return
	}
	reason := deviceField(req.ReconnectReason)
	increment := 0
	if reason != "" {
		increment = 1
	}
	_, err := t.app.DB().NewQuery(`
		UPDATE tokens SET
			client_version  = CASE WHEN {:version} != '' THEN {:version} ELSE client_version END,
			client_os       = CASE WHEN {:os} != '' THEN {:os} ELSE client_os END,
			client_arch     = CASE WHEN {:arch} != '' THEN {:arch} ELSE client_arch END,
			install_method  = CASE WHEN {:install} != '' THEN {:install} ELSE install_method END,
			country         = CASE WHEN {:country} != '' THEN {:country} ELSE country END,
			reconnects      = reconnects + {:increment},
			last_disconnect = CASE WHEN {:reason} != '' THEN {:reason} ELSE last_disconnect END
		WHERE token = {:token}
	`).Bind(dbx.Params{
		"version":   deviceField(req.Version),
		"os":        deviceField(req.OS),
		"arch":      deviceField(req.Arch),
		"install":   deviceField(req.Install),
		"country":   t.geo.Country(remoteAddr),
		"increment": increment,
		"reason":    reason,
		"token":     token,
	}).Execute()
	if err != nil {
		log.Printf("failed to record device details: %v", err)
	}
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
	owner, err := t.tokens.Verify(token)
	if errors.Is(err, ErrInvalidToken) {
		return "", errInvalidToken
	}
	if err != nil {
		return "", err
	}
	return owner.UserID, nil
}

func (t *tcpService) resolveTunnel(req tunnelRegistrationRequest) (string, string, string, int, bool, error) {
	if req.Reset && strings.TrimSpace(req.Subdomain) != "" {
		return "", "", "", 0, false, fmt.Errorf("reset and custom subdomain cannot be used together")
	}

	userID, err := t.resolveUserID(req.Token)
	if err != nil {
		return "", "", "", 0, false, err
	}
	limits, usedBytes, err := t.trafficAllowance(userID)
	if err != nil {
		return "", "", "", 0, false, fmt.Errorf("could not verify your subscription or monthly usage")
	}
	if usedBytes >= limits.MonthlyBytes {
		if limits.IsPro {
			return "", "", "", 0, false, fmt.Errorf("monthly traffic limit reached for Pro (%d GB)", limits.MonthlyBytes/(1024*1024*1024))
		}
		return "", "", "", 0, false, fmt.Errorf("monthly traffic limit reached for Free (%d GB); upgrade to Pro for %d GB", limits.MonthlyBytes/(1024*1024*1024), model.ProMonthlyBytes/(1024*1024*1024))
	}

	// A reconnecting CLI asks for the subdomain it already had, so a dropped
	// connection does not move the public URL. A name that was claimed by someone
	// else meanwhile, or a custom name the plan no longer covers, falls through to
	// the normal flow below.
	if resume := strings.TrimSpace(req.Resume); resume != "" && !req.Reset {
		record, err := t.findTunnelBySubdomain(resume)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", 0, false, err
		}
		if err == nil && record.GetString("user") == userID {
			isCustom := record.GetBool("is_custom")
			if !isCustom || limits.CustomSubdomains {
				subdomain, tunnelID, err := t.saveTunnel(resume, isCustom, userID, req.Port, req.Type)
				return subdomain, tunnelID, userID, limits.MaxActiveTunnels, isCustom, err
			}
		}
	}

	if strings.TrimSpace(req.Subdomain) != "" {
		if !limits.CustomSubdomains {
			return "", "", "", 0, false, fmt.Errorf("%w: custom subdomains are available on Pro; upgrade at https://goport.uz/dashboard#billing", ErrProRequired)
		}
		subdomain, err := normalizeRequestedSubdomain(req.Subdomain, t.domain)
		if err != nil {
			return "", "", "", 0, false, err
		}
		subdomain, tunnelID, err := t.saveTunnel(subdomain, true, userID, req.Port, req.Type)
		return subdomain, tunnelID, userID, limits.MaxActiveTunnels, true, err
	}

	if !req.Reset {
		// Free accounts skip custom names they kept from before custom subdomains
		// moved to Pro. Otherwise a newer custom row hides their generated one and
		// every start hands out a fresh random URL.
		record, err := t.findCurrentTunnel(userID, limits.CustomSubdomains)
		if err == nil {
			subdomain := record.GetString("subdomain")
			isCustom := record.GetBool("is_custom")
			if subdomain != "" && (!isCustom || limits.CustomSubdomains) {
				subdomain, tunnelID, err := t.saveTunnel(subdomain, isCustom, userID, req.Port, req.Type)
				return subdomain, tunnelID, userID, limits.MaxActiveTunnels, isCustom, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", "", "", 0, false, err
		}
	}

	subdomain, err := t.generateUniqueSubdomain()
	if err != nil {
		return "", "", "", 0, false, err
	}
	subdomain, tunnelID, err := t.saveTunnel(subdomain, false, userID, req.Port, req.Type)
	return subdomain, tunnelID, userID, limits.MaxActiveTunnels, false, err
}

func (t *tcpService) saveTunnel(subdomain string, isCustom bool, userID, localPort, protocol string) (string, string, error) {
	// Every way a custom name is handed out (--custom, a reconnect, or a
	// restart reusing the latest tunnel) comes through here, so a word added
	// to blocked_words also stops names that were allowed before.
	if isCustom && containsBlockedWord(subdomain, t.blockedWords()) {
		return "", "", errBlockedSubdomain
	}

	collection, err := t.app.FindCollectionByNameOrId(model.TunnelsCollection)
	if err != nil {
		return "", "", err
	}

	if existing, err := t.findTunnelBySubdomain(subdomain); err == nil {
		// The live session map decides whether a subdomain is in use, not the
		// is_current column, which can be left stale by an unclean shutdown.
		// Checking it before any write also stops a second CLI from rewriting the
		// connection metadata of a tunnel that is still online.
		if t.hasLiveSession(subdomain) && !t.releaseDeadSession(subdomain, userID) {
			return "", "", fmt.Errorf("subdomain %q is currently in use", subdomain)
		}

		owner := existing.GetString("user")
		if owner != "" && owner != userID {
			if err := t.tunnels.DeleteWithLogs(existing.Id); err != nil {
				return "", "", err
			}
		} else {
			t.recordMu.Lock()
			defer t.recordMu.Unlock()
			// Re-read under the lock: the liveness probe above can take seconds,
			// and saving the old copy would undo minutes added meanwhile.
			if fresh, err := t.findTunnelBySubdomain(subdomain); err == nil {
				existing = fresh
			}
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

func (t *tcpService) findCurrentTunnel(userID string, includeCustom bool) (*core.Record, error) {
	filter := "user = {:user}"
	if !includeCustom {
		filter += " && is_custom = false"
	}
	records, err := t.app.FindRecordsByFilter("tunnels", filter, "-updated", 1, 0, dbx.Params{
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
	t.recordMu.Lock()
	defer t.recordMu.Unlock()
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

// isReserved reports whether any connection currently holds the subdomain.
func (t *tcpService) isReserved(subdomain string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.owners[subdomain]
	return ok
}

// releaseDeadSession frees a subdomain whose session the server still holds but
// which no longer answers. That happens when a CLI's link dies without a clean
// close (Wi-Fi switch, sleep, NAT reset) and the CLI reconnects before the
// server's own keepalive notices. Only the same account's session is probed,
// and a session that answers is left alone, so two live CLIs can't evict each
// other. It reports whether the subdomain was freed.
func (t *tcpService) releaseDeadSession(subdomain, userID string) bool {
	t.mu.Lock()
	session := t.sessions[subdomain]
	owner := t.sessionUsers[subdomain]
	t.mu.Unlock()

	if session == nil || session.IsClosed() || owner != userID {
		return false
	}
	if sessionAnswers(session, staleSessionProbeTimeout) {
		return false
	}
	log.Printf("tunnel %s: held session stopped answering; releasing it for a reconnect", subdomain)
	_ = session.Close()
	return true
}

func sessionAnswers(session *yamux.Session, timeout time.Duration) bool {
	result := make(chan error, 1)
	go func() {
		_, err := session.Ping()
		result <- err
	}()
	select {
	case err := <-result:
		return err == nil
	case <-time.After(timeout):
		return false
	}
}

func (t *tcpService) reserveSession(subdomain, tunnelID, userID string, maxActive int, isCustom bool, conn net.Conn) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.sessions[subdomain]; ok {
		if existing == nil || !existing.IsClosed() {
			return fmt.Errorf("subdomain %q is already connected", subdomain)
		}
		t.endOnlineLocked(subdomain, time.Now())
		delete(t.sessions, subdomain)
		delete(t.owners, subdomain)
		delete(t.tunnelIDs, subdomain)
		delete(t.sessionUsers, subdomain)
		delete(t.sessionCustom, subdomain)
		delete(t.sessionOrder, subdomain)
	}

	active := 0
	for activeSubdomain, activeUserID := range t.sessionUsers {
		if activeUserID != userID {
			continue
		}
		if session, ok := t.sessions[activeSubdomain]; ok && (session == nil || !session.IsClosed()) {
			active++
		}
	}
	if active >= maxActive {
		return fmt.Errorf("your plan allows up to %d active tunnels; stop one or upgrade at https://goport.uz/dashboard#billing", maxActive)
	}

	t.sessions[subdomain] = nil
	t.owners[subdomain] = conn
	t.tunnelIDs[subdomain] = tunnelID
	t.sessionUsers[subdomain] = userID
	t.sessionCustom[subdomain] = isCustom
	t.nextSession++
	t.sessionOrder[subdomain] = t.nextSession
	return nil
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
	blocked := t.blockedWords()
	for i := 0; i < 10; i++ {
		subdomain, err := randomSubdomain(6)
		if err != nil {
			return "", err
		}
		// Random letters can spell a blocked word too; just draw again.
		if containsBlockedWord(subdomain, blocked) {
			continue
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
	userID := t.sessionUsers[subdomain]
	isCustom := t.sessionCustom[subdomain]
	t.mu.Unlock()

	if session == nil || session.IsClosed() || tunnelID == "" {
		http.Error(e.Response, "tunnel is not connected", http.StatusBadGateway)
		return true, nil
	}
	limits, usedBytes, err := t.trafficAllowance(userID)
	if err != nil {
		http.Error(e.Response, "could not verify tunnel allowance", http.StatusServiceUnavailable)
		return true, nil
	}
	if isCustom && !limits.CustomSubdomains {
		http.Error(e.Response, "this custom subdomain requires an active GoPort Pro subscription", http.StatusPaymentRequired)
		return true, nil
	}
	if !t.sessionWithinPlanLimit(subdomain, userID, limits.MaxActiveTunnels) {
		http.Error(e.Response, "this tunnel is outside your current plan allowance; stop extra tunnels or upgrade to Pro", http.StatusPaymentRequired)
		return true, nil
	}
	if usedBytes >= limits.MonthlyBytes {
		http.Error(e.Response, "monthly traffic allowance reached", http.StatusTooManyRequests)
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
		t.recordTraffic(tunnelID, subdomain, userID, copyTunnelStream(clientConn, stream))
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
		t.recordTraffic(tunnelID, subdomain, userID, written+maxInt64(outReq.ContentLength, 0))
		return true, nil
	}
	t.recordTraffic(tunnelID, subdomain, userID, written+maxInt64(outReq.ContentLength, 0))
	return true, nil
}

func (t *tcpService) sessionWithinPlanLimit(subdomain, userID string, maxActive int) bool {
	if maxActive <= 0 {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	targetOrder, ok := t.sessionOrder[subdomain]
	if !ok || t.sessionUsers[subdomain] != userID {
		return false
	}

	earlierActive := 0
	for activeSubdomain, activeUserID := range t.sessionUsers {
		if activeUserID != userID || t.sessionOrder[activeSubdomain] >= targetOrder {
			continue
		}
		session, exists := t.sessions[activeSubdomain]
		if exists && (session == nil || !session.IsClosed()) {
			earlierActive++
		}
	}
	return earlierActive < maxActive
}

func (t *tcpService) trafficAllowance(userID string) (model.PlanLimits, int64, error) {
	now := time.Now().UTC()
	t.quotaMu.Lock()
	entry, ok := t.quotaCache[userID]
	if ok && now.Before(entry.expiresAt) {
		t.quotaMu.Unlock()
		return entry.limits, entry.usedBytes, nil
	}
	t.quotaMu.Unlock()

	limits, err := t.billing.Plan(userID)
	if err != nil {
		return model.PlanLimits{}, 0, err
	}
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	usedBytes, err := t.usage.BytesForPeriod(userID, periodStart, now.Add(time.Second))
	if err != nil {
		return model.PlanLimits{}, 0, err
	}
	usedBytes += t.pendingBytesForUser(userID, periodStart)

	t.quotaMu.Lock()
	t.quotaCache[userID] = trafficQuotaCacheEntry{
		limits: limits, usedBytes: usedBytes, expiresAt: now.Add(5 * time.Second),
	}
	t.quotaMu.Unlock()
	return limits, usedBytes, nil
}

func (t *tcpService) pendingBytesForUser(userID string, from time.Time) int64 {
	t.statsMu.Lock()
	defer t.statsMu.Unlock()
	var total int64
	for key, stat := range t.stats {
		if key.userID == userID && !key.bucketStart.Before(from) {
			total += stat.bytes
		}
	}
	return total
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// recordTraffic keeps the completion timestamp with each delta so retries and
// flushes around a bucket boundary cannot move traffic into the wrong period.
func (t *tcpService) recordTraffic(tunnelID, subdomain, userID string, bytesTransferred int64) {
	if tunnelID == "" || subdomain == "" || userID == "" {
		return
	}

	completedAt := time.Now().UTC()
	key := tunnelStatKey{
		tunnelID:    tunnelID,
		subdomain:   subdomain,
		userID:      userID,
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

	if bytesTransferred > 0 {
		t.quotaMu.Lock()
		entry, ok := t.quotaCache[userID]
		if ok {
			entry.usedBytes += bytesTransferred
			t.quotaCache[userID] = entry
		}
		t.quotaMu.Unlock()
	}
}

func (t *tcpService) flushLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	online := time.NewTicker(onlineAccrualInterval)
	defer online.Stop()
	for {
		select {
		case <-t.stopFlush:
			return
		case <-ticker.C:
			t.flushStats()
		case now := <-online.C:
			t.flushOnline(now)
		}
	}
}

// endOnlineLocked queues the connected time a session built up since it was
// last counted and stops its clock. It must run while mu is held and before
// the session's tunnelIDs and sessionUsers entries are removed.
func (t *tcpService) endOnlineLocked(subdomain string, now time.Time) {
	since, ok := t.onlineSince[subdomain]
	if !ok {
		return
	}
	delete(t.onlineSince, subdomain)
	t.pendingOnline = append(t.pendingOnline, onlineDelta{
		tunnelID: t.tunnelIDs[subdomain],
		userID:   t.sessionUsers[subdomain],
		seconds:  now.Sub(since).Seconds(),
	})
}

// collectOnline returns the connected time of every live session since it was
// last counted, plus the time of sessions that ended, and restarts the clocks.
func (t *tcpService) collectOnline(now time.Time) []onlineDelta {
	t.mu.Lock()
	defer t.mu.Unlock()

	deltas := t.pendingOnline
	t.pendingOnline = nil
	for subdomain, since := range t.onlineSince {
		deltas = append(deltas, onlineDelta{
			tunnelID: t.tunnelIDs[subdomain],
			userID:   t.sessionUsers[subdomain],
			seconds:  now.Sub(since).Seconds(),
		})
		t.onlineSince[subdomain] = now
	}
	return deltas
}

// sumOnlineMinutes totals the deltas per tunnel and per user, in minutes
// rounded to two decimals. Several sessions of one account add up.
func sumOnlineMinutes(deltas []onlineDelta) (tunnels, users map[string]float64) {
	tunnelSeconds, userSeconds := map[string]float64{}, map[string]float64{}
	for _, d := range deltas {
		if d.seconds <= 0 {
			continue
		}
		if d.tunnelID != "" {
			tunnelSeconds[d.tunnelID] += d.seconds
		}
		if d.userID != "" {
			userSeconds[d.userID] += d.seconds
		}
	}
	toMinutes := func(seconds map[string]float64) map[string]float64 {
		minutes := make(map[string]float64, len(seconds))
		for id, s := range seconds {
			if m := math.Round(s/60*100) / 100; m > 0 {
				minutes[id] = m
			}
		}
		return minutes
	}
	return toMinutes(tunnelSeconds), toMinutes(userSeconds)
}

// flushOnline adds connected time to tunnels.minutes_used and
// users.minutes_used. The increments run in SQL so they never read a stale
// value, and they leave the tunnel's `updated` alone, which findCurrentTunnel
// sorts by. Failed writes are queued again for the next tick.
func (t *tcpService) flushOnline(now time.Time) {
	deltas := t.collectOnline(now)
	if len(deltas) == 0 {
		return
	}
	tunnels, users := sumOnlineMinutes(deltas)

	var retry []onlineDelta
	for id, minutes := range tunnels {
		t.recordMu.Lock()
		_, err := t.app.DB().NewQuery("UPDATE tunnels SET minutes_used = minutes_used + {:minutes} WHERE id = {:id}").
			Bind(dbx.Params{"minutes": minutes, "id": id}).Execute()
		t.recordMu.Unlock()
		if err != nil {
			log.Printf("failed to add %.2f minutes to tunnel %s: %v", minutes, id, err)
			retry = append(retry, onlineDelta{tunnelID: id, seconds: minutes * 60})
		}
	}
	for id, minutes := range users {
		_, err := t.app.DB().NewQuery("UPDATE users SET minutes_used = minutes_used + {:minutes} WHERE id = {:id}").
			Bind(dbx.Params{"minutes": minutes, "id": id}).Execute()
		if err != nil {
			log.Printf("failed to add %.2f minutes to user %s: %v", minutes, id, err)
			retry = append(retry, onlineDelta{userID: id, seconds: minutes * 60})
		}
	}
	if len(retry) > 0 {
		t.mu.Lock()
		t.pendingOnline = append(t.pendingOnline, retry...)
		t.mu.Unlock()
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
