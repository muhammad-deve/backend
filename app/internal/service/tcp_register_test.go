package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// fakeTokens resolves CLI tokens with verify.
type fakeTokens struct {
	verify func(token string) (*model.TokenOwner, error)
}

func (f fakeTokens) List(string) ([]model.TokenItem, error)          { return nil, nil }
func (f fakeTokens) Create(string, string) (*model.TokenItem, error) { return nil, nil }
func (f fakeTokens) Delete(string, string) error                     { return nil }
func (f fakeTokens) EnsureDefault(string) error                      { return nil }
func (f fakeTokens) Reset(string) error                              { return nil }
func (f fakeTokens) Verify(token string) (*model.TokenOwner, error)  { return f.verify(token) }

// registerOverPipe runs one registration through handleConnection, as a CLI
// would, and returns the server's reply.
func registerOverPipe(t *testing.T, service *tcpService, req tunnelRegistrationRequest) tunnelRegistrationResponse {
	t.Helper()
	serverEnd, cliEnd := net.Pipe()
	t.Cleanup(func() { cliEnd.Close() })
	if !service.handshakes.begin("pipe") {
		t.Fatal("no handshake slot")
	}
	go service.handleConnection(serverEnd, "pipe")
	go func() { _ = json.NewEncoder(cliEnd).Encode(req) }()
	return readRegistrationReply(t, cliEnd)
}

func TestInternalRegistrationErrorsStayInTheLog(t *testing.T) {
	service := NewTCPService(nil, nil, nil, nil, fakeTokens{verify: func(string) (*model.TokenOwner, error) {
		return nil, errors.New("sqlite: database is locked (SELECT * FROM tokens)")
	}}).(*tcpService)

	resp := registerOverPipe(t, service, tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_x"})
	if resp.Error != internalRegistrationError {
		t.Fatalf("error = %q, want the generic message", resp.Error)
	}
}

func TestRefusalsKeepTheirPermanentPhrases(t *testing.T) {
	cases := []struct {
		name   string
		verify func(string) (*model.TokenOwner, error)
		req    tunnelRegistrationRequest
		want   string
	}{
		{
			name:   "deleted or unknown token",
			verify: func(string) (*model.TokenOwner, error) { return nil, ErrInvalidToken },
			req:    tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_deleted", Resume: "myapp"},
			want:   "failed to register tunnel: authentication required",
		},
		{
			name: "token beyond the plan's devices",
			verify: func(string) (*model.TokenOwner, error) {
				return nil, fmt.Errorf("%w: this device token is outside your plan allowance; manage tokens", ErrProRequired)
			},
			req:  tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_extra"},
			want: "outside your plan allowance",
		},
		{
			name:   "reset with a custom name",
			verify: func(string) (*model.TokenOwner, error) { return &model.TokenOwner{UserID: "user_1"}, nil },
			req:    tunnelRegistrationRequest{Type: "http", Port: "3000", Token: "gp_x", Reset: true, Subdomain: "myapp"},
			want:   "failed to register tunnel: reset and custom subdomain cannot be used together",
		},
	}
	for _, c := range cases {
		service := NewTCPService(nil, nil, nil, nil, fakeTokens{verify: c.verify}).(*tcpService)
		resp := registerOverPipe(t, service, c.req)
		if !strings.Contains(resp.Error, c.want) {
			t.Errorf("%s: error = %q, want it to contain %q", c.name, resp.Error, c.want)
		}
	}
}
