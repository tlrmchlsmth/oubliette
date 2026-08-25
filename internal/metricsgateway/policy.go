package metricsgateway

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	OublietteLabel   = "oubliette_id"
	TrustDomainLabel = "oubliette_trust_domain"
)

type Scope struct {
	Subject               string
	Oubliette             string
	TrustDomain           string
	Upstream              string
	UpstreamAuthorization string
	Policy                Policy
}

type Policy struct {
	AllowedMetrics       []string
	SensitiveLabels      []string
	MaxLookback          time.Duration
	MinStep              time.Duration
	MaxExecutionTime     time.Duration
	MaxSamples           int
	MaxResponseBytes     int64
	MaxConcurrency       int
	MaxRequestsPerMinute int
}

func (p Policy) Rewrite(query, oubliette, trustDomain string) (string, error) {
	if oubliette == "" || trustDomain == "" {
		return "", errors.New("metrics scope is incomplete")
	}
	expr, err := parser.ParseExpr(query)
	if err != nil {
		return "", fmt.Errorf("parse PromQL: %w", err)
	}
	var rewriteErr error
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if rewriteErr != nil || node == nil {
			return rewriteErr
		}
		switch typed := node.(type) {
		case *parser.VectorSelector:
			if typed.Name == "" || !slices.Contains(p.AllowedMetrics, typed.Name) {
				rewriteErr = fmt.Errorf("metric %q is not allowed", typed.Name)
				return rewriteErr
			}
			for _, matcher := range typed.LabelMatchers {
				if matcher.Name == OublietteLabel || matcher.Name == TrustDomainLabel {
					rewriteErr = fmt.Errorf("query may not set reserved label %q", matcher.Name)
					return rewriteErr
				}
				if p.Sensitive(matcher.Name) {
					rewriteErr = fmt.Errorf("query may not select on sensitive label %q", matcher.Name)
					return rewriteErr
				}
			}
			if typed.Timestamp != nil || typed.StartOrEnd != 0 {
				rewriteErr = errors.New("explicit PromQL evaluation timestamps are not allowed")
				return rewriteErr
			}
			if p.MaxLookback > 0 && durationMagnitude(typed.OriginalOffset) > p.MaxLookback {
				rewriteErr = errors.New("PromQL offset exceeds lookback policy")
				return rewriteErr
			}
			oubMatcher, _ := labels.NewMatcher(labels.MatchEqual, OublietteLabel, oubliette)
			trustMatcher, _ := labels.NewMatcher(labels.MatchEqual, TrustDomainLabel, trustDomain)
			typed.LabelMatchers = append(typed.LabelMatchers, oubMatcher, trustMatcher)
		case *parser.MatrixSelector:
			if p.MaxLookback > 0 && typed.Range > p.MaxLookback {
				rewriteErr = errors.New("PromQL range exceeds lookback policy")
				return rewriteErr
			}
		case *parser.SubqueryExpr:
			if typed.Timestamp != nil || typed.StartOrEnd != 0 {
				rewriteErr = errors.New("explicit PromQL evaluation timestamps are not allowed")
				return rewriteErr
			}
			if p.MaxLookback > 0 && typed.Range+durationMagnitude(typed.OriginalOffset) > p.MaxLookback {
				rewriteErr = errors.New("PromQL subquery exceeds lookback policy")
				return rewriteErr
			}
			if p.MinStep > 0 && typed.Step > 0 && typed.Step < p.MinStep {
				rewriteErr = errors.New("PromQL subquery step is below policy minimum")
				return rewriteErr
			}
		case *parser.Call:
			if typed.Func.Name == "label_replace" || typed.Func.Name == "label_join" {
				rewriteErr = fmt.Errorf("function %s is not allowed by label policy", typed.Func.Name)
				return rewriteErr
			}
		case *parser.AggregateExpr:
			for _, label := range typed.Grouping {
				if p.Sensitive(label) {
					rewriteErr = fmt.Errorf("aggregation may not group by sensitive label %q", label)
					return rewriteErr
				}
			}
		case *parser.BinaryExpr:
			if typed.VectorMatching == nil {
				break
			}
			for _, label := range append(append([]string(nil), typed.VectorMatching.MatchingLabels...), typed.VectorMatching.Include...) {
				if p.Sensitive(label) {
					rewriteErr = fmt.Errorf("binary expression may not match or include sensitive label %q", label)
					return rewriteErr
				}
			}
		case *parser.StringLiteral:
			if p.Sensitive(typed.Val) {
				rewriteErr = fmt.Errorf("function may not reference sensitive label %q", typed.Val)
				return rewriteErr
			}
		}
		return nil
	})
	if rewriteErr != nil {
		return "", rewriteErr
	}
	return expr.String(), nil
}

func durationMagnitude(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func (p Policy) Sensitive(label string) bool {
	return slices.Contains(p.SensitiveLabels, label)
}
