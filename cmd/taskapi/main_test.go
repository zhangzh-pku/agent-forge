package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentforge/agentforge/pkg/api"
)

func TestWritePrometheusMetrics(t *testing.T) {
	rr := httptest.NewRecorder()
	writePrometheusMetrics(rr, api.RequestMetricsSnapshot{
		RequestsTotal:  3,
		LatencyMSTotal: 30,
		Status2xx:      2,
		Status4xx:      1,
		Errors5xxTotal: 0,
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", got)
	}

	body := rr.Body.String()
	for _, want := range []string{
		"agentforge_http_requests_total 3",
		"agentforge_http_request_latency_ms_total 30",
		"agentforge_http_request_latency_ms_avg 10.000000",
		"agentforge_http_responses_total{code_class=\"2xx\"} 2",
		"agentforge_http_responses_total{code_class=\"4xx\"} 1",
		"agentforge_http_5xx_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics output to contain %q, got:\n%s", want, body)
		}
	}
}

type stubReadinessChecker struct {
	err error
}

func (s stubReadinessChecker) HealthCheck(_ context.Context) error {
	return s.err
}

func TestBuildReadinessResponseReady(t *testing.T) {
	status, resp := buildReadinessResponse(
		context.Background(),
		stubReadinessChecker{err: nil},
		stubReadinessChecker{err: nil},
	)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if got, _ := resp["status"].(string); got != "ready" {
		t.Fatalf("expected status=ready, got %v", resp["status"])
	}
}

func TestBuildReadinessResponseNotReady(t *testing.T) {
	status, resp := buildReadinessResponse(
		context.Background(),
		stubReadinessChecker{err: errors.New("dynamo unavailable")},
		stubReadinessChecker{err: errors.New("sqs unavailable")},
	)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", status)
	}
	if got, _ := resp["status"].(string); got != "not_ready" {
		t.Fatalf("expected status=not_ready, got %v", resp["status"])
	}
	checks, ok := resp["checks"].(map[string]string)
	if !ok {
		t.Fatalf("expected checks map[string]string, got %T", resp["checks"])
	}
	if checks["state"] != "error" || checks["queue"] != "error" {
		t.Fatalf("unexpected checks: %+v", checks)
	}
	errs, ok := resp["errors"].(map[string]string)
	if !ok {
		t.Fatalf("expected errors map[string]string, got %T", resp["errors"])
	}
	if !strings.Contains(errs["state"], "dynamo") || !strings.Contains(errs["queue"], "sqs") {
		t.Fatalf("unexpected errors map: %+v", errs)
	}
}
