package runtimemetrics

import "testing"

func TestCountersSnapshot(t *testing.T) {
	resetForTest()
	defer resetForTest()

	IncClaimConflicts()
	IncFinalizeFailures()
	IncStreamPushErrors()
	IncRecoveryRuns()
	AddRecoveryRequeued(2)
	AddRecoveryErrors(3)

	snap := SnapshotCounters()
	if snap.ClaimConflicts != 1 {
		t.Fatalf("claim conflicts: expected 1, got %d", snap.ClaimConflicts)
	}
	if snap.FinalizeFailures != 1 {
		t.Fatalf("finalize failures: expected 1, got %d", snap.FinalizeFailures)
	}
	if snap.StreamPushErrors != 1 {
		t.Fatalf("stream push errors: expected 1, got %d", snap.StreamPushErrors)
	}
	if snap.RecoveryRuns != 1 {
		t.Fatalf("recovery runs: expected 1, got %d", snap.RecoveryRuns)
	}
	if snap.RecoveryRequeued != 2 {
		t.Fatalf("recovery requeued: expected 2, got %d", snap.RecoveryRequeued)
	}
	if snap.RecoveryErrors != 3 {
		t.Fatalf("recovery errors: expected 3, got %d", snap.RecoveryErrors)
	}
}

func TestRecoveryAccumulatorsIgnoreNonPositive(t *testing.T) {
	resetForTest()
	defer resetForTest()

	AddRecoveryRequeued(0)
	AddRecoveryRequeued(-1)
	AddRecoveryErrors(0)
	AddRecoveryErrors(-1)

	snap := SnapshotCounters()
	if snap.RecoveryRequeued != 0 {
		t.Fatalf("expected recovery requeued to stay at 0, got %d", snap.RecoveryRequeued)
	}
	if snap.RecoveryErrors != 0 {
		t.Fatalf("expected recovery errors to stay at 0, got %d", snap.RecoveryErrors)
	}
}

func resetForTest() {
	global.claimConflicts.Store(0)
	global.finalizeFailures.Store(0)
	global.streamPushErrors.Store(0)
	global.recoveryRuns.Store(0)
	global.recoveryRequeued.Store(0)
	global.recoveryErrors.Store(0)
}
