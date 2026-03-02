// Package api implements the HTTP API handlers and middleware.
package api

import (
	"context"
	"net/http"
)

// TenantInfo holds authentication context extracted from the request.
type TenantInfo struct {
	TenantID string
	UserID   string
}

type tenantContextKey struct{}

// GetTenant extracts TenantInfo from the request context.
func GetTenant(ctx context.Context) *TenantInfo {
	info, _ := ctx.Value(tenantContextKey{}).(*TenantInfo)
	return info
}

// AuthMiddleware extracts tenant/user from headers.
// v1: simple header-based auth. Designed to be replaceable with JWT/IAM.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get("X-Tenant-Id")
		userID := r.Header.Get("X-User-Id")
		if tenantID == "" {
			http.Error(w, `{"error":"missing X-Tenant-Id header"}`, http.StatusUnauthorized)
			return
		}
		if userID == "" {
			userID = "anonymous"
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, &TenantInfo{
			TenantID: tenantID,
			UserID:   userID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
