package service

import (
	"log"
	"net"
	"runtime/debug"
	"sync"
	"time"
)

// Limits on the :7000 handshake. A CLI 0.2.0 registration line is well under
// 1 KB (token, port, names, version info), so 8 KB leaves plenty of room.
const (
	maxRegistrationBytes     = 8 << 10
	maxPendingHandshakes     = 512
	maxPendingHandshakesByIP = 20
)

// handshakeLimiter counts connections that have not finished registering yet,
// in total and per IP, so slow or idle clients can't use up goroutines and
// file descriptors.
type handshakeLimiter struct {
	mu      sync.Mutex
	total   int
	byIP    map[string]int
	maxAll  int
	maxByIP int
}

func newHandshakeLimiter(maxAll, maxByIP int) *handshakeLimiter {
	return &handshakeLimiter{byIP: make(map[string]int), maxAll: maxAll, maxByIP: maxByIP}
}

// begin reserves a slot for ip. It reports false when a cap is reached.
func (l *handshakeLimiter) begin(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxAll || l.byIP[ip] >= l.maxByIP {
		return false
	}
	l.total++
	l.byIP[ip]++
	return true
}

func (l *handshakeLimiter) end(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP[ip] <= 1 {
		delete(l.byIP, ip)
	} else {
		l.byIP[ip]--
	}
	if l.total > 0 {
		l.total--
	}
}

// remoteIP returns the host part of a connection's remote address.
func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// recoverPanic logs a panic in a tunnel goroutine instead of letting it take
// down the whole process. Use it as `defer recoverPanic("...")`.
func recoverPanic(where string) {
	if r := recover(); r != nil {
		log.Printf("panic in %s: %v\n%s", where, r, debug.Stack())
	}
}

// acceptBackoff is how long acceptLoop waits after an Accept error; it
// doubles up to a second, like net/http.Server.
func acceptBackoff(previous time.Duration) time.Duration {
	if previous == 0 {
		return 5 * time.Millisecond
	}
	if next := previous * 2; next < time.Second {
		return next
	}
	return time.Second
}
