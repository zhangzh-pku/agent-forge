package api

import (
	"net/http"
	"net/http/httptest"
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
