package service

import (
	"testing"
	"time"
)

func liveSession(service *tcpService, subdomain, tunnelID, userID string, since time.Time) {
	service.sessions[subdomain] = nil
	service.tunnelIDs[subdomain] = tunnelID
	service.sessionUsers[subdomain] = userID
	service.onlineSince[subdomain] = since
}

func TestCollectOnlineCountsLiveAndEndedSessions(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	liveSession(service, "app", "tunnel_1", "user_1", start)
	liveSession(service, "api", "tunnel_2", "user_1", start)

	// "api" disconnects after 30s; its time waits for the next write.
	service.endOnlineLocked("api", start.Add(30*time.Second))
	if _, ok := service.onlineSince["api"]; ok {
		t.Fatal("an ended session must stop counting")
	}

	tunnels, users := sumOnlineMinutes(service.collectOnline(start.Add(90 * time.Second)))
	if tunnels["tunnel_1"] != 1.5 || tunnels["tunnel_2"] != 0.5 {
		t.Fatalf("tunnel minutes = %v, want tunnel_1 1.5 and tunnel_2 0.5", tunnels)
	}
	// Both subdomains belong to user_1, so their time adds up.
	if users["user_1"] != 2 {
		t.Fatalf("user minutes = %v, want 2", users["user_1"])
	}

	// The next write only counts time since this one.
	tunnels, users = sumOnlineMinutes(service.collectOnline(start.Add(150 * time.Second)))
	if tunnels["tunnel_1"] != 1 || users["user_1"] != 1 || len(tunnels) != 1 {
		t.Fatalf("second write: tunnels %v users %v, want only tunnel_1 at 1 minute", tunnels, users)
	}
}

func TestEndOnlineIgnoresSessionsThatNeverWentLive(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, nil).(*tcpService)
	service.endOnlineLocked("never-connected", time.Now())
	if len(service.pendingOnline) != 0 {
		t.Fatal("a registration that failed before going live has no time to count")
	}
}

func TestSumOnlineMinutesRoundsAndSkipsNothing(t *testing.T) {
	tunnels, users := sumOnlineMinutes([]onlineDelta{
		{tunnelID: "tunnel_1", userID: "user_1", seconds: 37},
		{tunnelID: "tunnel_2", userID: "user_2", seconds: 0.1},
		{tunnelID: "tunnel_3", userID: "user_3", seconds: -5},
	})
	if tunnels["tunnel_1"] != 0.62 || users["user_1"] != 0.62 {
		t.Fatalf("37s should round to 0.62 minutes, got %v / %v", tunnels["tunnel_1"], users["user_1"])
	}
	if _, ok := tunnels["tunnel_2"]; ok {
		t.Fatal("time that rounds to 0.00 minutes should not be written")
	}
	if _, ok := tunnels["tunnel_3"]; ok {
		t.Fatal("negative time (clock jump) must be ignored")
	}
}
