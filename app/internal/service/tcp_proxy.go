package service

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
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

	stream, err := session.Open()
	if err != nil {
		log.Printf("tunnel %s: failed to open stream: %v", subdomain, err)
		http.Error(w, "failed to open tunnel stream", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Upgrade requests must keep their Connection and Upgrade headers all the
	// way to the local application. Next.js uses this for the dev HMR socket.
	upgradeRequest := isUpgradeRequest(r)
	outReq, err := buildForwardRequest(r, upgradeRequest)
	if err != nil {
		log.Printf("tunnel %s: failed to build forward request: %v", subdomain, err)
		http.Error(w, "failed to prepare tunnel request", http.StatusBadGateway)
		return
	}

	if err := outReq.Write(stream); err != nil {
		log.Printf("tunnel %s: failed to write request to stream: %v", subdomain, err)
		http.Error(w, "failed to send request to tunnel", http.StatusBadGateway)
		return
	}

	if upgradeRequest {
		clientConn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			log.Printf("tunnel %s: failed to hijack upgrade connection: %v", subdomain, err)
			return
		}
		defer clientConn.Close()

		// An upgraded connection is counted once, when it closes, together with
		// everything relayed over it.
		t.recordTraffic(tunnelID, subdomain, userID, copyTunnelStream(clientConn, stream))
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), outReq)
	if err != nil {
		log.Printf("tunnel %s: failed to read response from stream: %v", subdomain, err)
		http.Error(w, "failed to read tunnel response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	written, err := io.Copy(w, resp.Body)
	if err != nil && !isClosedErr(err) {
		log.Printf("tunnel %s: error copying response body: %v", subdomain, err)
	}
	t.recordTraffic(tunnelID, subdomain, userID, written+maxInt64(outReq.ContentLength, 0))
}

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
	out.Header.Del(tunnelHopHeader)
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
