// Package main implements the WebSocket $connect Lambda handler.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	appcfg "github.com/agentforge/agentforge/internal/config"
	"github.com/agentforge/agentforge/internal/telemetry"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// APIGatewayWebSocketRequest represents the incoming API Gateway WebSocket event.
// In production, use github.com/aws/aws-lambda-go/events.APIGatewayWebsocketProxyRequest.
type APIGatewayWebSocketRequest struct {
	RequestContext struct {
		ConnectionID string `json:"connectionId"`
		RouteKey     string `json:"routeKey"`
		Authorizer   struct {
			JWT struct {
				Claims map[string]string `json:"claims"`
			} `json:"jwt"`
			Lambda map[string]interface{} `json:"lambda"`
		} `json:"authorizer"`
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

	tenantID, userID := extractTrustedIdentity(event)
	if tenantID == "" || userID == "" {
		return &APIGatewayResponse{StatusCode: 401, Body: `{"error":"authenticated tenant/user identity required"}`}, nil
	}

	taskID := event.QueryStringParameters["task_id"]
	runID := event.QueryStringParameters["run_id"]

	// Validate task ownership: ensure the authenticated tenant/user owns this task.
	if taskID != "" {
		task, err := store.GetTask(ctx, taskID)
		if err != nil {
			return &APIGatewayResponse{StatusCode: 404, Body: `{"error":"task not found"}`}, nil
		}
		if task.TenantID != tenantID {
			return &APIGatewayResponse{StatusCode: 403, Body: `{"error":"forbidden"}`}, nil
		}
		if task.UserID != "" && task.UserID != userID {
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

	log.Printf("Connected: conn=%s tenant=%s user=%s task=%s", connID, tenantID, userID, taskID)
	return &APIGatewayResponse{StatusCode: 200, Body: `{"status":"connected"}`}, nil
}

func extractTrustedIdentity(event APIGatewayWebSocketRequest) (tenantID, userID string) {
	tenantID = firstNonEmpty(
		event.RequestContext.Authorizer.JWT.Claims["tenant_id"],
		event.RequestContext.Authorizer.JWT.Claims["tenantId"],
		event.RequestContext.Authorizer.JWT.Claims["custom:tenant_id"],
		lambdaClaimString(event.RequestContext.Authorizer.Lambda, "tenant_id", "tenantId", "custom:tenant_id"),
	)
	userID = firstNonEmpty(
		event.RequestContext.Authorizer.JWT.Claims["sub"],
		event.RequestContext.Authorizer.JWT.Claims["user_id"],
		event.RequestContext.Authorizer.JWT.Claims["userId"],
		event.RequestContext.Authorizer.JWT.Claims["cognito:username"],
		lambdaClaimString(event.RequestContext.Authorizer.Lambda, "sub", "user_id", "userId", "username", "cognito:username"),
	)
	return tenantID, userID
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func lambdaClaimString(claims map[string]interface{}, keys ...string) string {
	if len(claims) == 0 {
		return ""
	}
	for _, k := range keys {
		v, ok := claims[k]
		if !ok || v == nil {
			continue
		}
		switch s := v.(type) {
		case string:
			if strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		case json.Number:
			str := strings.TrimSpace(s.String())
			if str != "" {
				return str
			}
		}
	}
	return ""
}

func main() {
	telemetryCfg, err := appcfg.LoadTelemetryRuntimeConfigFromEnv("agentforge-ws-connect")
	if err != nil {
		log.Fatalf("failed to load telemetry config: %v", err)
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetryCfg)
	if err != nil {
		log.Fatalf("failed to initialize telemetry: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			log.Printf("telemetry shutdown error: %v", err)
		}
	}()

	store, err := initStore(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	// Demo: process a sample event from stdin (useful for local testing).
	var event APIGatewayWebSocketRequest
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		fmt.Println("WebSocket $connect Lambda handler")
		fmt.Println("Usage: echo '{...}' | go run cmd/wsconnect/main.go")
		return
	}

	resp, err := handler(context.Background(), store, event)
	if err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}

func initStore(ctx context.Context) (state.Store, error) {
	if appcfg.RuntimeModeFromEnv() != appcfg.RuntimeModeAWS {
		return state.NewMemoryStore(), nil
	}

	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	stateCfg, err := appcfg.LoadAWSStateConfigFromEnv()
	if err != nil {
		return nil, err
	}
	eventRetention, err := appcfg.EventRetentionFromEnv()
	if err != nil {
		return nil, err
	}

	return state.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), state.DynamoStoreConfig{
		TasksTable:       stateCfg.TasksTable,
		RunsTable:        stateCfg.RunsTable,
		StepsTable:       stateCfg.StepsTable,
		ConnectionsTable: stateCfg.ConnectionsTable,
		ConnectionIndex:  stateCfg.ConnectionIndex,
		EventRetention:   eventRetention,
	})
}
