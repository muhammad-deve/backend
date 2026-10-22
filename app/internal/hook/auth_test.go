package hook

import (
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestOAuthAccess(t *testing.T) {
	users := core.NewAuthCollection("users")
	record := func(email string, verified bool) *core.Record {
		r := core.NewRecord(users)
		r.SetEmail(email)
		r.SetVerified(verified)
		return r
	}

	tests := []struct {
		name   string
		record *core.Record
		email  string
		want   oauthDecision
	}{
		{"new account", nil, "dev@example.com", oauthAllow},
		{"new account without email", nil, "", oauthRefuseNoEmail},
		{"verified account", record("old@example.com", true), "dev@example.com", oauthAllow},
		{"pre-claim with another email", record("victim@example.com", false), "attacker@gmail.com", oauthRefuseMismatch},
		{"unverified without provider email", record("victim@example.com", false), "", oauthRefuseMismatch},
		{"owner claims a pending account", record("dev@example.com", false), "Dev@Example.com", oauthClaimPending},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := oauthAccess(test.record, test.email); got != test.want {
				t.Fatalf("oauthAccess() = %v, want %v", got, test.want)
			}
		})
	}
}
