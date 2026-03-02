package model

// StreamEventType classifies stream events.
type StreamEventType string

const (
	StreamEventTokenChunk StreamEventType = "token_chunk"
	StreamEventStepStart  StreamEventType = "step_start"
	StreamEventStepEnd    StreamEventType = "step_end"
	StreamEventToolCall   StreamEventType = "tool_call"
	StreamEventToolResult StreamEventType = "tool_result"
	StreamEventComplete   StreamEventType = "complete"
	StreamEventError      StreamEventType = "error"
)

// StreamEvent is the envelope for all WebSocket push events.
type StreamEvent struct {
	TaskID string          `json:"task_id"`
	RunID  string          `json:"run_id"`
	Seq    int64           `json:"seq"`
	TS     int64           `json:"ts"`
	Type   StreamEventType `json:"type"`
	Data   interface{}     `json:"data,omitempty"`
}
