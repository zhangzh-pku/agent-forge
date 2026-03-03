// Package api implements the HTTP API handlers and middleware.
package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/agentforge/agentforge/pkg/util"
)

// TenantInfo holds authentication context extracted from the request.
type TenantInfo struct {
	TenantID  string
	UserID    string
	RequestID string
}

type tenantContextKey struct{}

const (
	authModeHeader  = "header"
	authModeTrusted = "trusted"

	headerTenantID        = "X-Tenant-Id"
	headerUserID          = "X-User-Id"
	headerTrustedTenantID = "X-Authenticated-Tenant-Id"
	headerTrustedUserID   = "X-Authenticated-User-Id"
)

// GetTenant extracts TenantInfo from the request context.
func GetTenant(ctx context.Context) *TenantInfo {
	info, _ := ctx.Value(tenantContextKey{}).(*TenantInfo)
	return info
}

// AuthMiddleware extracts tenant/user from headers.
// Modes:
//   - header: reads X-Tenant-Id / X-User-Id (local/dev only)
//   - trusted: reads trusted identity headers injected by an upstream authorizer
//     (X-Authenticated-Tenant-Id / X-Authenticated-User-Id)
//
// Mode selection:
//   - AGENTFORGE_AUTH_MODE=header|trusted
//   - default: trusted in aws runtime, header otherwise.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMode := effectiveAuthMode()
		tenantID, userID := extractIdentityFromRequest(r, authMode)
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = util.NewID("req_")
		}
		w.Header().Set("X-Request-Id", requestID)
		if tenantID == "" {
			http.Error(w, `{"error":"missing authenticated tenant identity","request_id":"`+requestID+`"}`, http.StatusUnauthorized)
			return
		}
		if userID == "" {
			http.Error(w, `{"error":"missing authenticated user identity","request_id":"`+requestID+`"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, &TenantInfo{
			TenantID:  tenantID,
			UserID:    userID,
			RequestID: requestID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractIdentityFromRequest(r *http.Request, mode string) (tenantID, userID string) {
	switch mode {
	case authModeTrusted:
		return strings.TrimSpace(r.Header.Get(headerTrustedTenantID)), strings.TrimSpace(r.Header.Get(headerTrustedUserID))
	default:
		return strings.TrimSpace(r.Header.Get(headerTenantID)), strings.TrimSpace(r.Header.Get(headerUserID))
	}
}

func effectiveAuthMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFORGE_AUTH_MODE")))
	switch mode {
	case authModeHeader:
		return authModeHeader
	case authModeTrusted, "trusted_claims", "claims":
		return authModeTrusted
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("AGENTFORGE_RUNTIME")), "aws") {
		return authModeTrusted
	}
	return authModeHeader
}
