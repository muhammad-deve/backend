package service

import (
	"testing"
	"time"
)

func TestSessionWithinPlanLimitKeepsEarliestSessions(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	for index, subdomain := range []string{"first", "second", "third"} {
		service.sessions[subdomain] = nil
		service.sessionUsers[subdomain] = "user_1"
		service.sessionOrder[subdomain] = uint64(index + 1)
	}
	service.sessions["other-user"] = nil
	service.sessionUsers["other-user"] = "user_2"
	service.sessionOrder["other-user"] = 4

	if !service.sessionWithinPlanLimit("first", "user_1", 2) {
		t.Fatal("the earliest session should remain available")
	}
	if !service.sessionWithinPlanLimit("second", "user_1", 2) {
		t.Fatal("the second session should remain available")
	}
	if service.sessionWithinPlanLimit("third", "user_1", 2) {
		t.Fatal("a session above the downgraded plan limit must be blocked")
	}
	if !service.sessionWithinPlanLimit("other-user", "user_2", 1) {
		t.Fatal("sessions from another user must not affect the limit")
	}
}

func TestPendingTrafficSurvivesTunnelDisconnect(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.quotaCache["user_1"] = trafficQuotaCacheEntry{usedBytes: 100, expiresAt: time.Now().Add(time.Minute)}

	service.recordTraffic("tunnel_1", "closed-subdomain", "user_1", 25)
	if got := service.pendingBytesForUser("user_1", time.Now().Add(-time.Hour)); got != 25 {
		t.Fatalf("pendingBytesForUser() = %d, want 25", got)
	}
	if got := service.quotaCache["user_1"].usedBytes; got != 125 {
		t.Fatalf("cached usage = %d, want 125", got)
	}
}
