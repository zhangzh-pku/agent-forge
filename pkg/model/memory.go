package model

// MessageRole represents a memory message role.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

// MemoryToolCall represents an assistant-requested tool invocation.
type MemoryToolCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// MemorySnapshot holds the agent's working memory at a checkpoint.
type MemorySnapshot struct {
	RunID      string                 `json:"run_id"`
	StepIndex  int                    `json:"step_index"`
	Messages   []MemoryMessage        `json:"messages"`
	Scratchpad string                 `json:"scratchpad,omitempty"`
	ToolState  map[string]interface{} `json:"tool_state,omitempty"`
}

// MemoryMessage is a single message in the agent's conversation history.
type MemoryMessage struct {
	Role       MessageRole      `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []MemoryToolCall `json:"tool_calls,omitempty"`
}
