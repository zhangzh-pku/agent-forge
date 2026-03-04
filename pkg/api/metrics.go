package api

import "sync/atomic"

type requestMetrics struct {
	requestsTotal  atomic.Int64
	latencyMSTotal atomic.Int64

	status1xx atomic.Int64
	status2xx atomic.Int64
	status3xx atomic.Int64
	status4xx atomic.Int64
	status5xx atomic.Int64

	errors5xxTotal atomic.Int64
}

var globalRequestMetrics requestMetrics

// RequestMetricsSnapshot is a point-in-time view of API request counters.
type RequestMetricsSnapshot struct {
	RequestsTotal  int64
	LatencyMSTotal int64
	Status1xx      int64
	Status2xx      int64
	Status3xx      int64
	Status4xx      int64
	Status5xx      int64
	Errors5xxTotal int64
}

func observeRequestMetrics(status int, latencyMS int64) {
	globalRequestMetrics.requestsTotal.Add(1)
	globalRequestMetrics.latencyMSTotal.Add(latencyMS)

	switch {
	case status >= 500:
		globalRequestMetrics.status5xx.Add(1)
		globalRequestMetrics.errors5xxTotal.Add(1)
	case status >= 400:
		globalRequestMetrics.status4xx.Add(1)
	case status >= 300:
		globalRequestMetrics.status3xx.Add(1)
	case status >= 200:
		globalRequestMetrics.status2xx.Add(1)
	default:
		globalRequestMetrics.status1xx.Add(1)
	}
}

// SnapshotRequestMetrics returns an atomic snapshot for /metrics export.
func SnapshotRequestMetrics() RequestMetricsSnapshot {
	return RequestMetricsSnapshot{
		RequestsTotal:  globalRequestMetrics.requestsTotal.Load(),
		LatencyMSTotal: globalRequestMetrics.latencyMSTotal.Load(),
		Status1xx:      globalRequestMetrics.status1xx.Load(),
		Status2xx:      globalRequestMetrics.status2xx.Load(),
		Status3xx:      globalRequestMetrics.status3xx.Load(),
		Status4xx:      globalRequestMetrics.status4xx.Load(),
		Status5xx:      globalRequestMetrics.status5xx.Load(),
		Errors5xxTotal: globalRequestMetrics.errors5xxTotal.Load(),
	}
}

func resetRequestMetricsForTest() {
	globalRequestMetrics.requestsTotal.Store(0)
	globalRequestMetrics.latencyMSTotal.Store(0)
	globalRequestMetrics.status1xx.Store(0)
	globalRequestMetrics.status2xx.Store(0)
	globalRequestMetrics.status3xx.Store(0)
	globalRequestMetrics.status4xx.Store(0)
	globalRequestMetrics.status5xx.Store(0)
	globalRequestMetrics.errors5xxTotal.Store(0)
}
