package model

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
	Role    string `json:"role"`
	Content string `json:"content"`
}
