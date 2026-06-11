package service

import (
	"fmt"
	"os"
	"sort"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

type DashboardI interface {
	// GetDashboard builds the dashboard payload for an authenticated user.
	GetDashboard(user *core.Record) (*model.DashboardResponse, error)
}

type dashboardService struct {
	app    *pocketbase.PocketBase
	tokens TokensI
	domain string
}

func NewDashboardService(app *pocketbase.PocketBase, tokens TokensI) DashboardI {
	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}
	return &dashboardService{
		app:    app,
		tokens: tokens,
		domain: domain,
	}
}

func (s *dashboardService) GetDashboard(user *core.Record) (*model.DashboardResponse, error) {
	if user == nil {
		return nil, fmt.Errorf("missing authenticated user")
	}

	// Make sure the account always has at least its default token.
	if err := s.tokens.EnsureDefault(user.Id); err != nil {
		return nil, fmt.Errorf("failed to ensure default token: %w", err)
	}

	tokens, err := s.tokens.List(user.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to load tokens: %w", err)
	}

	resp := &model.DashboardResponse{
		Name:    user.GetString("name"),
		Email:   user.Email(),
		Avatar:  user.GetString("avatar_url"),
		Domains: []model.DashboardDomain{},
		Tokens:  tokens,
	}

	tunnels, err := s.app.FindRecordsByFilter(
		model.TunnelsCollection,
		"user = {:user}",
		"-updated",
		200,
		0,
		dbx.Params{"user": user.Id},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load tunnels: %w", err)
	}

	for _, tunnel := range tunnels {
		subdomain := tunnel.GetString("subdomain")
		if subdomain == "" {
			continue
		}

		requests, bytes, lastActive := s.tunnelStats(tunnel.Id)
		resp.TotalRequests += requests
		resp.TotalBytes += bytes

		resp.Domains = append(resp.Domains, model.DashboardDomain{
			Subdomain:  subdomain,
			URL:        fmt.Sprintf("https://%s.%s", subdomain, s.domain),
			IsCustom:   tunnel.GetBool("is_custom"),
			Requests:   requests,
			Bytes:      bytes,
			LastActive: lastActive,
			Created:    tunnel.GetString("created"),
		})
	}

	// Custom subdomains first, then most recently created.
	sort.SliceStable(resp.Domains, func(i, j int) bool {
		if resp.Domains[i].IsCustom != resp.Domains[j].IsCustom {
			return resp.Domains[i].IsCustom
		}
		return resp.Domains[i].Created > resp.Domains[j].Created
	})

	return resp, nil
}

// tunnelStats sums the request count and bytes transferred across all log rows
// for a tunnel and returns the most recent activity timestamp.
func (s *dashboardService) tunnelStats(tunnelID string) (requests int64, bytes int64, lastActive string) {
	logs, err := s.app.FindRecordsByFilter(
		model.TunnelLogsCollection,
		"tunnel_id = {:tunnel}",
		"-last_active",
		500,
		0,
		dbx.Params{"tunnel": tunnelID},
	)
	if err != nil {
		return 0, 0, ""
	}

	for _, l := range logs {
		requests += int64(l.GetFloat("request_count"))
		bytes += int64(l.GetFloat("bytes_transferred"))
		if lastActive == "" {
			if la := l.GetString("last_active"); la != "" {
				lastActive = la
			}
		}
	}
	return requests, bytes, lastActive
}
