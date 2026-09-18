package wire_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

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

// TestWire_FromObjectResultSummary_ClampsOutOfRangeTimestamps is WRIT-280's
// unit-level coverage: an out-of-range author timestamp reaching
// created_at/updated_at must not make ObjectSummary fail to marshal
// (time.Time.MarshalJSON refuses years outside [0,9999]) -- it must clamp
// to the nearest RFC 3339-representable bound and carry the true value in
// the sibling *_epoch field, while an in-range timestamp is left
// byte-identical with no sibling field at all.
func TestWire_FromObjectResultSummary_ClampsOutOfRangeTimestamps(t *testing.T) {
	base := writ.ObjectResult{
		ObjectID:     "0123456789abcdef0123456789abcdef",
		ObjectType:   "acme.ticket",
		Author:       writ.Author{Name: "Alice", Email: "alice@example.com"},
		OpCount:      3,
		Verification: "valid",
	}

	t.Run("in range", func(t *testing.T) {
		r := base
		r.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		r.UpdatedAt = time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)

		summary := wire.FromObjectResultSummary(r)
		if summary.CreatedAtEpoch != nil {
			t.Errorf("CreatedAtEpoch = %v, want nil for an in-range timestamp", *summary.CreatedAtEpoch)
		}
		if summary.UpdatedAtEpoch != nil {
			t.Errorf("UpdatedAtEpoch = %v, want nil for an in-range timestamp", *summary.UpdatedAtEpoch)
		}

		got, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		want := `{"object_id":"0123456789abcdef0123456789abcdef","object_type":"acme.ticket","author":{"name":"Alice","email":"alice@example.com"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-06-01T12:30:00Z","op_count":3,"verification":"valid"}`
		if string(got) != want {
			t.Errorf("json.Marshal(in-range) =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("clamped upward", func(t *testing.T) {
		r := base
		hostile := time.Unix(300000000000, 0).UTC() // year 11476
		r.CreatedAt = hostile
		r.UpdatedAt = hostile

		summary := wire.FromObjectResultSummary(r)
		if summary.CreatedAtEpoch == nil || *summary.CreatedAtEpoch != 300000000000 {
			t.Fatalf("CreatedAtEpoch = %v, want 300000000000", summary.CreatedAtEpoch)
		}
		if summary.UpdatedAtEpoch == nil || *summary.UpdatedAtEpoch != 300000000000 {
			t.Fatalf("UpdatedAtEpoch = %v, want 300000000000", summary.UpdatedAtEpoch)
		}

		got, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		want := `{"object_id":"0123456789abcdef0123456789abcdef","object_type":"acme.ticket","author":{"name":"Alice","email":"alice@example.com"},"created_at":"9999-12-31T23:59:59Z","created_at_epoch":300000000000,"updated_at":"9999-12-31T23:59:59Z","updated_at_epoch":300000000000,"op_count":3,"verification":"valid"}`
		if string(got) != want {
			t.Errorf("json.Marshal(clamped upward) =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("clamped downward", func(t *testing.T) {
		r := base
		hostile := time.Unix(-70000000000, 0).UTC() // pre-year-0
		r.CreatedAt = hostile
		r.UpdatedAt = hostile

		summary := wire.FromObjectResultSummary(r)
		if summary.CreatedAtEpoch == nil || *summary.CreatedAtEpoch != -70000000000 {
			t.Fatalf("CreatedAtEpoch = %v, want -70000000000", summary.CreatedAtEpoch)
		}
		if summary.UpdatedAtEpoch == nil || *summary.UpdatedAtEpoch != -70000000000 {
			t.Fatalf("UpdatedAtEpoch = %v, want -70000000000", summary.UpdatedAtEpoch)
		}

		got, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		want := `{"object_id":"0123456789abcdef0123456789abcdef","object_type":"acme.ticket","author":{"name":"Alice","email":"alice@example.com"},"created_at":"0000-01-01T00:00:00Z","created_at_epoch":-70000000000,"updated_at":"0000-01-01T00:00:00Z","updated_at_epoch":-70000000000,"op_count":3,"verification":"valid"}`
		if string(got) != want {
			t.Errorf("json.Marshal(clamped downward) =\n%s\nwant\n%s", got, want)
		}
	})

	// Regression for the round-1 review finding: time.Unix(sec, 0) builds
	// its internal absolute time as sec + a fixed constant, which
	// overflows int64 for a sec this large and wraps around -- so
	// comparing the resulting time.Time against the bounds with
	// Before/After (rather than comparing raw epoch seconds) used to read
	// this far-future timestamp as "before year 0" and clamp it to
	// rfc3339Min instead of rfc3339Max. math.MaxInt64 is comfortably past
	// that overflow threshold and must still clamp upward.
	t.Run("clamped upward beyond int64 overflow boundary", func(t *testing.T) {
		r := base
		hostile := time.Unix(math.MaxInt64, 0).UTC()
		r.CreatedAt = hostile
		r.UpdatedAt = hostile

		summary := wire.FromObjectResultSummary(r)
		if summary.CreatedAtEpoch == nil || *summary.CreatedAtEpoch != math.MaxInt64 {
			t.Fatalf("CreatedAtEpoch = %v, want %d", summary.CreatedAtEpoch, int64(math.MaxInt64))
		}
		if summary.UpdatedAtEpoch == nil || *summary.UpdatedAtEpoch != math.MaxInt64 {
			t.Fatalf("UpdatedAtEpoch = %v, want %d", summary.UpdatedAtEpoch, int64(math.MaxInt64))
		}

		got, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		want := `{"object_id":"0123456789abcdef0123456789abcdef","object_type":"acme.ticket","author":{"name":"Alice","email":"alice@example.com"},"created_at":"9999-12-31T23:59:59Z","created_at_epoch":9223372036854775807,"updated_at":"9999-12-31T23:59:59Z","updated_at_epoch":9223372036854775807,"op_count":3,"verification":"valid"}`
		if string(got) != want {
			t.Errorf("json.Marshal(clamped upward beyond overflow boundary) =\n%s\nwant\n%s", got, want)
		}
	})
}
