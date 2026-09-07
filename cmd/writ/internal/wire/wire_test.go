package wire_test

import (
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
)

func TestWire_SyncConverters(t *testing.T) {
	status := wire.FromSyncStatus(writ.SyncStatus{
		Remote:   "origin",
		Unsynced: 3,
	})
	if status.Remote != "origin" || status.Unsynced != 3 {
		t.Errorf("FromSyncStatus mismatch: %+v", status)
	}

	result := wire.FromSyncResult("origin", writ.SyncResult{
		OpsFetched:     2,
		OpsPushed:      1,
		ObjectsTouched: 1,
		Unsynced:       0,
	})
	if result.Remote != "origin" || result.OpsFetched != 2 || result.OpsPushed != 1 || result.ObjectsTouched != 1 || result.Unsynced != 0 {
		t.Errorf("FromSyncResult mismatch: %+v", result)
	}

	syncErr := &writ.SyncError{
		Remote:    "origin",
		Kind:      "auth",
		Message:   "permission denied",
		Advice:    "check ssh key",
		Retryable: false,
		Unsynced:  2,
	}

	statusFail := wire.FromSyncStatusFailure("origin", syncErr, 2)
	if statusFail.Remote != "origin" || statusFail.Unsynced != 2 || statusFail.Failure == nil {
		t.Fatalf("FromSyncStatusFailure mismatch: %+v", statusFail)
	}
	if statusFail.Failure.Kind != "auth" || statusFail.Failure.Message != "permission denied" || statusFail.Failure.Advice != "check ssh key" || statusFail.Failure.Retryable {
		t.Errorf("statusFail.Failure mismatch: %+v", statusFail.Failure)
	}

	resFail := wire.FromSyncResultFailure("origin", writ.SyncResult{OpsFetched: 0, OpsPushed: 0, Unsynced: 2}, syncErr)
	if resFail.Remote != "origin" || resFail.Unsynced != 2 || resFail.Failure == nil {
		t.Fatalf("FromSyncResultFailure mismatch: %+v", resFail)
	}
	if resFail.Failure.Kind != "auth" || resFail.Failure.Message != "permission denied" || resFail.Failure.Advice != "check ssh key" || resFail.Failure.Retryable {
		t.Errorf("resFail.Failure mismatch: %+v", resFail.Failure)
	}
}
