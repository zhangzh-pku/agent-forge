package engine

import "context"

// ToolResult is the output of a tool invocation.
type ToolResult struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Tool is a single executable tool available to the agent.
type Tool interface {
	// Name returns the tool's identifier (e.g. "fs.read").
	Name() string
	// Description returns a brief description for the LLM.
	Description() string
	// Execute runs the tool with JSON-encoded args. Must respect context cancellation.
	Execute(ctx context.Context, args string) (*ToolResult, error)
}

// ToolRegistry manages available tools.
type ToolRegistry interface {
	// Register adds a tool.
	Register(tool Tool)
	// Get returns a tool by name, or nil if not found.
	Get(name string) Tool
	// List returns all registered tool names.
	List() []string
}
