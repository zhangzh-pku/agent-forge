package engine

import "sync/atomic"

// Metrics tracks basic execution metrics.
// In production, these counters can be emitted to CloudWatch, Prometheus,
// or OpenTelemetry. The v1 implementation uses atomic counters that can be
// read by a metrics exporter goroutine.
type Metrics struct {
	taskStarted      atomic.Int64
	taskSucceeded    atomic.Int64
	taskFailed       atomic.Int64
	taskAborted      atomic.Int64
	stepLatencyTotal atomic.Int64 // cumulative latency in ms
	stepCount        atomic.Int64 // total steps (for computing average)
	streamPushErrors atomic.Int64
}

// NewMetrics creates a new metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// TaskStarted increments the task_started counter.
func (m *Metrics) TaskStarted() { m.taskStarted.Add(1) }

// TaskSucceeded increments the task_succeeded counter.
func (m *Metrics) TaskSucceeded() { m.taskSucceeded.Add(1) }

// TaskFailed increments the task_failed counter.
func (m *Metrics) TaskFailed() { m.taskFailed.Add(1) }

// TaskAborted increments the task_aborted counter.
func (m *Metrics) TaskAborted() { m.taskAborted.Add(1) }

// StepLatency records a step's latency in milliseconds.
func (m *Metrics) StepLatency(ms int64) {
	m.stepLatencyTotal.Add(ms)
	m.stepCount.Add(1)
}

// StreamPushError increments the stream_push_errors counter.
func (m *Metrics) StreamPushError() { m.streamPushErrors.Add(1) }

// Snapshot returns a point-in-time copy of all metric values.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		TaskStarted:      m.taskStarted.Load(),
		TaskSucceeded:    m.taskSucceeded.Load(),
		TaskFailed:       m.taskFailed.Load(),
		TaskAborted:      m.taskAborted.Load(),
		StepLatencyTotal: m.stepLatencyTotal.Load(),
		StepCount:        m.stepCount.Load(),
		StreamPushErrors: m.streamPushErrors.Load(),
	}
}

// MetricsSnapshot is a point-in-time copy of all metric counters.
type MetricsSnapshot struct {
	TaskStarted      int64 `json:"task_started"`
	TaskSucceeded    int64 `json:"task_succeeded"`
	TaskFailed       int64 `json:"task_failed"`
	TaskAborted      int64 `json:"task_aborted"`
	StepLatencyTotal int64 `json:"step_latency_total_ms"`
	StepCount        int64 `json:"step_count"`
	StreamPushErrors int64 `json:"stream_push_errors"`
}
