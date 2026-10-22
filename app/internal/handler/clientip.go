package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
)

// ClientIP returns the caller's address for rate limits, using the same
// rules as the share guard: proxy headers are trusted only from private or
// loopback addresses, and IPv6 addresses are grouped by /64.
func ClientIP(r *http.Request) string {
	key, _ := shareVisitor(r)
	return key
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
