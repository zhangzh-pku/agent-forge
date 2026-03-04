// Package api implements the HTTP API handlers and middleware.
package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agentforge/agentforge/internal/util"
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
		startedAt := time.Now().UTC()
		lrw := &loggingResponseWriter{ResponseWriter: w}

		authMode := effectiveAuthMode()
		requestID := r.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = util.NewID("req_")
		}
		lrw.Header().Set("X-Request-Id", requestID)
		tenantID, userID := extractIdentityFromRequest(r, authMode)
		if tenantID == "" {
			http.Error(lrw, `{"error":"missing authenticated tenant identity","request_id":"`+requestID+`"}`, http.StatusUnauthorized)
			logRequest(r, lrw, startedAt, requestID, tenantID, userID)
			return
		}
		if userID == "" {
			http.Error(lrw, `{"error":"missing authenticated user identity","request_id":"`+requestID+`"}`, http.StatusUnauthorized)
			logRequest(r, lrw, startedAt, requestID, tenantID, userID)
			return
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, &TenantInfo{
			TenantID:  tenantID,
			UserID:    userID,
			RequestID: requestID,
		})
		next.ServeHTTP(lrw, r.WithContext(ctx))
		logRequest(r, lrw, startedAt, requestID, tenantID, userID)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status      int
	writtenByte int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.writtenByte += n
	return n, err
}

func logRequest(r *http.Request, w *loggingResponseWriter, startedAt time.Time, requestID, tenantID, userID string) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	latencyMS := time.Since(startedAt).Milliseconds()
	observeRequestMetrics(status, latencyMS)
	log.Printf(
		"request method=%s path=%s status=%d latency_ms=%d request_id=%s tenant_id=%s user_id=%s bytes=%d",
		r.Method,
		r.URL.Path,
		status,
		latencyMS,
		requestID,
		tenantID,
		userID,
		w.writtenByte,
	)
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
