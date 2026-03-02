// Package main implements the WebSocket $connect Lambda handler.
//
// In AWS, this is deployed as a Lambda function behind API Gateway WebSocket
// $connect route. It extracts connection context from the API Gateway event,
// validates the tenant, and registers the connection in DynamoDB.
//
// In local mode, WebSocket connect is handled by the taskapi server's
// /ws/connect HTTP endpoint instead.
//
// Build for Lambda:
//
//	GOOS=linux GOARCH=arm64 go build -o bootstrap cmd/wsconnect/main.go
//	zip ws-connect.zip bootstrap
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
)

// APIGatewayWebSocketRequest represents the incoming API Gateway WebSocket event.
// In production, use github.com/aws/aws-lambda-go/events.APIGatewayWebsocketProxyRequest.
type APIGatewayWebSocketRequest struct {
	RequestContext struct {
		ConnectionID string `json:"connectionId"`
		RouteKey     string `json:"routeKey"`
	} `json:"requestContext"`
	QueryStringParameters map[string]string `json:"queryStringParameters"`
	Headers               map[string]string `json:"headers"`
}

// APIGatewayResponse is the response format for API Gateway.
type APIGatewayResponse struct {
	StatusCode int    `json:"statusCode"`
	Body       string `json:"body"`
}

// handler processes the $connect event.
func handler(ctx context.Context, store state.Store, event APIGatewayWebSocketRequest) (*APIGatewayResponse, error) {
	connID := event.RequestContext.ConnectionID
	if connID == "" {
		return &APIGatewayResponse{StatusCode: 400, Body: `{"error":"missing connectionId"}`}, nil
	}

	// Extract tenant from query params or headers.
	tenantID := event.QueryStringParameters["tenant_id"]
	if tenantID == "" {
		tenantID = event.Headers["x-tenant-id"]
	}
	if tenantID == "" {
		return &APIGatewayResponse{StatusCode: 401, Body: `{"error":"tenant_id required"}`}, nil
	}

	userID := event.QueryStringParameters["user_id"]
	if userID == "" {
		userID = event.Headers["x-user-id"]
	}
	if userID == "" {
		userID = "anonymous"
	}

	taskID := event.QueryStringParameters["task_id"]
	runID := event.QueryStringParameters["run_id"]

	// Validate task ownership: ensure the tenant actually owns this task.
	if taskID != "" {
		task, err := store.GetTask(ctx, taskID)
		if err != nil {
			return &APIGatewayResponse{StatusCode: 404, Body: `{"error":"task not found"}`}, nil
		}
		if task.TenantID != tenantID {
			return &APIGatewayResponse{StatusCode: 403, Body: `{"error":"forbidden"}`}, nil
		}
	}

	conn := &model.Connection{
		ConnectionID: connID,
		TenantID:     tenantID,
		UserID:       userID,
		TaskID:       taskID,
		RunID:        runID,
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}

	if err := store.PutConnection(ctx, conn); err != nil {
		log.Printf("ERROR: PutConnection failed: %v", err)
		return &APIGatewayResponse{StatusCode: 500, Body: `{"error":"internal"}`}, nil
	}

	log.Printf("Connected: conn=%s tenant=%s task=%s", connID, tenantID, taskID)
	return &APIGatewayResponse{StatusCode: 200, Body: `{"status":"connected"}`}, nil
}

func main() {
	// In production, replace this with:
	//   import "github.com/aws/aws-lambda-go/lambda"
	//   store := dynamodb.NewStore(...)
	//   lambda.Start(func(ctx context.Context, event APIGatewayWebSocketRequest) (*APIGatewayResponse, error) {
	//       return handler(ctx, store, event)
	//   })
	//
	// For local development, the taskapi server handles /ws/connect directly.

	_ = os.Getenv("CONNECTIONS_TABLE") // Would be used for DynamoDB store init.
	_ = os.Getenv("TASKS_TABLE")       // Would be used for DynamoDB task store init.

	// Demo: process a sample event from stdin (useful for testing).
	var store state.Store = state.NewMemoryStore()
	var event APIGatewayWebSocketRequest
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		fmt.Println("WebSocket $connect Lambda handler")
		fmt.Println("Usage: echo '{...}' | go run cmd/wsconnect/main.go")
		fmt.Println("In local mode, use the taskapi server's /ws/connect endpoint.")
		return
	}

	resp, err := handler(context.Background(), store, event)
	if err != nil {
		log.Fatal(err)
	}
	json.NewEncoder(os.Stdout).Encode(resp)
}
