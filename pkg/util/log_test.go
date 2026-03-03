package util

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestLoggerIncludesTimestampAndFields(t *testing.T) {
	var buf bytes.Buffer
	log := NewLoggerWithWriter(&buf).With("component", "test")
	log.Info("hello", map[string]interface{}{"k": "v"})

	var entry map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("decode log json: %v", err)
	}
	if entry["level"] != "info" {
		t.Fatalf("unexpected level: %v", entry["level"])
	}
	if entry["msg"] != "hello" {
		t.Fatalf("unexpected msg: %v", entry["msg"])
	}
	if entry["component"] != "test" {
		t.Fatalf("unexpected component field: %v", entry["component"])
	}
	if _, ok := entry["ts"]; !ok {
		t.Fatal("expected ts field")
	}
}
