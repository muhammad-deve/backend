package model

import "testing"

func TestFreePlanLimits(t *testing.T) {
	limits := FreePlanLimits()

	if limits.MaxActiveTunnels != 1 {
		t.Fatalf("MaxActiveTunnels = %d, want 1", limits.MaxActiveTunnels)
	}
	if limits.CustomSubdomains {
		t.Fatal("Free plan must not include custom subdomains")
	}
}
