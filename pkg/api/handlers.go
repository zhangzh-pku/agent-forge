package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/task"
	"github.com/agentforge/agentforge/pkg/tenant"
)

// maxRequestBodySize is the maximum allowed request body size (1 MB).
const maxRequestBodySize = 1 << 20

// Handler holds HTTP handler dependencies.
type Handler struct {
	svc           *task.Service
	tenantRuntime TenantRuntimeProvider
}

// TenantRuntimeProvider exposes tenant-level runtime metrics and alerts.
type TenantRuntimeProvider interface {
	TenantSnapshot(tenantID string) tenant.RuntimeSnapshot
	TenantAlerts(tenantID string, limit int) []tenant.Alert
}

// NewHandler creates a new API handler.
func NewHandler(svc *task.Service) *Handler {
	return &Handler{svc: svc}
}

// SetTenantRuntimeProvider wires tenant runtime metrics/alerts into the API.
func (h *Handler) SetTenantRuntimeProvider(provider TenantRuntimeProvider) {
	h.tenantRuntime = provider
}

// RegisterRoutes registers all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/tasks", AuthMiddleware(http.HandlerFunc(h.handleTasks)))
	mux.Handle("/tasks/", AuthMiddleware(http.HandlerFunc(h.handleTaskByID)))
	mux.Handle("/tenants/", AuthMiddleware(http.HandlerFunc(h.handleTenantByID)))
}

func (h *Handler) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createTask(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *Handler) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /tasks/{task_id}, /tasks/{task_id}/abort, /tasks/{task_id}/resume,
	// /tasks/{task_id}/runs/{run_id}/steps
	path := strings.TrimPrefix(r.URL.Path, "/tasks/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing task_id"})
		return
	}

	taskID := parts[0]

	if len(parts) == 1 {
		// GET /tasks/{task_id}
		if r.Method == http.MethodGet {
			h.getTask(w, r, taskID)
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	switch parts[1] {
	case "abort":
		if r.Method == http.MethodPost {
			h.abortTask(w, r, taskID)
			return
		}
	case "resume":
		if r.Method == http.MethodPost {
			h.resumeTask(w, r, taskID)
			return
		}
	case "runs":
		if len(parts) == 3 && r.Method == http.MethodGet {
			h.getRun(w, r, taskID, parts[2])
			return
		}
		if len(parts) >= 5 && parts[3] == "events" && parts[4] == "replay" && r.Method == http.MethodGet {
			h.replayEvents(w, r, taskID, parts[2])
			return
		}
		if len(parts) >= 5 && parts[3] == "events" && parts[4] == "compact" && r.Method == http.MethodPost {
			h.compactEvents(w, r, taskID, parts[2])
			return
		}
		if len(parts) >= 4 && parts[3] == "steps" && r.Method == http.MethodGet {
			h.listSteps(w, r, taskID, parts[2])
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *Handler) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /tenants/{tenant_id}/runtime or /tenants/{tenant_id}/alerts
	path := strings.TrimPrefix(r.URL.Path, "/tenants/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	tenantID := parts[0]
	switch parts[1] {
	case "runtime":
		if r.Method == http.MethodGet {
			h.getTenantRuntime(w, r, tenantID)
			return
		}
	case "alerts":
		if r.Method == http.MethodGet {
			h.getTenantAlerts(w, r, tenantID)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenant(r.Context())

	var body struct {
		Prompt         string      `json:"prompt"`
		ModelConfig    interface{} `json:"model_config,omitempty"`
		IdempotencyKey string      `json:"idempotency_key,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if body.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}

	// Also check header for idempotency key.
	if ik := r.Header.Get("Idempotency-Key"); ik != "" && body.IdempotencyKey == "" {
		body.IdempotencyKey = ik
	}

	// Parse model_config into the typed struct if present.
	var mc *model.ModelConfig
	if body.ModelConfig != nil {
		raw, err := json.Marshal(body.ModelConfig)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model_config"})
			return
		}
		mc = &model.ModelConfig{}
		if err := json.Unmarshal(raw, mc); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model_config format"})
			return
		}
	}

	req := &task.CreateRequest{
		TenantID:       tenant.TenantID,
		UserID:         tenant.UserID,
		Prompt:         body.Prompt,
		ModelConfig:    mc,
		IdempotencyKey: body.IdempotencyKey,
	}

	resp, err := h.svc.Create(r.Context(), req)
	if err != nil {
		if errors.Is(err, task.ErrValidation) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid create request"})
			return
		}
		writeInternalError(w, tenant.RequestID)
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request, taskID string) {
	tenant := GetTenant(r.Context())
	t, err := h.svc.GetForUser(r.Context(), tenant.TenantID, tenant.UserID, taskID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			writeInternalError(w, tenant.RequestID)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) abortTask(w http.ResponseWriter, r *http.Request, taskID string) {
	tenant := GetTenant(r.Context())

	var body struct {
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	t, err := h.svc.Abort(r.Context(), tenant.TenantID, taskID, &task.AbortRequest{
		TenantID: tenant.TenantID,
		UserID:   tenant.UserID,
		Reason:   body.Reason,
	})
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
		writeInternalError(w, tenant.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) resumeTask(w http.ResponseWriter, r *http.Request, taskID string) {
	tenant := GetTenant(r.Context())

	var body struct {
		FromRunID           string             `json:"from_run_id"`
		FromStepIndex       int                `json:"from_step_index"`
		ModelConfigOverride *model.ModelConfig `json:"model_config_override,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if body.FromRunID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from_run_id is required"})
		return
	}

	resp, err := h.svc.Resume(r.Context(), taskID, &task.ResumeRequest{
		TenantID:            tenant.TenantID,
		UserID:              tenant.UserID,
		FromRunID:           body.FromRunID,
		FromStepIndex:       body.FromStepIndex,
		ModelConfigOverride: body.ModelConfigOverride,
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		case errors.Is(err, task.ErrValidation):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid resume request"})
		default:
			writeInternalError(w, tenant.RequestID)
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listSteps(w http.ResponseWriter, r *http.Request, taskID, runID string) {
	tenant := GetTenant(r.Context())

	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if from < 0 {
		from = 0
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	steps, err := h.svc.ListStepsForUser(r.Context(), tenant.TenantID, tenant.UserID, taskID, runID, from, limit)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task or run not found"})
			return
		}
		writeInternalError(w, tenant.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"steps": steps})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request, taskID, runID string) {
	tenant := GetTenant(r.Context())
	run, err := h.svc.GetRunForUser(r.Context(), tenant.TenantID, tenant.UserID, taskID, runID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			writeInternalError(w, tenant.RequestID)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) replayEvents(w http.ResponseWriter, r *http.Request, taskID, runID string) {
	tenant := GetTenant(r.Context())
	fromSeq, _ := strconv.ParseInt(r.URL.Query().Get("from_seq"), 10, 64)
	fromTS, _ := strconv.ParseInt(r.URL.Query().Get("from_ts"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	events, err := h.svc.ReplayEventsForUser(r.Context(), tenant.TenantID, tenant.UserID, taskID, runID, fromSeq, fromTS, limit)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task or run not found"})
			return
		}
		writeInternalError(w, tenant.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events})
}

func (h *Handler) compactEvents(w http.ResponseWriter, r *http.Request, taskID, runID string) {
	tenant := GetTenant(r.Context())
	var body struct {
		BeforeTS int64 `json:"before_ts"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	removed, err := h.svc.CompactRunEventsForUser(r.Context(), tenant.TenantID, tenant.UserID, taskID, runID, body.BeforeTS)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task or run not found"})
			return
		}
		writeInternalError(w, tenant.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"removed": removed})
}

func (h *Handler) getTenantRuntime(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenantCtx := GetTenant(r.Context())
	if tenantCtx.TenantID != tenantID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	if h.tenantRuntime == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "tenant runtime metrics not configured"})
		return
	}
	snap := h.tenantRuntime.TenantSnapshot(tenantID)
	if snap.TenantID == "" {
		snap.TenantID = tenantID
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *Handler) getTenantAlerts(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenantCtx := GetTenant(r.Context())
	if tenantCtx.TenantID != tenantID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	if h.tenantRuntime == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "tenant alerts not configured"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts": h.tenantRuntime.TenantAlerts(tenantID, limit),
	})
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

func writeInternalError(w http.ResponseWriter, requestID string) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error":      "internal server error",
		"request_id": requestID,
	})
}
