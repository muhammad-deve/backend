package service

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// tunnelHopHeader lets a request that already came out of a tunnel skip tunnel
// routing. It exists for exposing a GoPort backend through a tunnel (for
// testing): the public Host is kept end to end, so without it that backend
// would try to route the request to a tunnel of its own and fail with "tunnel
// is not connected". Visitors can send any header, so it is honoured only when
// its value equals TUNNEL_HOP_SECRET, and it is never forwarded to a CLI.
const tunnelHopHeader = "X-Goport-Tunnel"

// proxyLimits bounds tunneled HTTP. Each service has its own copy so tests
// can shrink them.
type proxyLimits struct {
	// maxRequestBody matches nginx's client_max_body_size.
	maxRequestBody int64
	// maxResponseHead caps the status line and headers a CLI sends.
	maxResponseHead int64
	// responseTimeout is how long the CLI gets to start its response.
	responseTimeout time.Duration
	// idleTimeout is the longest pause allowed while a body is moving. It
	// restarts on every read and write, so a long download or an SSE stream
	// that keeps sending is never cut.
	idleTimeout time.Duration
	// upgradeIdleTimeout ends a WebSocket after this long with no bytes
	// either way, the same as nginx's proxy_read_timeout for tunnel hosts.
	upgradeIdleTimeout time.Duration
	// A response or WebSocket that is still moving records its traffic and
	// checks the monthly quota every quotaCheckBytes or quotaCheckInterval.
	quotaCheckBytes    int64
	quotaCheckInterval time.Duration
}

func defaultProxyLimits() proxyLimits {
	return proxyLimits{
		maxRequestBody:     50 << 20,
		maxResponseHead:    64 << 10,
		responseTimeout:    5 * time.Minute,
		idleTimeout:        10 * time.Minute,
		upgradeIdleTimeout: time.Hour,
		quotaCheckBytes:    4 << 20,
		quotaCheckInterval: 30 * time.Second,
	}
}

var (
	errBodyTooLarge         = errors.New("request body too large")
	errResponseHeadTooLarge = errors.New("tunnel response headers are too large")
	errStreamAborted        = errors.New("tunnel stream aborted")
)

// ServeTunnel serves r when its Host is a tunnel subdomain and reports whether
// it did. It runs before PocketBase's router, so PocketBase's middlewares
// (CORS, security headers, body limit, request log) never touch tunnel
// traffic and visitors can't reach the PocketBase API on a tunnel host.
func (t *tcpService) ServeTunnel(w http.ResponseWriter, r *http.Request) bool {
	if hop := r.Header.Get(tunnelHopHeader); hop != "" {
		if t.hopSecret != "" && subtle.ConstantTimeCompare([]byte(hop), []byte(t.hopSecret)) == 1 {
			return false
		}
	}
	subdomain, ok := t.subdomainFromHost(r.Host)
	if !ok {
		return false
	}
	r.Header.Del(tunnelHopHeader)
	t.serveTunnel(w, r, subdomain)
	return true
}

func (t *tcpService) serveTunnel(w http.ResponseWriter, r *http.Request, subdomain string) {
	t.mu.Lock()
	session := t.sessions[subdomain]
	tunnelID := t.tunnelIDs[subdomain]
	userID := t.sessionUsers[subdomain]
	isCustom := t.sessionCustom[subdomain]
	sessionType := t.sessionTypes[subdomain]
	shareToken := t.shareTokens[subdomain]
	shareExpiry := t.shareExpires[subdomain]
	t.mu.Unlock()
	// Only share tunnels are gated to their /s/<token> path; an ordinary
	// tunnel forwards /s/ like any other path.
	if sessionType == "share" && !sharePathMatches(r.URL.Path, shareToken) {
		http.NotFound(w, r)
		return
	}
	if !shareExpiry.IsZero() && !time.Now().Before(shareExpiry) {
		http.Error(w, "This share has expired.", http.StatusGone)
		return
	}

	if session == nil || session.IsClosed() || tunnelID == "" {
		http.Error(w, "tunnel is not connected", http.StatusBadGateway)
		return
	}
	limits, usedBytes, err := t.trafficAllowance(userID)
	if err != nil {
		http.Error(w, "could not verify tunnel allowance", http.StatusServiceUnavailable)
		return
	}
	if isCustom && !limits.CustomSubdomains {
		http.Error(w, "this custom subdomain requires an active GoPort Pro subscription", http.StatusPaymentRequired)
		return
	}
	if !t.sessionWithinPlanLimit(subdomain, userID, limits.MaxActiveTunnels) {
		http.Error(w, "this tunnel is outside your current plan allowance; stop extra tunnels or upgrade to Pro", http.StatusPaymentRequired)
		return
	}
	if usedBytes >= limits.MonthlyBytes {
		http.Error(w, "monthly traffic allowance reached", http.StatusTooManyRequests)
		return
	}
	if r.ContentLength > t.proxy.maxRequestBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	stream, err := session.Open()
	if err != nil {
		log.Printf("tunnel %s: failed to open stream: %v", subdomain, err)
		http.Error(w, "failed to open tunnel stream", http.StatusBadGateway)
		return
	}
	x := &tunnelExchange{
		t:         t,
		w:         w,
		r:         r,
		rc:        http.NewResponseController(w),
		limits:    t.proxy,
		stream:    newTunnelStream(stream, t.proxy.idleTimeout),
		meter:     t.newMeter(tunnelID, subdomain, userID),
		subdomain: subdomain,
	}
	defer x.meter.finish()
	defer x.stream.Close()
	// A visitor who goes away ends the exchange at once instead of leaving
	// the stream and this goroutine waiting on the CLI.
	stopWatching := context.AfterFunc(r.Context(), x.stream.abort)
	defer stopWatching()

	// Upgrade requests must keep their Connection and Upgrade headers all the
	// way to the local application. Next.js uses this for the dev HMR socket.
	if isUpgradeRequest(r) {
		x.serveUpgrade()
		return
	}
	x.serveHTTP()
}

// tunnelExchange is one visitor request relayed over one yamux stream.
type tunnelExchange struct {
	t         *tcpService
	w         http.ResponseWriter
	r         *http.Request
	rc        *http.ResponseController
	limits    proxyLimits
	stream    *tunnelStream
	meter     *trafficMeter
	subdomain string
}

func (x *tunnelExchange) serveHTTP() {
	outReq, body := buildForwardRequest(x.r, false, x.limits.maxRequestBody)

	// PocketBase's server sets absolute read and write deadlines (5 min). The
	// read one would also cancel r.Context() in the middle of a long SSE
	// response, so tunnel traffic uses idle deadlines instead: per body read
	// here, and per response write in writeResponse.
	if body != nil {
		body.onRead = x.extendReadDeadline
		// Runs after the writer below has finished, also when the exchange
		// fails.
		defer func() { x.meter.add(body.read.Load()) }()
	} else {
		_ = x.rc.SetReadDeadline(time.Time{})
	}

	// The request is sent while the response is read, so an app that answers
	// before reading the whole body (its own 413, say) still works.
	writeDone := make(chan error, 1)
	go func() {
		defer recoverPanic("tunnel request writer")
		err := outReq.Write(x.stream)
		if body != nil && body.tooLarge.Load() {
			x.stream.abort()
		} else if err == nil {
			// The body is done; nothing more is expected from the visitor.
			_ = x.rc.SetReadDeadline(time.Time{})
		}
		writeDone <- err
	}()
	// The writer may still be reading r.Body, which must stop before this
	// handler returns.
	defer func() {
		select {
		case <-writeDone:
		default:
			_ = x.stream.Close()
			_ = x.rc.SetReadDeadline(time.Now())
			<-writeDone
		}
	}()

	resp, _, err := x.readResponse(outReq)
	if err != nil {
		switch {
		case body != nil && body.tooLarge.Load():
			http.Error(x.w, "request body too large", http.StatusRequestEntityTooLarge)
		case x.r.Context().Err() != nil:
			// The visitor left; there is nobody to answer.
		case errors.Is(err, errResponseHeadTooLarge):
			log.Printf("tunnel %s: %v", x.subdomain, err)
			http.Error(x.w, "tunnel response headers are too large", http.StatusBadGateway)
		case isTimeout(err):
			http.Error(x.w, "the tunnel did not answer in time", http.StatusGatewayTimeout)
		default:
			log.Printf("tunnel %s: failed to read response from stream: %v", x.subdomain, err)
			http.Error(x.w, "failed to read tunnel response", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	x.writeResponse(resp)
}

// readResponse reads the CLI's response head, capped at maxResponseHead
// bytes. The returned reader carries the rest of the
// stream (the body, or the upgraded connection).
func (x *tunnelExchange) readResponse(outReq *http.Request) (*http.Response, *bufio.Reader, error) {
	limit := &headLimitReader{r: x.stream, left: x.limits.maxResponseHead}
	head := bufio.NewReader(limit)
	x.stream.setReadTimeout(x.limits.responseTimeout)
	resp, err := http.ReadResponse(head, outReq)
	if err != nil {
		return nil, nil, err
	}
	limit.lift()
	x.stream.setReadTimeout(x.limits.idleTimeout)
	return resp, head, nil
}

// writeResponse sends resp to the visitor. Streamed responses (no
// Content-Length, chunked, or SSE) are flushed after every read so events
// reach the browser as they happen. A response still running when the
// account's monthly quota runs out is cut off.
func (x *tunnelExchange) writeResponse(resp *http.Response) {
	copyResponseHeaders(x.w.Header(), resp.Header, x.t.domain)
	x.extendWriteDeadline()
	x.w.WriteHeader(resp.StatusCode)

	flush := resp.ContentLength < 0 || len(resp.TransferEncoding) > 0 || isEventStream(resp.Header)
	if flush {
		_ = x.rc.Flush()
	}
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			x.extendWriteDeadline()
			if _, err := x.w.Write(buf[:n]); err != nil {
				return
			}
			if flush {
				_ = x.rc.Flush()
			}
			if x.meter.add(int64(n)) && x.meter.overQuota() {
				x.stream.abort()
				// Drops the connection, so the visitor can't take the cut
				// response for a complete one.
				panic(http.ErrAbortHandler)
			}
		}
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
			if x.r.Context().Err() == nil && !errors.Is(readErr, errStreamAborted) {
				log.Printf("tunnel %s: error copying response body: %v", x.subdomain, readErr)
			}
			panic(http.ErrAbortHandler)
		}
	}
}

// serveUpgrade relays a WebSocket (or other Upgrade) request. The response
// head is parsed like any other, so a CLI that answers without switching
// protocols gets an ordinary response, and a 101 gets its cookies scoped too.
func (x *tunnelExchange) serveUpgrade() {
	outReq, body := buildForwardRequest(x.r, true, x.limits.maxRequestBody)
	if body != nil {
		body.onRead = x.extendReadDeadline
	}
	if err := outReq.Write(x.stream); err != nil {
		switch {
		case body != nil && body.tooLarge.Load():
			http.Error(x.w, "request body too large", http.StatusRequestEntityTooLarge)
		case x.r.Context().Err() == nil:
			log.Printf("tunnel %s: failed to write upgrade request: %v", x.subdomain, err)
			http.Error(x.w, "failed to send request to tunnel", http.StatusBadGateway)
		}
		return
	}

	resp, tunnelSide, err := x.readResponse(outReq)
	if err != nil {
		if x.r.Context().Err() == nil {
			log.Printf("tunnel %s: failed to read upgrade response: %v", x.subdomain, err)
			http.Error(x.w, "failed to read tunnel response", http.StatusBadGateway)
		}
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		x.writeResponse(resp)
		return
	}

	clientConn, clientSide, err := x.rc.Hijack()
	if err != nil {
		log.Printf("tunnel %s: failed to hijack upgrade connection: %v", x.subdomain, err)
		return
	}
	defer clientConn.Close()

	switched := make(http.Header, len(resp.Header))
	copyUpgradeHeaders(switched, resp.Header, x.t.domain)
	fmt.Fprintf(clientSide, "HTTP/1.1 %s\r\n", resp.Status)
	_ = switched.Write(clientSide)
	_, _ = clientSide.WriteString("\r\n")
	if err := clientSide.Flush(); err != nil {
		return
	}

	// The relay watches idleness itself, across both directions.
	x.stream.setReadTimeout(0)
	x.stream.setWriteTimeout(0)
	// Bytes are recorded and the quota checked while the connection runs.
	relayUpgraded(clientConn, clientSide.Reader, x.stream, tunnelSide, x.limits.upgradeIdleTimeout, x.meter)
}

// relayUpgraded copies both ways until either side ends, nothing moves for
// idle, or the account runs out of monthly traffic.
func relayUpgraded(client net.Conn, fromClient io.Reader, stream *tunnelStream, fromTunnel io.Reader, idle time.Duration, meter *trafficMeter) {
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	var once sync.Once
	stop := func() {
		once.Do(func() {
			stream.abort()
			_ = client.Close()
		})
	}
	pipe := func(dst io.Writer, src io.Reader) {
		defer stop()
		defer recoverPanic("tunnel upgrade relay")
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
				lastActive.Store(time.Now().UnixNano())
				if meter.add(int64(n)) && meter.overQuota() {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipe(stream, fromClient) }()
	go func() { defer wg.Done(); pipe(client, fromTunnel) }()

	done := make(chan struct{})
	go func() {
		defer recoverPanic("tunnel upgrade watchdog")
		ticker := time.NewTicker(min(idleCheckInterval(idle), meter.interval))
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				// The quota is checked even when this connection is quiet,
				// since other tunnels of the account use it up too.
				if now.Sub(time.Unix(0, lastActive.Load())) >= idle || (meter.due() && meter.overQuota()) {
					stop()
					return
				}
			}
		}
	}()
	wg.Wait()
	close(done)
}

// idleCheckInterval is how often an upgraded connection is checked for
// idleness: a quarter of the timeout, at most every 30 seconds.
func idleCheckInterval(idle time.Duration) time.Duration {
	return min(max(idle/4, time.Millisecond), 30*time.Second)
}

func (x *tunnelExchange) extendReadDeadline() {
	_ = x.rc.SetReadDeadline(time.Now().Add(x.limits.idleTimeout))
}

func (x *tunnelExchange) extendWriteDeadline() {
	_ = x.rc.SetWriteDeadline(time.Now().Add(x.limits.idleTimeout))
}

func isEventStream(h http.Header) bool {
	mediaType, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// tunnelStream is a yamux stream whose reads and writes each get a fresh
// deadline, so a stalled peer times out without limiting how long a busy
// stream may live. A timeout of 0 means no deadline. abort ends it for good
// and wakes any blocked read or write.
type tunnelStream struct {
	net.Conn
	mu           sync.Mutex
	readTimeout  time.Duration
	writeTimeout time.Duration
	aborted      bool
}

func newTunnelStream(conn net.Conn, idle time.Duration) *tunnelStream {
	return &tunnelStream{Conn: conn, readTimeout: idle, writeTimeout: idle}
}

func (s *tunnelStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.aborted {
		s.mu.Unlock()
		return 0, errStreamAborted
	}
	_ = s.Conn.SetReadDeadline(deadlineAfter(s.readTimeout))
	s.mu.Unlock()
	return s.Conn.Read(p)
}

func (s *tunnelStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	if s.aborted {
		s.mu.Unlock()
		return 0, errStreamAborted
	}
	_ = s.Conn.SetWriteDeadline(deadlineAfter(s.writeTimeout))
	s.mu.Unlock()
	return s.Conn.Write(p)
}

func (s *tunnelStream) setReadTimeout(d time.Duration) {
	s.mu.Lock()
	s.readTimeout = d
	s.mu.Unlock()
}

func (s *tunnelStream) setWriteTimeout(d time.Duration) {
	s.mu.Lock()
	s.writeTimeout = d
	s.mu.Unlock()
}

func (s *tunnelStream) abort() {
	s.mu.Lock()
	s.aborted = true
	s.mu.Unlock()
	_ = s.Conn.SetDeadline(time.Unix(1, 0))
	_ = s.Conn.Close()
}

func deadlineAfter(d time.Duration) time.Time {
	if d <= 0 {
		return time.Time{}
	}
	return time.Now().Add(d)
}

// cappedBody passes a request body through until more than its limit has
// been read, then fails with errBodyTooLarge. It counts the bytes read.
type cappedBody struct {
	r        io.Reader
	left     int64
	read     atomic.Int64
	tooLarge atomic.Bool
	onRead   func()
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.tooLarge.Load() {
		return 0, errBodyTooLarge
	}
	if b.onRead != nil {
		b.onRead()
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.r.Read(p)
	if int64(n) > b.left {
		n = int(b.left)
		b.left = 0
		b.read.Add(int64(n))
		b.tooLarge.Store(true)
		return n, errBodyTooLarge
	}
	b.left -= int64(n)
	b.read.Add(int64(n))
	return n, err
}

// headLimitReader fails with errResponseHeadTooLarge once its limit is used
// up, until lift is called after the response head has been parsed.
type headLimitReader struct {
	r    io.Reader
	left int64
}

func (h *headLimitReader) Read(p []byte) (int, error) {
	if h.left < 0 {
		return h.r.Read(p)
	}
	if h.left == 0 {
		return 0, errResponseHeadTooLarge
	}
	if int64(len(p)) > h.left {
		p = p[:h.left]
	}
	n, err := h.r.Read(p)
	h.left -= int64(n)
	return n, err
}

func (h *headLimitReader) lift() { h.left = -1 }

func sharePathMatches(requestPath, token string) bool {
	if token == "" {
		return false
	}
	prefix := "/s/" + token
	if len(requestPath) < len(prefix) || subtle.ConstantTimeCompare([]byte(requestPath[:len(prefix)]), []byte(prefix)) != 1 {
		return false
	}
	return len(requestPath) == len(prefix) || requestPath[len(prefix)] == '/'
}

// buildForwardRequest creates the request written to the tunnel stream. The
// visitor's body is streamed through a cappedBody (nil when there is none),
// never buffered.
func buildForwardRequest(in *http.Request, preserveUpgradeHeaders bool, maxBody int64) (*http.Request, *cappedBody) {
	out := &http.Request{
		Method:     in.Method,
		URL:        &url.URL{Path: in.URL.Path, RawPath: in.URL.RawPath, RawQuery: in.URL.RawQuery, Opaque: in.URL.Opaque},
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header, len(in.Header)),
		Host:       in.Host,
	}

	var body *cappedBody
	if in.Body != nil && in.Body != http.NoBody && in.ContentLength != 0 {
		body = &cappedBody{r: in.Body, left: maxBody}
		out.Body = io.NopCloser(body)
		// -1 (length unknown) makes Request.Write send it chunked.
		out.ContentLength = in.ContentLength
	}

	// Copy request headers. Upgrade headers are only forwarded when this is an
	// upgrade request; they are invalid on ordinary proxied requests.
	for key, values := range in.Header {
		if isHopByHopHeader(key) && !preserveUpgradeHeaders {
			continue
		}
		// Expect is answered here (Go sends 100 Continue when the body is
		// read); a CLI reads a single response and would take a 100 as final.
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Expect") {
			continue
		}
		out.Header[key] = append([]string(nil), values...)
	}
	// Request.Write adds its own User-Agent when there is none; an empty
	// value keeps the visitor's request as it was.
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header["User-Agent"] = []string{""}
	}
	out.Header.Del(tunnelHopHeader)
	if !preserveUpgradeHeaders {
		out.Close = true
		out.Header.Set("Connection", "close")
	}
	return out, body
}

func isUpgradeRequest(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade")
}

// IsTunnelHost reports whether host is a tunnel subdomain rather than the
// site or API itself.
func (t *tcpService) IsTunnelHost(host string) bool {
	_, ok := t.subdomainFromHost(host)
	return ok
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

// copyResponseHeaders copies a tunneled response's headers, dropping
// hop-by-hop ones and scoping cookies to the tunnel's own host.
func copyResponseHeaders(dst, src http.Header, baseDomain string) {
	listed := map[string]bool{}
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				listed[http.CanonicalHeaderKey(name)] = true
			}
		}
	}
	for key, values := range src {
		if isHopByHopHeader(key) || listed[http.CanonicalHeaderKey(key)] {
			continue
		}
		copyHeaderValues(dst, key, values, baseDomain)
	}
}

// copyUpgradeHeaders copies the headers of a 101 response, which must keep
// Connection and Upgrade.
func copyUpgradeHeaders(dst, src http.Header, baseDomain string) {
	for key, values := range src {
		copyHeaderValues(dst, key, values, baseDomain)
	}
}

func copyHeaderValues(dst http.Header, key string, values []string, baseDomain string) {
	setCookie := strings.EqualFold(key, "Set-Cookie")
	for _, value := range values {
		if setCookie {
			value = hostOnlyCookie(value, baseDomain)
		}
		dst.Add(key, value)
	}
}

// hostOnlyCookie removes a Domain attribute that would share the cookie with
// the base domain or a parent of it. Tunnels share goport.uz's registrable
// domain, so such a cookie would reach goport.uz and every other tunnel.
func hostOnlyCookie(cookie, baseDomain string) string {
	parts := strings.Split(cookie, ";")
	kept := parts[:1]
	for _, part := range parts[1:] {
		name, value, _ := strings.Cut(part, "=")
		if strings.EqualFold(strings.TrimSpace(name), "domain") && coversDomain(value, baseDomain) {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == len(parts) {
		return cookie
	}
	return strings.Join(kept, ";")
}

// coversDomain reports whether a cookie Domain value is base or a parent of
// it.
func coversDomain(value, base string) bool {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), `."`))
	base = strings.ToLower(strings.Trim(strings.TrimSpace(base), "."))
	if value == "" || base == "" {
		return false
	}
	return value == base || strings.HasSuffix(base, "."+value)
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-connection", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
