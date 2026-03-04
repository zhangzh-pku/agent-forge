package runtimemetrics

import "sync/atomic"

type counters struct {
	claimConflicts   atomic.Int64
	finalizeFailures atomic.Int64
	streamPushErrors atomic.Int64
	recoveryRuns     atomic.Int64
	recoveryRequeued atomic.Int64
	recoveryErrors   atomic.Int64
}

var global counters

// Snapshot is a point-in-time view of runtime reliability counters.
type Snapshot struct {
	ClaimConflicts   int64
	FinalizeFailures int64
	StreamPushErrors int64
	RecoveryRuns     int64
	RecoveryRequeued int64
	RecoveryErrors   int64
}

func IncClaimConflicts() {
	global.claimConflicts.Add(1)
}

func IncFinalizeFailures() {
	global.finalizeFailures.Add(1)
}

func IncStreamPushErrors() {
	global.streamPushErrors.Add(1)
}

func IncRecoveryRuns() {
	global.recoveryRuns.Add(1)
}

func AddRecoveryRequeued(n int64) {
	if n > 0 {
		global.recoveryRequeued.Add(n)
	}
}

func AddRecoveryErrors(n int64) {
	if n > 0 {
		global.recoveryErrors.Add(n)
	}
}

// SnapshotCounters returns an atomic snapshot for metrics export.
func SnapshotCounters() Snapshot {
	return Snapshot{
		ClaimConflicts:   global.claimConflicts.Load(),
		FinalizeFailures: global.finalizeFailures.Load(),
		StreamPushErrors: global.streamPushErrors.Load(),
		RecoveryRuns:     global.recoveryRuns.Load(),
		RecoveryRequeued: global.recoveryRequeued.Load(),
		RecoveryErrors:   global.recoveryErrors.Load(),
	}
}
