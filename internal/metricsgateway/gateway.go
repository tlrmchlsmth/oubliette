package metricsgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type AuditEvent struct {
	Time           time.Time
	Subject        string
	Oubliette      string
	OriginalQuery  string
	EffectiveQuery string
	Path           string
	Start          string
	End            string
	Step           string
	EvaluationTime string
	Status         int
	Error          string
}

type Gateway struct {
	Resolver ScopeResolver
	Client   *http.Client
	Now      func() time.Time
	Audit    func(context.Context, AuditEvent)

	mu    sync.Mutex
	usage map[string]*usage
}

type usage struct {
	inFlight int
	window   time.Time
	requests int
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	event := AuditEvent{Time: g.now(), Path: r.URL.Path}
	status := http.StatusOK
	defer func() {
		event.Status = status
		if g.Audit != nil {
			g.Audit(r.Context(), event)
		}
	}()

	if g.Resolver == nil {
		status = writeError(w, http.StatusServiceUnavailable, "metrics gateway is not configured")
		return
	}
	scope, err := g.Resolver.Resolve(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		status = writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	event.Subject, event.Oubliette = scope.Subject, scope.Oubliette
	release, err := g.acquire(scope)
	if err != nil {
		status = writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer release()

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		status = writeError(w, http.StatusMethodNotAllowed, "only GET and POST are supported")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		status = writeError(w, http.StatusBadRequest, "invalid request parameters")
		return
	}
	form := cloneValues(r.Form)
	if err := g.rewriteRequest(r.URL.Path, form, scope, &event); err != nil {
		event.Error = err.Error()
		status = writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := g.validateBudget(r.URL.Path, form, scope.Policy); err != nil {
		event.Error = err.Error()
		status = writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	event.Start, event.End, event.Step, event.EvaluationTime = form.Get("start"), form.Get("end"), form.Get("step"), form.Get("time")
	upstream, err := url.Parse(scope.Upstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		status = writeError(w, http.StatusServiceUnavailable, "metrics upstream is unavailable")
		return
	}
	upstream.Path = r.URL.Path
	upstream.RawQuery = ""
	upstreamContext := r.Context()
	cancel := func() {}
	if scope.Policy.MaxExecutionTime > 0 {
		upstreamContext, cancel = context.WithTimeout(upstreamContext, scope.Policy.MaxExecutionTime)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(upstreamContext, http.MethodPost, upstream.String(), strings.NewReader(form.Encode()))
	if err != nil {
		status = writeError(w, http.StatusInternalServerError, "construct upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if scope.UpstreamAuthorization != "" {
		req.Header.Set("Authorization", scope.UpstreamAuthorization)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		event.Error = err.Error()
		status = writeError(w, http.StatusBadGateway, "metrics upstream request failed")
		return
	}
	defer resp.Body.Close()
	limit := scope.Policy.MaxResponseBytes
	if limit <= 0 {
		limit = 4 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		status = writeError(w, http.StatusBadGateway, "metrics response exceeded policy")
		return
	}
	filtered, samples, err := filterResponse(body, scope.Policy, r.URL.Path)
	if err != nil {
		status = writeError(w, http.StatusBadGateway, "invalid metrics upstream response")
		return
	}
	if scope.Policy.MaxSamples > 0 && samples > scope.Policy.MaxSamples {
		status = writeError(w, http.StatusUnprocessableEntity, "metrics response exceeded sample budget")
		return
	}
	status = resp.StatusCode
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(filtered)
}

func (g *Gateway) rewriteRequest(path string, form url.Values, scope Scope, event *AuditEvent) error {
	switch {
	case path == "/api/v1/query" || path == "/api/v1/query_range":
		query := form.Get("query")
		if query == "" {
			return errors.New("query is required")
		}
		rewritten, err := scope.Policy.Rewrite(query, scope.Oubliette, scope.TrustDomain)
		if err != nil {
			return err
		}
		event.OriginalQuery, event.EffectiveQuery = query, rewritten
		form.Set("query", rewritten)
		return nil
	case path == "/api/v1/series" || path == "/api/v1/labels" || (strings.HasPrefix(path, "/api/v1/label/") && strings.HasSuffix(path, "/values")):
		if strings.HasPrefix(path, "/api/v1/label/") {
			label := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/label/"), "/values")
			decoded, err := url.PathUnescape(label)
			if err != nil || decoded == "" || scope.Policy.Sensitive(decoded) {
				return errors.New("label is not available")
			}
		}
		matches := form["match[]"]
		if len(matches) == 0 {
			return errors.New("at least one match[] selector is required")
		}
		rewritten := make([]string, 0, len(matches))
		for _, match := range matches {
			effective, err := scope.Policy.Rewrite(match, scope.Oubliette, scope.TrustDomain)
			if err != nil {
				return err
			}
			rewritten = append(rewritten, effective)
		}
		event.OriginalQuery = strings.Join(matches, "; ")
		event.EffectiveQuery = strings.Join(rewritten, "; ")
		form["match[]"] = rewritten
		return nil
	default:
		return errors.New("Prometheus endpoint is not allowed")
	}
}

func (g *Gateway) validateBudget(path string, form url.Values, policy Policy) error {
	now := g.now()
	if path == "/api/v1/query_range" {
		start, err := parsePromTime(form.Get("start"))
		if err != nil {
			return errors.New("valid start is required")
		}
		end, err := parsePromTime(form.Get("end"))
		if err != nil || end.Before(start) {
			return errors.New("valid end is required")
		}
		step, err := parsePromDuration(form.Get("step"))
		if err != nil || step <= 0 {
			return errors.New("valid step is required")
		}
		if policy.MaxLookback > 0 && (end.Sub(start) > policy.MaxLookback || start.Before(now.Add(-policy.MaxLookback))) {
			return errors.New("query range exceeds lookback policy")
		}
		if policy.MinStep > 0 && step < policy.MinStep {
			return errors.New("query step is below policy minimum")
		}
		if policy.MaxSamples > 0 && int(math.Ceil(end.Sub(start).Seconds()/step.Seconds()))+1 > policy.MaxSamples {
			return errors.New("query range exceeds point budget")
		}
	}
	if path == "/api/v1/query" && form.Get("time") != "" {
		at, err := parsePromTime(form.Get("time"))
		if err != nil {
			return errors.New("invalid query time")
		}
		if policy.MaxLookback > 0 && at.Before(now.Add(-policy.MaxLookback)) {
			return errors.New("query time exceeds lookback policy")
		}
	}
	if path == "/api/v1/series" || path == "/api/v1/labels" || (strings.HasPrefix(path, "/api/v1/label/") && strings.HasSuffix(path, "/values")) {
		start, err := parsePromTime(form.Get("start"))
		if err != nil {
			return errors.New("valid start is required for metadata queries")
		}
		end, err := parsePromTime(form.Get("end"))
		if err != nil || end.Before(start) {
			return errors.New("valid end is required for metadata queries")
		}
		if policy.MaxLookback > 0 && (end.Sub(start) > policy.MaxLookback || start.Before(now.Add(-policy.MaxLookback)) || end.After(now.Add(time.Minute))) {
			return errors.New("metadata query range exceeds lookback policy")
		}
	}
	return nil
}

func (g *Gateway) acquire(scope Scope) (func(), error) {
	key := scope.Subject + "\x00" + scope.Oubliette
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.usage == nil {
		g.usage = map[string]*usage{}
	}
	current := g.usage[key]
	if current == nil {
		current = &usage{window: now}
		g.usage[key] = current
	}
	if now.Sub(current.window) >= time.Minute {
		current.window, current.requests = now, 0
	}
	if scope.Policy.MaxRequestsPerMinute > 0 && current.requests >= scope.Policy.MaxRequestsPerMinute {
		return nil, errors.New("metrics request rate exceeded")
	}
	if scope.Policy.MaxConcurrency > 0 && current.inFlight >= scope.Policy.MaxConcurrency {
		return nil, errors.New("metrics concurrency exceeded")
	}
	current.requests++
	current.inFlight++
	return func() {
		g.mu.Lock()
		current.inFlight--
		g.mu.Unlock()
	}, nil
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

func filterResponse(body []byte, policy Policy, path string) ([]byte, int, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, err
	}
	samples := filterValue(value, policy)
	if path == "/api/v1/labels" {
		if root, ok := value.(map[string]any); ok {
			if data, ok := root["data"].([]any); ok {
				filtered := data[:0]
				for _, item := range data {
					label, ok := item.(string)
					if ok && !policy.Sensitive(label) {
						filtered = append(filtered, item)
					}
				}
				root["data"] = filtered
			}
		}
	}
	out, err := json.Marshal(value)
	return out, samples, err
}

func filterValue(value any, policy Policy) int {
	samples := 0
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if policy.Sensitive(key) {
				delete(typed, key)
				continue
			}
			if key == "value" {
				samples++
			}
			if key == "values" {
				if values, ok := child.([]any); ok {
					samples += len(values)
				}
			}
			samples += filterValue(child, policy)
		}
	case []any:
		for _, child := range typed {
			samples += filterValue(child, policy)
		}
	}
	return samples
}

func parsePromTime(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Time{}, err
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), nil
}

func parsePromDuration(value string) (time.Duration, error) {
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return time.Duration(seconds * float64(time.Second)), nil
	}
	return time.ParseDuration(value)
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func writeError(w http.ResponseWriter, status int, message string) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": message})
	return status
}

var _ http.Handler = (*Gateway)(nil)
