package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthMiddlewareSetsRequestID(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "header")
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := GetTenant(r.Context())
		if tenant == nil || tenant.RequestID == "" {
			t.Fatal("expected request id in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if rr.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id header")
	}
}

func TestAuthMiddlewareUsesProvidedRequestID(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "header")
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := GetTenant(r.Context())
		if tenant.RequestID != "req_custom" {
			t.Fatalf("expected req_custom, got %s", tenant.RequestID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("X-Request-Id", "req_custom")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Header().Get("X-Request-Id") != "req_custom" {
		t.Fatalf("unexpected response request id header: %s", rr.Header().Get("X-Request-Id"))
	}
}

func TestAuthMiddlewareRequiresUserIdentity(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "header")
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddlewareTrustedModeUsesTrustedHeaders(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "trusted")
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := GetTenant(r.Context())
		if tenant == nil {
			t.Fatal("expected tenant info in context")
		}
		if tenant.TenantID != "trusted_tenant" || tenant.UserID != "trusted_user" {
			t.Fatalf("unexpected tenant context: %+v", tenant)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Authenticated-Tenant-Id", "trusted_tenant")
	req.Header.Set("X-Authenticated-User-Id", "trusted_user")
	// Spoofed client headers should be ignored in trusted mode.
	req.Header.Set("X-Tenant-Id", "spoof_tenant")
	req.Header.Set("X-User-Id", "spoof_user")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

func TestAuthMiddlewareTrustedModeRejectsLegacyHeaders(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "trusted")
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddlewareLogsStructuredRequestFields(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "header")
	logBuf, restoreLog := captureStdLogger(t)
	defer restoreLog()

	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks/task_123", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("X-Request-Id", "req_custom")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	line := logBuf.String()
	for _, want := range []string{
		"request method=GET",
		"path=/tasks/task_123",
		"status=204",
		"request_id=req_custom",
		"tenant_id=tnt_1",
		"user_id=user_1",
		"latency_ms=",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected log to contain %q, got: %s", want, line)
		}
	}
}

func TestAuthMiddlewareRecordsRequestMetrics(t *testing.T) {
	t.Setenv("AGENTFORGE_AUTH_MODE", "header")
	resetRequestMetricsForTest()
	t.Cleanup(resetRequestMetricsForTest)

	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	okReq := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	okReq.Header.Set("X-Tenant-Id", "tnt_1")
	okReq.Header.Set("X-User-Id", "user_1")
	okReqRec := httptest.NewRecorder()
	handler.ServeHTTP(okReqRec, okReq)

	badReq := httptest.NewRequest(http.MethodPost, "/tasks", nil)
	badReq.Header.Set("X-Tenant-Id", "tnt_1")
	badReqRec := httptest.NewRecorder()
	handler.ServeHTTP(badReqRec, badReq)

	snap := SnapshotRequestMetrics()
	if snap.RequestsTotal != 2 {
		t.Fatalf("expected 2 total requests, got %d", snap.RequestsTotal)
	}
	if snap.Status2xx != 1 {
		t.Fatalf("expected 1 success response, got %d", snap.Status2xx)
	}
	if snap.Status4xx != 1 {
		t.Fatalf("expected 1 auth rejection response, got %d", snap.Status4xx)
	}
	if snap.Status5xx != 0 {
		t.Fatalf("expected 0 server errors, got %d", snap.Status5xx)
	}
	if snap.LatencyMSTotal < 0 {
		t.Fatalf("expected non-negative latency total, got %d", snap.LatencyMSTotal)
	}
}

func captureStdLogger(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()

	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")

	return &buf, func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	}
}
