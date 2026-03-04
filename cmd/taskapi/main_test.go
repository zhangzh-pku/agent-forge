package main

import (
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
