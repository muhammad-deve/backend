package handler

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// ClientIP returns the caller's address for rate limits. Proxy headers are
// trusted only when the request came from a private or loopback address (the
// nginx container); the API port is also reachable directly, and there anyone
// could forge them. IPv6 addresses are grouped by /64.
func ClientIP(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	address := net.ParseIP(remote)
	if address != nil && (address.IsLoopback() || address.IsPrivate()) {
		if forwarded := proxiedClientIP(r); forwarded != nil {
			address = forwarded
		}
	}
	if address == nil {
		return remote
	}
	if address.To4() == nil {
		return address.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return address.String()
}

// proxiedClientIP reads the address nginx passes on. X-Real-IP is set by
// nginx; in X-Forwarded-For only the right-most entry, the one nginx added,
// can be trusted.
func proxiedClientIP(r *http.Request) net.IP {
	if address := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); address != nil {
		return address
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	return net.ParseIP(strings.TrimSpace(parts[len(parts)-1]))
}

// TooManyAttempts writes the standard 429 response.
func TooManyAttempts(e *core.RequestEvent, retryAfterSeconds int) error {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	minutes := (retryAfterSeconds + 59) / 60
	wait := fmt.Sprintf("%d minutes", minutes)
	if minutes == 1 {
		wait = "1 minute"
	}
	e.Response.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	return e.JSON(http.StatusTooManyRequests, map[string]any{
		"error":      "Too many attempts. Try again in " + wait + ".",
		"retryAfter": retryAfterSeconds,
	})
}
