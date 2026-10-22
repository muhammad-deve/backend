package service

import (
	"log"
	"sync"
	"time"
)

// trafficMeter counts the bytes of one tunneled exchange. Every few MB, or
// every interval, it records them and checks the account's monthly quota, so
// a WebSocket or a long stream can't run past the quota until it closes.
type trafficMeter struct {
	t          *tcpService
	tunnelID   string
	subdomain  string
	userID     string
	checkBytes int64
	interval   time.Duration

	mu         sync.Mutex
	unrecorded int64
	lastCheck  time.Time
}

func (t *tcpService) newMeter(tunnelID, subdomain, userID string) *trafficMeter {
	return &trafficMeter{
		t:          t,
		tunnelID:   tunnelID,
		subdomain:  subdomain,
		userID:     userID,
		checkBytes: t.proxy.quotaCheckBytes,
		interval:   t.proxy.quotaCheckInterval,
		lastCheck:  time.Now(),
	}
}

// add counts n bytes and reports whether the quota is due for a check.
func (m *trafficMeter) add(n int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unrecorded += n
	return m.unrecorded >= m.checkBytes || time.Since(m.lastCheck) >= m.interval
}

// due reports whether the quota hasn't been checked for an interval.
func (m *trafficMeter) due() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Since(m.lastCheck) >= m.interval
}

// overQuota records the bytes counted so far and reports whether the account
// has used up its monthly traffic. A failed lookup doesn't cut anything.
func (m *trafficMeter) overQuota() bool {
	m.mu.Lock()
	n := m.unrecorded
	m.unrecorded = 0
	m.lastCheck = time.Now()
	m.mu.Unlock()
	if n > 0 {
		m.t.addTraffic(m.tunnelID, m.subdomain, m.userID, n, 0)
	}
	limits, used, err := m.t.trafficAllowance(m.userID)
	if err != nil || used < limits.MonthlyBytes {
		return false
	}
	log.Printf("tunnel %s: monthly traffic allowance reached; closing the connection", m.subdomain)
	return true
}

// finish records what is left and counts the exchange as one request.
func (m *trafficMeter) finish() {
	m.mu.Lock()
	n := m.unrecorded
	m.unrecorded = 0
	m.mu.Unlock()
	m.t.addTraffic(m.tunnelID, m.subdomain, m.userID, n, 1)
}
