package service

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/repository"
)

var ErrInvalidUsageRange = errors.New("invalid usage range")

type UsageI interface {
	Get(userID, requestedRange string) (*model.UsageResponse, error)
}

type usageRangeConfig struct {
	duration time.Duration
	interval time.Duration
	points   int
}

var usageRanges = map[string]usageRangeConfig{
	"hour": {duration: time.Hour, interval: model.UsageBucketDuration, points: 12},
	"day":  {duration: 24 * time.Hour, interval: time.Hour, points: 24},
	"week": {duration: 7 * 24 * time.Hour, interval: 24 * time.Hour, points: 7},
}

type usageService struct {
	tunnels repository.TunnelsI
	usage   repository.UsageI
	domain  string
}

func NewUsageService(tunnels repository.TunnelsI, usage repository.UsageI) UsageI {
	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}
	return &usageService{tunnels: tunnels, usage: usage, domain: domain}
}

func (s *usageService) Get(userID, requestedRange string) (*model.UsageResponse, error) {
	rangeName := strings.ToLower(strings.TrimSpace(requestedRange))
	if rangeName == "" {
		rangeName = "day"
	}
	config, ok := usageRanges[rangeName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrInvalidUsageRange, rangeName)
	}

	to := time.Now().UTC().Truncate(config.interval).Add(config.interval)
	from := to.Add(-config.duration)

	tunnels, err := s.tunnels.ListOwned(userID)
	if err != nil {
		return nil, fmt.Errorf("load tunnels: %w", err)
	}
	buckets, err := s.usage.ListBuckets(userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("load usage buckets: %w", err)
	}

	sort.SliceStable(tunnels, func(i, j int) bool {
		if tunnels[i].IsCustom != tunnels[j].IsCustom {
			return tunnels[i].IsCustom
		}
		return tunnels[i].Created > tunnels[j].Created
	})

	response := &model.UsageResponse{
		Range:         rangeName,
		From:          from,
		To:            to,
		BucketSeconds: int64(config.interval.Seconds()),
		Series:        make([]model.UsageSeries, 0, len(tunnels)),
	}
	seriesByTunnel := make(map[string]*model.UsageSeries, len(tunnels))
	for _, tunnel := range tunnels {
		points := make([]model.UsagePoint, config.points)
		for index := range points {
			points[index].Timestamp = from.Add(time.Duration(index) * config.interval)
		}
		response.Series = append(response.Series, model.UsageSeries{
			TunnelID:  tunnel.ID,
			Subdomain: tunnel.Subdomain,
			URL:       fmt.Sprintf("https://%s.%s", tunnel.Subdomain, s.domain),
			Points:    points,
		})
		seriesByTunnel[tunnel.ID] = &response.Series[len(response.Series)-1]
	}

	for _, bucket := range buckets {
		series := seriesByTunnel[bucket.TunnelID]
		if series == nil {
			continue
		}
		pointTime := bucket.BucketStart.UTC().Truncate(config.interval)
		pointIndex := int(pointTime.Sub(from) / config.interval)
		if pointIndex < 0 || pointIndex >= len(series.Points) {
			continue
		}
		series.Points[pointIndex].Requests += bucket.Requests
		series.Points[pointIndex].Bytes += bucket.Bytes
	}

	return response, nil
}
