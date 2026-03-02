package engine

import "sync"

// DefaultRegistry is a simple in-memory tool registry.
type DefaultRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new tool registry.
func NewRegistry() *DefaultRegistry {
	return &DefaultRegistry{tools: make(map[string]Tool)}
}

func (r *DefaultRegistry) Register(tool Tool) {
	r.mu.Lock()
	r.tools[tool.Name()] = tool
	r.mu.Unlock()
}

func (r *DefaultRegistry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

func (r *DefaultRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
