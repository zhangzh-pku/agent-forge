package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/task"
)

// WSHandler handles WebSocket connect/disconnect events.
type WSHandler struct {
	connStore state.ConnectionStore
	taskSvc   *task.Service
}

// NewWSHandler creates a new WebSocket handler.
// taskSvc is used to validate that the requesting tenant owns the task being subscribed to.
func NewWSHandler(connStore state.ConnectionStore, taskSvc *task.Service) *WSHandler {
	return &WSHandler{connStore: connStore, taskSvc: taskSvc}
}

// RegisterRoutes registers WebSocket management routes.
func (h *WSHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/ws/connect", AuthMiddleware(http.HandlerFunc(h.handleConnect)))
	mux.Handle("/ws/disconnect", AuthMiddleware(http.HandlerFunc(h.handleDisconnect)))
}

func (h *WSHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenant(r.Context())

	var body struct {
		ConnectionID string `json:"connection_id"`
		TaskID       string `json:"task_id"`
		RunID        string `json:"run_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	if body.ConnectionID == "" || body.TaskID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connection_id and task_id required"})
		return
	}

	// Validate that the task belongs to the requesting tenant.
	if h.taskSvc != nil {
		if _, err := h.taskSvc.GetForUser(r.Context(), tenant.TenantID, tenant.UserID, body.TaskID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
	}

	conn := &model.Connection{
		ConnectionID: body.ConnectionID,
		TenantID:     tenant.TenantID,
		UserID:       tenant.UserID,
		TaskID:       body.TaskID,
		RunID:        body.RunID,
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}

	if err := h.connStore.PutConnection(r.Context(), conn); err != nil {
		writeInternalError(w, tenant.RequestID)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (h *WSHandler) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	tenant := GetTenant(r.Context())

	var body struct {
		ConnectionID string `json:"connection_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	if body.ConnectionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "connection_id required"})
		return
	}

	// Validate the connection belongs to the requesting tenant.
	conn, err := h.connStore.GetConnection(r.Context(), body.ConnectionID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "connection not found"})
		return
	}
	if conn.TenantID != tenant.TenantID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "connection not found"})
		return
	}

	if err := h.connStore.DeleteConnection(r.Context(), body.ConnectionID); err != nil {
		writeInternalError(w, tenant.RequestID)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
}
