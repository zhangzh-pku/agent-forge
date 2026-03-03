package state

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestNewDynamoStoreRejectsNegativeEventRetention(t *testing.T) {
	_, err := NewDynamoStore(&dynamodb.Client{}, DynamoStoreConfig{
		TasksTable:       "tasks",
		RunsTable:        "runs",
		StepsTable:       "steps",
		ConnectionsTable: "connections",
		EventRetention:   -time.Second,
	})
	if err == nil {
		t.Fatal("expected error for negative event retention")
	}
}

func TestNewDynamoStoreAllowsZeroEventRetention(t *testing.T) {
	store, err := NewDynamoStore(&dynamodb.Client{}, DynamoStoreConfig{
		TasksTable:       "tasks",
		RunsTable:        "runs",
		StepsTable:       "steps",
		ConnectionsTable: "connections",
		EventRetention:   0,
	})
	if err != nil {
		t.Fatalf("NewDynamoStore error: %v", err)
	}
	if store.eventRetention != 0 {
		t.Fatalf("expected eventRetention=0, got %s", store.eventRetention)
	}
}
