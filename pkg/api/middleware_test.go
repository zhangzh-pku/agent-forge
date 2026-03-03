package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddlewareSetsRequestID(t *testing.T) {
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
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := GetTenant(r.Context())
		if tenant.RequestID != "req_custom" {
			t.Fatalf("expected req_custom, got %s", tenant.RequestID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-Request-Id", "req_custom")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Header().Get("X-Request-Id") != "req_custom" {
		t.Fatalf("unexpected response request id header: %s", rr.Header().Get("X-Request-Id"))
	}
}
