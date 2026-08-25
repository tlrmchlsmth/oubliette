package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PrometheusTargetAPI struct {
	BaseURL       string
	Authorization string
	Client        *http.Client
	MaxBytes      int64
}

// ValidateCoverage reads the operator-only active-target API. It never uses
// the agent query gateway, which intentionally denies target discovery.
func (p PrometheusTargetAPI) ValidateCoverage(ctx context.Context, set TargetSet) error {
	endpoint, err := url.Parse(p.BaseURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return errors.New("Prometheus target API URL is invalid")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/targets"
	query := endpoint.Query()
	query.Set("state", "active")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	if p.Authorization != "" {
		request.Header.Set("Authorization", p.Authorization)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("read Prometheus active targets: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Prometheus active targets returned HTTP %d", response.StatusCode)
	}
	limit := p.MaxBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return errors.New("Prometheus active-target response exceeded policy")
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ActiveTargets []struct {
				Labels map[string]string `json:"labels"`
				Health string            `json:"health"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Status != "success" {
		return errors.New("Prometheus active-target response is invalid")
	}
	discovered := make([]DiscoveredTarget, 0, len(payload.Data.ActiveTargets))
	for _, target := range payload.Data.ActiveTargets {
		if target.Labels[LabelOubliette] != set.Oubliette {
			continue
		}
		discovered = append(discovered, DiscoveredTarget{Health: target.Health, Labels: target.Labels})
	}
	return ValidateTargetCoverage(set, discovered)
}
