package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/task"
)

// maxRequestBodySize is the maximum allowed request body size (1 MB).
const maxRequestBodySize = 1 << 20

// Handler holds HTTP handler dependencies.
type Handler struct {
	svc *task.Service
}

// NewHandler creates a new API handler.
func NewHandler(svc *task.Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/tasks", AuthMiddleware(http.HandlerFunc(h.handleTasks)))
	mux.Handle("/tasks/", AuthMiddleware(http.HandlerFunc(h.handleTaskByID)))
}

func (h *Handler) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.createTask(w, r)
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /tasks/{task_id}, /tasks/{task_id}/abort, /tasks/{task_id}/resume,
	// /tasks/{task_id}/runs/{run_id}/steps
	path := strings.TrimPrefix(r.URL.Path, "/tasks/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"missing task_id"}`, http.StatusBadRequest)
		return
	}

	taskID := parts[0]

	if len(parts) == 1 {
		// GET /tasks/{task_id}
		if r.Method == http.MethodGet {
			h.getTask(w, r, taskID)
			return
		}
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
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
		if len(parts) >= 4 && parts[3] == "steps" && r.Method == http.MethodGet {
			h.listSteps(w, r, taskID, parts[2])
			return
		}
	}

	http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request, taskID string) {
	tenant := GetTenant(r.Context())
	t, err := h.svc.Get(r.Context(), tenant.TenantID, taskID)
	if err != nil {
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
		Reason:   body.Reason,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		FromRunID:           body.FromRunID,
		FromStepIndex:       body.FromStepIndex,
		ModelConfigOverride: body.ModelConfigOverride,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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

	steps, err := h.svc.ListSteps(r.Context(), tenant.TenantID, taskID, runID, from, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"steps": steps})
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
