package stream

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
)

// ErrConnectionGone is a sentinel error indicating the WebSocket connection
// no longer exists (HTTP 410 Gone from API Gateway).
var ErrConnectionGone = &GoneError{}

// GoneError indicates a connection has been removed.
type GoneError struct{}

func (e *GoneError) Error() string { return "connection gone (410)" }

// IsGoneError checks if an error indicates a 410 Gone connection.
// It matches the sentinel GoneError and common AWS SDK error patterns.
func IsGoneError(err error) bool {
	if err == nil {
		return false
	}
	// Match our sentinel type.
	if _, ok := err.(*GoneError); ok {
		return true
	}
	// Match common AWS SDK error patterns for GoneException / 410 status.
	msg := err.Error()
	return strings.Contains(msg, "GoneException") || strings.Contains(msg, "410")
}

// AWSPusher sends events via API Gateway Management API (PostToConnection).
// In production, this would use the AWS SDK v2 apigatewaymanagementapi client.
// This implementation provides the structure; actual AWS calls require deployment.
type AWSPusher struct {
	// endpoint is the API Gateway WebSocket management endpoint.
	endpoint string
	// postFunc is the actual PostToConnection implementation.
	// In production: apigatewaymanagementapi.Client.PostToConnection
	// For testing: can be replaced with a mock.
	postFunc func(ctx context.Context, connectionID string, data []byte) error
}

// NewAWSPusher creates a new AWS API Gateway pusher.
// postFunc should wrap apigatewaymanagementapi.Client.PostToConnection.
func NewAWSPusher(endpoint string, postFunc func(ctx context.Context, connectionID string, data []byte) error) *AWSPusher {
	return &AWSPusher{
		endpoint: endpoint,
		postFunc: postFunc,
	}
}

func (p *AWSPusher) Push(ctx context.Context, connectionID string, event *model.StreamEvent) (bool, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return false, err
	}

	if err := p.postFunc(ctx, connectionID, data); err != nil {
		if IsGoneError(err) {
			// Connection stale — return alive=false so caller can clean up.
			return false, nil
		}
		// Transient error — return alive=true so connection is NOT removed.
		return true, err
	}
	return true, nil
}
