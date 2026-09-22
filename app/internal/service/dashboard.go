package service

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
)

type DashboardI interface {
	GetDashboard(user *core.Record) (*model.DashboardResponse, error)
	PlanForToken(token string) (*model.CLIPlanResponse, error)
}

type dashboardService struct {
	tokens  TokensI
	tunnels repository.TunnelsI
	usage   repository.UsageI
	billing BillingI
	domain  string
}

func NewDashboardService(tokens TokensI, tunnels repository.TunnelsI, usage repository.UsageI, billing BillingI) DashboardI {
	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}
	return &dashboardService{tokens: tokens, tunnels: tunnels, usage: usage, billing: billing, domain: domain}
}

// PlanForToken resolves a CLI token to its account's allowance. The CLI has no
// PocketBase session, so it cannot use the authenticated dashboard route; the
// token it already holds is the credential here.
func (s *dashboardService) PlanForToken(token string) (*model.CLIPlanResponse, error) {
	owner, err := s.tokens.Verify(token)
	if err != nil {
		return nil, err
	}

	limits, err := s.billing.Plan(owner.UserID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthBytes, err := s.usage.BytesForPeriod(owner.UserID, periodStart, now.Add(time.Second))
	if err != nil {
		return nil, err
	}

	return &model.CLIPlanResponse{
		Plan:         limits.Key,
		IsPro:        limits.IsPro,
		MonthlyBytes: limits.MonthlyBytes,
		MonthBytes:   monthBytes,
	}, nil
}

func (s *dashboardService) GetDashboard(user *core.Record) (*model.DashboardResponse, error) {
	if user == nil {
		return nil, fmt.Errorf("missing authenticated user")
	}

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
		Avatar:  dashboardAvatarURL(user),
		Domains: []model.DashboardDomain{},
		Tokens:  tokens,
	}
	billing, err := s.billing.Get(user.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to load billing: %w", err)
	}
	resp.Billing = billing

	// Month-to-date traffic, measured the same way the tunnel server measures it
	// when it enforces the plan limit, so the dashboard and the CLI never
	// disagree about how much allowance is left.
	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthBytes, err := s.usage.BytesForPeriod(user.Id, periodStart, now.Add(time.Second))
	if err != nil {
		return nil, fmt.Errorf("failed to load monthly usage: %w", err)
	}
	resp.MonthBytes = monthBytes
	resp.MonthResetsAt = periodStart.AddDate(0, 1, 0).Format(time.RFC3339)

	tunnels, err := s.tunnels.ListOwned(user.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to load tunnels: %w", err)
	}

	for _, tunnel := range tunnels {
		if tunnel.Subdomain == "" {
			continue
		}
		total, err := s.usage.Total(tunnel.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to load usage for tunnel %s: %w", tunnel.ID, err)
		}
		resp.TotalRequests += total.Requests
		resp.TotalBytes += total.Bytes
		resp.Domains = append(resp.Domains, model.DashboardDomain{
			Subdomain:  tunnel.Subdomain,
			URL:        fmt.Sprintf("https://%s.%s", tunnel.Subdomain, s.domain),
			IsCustom:   tunnel.IsCustom,
			IsCurrent:  tunnel.IsCurrent,
			LocalPort:  tunnel.LocalPort,
			Protocol:   tunnel.Protocol,
			Requests:   total.Requests,
			Bytes:      total.Bytes,
			LastActive: total.LastActive,
			Created:    tunnel.Created,
		})
	}

	sort.SliceStable(resp.Domains, func(i, j int) bool {
		if resp.Domains[i].IsCustom != resp.Domains[j].IsCustom {
			return resp.Domains[i].IsCustom
		}
		return resp.Domains[i].Created > resp.Domains[j].Created
	})

	return resp, nil
}

func dashboardAvatarURL(user *core.Record) string {
	if filename := user.GetString("avatar"); filename != "" {
		return fmt.Sprintf(
			"/api/files/%s/%s/%s?thumb=256x256f",
			user.Collection().Id,
			user.Id,
			url.PathEscape(filename),
		)
	}
	return strings.TrimSpace(user.GetString("avatar_url"))
}
