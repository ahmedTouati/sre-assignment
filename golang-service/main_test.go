package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHealthHandler(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)

	healthHandler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "healthy" {
		t.Fatalf("expected healthy response, got %q", body["status"])
	}
}

func TestServiceMetricsRegistered(t *testing.T) {
	tokensTotal.WithLabelValues("success").Add(0)
	grpcRequestsTotal.WithLabelValues("/token.TokenService/MintToken", "OK").Add(0)
	grpcRequestDuration.WithLabelValues("/token.TokenService/MintToken", "OK").Observe(0)
	redisOperationDuration.WithLabelValues("set", "success").Observe(0)

	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	found := make(map[string]bool, len(metrics))
	for _, metric := range metrics {
		found[metric.GetName()] = true
	}

	for _, name := range []string{
		"grpc_server_requests_total",
		"grpc_server_request_duration_seconds",
		"redis_queue_depth",
		"redis_operation_duration_seconds",
		"token_mint_duration_seconds",
		"tokens_minted_total",
	} {
		if !found[name] {
			t.Errorf("metric %q is not registered", name)
		}
	}
}
