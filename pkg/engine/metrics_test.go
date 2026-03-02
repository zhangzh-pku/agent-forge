package engine

import "testing"

func TestMetrics(t *testing.T) {
	m := NewMetrics()

	m.TaskStarted()
	m.TaskStarted()
	m.TaskSucceeded()
	m.TaskFailed()
	m.TaskAborted()
	m.StepLatency(150)
	m.StepLatency(250)
	m.StreamPushError()

	snap := m.Snapshot()

	if snap.TaskStarted != 2 {
		t.Fatalf("task_started: expected 2, got %d", snap.TaskStarted)
	}
	if snap.TaskSucceeded != 1 {
		t.Fatalf("task_succeeded: expected 1, got %d", snap.TaskSucceeded)
	}
	if snap.TaskFailed != 1 {
		t.Fatalf("task_failed: expected 1, got %d", snap.TaskFailed)
	}
	if snap.TaskAborted != 1 {
		t.Fatalf("task_aborted: expected 1, got %d", snap.TaskAborted)
	}
	if snap.StepLatencyTotal != 400 {
		t.Fatalf("step_latency_total: expected 400, got %d", snap.StepLatencyTotal)
	}
	if snap.StepCount != 2 {
		t.Fatalf("step_count: expected 2, got %d", snap.StepCount)
	}
	if snap.StreamPushErrors != 1 {
		t.Fatalf("stream_push_errors: expected 1, got %d", snap.StreamPushErrors)
	}
}
