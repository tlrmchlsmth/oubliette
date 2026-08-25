package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/metricsgateway"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	var listen, upstream, profileGeneration, allowedMetrics, sensitiveLabels, keyFile, upstreamTokenFile string
	var maxLookback, minStep, maxExecutionTime, maxTokenTTL time.Duration
	var maxSamples, maxConcurrency, maxRequestsPerMinute int
	var maxResponseBytes int64
	flag.StringVar(&listen, "listen", ":8082", "HTTP listen address")
	flag.StringVar(&upstream, "prometheus-url", "", "operator-owned Prometheus base URL")
	flag.StringVar(&profileGeneration, "profile-generation", "", "trusted metrics profile generation")
	flag.StringVar(&allowedMetrics, "allowed-metrics", "", "comma-separated metric allowlist")
	flag.StringVar(&sensitiveLabels, "sensitive-labels", "node,host_ip,pod_ip", "comma-separated labels removed from agent results")
	flag.StringVar(&keyFile, "token-key-file", "/var/run/secrets/oubliette-metrics/hmac-key", "file containing the metrics token HMAC key")
	flag.StringVar(&upstreamTokenFile, "prometheus-token-file", "", "optional file containing an upstream Prometheus bearer token")
	flag.DurationVar(&maxLookback, "max-lookback", time.Hour, "maximum query lookback and range")
	flag.DurationVar(&minStep, "min-step", 15*time.Second, "minimum range query step")
	flag.DurationVar(&maxExecutionTime, "max-execution-time", 10*time.Second, "maximum upstream query execution time")
	flag.DurationVar(&maxTokenTTL, "max-token-ttl", 15*time.Minute, "maximum accepted metrics credential lifetime")
	flag.IntVar(&maxSamples, "max-samples", 10000, "maximum returned samples")
	flag.Int64Var(&maxResponseBytes, "max-response-bytes", 4<<20, "maximum upstream response size")
	flag.IntVar(&maxConcurrency, "max-concurrency", 2, "maximum concurrent requests per subject and Oubliette")
	flag.IntVar(&maxRequestsPerMinute, "max-requests-per-minute", 60, "maximum requests per minute per subject and Oubliette")
	flag.Parse()

	key, err := os.ReadFile(keyFile)
	if err != nil {
		log.Fatal(err)
	}
	key = []byte(strings.TrimSpace(string(key)))
	if len(key) < 32 {
		log.Fatal("metrics token key must contain at least 32 bytes")
	}
	metrics := splitList(allowedMetrics)
	if upstream == "" || profileGeneration == "" || len(metrics) == 0 {
		log.Fatal("prometheus-url, profile-generation, and allowed-metrics are required")
	}
	upstreamAuthorization := ""
	if upstreamTokenFile != "" {
		token, err := os.ReadFile(upstreamTokenFile)
		if err != nil {
			log.Fatal(err)
		}
		upstreamAuthorization = "Bearer " + strings.TrimSpace(string(token))
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(oubv1.AddToScheme(scheme))
	kube, err := client.New(config.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Fatal(err)
	}
	policy := metricsgateway.Policy{
		AllowedMetrics:       metrics,
		SensitiveLabels:      splitList(sensitiveLabels),
		MaxLookback:          maxLookback,
		MinStep:              minStep,
		MaxExecutionTime:     maxExecutionTime,
		MaxSamples:           maxSamples,
		MaxResponseBytes:     maxResponseBytes,
		MaxConcurrency:       maxConcurrency,
		MaxRequestsPerMinute: maxRequestsPerMinute,
	}
	resolver := metricsgateway.KubernetesResolver{
		Client: kube,
		Codec: metricsgateway.TokenCodec{
			Key:      key,
			Audience: "oubliette-metrics",
			MaxTTL:   maxTokenTTL,
		},
		Upstream:              upstream,
		UpstreamAuthorization: upstreamAuthorization,
		ProfileGeneration:     profileGeneration,
		Policy:                policy,
	}
	gateway := &metricsgateway.Gateway{
		Resolver: resolver,
		Audit: func(_ context.Context, event metricsgateway.AuditEvent) {
			encoded, _ := json.Marshal(event)
			log.Printf("metrics_audit=%s", encoded)
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", gateway)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("oubliette metrics gateway listening on %s", listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
