package metricsaccess

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"
)

type HTTPHandler struct {
	Issuer      Issuer
	BearerToken string
}

type issueRequest struct {
	Subject    string    `json:"subject"`
	Oubliette  string    `json:"oubliette"`
	Placement  Placement `json:"placement"`
	TTLSeconds int64     `json:"ttlSeconds,omitempty"`
}

type issueResponse struct {
	EndpointIdentity string `json:"endpointIdentity"`
	Credential       string `json:"credential"`
	ExpiresAt        string `json:"expiresAt"`
}

func (h HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	got, want := r.Header.Get("Authorization"), "Bearer "+h.BearerToken
	if h.BearerToken == "" || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	var request issueRequest
	if decoder.Decode(&request) != nil || request.TTLSeconds < 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var response issueResponse
	err := h.Issuer.IssueTo(r.Context(), Request{
		Subject: request.Subject, Oubliette: request.Oubliette, Placement: request.Placement,
		TTL: time.Duration(request.TTLSeconds) * time.Second,
	}, ConnectorSink(func(_ context.Context, endpoint string, credential []byte, expiresAt time.Time) error {
		response = issueResponse{EndpointIdentity: endpoint, Credential: string(credential), ExpiresAt: expiresAt.UTC().Format(time.RFC3339)}
		return nil
	}))
	if err != nil {
		http.Error(w, "metrics access is unavailable", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return
	}
}
