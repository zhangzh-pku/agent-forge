package state

import (
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

// ensureQueuedAt returns a run value suitable for persistence. If a run is in
// RUN_QUEUED and queued_at is missing, it sets queued_at to now.
func ensureQueuedAt(run *model.Run, now time.Time) *model.Run {
	if run == nil || run.Status != model.RunStatusQueued || run.QueuedAt != nil {
		return run
	}
	cp := *run
	ts := now.UTC()
	cp.QueuedAt = &ts
	return &cp
}
