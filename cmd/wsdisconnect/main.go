// Package main implements the WebSocket $disconnect Lambda handler.
//
// In AWS, this is deployed as a Lambda function behind API Gateway WebSocket
// $disconnect route. It removes the connection record from DynamoDB.
//
// In local mode, WebSocket disconnect is handled by the taskapi server's
// /ws/disconnect HTTP endpoint instead.
//
// Build for Lambda:
//
//	GOOS=linux GOARCH=arm64 go build -o bootstrap cmd/wsdisconnect/main.go
//	zip ws-disconnect.zip bootstrap
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/agentforge/agentforge/pkg/state"
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
	// In production, replace this with:
	//   import "github.com/aws/aws-lambda-go/lambda"
	//   store := dynamodb.NewStore(...)
	//   lambda.Start(func(ctx context.Context, event APIGatewayWebSocketRequest) (*APIGatewayResponse, error) {
	//       return handler(ctx, store, event)
	//   })

	_ = os.Getenv("CONNECTIONS_TABLE")

	// Demo: process a sample event from stdin.
	store := state.NewMemoryStore()
	var event APIGatewayWebSocketRequest
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		fmt.Println("WebSocket $disconnect Lambda handler")
		fmt.Println("Usage: echo '{...}' | go run cmd/wsdisconnect/main.go")
		fmt.Println("In local mode, use the taskapi server's /ws/disconnect endpoint.")
		return
	}

	resp, err := handler(context.Background(), store, event)
	if err != nil {
		log.Fatal(err)
	}
	json.NewEncoder(os.Stdout).Encode(resp)
}
