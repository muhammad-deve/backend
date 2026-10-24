package service

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// smallQuota gives userID a monthly allowance of limit bytes.
func smallQuota(service *tcpService, userID string, limit, used int64) {
	service.quotaMu.Lock()
	defer service.quotaMu.Unlock()
	service.quotaCache[userID] = trafficQuotaCacheEntry{
		limits:    model.PlanLimits{MaxActiveTunnels: 1, MonthlyBytes: limit, CustomSubdomains: true},
		usedBytes: used,
		expiresAt: time.Now().Add(time.Hour),
	}
}

func recordedTraffic(service *tcpService, userID string) (requests, bytes int64) {
	service.statsMu.Lock()
	defer service.statsMu.Unlock()
	for key, stat := range service.stats {
		if key.userID == userID {
			requests += stat.requests
			bytes += stat.bytes
		}
	}
	return requests, bytes
}

// expectClosed waits for the server to close conn.
func expectClosed(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	for {
		if _, err := reader.ReadByte(); err != nil {
			if isTimeout(err) {
				t.Fatal("the connection was not closed")
			}
			return
		}
	}
}

func TestWebSocketIsMeteredAndCutAtTheQuota(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.quotaCheckBytes = 256
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoUpgradeCLI)
	smallQuota(service, "user_1", 4096, 0)

	conn, reader, resp := dialUpgrade(t, front.URL, "myapp.goport.uz")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
	// Each byte crosses twice (sent and echoed), so this goes over 4 KB.
	go func() {
		for i := 0; i < 8; i++ {
			if _, err := conn.Write([]byte(strings.Repeat("x", 512))); err != nil {
				return
			}
		}
	}()
	expectClosed(t, reader)

	// The relay finishes recording just after it closes the connection.
	deadline := time.Now().Add(3 * time.Second)
	for {
		requests, bytes := recordedTraffic(service, "user_1")
		if requests == 1 && bytes >= 4096 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recorded %d requests and %d bytes, want 1 request and at least 4096 bytes", requests, bytes)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQuietWebSocketIsCutWhenTheQuotaRunsOutElsewhere(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.quotaCheckInterval = 50 * time.Millisecond
	front, _ := fakeTunnel(t, service, "myapp", "user_1", echoUpgradeCLI)
	smallQuota(service, "user_1", 4096, 0)

	_, reader, resp := dialUpgrade(t, front.URL, "myapp.goport.uz")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d, want 101", resp.StatusCode)
	}
	// Another tunnel of the account uses up the month.
	service.recordTraffic("tunnel_other", "other", "user_1", 4096)
	expectClosed(t, reader)
}

func TestLongResponseIsCutAtTheQuota(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.proxy.quotaCheckBytes = 512
	front, _ := fakeTunnel(t, service, "myapp", "user_1", func(stream net.Conn, _ *bufio.Reader, _ *http.Request) {
		_, _ = io.WriteString(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n")
		for i := 0; i < 64; i++ {
			if _, err := stream.Write([]byte(strings.Repeat("y", 1024))); err != nil {
				return
			}
		}
	})
	smallQuota(service, "user_1", 2048, 0)

	resp := doTunnel(t, tunnelRequest(t, front, http.MethodGet, "myapp.goport.uz", "/big", nil))
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("read %d bytes and a clean end; the response should be cut off", len(body))
	}
	if len(body) >= 64*1024 {
		t.Fatalf("the whole %d byte response got through", len(body))
	}
}
