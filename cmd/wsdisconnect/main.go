// Package main implements the WebSocket $disconnect Lambda handler.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	appcfg "github.com/agentforge/agentforge/pkg/config"
	"github.com/agentforge/agentforge/pkg/state"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// APIGatewayWebSocketRequest represents the incoming API Gateway WebSocket event.
type APIGatewayWebSocketRequest struct {
	RequestContext struct {
		ConnectionID string `json:"connectionId"`
		RouteKey     string `json:"routeKey"`
	} `json:"requestContext"`
}

// APIGatewayResponse is the response format for API Gateway.
type APIGatewayResponse struct {
	StatusCode int    `json:"statusCode"`
	Body       string `json:"body"`
}

// handler processes the $disconnect event.
func handler(ctx context.Context, store state.ConnectionStore, event APIGatewayWebSocketRequest) (*APIGatewayResponse, error) {
	connID := event.RequestContext.ConnectionID
	if connID == "" {
		return &APIGatewayResponse{StatusCode: 400, Body: `{"error":"missing connectionId"}`}, nil
	}

	if err := store.DeleteConnection(ctx, connID); err != nil {
		log.Printf("ERROR: DeleteConnection failed: %v", err)
		return &APIGatewayResponse{StatusCode: 500, Body: `{"error":"internal"}`}, nil
	}

	log.Printf("Disconnected: conn=%s", connID)
	return &APIGatewayResponse{StatusCode: 200, Body: `{"status":"disconnected"}`}, nil
}

func main() {
	store, err := initStore(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	// Demo: process a sample event from stdin.
	var event APIGatewayWebSocketRequest
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		fmt.Println("WebSocket $disconnect Lambda handler")
		fmt.Println("Usage: echo '{...}' | go run cmd/wsdisconnect/main.go")
		return
	}

	resp, err := handler(context.Background(), store, event)
	if err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}

func initStore(ctx context.Context) (state.ConnectionStore, error) {
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

	return state.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), state.DynamoStoreConfig{
		TasksTable:       stateCfg.TasksTable,
		RunsTable:        stateCfg.RunsTable,
		StepsTable:       stateCfg.StepsTable,
		ConnectionsTable: stateCfg.ConnectionsTable,
		ConnectionIndex:  stateCfg.ConnectionIndex,
	})
}
