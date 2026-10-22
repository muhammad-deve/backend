package service

import (
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
	"time"

	"github.com/hashicorp/yamux"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
	"gitlab.yurtal.tech/company/pocketbase-app-template/pkg/geoip"
)

type TCPI interface {
	Start() error
	Stop()
	// ServeTunnel serves a request for a tunnel host and reports whether it
	// did; requests for any other host are left to the caller.
	ServeTunnel(w http.ResponseWriter, r *http.Request) bool
	IsTunnelHost(host string) bool
	StopTunnel(userID, subdomain string) error
	DeleteTunnel(userID, subdomain string) error
	// CloseSessionsForToken ends the tunnels opened with a token (by row id)
	// and returns how many it closed.
	CloseSessionsForToken(tokenID string) int
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
	ShareToken      string `json:"share_token,omitempty"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
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
	// A stream the server closed is reset if the CLI hasn't closed its end a
	// minute later, instead of holding its buffer for the default 5 minutes.
	cfg.StreamCloseTimeout = time.Minute
	cfg.LogOutput = log.Writer()
	return cfg
}

// maxCLIOpenedStreams is how many streams a CLI may open before its session is
// closed. Released CLIs never open one; the server opens a stream per request.
const maxCLIOpenedStreams = 16

// refuseCLIStreams accepts and closes streams the CLI opens, so they can't pile
// up in yamux's accept backlog, and ends a session that keeps opening them.
func refuseCLIStreams(session *yamux.Session, subdomain string) {
	defer recoverPanic("tunnel stream refusal")
	for opened := 1; ; opened++ {
		stream, err := session.Accept()
		if err != nil {
			return
		}
		_ = stream.Close()
		if opened >= maxCLIOpenedStreams {
			log.Printf("tunnel %s: CLI opened %d streams of its own; closing the session", subdomain, opened)
			_ = session.Close()
			return
		}
	}
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
	sessionTypes  map[string]string
	shareTokens   map[string]string
	shareExpires  map[string]time.Time
	sessionOrder  map[string]uint64
	// sessionTokens is the token row id each session registered with.
	sessionTokens map[string]string
	// stopped holds subdomains stopped from the dashboard (key stopKey) until
	// their hold ends; revokedTokens holds tokens deleted moments ago.
	stopped       map[string]time.Time
	revokedTokens map[string]time.Time
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
	// claimMu makes finding, deciding on and saving a subdomain one step.
	claimMu sync.Mutex
	// randomName draws a generated subdomain; tests replace it.
	randomName func() (string, error)
	geo        *geoip.DB
	// handshakes caps connections that haven't finished registering.
	handshakes *handshakeLimiter
	// Registrations reaching the database, per IP and per token.
	registrationsByIP    *otpRateLimiter
	registrationsByToken *otpRateLimiter
	// hopSecret is TUNNEL_HOP_SECRET; see tunnelHopHeader.
	hopSecret string
	proxy     proxyLimits
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
		addr:                 ":" + port,
		conns:                make(map[string]net.Conn),
		sessions:             make(map[string]*yamux.Session),
		owners:               make(map[string]net.Conn),
		tunnelIDs:            make(map[string]string),
		sessionUsers:         make(map[string]string),
		sessionCustom:        make(map[string]bool),
		sessionTypes:         make(map[string]string),
		shareTokens:          make(map[string]string),
		shareExpires:         make(map[string]time.Time),
		sessionOrder:         make(map[string]uint64),
		sessionTokens:        make(map[string]string),
		stopped:              make(map[string]time.Time),
		revokedTokens:        make(map[string]time.Time),
		app:                  app,
		domain:               domain,
		stats:                make(map[tunnelStatKey]*tunnelStat),
		stopFlush:            make(chan struct{}),
		tunnels:              tunnels,
		usage:                usage,
		billing:              billing,
		tokens:               tokens,
		quotaCache:           make(map[string]trafficQuotaCacheEntry),
		onlineSince:          make(map[string]time.Time),
		geo:                  geoip.Open(""),
		handshakes:           newHandshakeLimiter(maxPendingHandshakes, maxPendingHandshakesByIP),
		registrationsByIP:    newOTPRateLimiter(time.Minute, maxRegistrationsPerIP, 0),
		registrationsByToken: newOTPRateLimiter(time.Minute, maxRegistrationsPerToken, 0),
		hopSecret:            strings.TrimSpace(os.Getenv("TUNNEL_HOP_SECRET")),
		proxy:                defaultProxyLimits(),
		randomName:           func() (string, error) { return randomSubdomain(6) },
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
		t.forgetSessionLocked(subdomain)
	}
	t.mu.Unlock()

	// Persist any traffic and connected time accumulated since the last flush.
	t.flushStats()
	t.flushOnline(now)

	log.Printf("TCP tunnel listener stopped")
}

// acceptLoop runs until the listener is closed. Other Accept errors (for
// example too many open files) are logged and retried after a short pause, so
// one bad moment can't stop new tunnels until the next restart.
func (t *tcpService) acceptLoop() {
	var delay time.Duration
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			delay = acceptBackoff(delay)
			log.Printf("TCP accept error: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0

		remoteAddr := conn.RemoteAddr().String()
		ip := remoteIP(conn.RemoteAddr())
		if !t.handshakes.begin(ip) {
			log.Printf("refusing CLI connection from %s: too many pending handshakes", remoteAddr)
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			t.sendRegistrationError(conn, "the server is busy, try again in a moment")
			conn.Close()
			continue
		}
		log.Printf("new CLI connection from %s", remoteAddr)

		t.mu.Lock()
		t.conns[remoteAddr] = conn
		t.mu.Unlock()

		go t.handleConnection(conn, ip)
	}
}

func (t *tcpService) handleConnection(conn net.Conn, ip string) {
	var subdomain string
	var sessionReserved bool

	// Registered first so it runs last, after the cleanup below.
	defer recoverPanic("tunnel connection")

	// The handshake slot is freed once registration is answered, or when the
	// connection ends, whichever comes first.
	var handshakeDone sync.Once
	endHandshake := func() { handshakeDone.Do(func() { t.handshakes.end(ip) }) }
	defer endHandshake()

	defer func() {
		remoteAddr := conn.RemoteAddr().String()
		conn.Close()

		t.mu.Lock()
		delete(t.conns, remoteAddr)
		ownedSession := sessionReserved && subdomain != "" && t.owners[subdomain] == conn
		if ownedSession {
			t.endOnlineLocked(subdomain, time.Now())
			t.forgetSessionLocked(subdomain)
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
	limited := &io.LimitedReader{R: conn, N: maxRegistrationBytes}
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		if limited.N <= 0 {
			log.Printf("tunnel registration from %s is larger than %d bytes", conn.RemoteAddr(), maxRegistrationBytes)
			t.sendRegistrationError(conn, "registration request is too large")
			return
		}
		log.Printf("failed to read tunnel registration: %v", err)
		return
	}
	if req.Port == "" {
		log.Printf("tunnel registration missing port")
		t.sendRegistrationError(conn, "missing port")
		return
	}
	req.Type = normalizedProtocol(req.Type)
	if req.Type != "http" && req.Type != "tcp" && req.Type != "share" {
		t.sendRegistrationError(conn, "unsupported tunnel type")
		return
	}
	if req.Type == "share" {
		if len(req.ShareToken) < 24 || strings.ContainsAny(req.ShareToken, "/\\") {
			t.sendRegistrationError(conn, "invalid share token")
			return
		}
		if req.ExpiresAt > 0 && req.ExpiresAt <= time.Now().Unix() {
			t.sendRegistrationError(conn, "share expiration must be in the future")
			return
		}
	}

	// Every registration below costs database work.
	if allowed, _ := t.registrationsByIP.Allow(ip); !allowed {
		log.Printf("too many tunnel registrations from %s", conn.RemoteAddr())
		t.sendRegistrationError(conn, tooManyRegistrationsMessage)
		return
	}
	if allowed, _ := t.registrationsByToken.Allow(tokenKey(req.Token)); !allowed {
		log.Printf("too many tunnel registrations with one token from %s", conn.RemoteAddr())
		t.sendRegistrationError(conn, tooManyRegistrationsMessage)
		return
	}

	claim, err := t.resolveTunnel(req)
	if err != nil {
		var refused *refusal
		if errors.As(err, &refused) {
			log.Printf("tunnel registration from %s refused: %s", conn.RemoteAddr(), refused.text)
			t.sendRegistrationError(conn, refused.text)
			return
		}
		// Database and other internal errors stay in the log.
		log.Printf("failed to register tunnel: %v", err)
		t.sendRegistrationError(conn, internalRegistrationError)
		return
	}
	subdomain = claim.subdomain
	if err := t.reserveSession(claim, req, conn); err != nil {
		t.sendRegistrationError(conn, err.Error())
		return
	}
	sessionReserved = true

	resp := tunnelRegistrationResponse{
		Subdomain: subdomain,
		URL:       fmt.Sprintf("https://%s.%s", subdomain, t.domain),
	}
	if req.Type == "share" {
		resp.URL += "/s/" + req.ShareToken
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("failed to send tunnel registration response: %v", err)
		return
	}

	// The handshake is done; from here yamux keepalives detect a dead link.
	_ = conn.SetDeadline(time.Time{})
	endHandshake()

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
	go refuseCLIStreams(session, subdomain)

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

// refusal is a registration error whose text is sent to the CLI as is. Any
// other error is logged and the CLI gets internalRegistrationError. CLIs stop
// retrying only when the text contains one of their permanent phrases
// ("authentication required", "subdomain cannot", ...), so refusal texts
// must keep them.
type refusal struct {
	text string
	err  error
}

func (r *refusal) Error() string { return r.text }
func (r *refusal) Unwrap() error { return r.err }

// refuse sends err's text with the prefix registration refusals have always
// had.
func refuse(err error) error {
	return &refusal{text: "failed to register tunnel: " + err.Error(), err: err}
}

// refuseWith sends text exactly.
func refuseWith(text string) error {
	return &refusal{text: text}
}

// internalRegistrationError has no permanent phrase, so CLIs retry.
const internalRegistrationError = "failed to register tunnel: internal server error, try again"

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
		return "", refuse(errInvalidToken)
	}
	owner, err := t.tokens.Verify(token)
	if errors.Is(err, ErrInvalidToken) {
		return "", refuse(errInvalidToken)
	}
	if errors.Is(err, ErrProRequired) {
		// A token beyond the plan's device allowance.
		return "", refuse(err)
	}
	if err != nil {
		return "", err
	}
	return owner.UserID, nil
}

// tunnelClaim is a registration that passed resolveTunnel.
type tunnelClaim struct {
	subdomain string
	tunnelID  string
	userID    string
	// tokenID is the token's row id, for CloseSessionsForToken.
	tokenID   string
	maxActive int
	// needsPro is true for a custom name the plan must cover.
	needsPro bool
}

func (t *tcpService) resolveTunnel(req tunnelRegistrationRequest) (tunnelClaim, error) {
	if req.Reset && strings.TrimSpace(req.Subdomain) != "" {
		return tunnelClaim{}, refuse(fmt.Errorf("reset and custom subdomain cannot be used together"))
	}

	userID, err := t.resolveUserID(req.Token)
	if err != nil {
		return tunnelClaim{}, err
	}
	tokenID := t.tokenID(strings.TrimSpace(req.Token))
	limits, usedBytes, err := t.trafficAllowance(userID)
	if err != nil {
		log.Printf("could not load the plan or usage of %s: %v", userID, err)
		return tunnelClaim{}, refuse(fmt.Errorf("could not verify your subscription or monthly usage"))
	}
	if usedBytes >= limits.MonthlyBytes {
		if limits.IsPro {
			return tunnelClaim{}, refuse(fmt.Errorf("monthly traffic limit reached for Pro (%d GB)", limits.MonthlyBytes/(1024*1024*1024)))
		}
		return tunnelClaim{}, refuse(fmt.Errorf("monthly traffic limit reached for Free (%d GB); upgrade to Pro for %d GB", limits.MonthlyBytes/(1024*1024*1024), model.ProMonthlyBytes/(1024*1024*1024)))
	}
	claimed := func(subdomain, tunnelID string, needsPro bool, err error) (tunnelClaim, error) {
		if err != nil {
			return tunnelClaim{}, err
		}
		return tunnelClaim{subdomain, tunnelID, userID, tokenID, limits.MaxActiveTunnels, needsPro}, nil
	}
	// Checked before any row is written; reserveSession checks again under
	// the lock. A held session of the same name is replaced, so it doesn't
	// count.
	full := func(subdomain string) error {
		if t.activeSessions(userID, subdomain) >= limits.MaxActiveTunnels {
			return refuseWith(planLimitMessage(limits.MaxActiveTunnels))
		}
		return nil
	}

	if req.Type == "share" && strings.TrimSpace(req.Resume) == "" {
		if err := full(""); err != nil {
			return tunnelClaim{}, err
		}
		subdomain, tunnelID, err := t.createRandomTunnel(userID, req.Port, req.Type)
		return claimed(subdomain, tunnelID, false, err)
	}

	// A reconnecting CLI asks for the subdomain it already had, so a dropped
	// connection does not move the public URL. A name that was claimed by someone
	// else meanwhile, a custom name the plan no longer covers, or a saved name
	// that breaks the naming rules falls through to the normal flow below.
	if resume := strings.TrimSpace(req.Resume); resume != "" && !req.Reset {
		record, err := t.findTunnelBySubdomain(resume)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return tunnelClaim{}, err
		}
		if err == nil && record.GetString("user") == userID && validateLegacySubdomain(resume) == nil {
			isCustom := record.GetBool("is_custom")
			if !isCustom || limits.CustomSubdomains || isGrandfathered(record) {
				if err := full(resume); err != nil {
					return tunnelClaim{}, err
				}
				subdomain, tunnelID, err := t.saveTunnel(resume, isCustom, userID, req.Port, req.Type)
				return claimed(subdomain, tunnelID, needsPro(record), err)
			}
		}
	}

	if strings.TrimSpace(req.Subdomain) != "" {
		subdomain := cleanRequestedSubdomain(req.Subdomain, t.domain)
		if err := validateSubdomain(subdomain); err != nil {
			// A name the account already holds keeps working after the rules
			// for new names got stricter.
			if validateLegacySubdomain(subdomain) != nil || !t.ownsName(subdomain, userID) {
				return tunnelClaim{}, refuse(err)
			}
		}
		owned := t.ownsGrandfatheredName(subdomain, userID)
		if !limits.CustomSubdomains && !owned {
			return tunnelClaim{}, refuse(fmt.Errorf("%w: custom subdomains are available on Pro; upgrade at https://goport.uz/dashboard#billing", ErrProRequired))
		}
		if err := full(subdomain); err != nil {
			return tunnelClaim{}, err
		}
		subdomain, tunnelID, err := t.saveTunnel(subdomain, true, userID, req.Port, req.Type)
		return claimed(subdomain, tunnelID, !owned, err)
	}

	if !req.Reset {
		// Free accounts skip custom names they claimed after custom subdomains
		// moved to Pro. Otherwise a newer custom row hides their generated one and
		// every start hands out a fresh random URL.
		record, err := t.findCurrentTunnel(userID, limits.CustomSubdomains)
		if err == nil {
			subdomain := record.GetString("subdomain")
			isCustom := record.GetBool("is_custom")
			// A saved name that breaks the naming rules (rows once written
			// straight through the REST API) gets a fresh random name instead.
			if validateLegacySubdomain(subdomain) == nil && (!isCustom || limits.CustomSubdomains || isGrandfathered(record)) {
				if err := full(subdomain); err != nil {
					return tunnelClaim{}, err
				}
				subdomain, tunnelID, err := t.saveTunnel(subdomain, isCustom, userID, req.Port, req.Type)
				return claimed(subdomain, tunnelID, needsPro(record), err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return tunnelClaim{}, err
		}
	}

	if err := full(""); err != nil {
		return tunnelClaim{}, err
	}
	subdomain, tunnelID, err := t.createRandomTunnel(userID, req.Port, req.Type)
	return claimed(subdomain, tunnelID, false, err)
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
	liveOwner := t.sessionUsers[subdomain]
	// The row says who owns the name; the session map says who is connected
	// under it now. Only a session of the same account can be stopped.
	if liveOwner != userID || session == nil || session.IsClosed() {
		t.mu.Unlock()
		return ErrTunnelInactive
	}
	// Without this the CLI resumes the tunnel within seconds.
	t.markStoppedLocked(userID, subdomain, time.Now())
	t.mu.Unlock()

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

	// isReserved also covers a registration still completing its handshake.
	if record.IsCurrent || t.isReserved(subdomain) {
		return ErrTunnelActive
	}

	return t.tunnels.DeleteWithLogs(record.ID)
}

// customProOnlySince is when custom subdomains became a Pro feature (billing
// launched 2026-09-12). A custom name its owner created before then stays
// theirs on any plan; new custom names need Pro.
var customProOnlySince = time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)

// isGrandfathered reports whether a tunnel row is a custom name created
// before custom subdomains became Pro-only.
func isGrandfathered(record *core.Record) bool {
	return record.GetBool("is_custom") && record.GetDateTime("created").Time().Before(customProOnlySince)
}

// needsPro reports whether traffic on this tunnel requires an active Pro plan.
func needsPro(record *core.Record) bool {
	return record.GetBool("is_custom") && !isGrandfathered(record)
}

// ownsGrandfatheredName reports whether userID owns subdomain as a custom
// name from before custom subdomains became Pro-only.
func (t *tcpService) ownsGrandfatheredName(subdomain, userID string) bool {
	record, err := t.findTunnelBySubdomain(subdomain)
	return err == nil && record.GetString("user") == userID && isGrandfathered(record)
}

func (t *tcpService) findCurrentTunnel(userID string, includeCustom bool) (*core.Record, error) {
	filter := "user = {:user}"
	if !includeCustom {
		// Grandfathered custom names still count as the account's own.
		filter += " && (is_custom = false || created < {:proOnlySince})"
	}
	records, err := t.app.FindRecordsByFilter("tunnels", filter, "-updated", 1, 0, dbx.Params{
		"user":         userID,
		"proOnlySince": customProOnlySince.Format(types.DefaultDateLayout),
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

func (t *tcpService) reserveSession(claim tunnelClaim, req tunnelRegistrationRequest, conn net.Conn) error {
	subdomain, userID, maxActive := claim.subdomain, claim.userID, claim.maxActive
	t.mu.Lock()
	defer t.mu.Unlock()

	// The token was deleted while this registration was verifying it.
	if claim.tokenID != "" && t.tokenRevokedLocked(claim.tokenID) {
		return refuse(errInvalidToken)
	}

	if existing, ok := t.sessions[subdomain]; ok {
		if existing == nil || !existing.IsClosed() {
			return fmt.Errorf("subdomain %q is already connected", subdomain)
		}
		t.endOnlineLocked(subdomain, time.Now())
		t.forgetSessionLocked(subdomain)
	}

	if t.activeSessionsLocked(userID, "") >= maxActive {
		return refuseWith(planLimitMessage(maxActive))
	}

	t.sessions[subdomain] = nil
	t.owners[subdomain] = conn
	t.tunnelIDs[subdomain] = claim.tunnelID
	t.sessionUsers[subdomain] = userID
	t.sessionTokens[subdomain] = claim.tokenID
	// sessionCustom means "needs Pro": custom names created before custom
	// subdomains became Pro-only are false so their traffic flows on any
	// plan.
	t.sessionCustom[subdomain] = claim.needsPro
	t.sessionTypes[subdomain] = req.Type
	if req.Type == "share" {
		t.shareTokens[subdomain] = req.ShareToken
		if req.ExpiresAt > 0 {
			t.shareExpires[subdomain] = time.Unix(req.ExpiresAt, 0)
		}
	}
	t.nextSession++
	t.sessionOrder[subdomain] = t.nextSession
	return nil
}

// activeSessions counts userID's live and still-registering sessions,
// leaving out except.
func (t *tcpService) activeSessions(userID, except string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeSessionsLocked(userID, except)
}

func (t *tcpService) activeSessionsLocked(userID, except string) int {
	active := 0
	for subdomain, owner := range t.sessionUsers {
		if owner != userID || subdomain == except {
			continue
		}
		if session, ok := t.sessions[subdomain]; ok && (session == nil || !session.IsClosed()) {
			active++
		}
	}
	return active
}

// planLimitMessage has no permanent phrase on purpose: the CLI keeps retrying
// and connects once the user stops another tunnel.
func planLimitMessage(maxActive int) string {
	return fmt.Sprintf("your plan allows up to %d active tunnels; stop one or upgrade at https://goport.uz/dashboard#billing", maxActive)
}

// forgetSessionLocked drops every entry of a session. Must hold t.mu.
func (t *tcpService) forgetSessionLocked(subdomain string) {
	delete(t.sessions, subdomain)
	delete(t.owners, subdomain)
	delete(t.tunnelIDs, subdomain)
	delete(t.sessionUsers, subdomain)
	delete(t.sessionTokens, subdomain)
	delete(t.sessionCustom, subdomain)
	delete(t.sessionTypes, subdomain)
	delete(t.shareTokens, subdomain)
	delete(t.shareExpires, subdomain)
	delete(t.sessionOrder, subdomain)
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

// recordTraffic counts one finished request and the bytes it moved.
func (t *tcpService) recordTraffic(tunnelID, subdomain, userID string, bytesTransferred int64) {
	t.addTraffic(tunnelID, subdomain, userID, bytesTransferred, 1)
}

// addTraffic keeps the completion timestamp with each delta so retries and
// flushes around a bucket boundary cannot move traffic into the wrong period.
// Long transfers add their bytes as they go, with requests set to 0.
func (t *tcpService) addTraffic(tunnelID, subdomain, userID string, bytesTransferred, requests int64) {
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
	stat.requests += requests
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
			t.safely("stats flush", t.flushStats)
		case now := <-online.C:
			t.safely("online time flush", func() { t.flushOnline(now) })
		}
	}
}

// safely runs fn and logs a panic instead of ending the caller's loop.
func (t *tcpService) safely(where string, fn func()) {
	defer recoverPanic(where)
	fn()
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
