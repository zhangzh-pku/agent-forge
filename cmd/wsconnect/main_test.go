package main

import (
	"context"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
)

func newConnectEvent() APIGatewayWebSocketRequest {
	var ev APIGatewayWebSocketRequest
	ev.RequestContext.ConnectionID = "conn_1"
	ev.QueryStringParameters = map[string]string{}
	ev.Headers = map[string]string{}
	ev.RequestContext.Authorizer.JWT.Claims = map[string]string{}
	ev.RequestContext.Authorizer.Lambda = map[string]interface{}{}
	return ev
}

func TestHandlerRejectsQueryHeaderIdentity(t *testing.T) {
	store := state.NewMemoryStore()
	ev := newConnectEvent()
	ev.QueryStringParameters["tenant_id"] = "tnt_1"
	ev.QueryStringParameters["user_id"] = "user_1"
	ev.Headers["x-tenant-id"] = "tnt_1"
	ev.Headers["x-user-id"] = "user_1"

	resp, err := handler(context.Background(), store, ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHandlerUsesAuthorizerClaimsAndEnforcesUserOwnership(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	now := time.Now().UTC()
	if err := store.PutTask(ctx, &model.Task{
		TaskID:      "task_1",
		TenantID:    "tnt_1",
		UserID:      "user_1",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_1",
		Prompt:      "test",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("put task: %v", err)
	}

	// Same tenant but wrong user should be rejected.
	evForbidden := newConnectEvent()
	evForbidden.QueryStringParameters["task_id"] = "task_1"
	evForbidden.RequestContext.Authorizer.JWT.Claims["tenant_id"] = "tnt_1"
	evForbidden.RequestContext.Authorizer.JWT.Claims["sub"] = "user_2"

	resp, err := handler(ctx, store, evForbidden)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}

	// Correct tenant and user should pass.
	evOK := newConnectEvent()
	evOK.QueryStringParameters["task_id"] = "task_1"
	evOK.RequestContext.Authorizer.JWT.Claims["tenant_id"] = "tnt_1"
	evOK.RequestContext.Authorizer.JWT.Claims["sub"] = "user_1"

	resp, err = handler(ctx, store, evOK)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	conn, err := store.GetConnection(ctx, "conn_1")
	if err != nil {
		t.Fatalf("expected stored connection: %v", err)
	}
	if conn.TenantID != "tnt_1" || conn.UserID != "user_1" {
		t.Fatalf("unexpected connection identity: tenant=%s user=%s", conn.TenantID, conn.UserID)
	}
}
