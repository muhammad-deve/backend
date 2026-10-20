package service

import "testing"

func TestSharePathMatchesOnlyTokenBoundary(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz123456"
	for _, value := range []string{"/s/" + token, "/s/" + token + "/api/list", "/s/" + token + "/file"} {
		if !sharePathMatches(value, token) {
			t.Errorf("expected %q to match", value)
		}
	}
	for _, value := range []string{"/", "/s/wrong", "/s/" + token + "suffix", "/s/"} {
		if sharePathMatches(value, token) {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}
