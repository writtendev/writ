package wire_test

import (
	"encoding/json"
	"errors"
	"fmt"
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

// TestWire_FromInitResult_OutcomeAndRemoteStatus is WRIT-304's unit-level
// coverage of FromInitResult's derivation logic, isolated from the CLI:
// outcome derivation (complete/partial/stopped), every InitRemote status
// including the two not directly reachable through a single `writ init`
// invocation (not-attempted, and a "failed" remote classified "other"),
// and the reason-code catalogue for skipped/failed remotes.
func TestWire_FromInitResult_OutcomeAndRemoteStatus(t *testing.T) {
	t.Run("complete: no remotes, no error", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{WriterID: "w", RepoID: "r"}, nil, "")
		if got.Outcome != "complete" {
			t.Errorf("Outcome = %q, want complete", got.Outcome)
		}
		if got.Remotes == nil || len(got.Remotes) != 0 {
			t.Errorf("Remotes = %#v, want a non-nil empty slice", got.Remotes)
		}
	})

	t.Run("partial: a discovered remote skipped, classified by sentinel", func(t *testing.T) {
		res := writ.InitResult{
			WriterID: "w", RepoID: "r",
			Remotes: []writ.RemoteInit{
				{Name: "origin", Refspec: "+refs/writ/*:refs/remotes/origin/writ/*", Repaired: true},
				{Name: "ghost", Skipped: true, Err: fmt.Errorf("remote %q: %w", "ghost", writ.ErrUnknownRemote)},
				{Name: "-x", Skipped: true, Err: fmt.Errorf("remote %q: %w", "-x", writ.ErrInvalidRemoteName)},
			},
		}
		got := wire.FromInitResult(res, nil, "")
		if got.Outcome != "partial" {
			t.Fatalf("Outcome = %q, want partial", got.Outcome)
		}
		if len(got.Remotes) != 3 {
			t.Fatalf("Remotes = %#v, want 3 entries", got.Remotes)
		}
		if r := got.Remotes[0]; r.Status != "configured" || r.Refspec == "" || r.Reason != nil {
			t.Errorf("Remotes[0] = %+v, want configured with a refspec and no reason", r)
		}
		if r := got.Remotes[1]; r.Status != "skipped" || r.Reason == nil || r.Reason.Code != "unknown-remote" {
			t.Errorf("Remotes[1] = %+v, want skipped/unknown-remote", r)
		}
		if r := got.Remotes[2]; r.Status != "skipped" || r.Reason == nil || r.Reason.Code != "invalid-name" {
			t.Errorf("Remotes[2] = %+v, want skipped/invalid-name", r)
		}
	})

	t.Run("already-configured: Repaired false", func(t *testing.T) {
		res := writ.InitResult{
			WriterID: "w", RepoID: "r",
			Remotes: []writ.RemoteInit{
				{Name: "origin", Refspec: "+refs/writ/*:refs/remotes/origin/writ/*", Repaired: false},
			},
		}
		got := wire.FromInitResult(res, nil, "")
		if got.Remotes[0].Status != "already-configured" {
			t.Errorf("Status = %q, want already-configured", got.Remotes[0].Status)
		}
	})

	t.Run("stopped: a failed remote classified other, plus a not-attempted one", func(t *testing.T) {
		runErr := errors.New("remote \"origin\": config write failed")
		res := writ.InitResult{
			WriterID: "w", RepoID: "r",
			Remotes: []writ.RemoteInit{
				{Name: "origin", Err: runErr},
				{Name: "upstream", NotAttempted: true},
			},
		}
		got := wire.FromInitResult(res, runErr, "")
		if got.Outcome != "stopped" {
			t.Fatalf("Outcome = %q, want stopped", got.Outcome)
		}
		if r := got.Remotes[0]; r.Status != "failed" || r.Reason == nil || r.Reason.Code != "other" || r.Reason.Message != runErr.Error() {
			t.Errorf("Remotes[0] = %+v, want failed/other with the run error's message", r)
		}
		if r := got.Remotes[1]; r.Status != "not-attempted" || r.Reason != nil || r.Refspec != "" {
			t.Errorf("Remotes[1] = %+v, want not-attempted with no reason and no refspec", r)
		}
	})

	t.Run("stopped: no remotes reached yet still reports stopped", func(t *testing.T) {
		runErr := errors.New("boom")
		got := wire.FromInitResult(writ.InitResult{WriterID: "w", RepoID: "r"}, runErr, "")
		if got.Outcome != "stopped" {
			t.Errorf("Outcome = %q, want stopped", got.Outcome)
		}
	})
}

// TestWire_FromInitResult_IdentityAndStarterSchema covers the
// person/signing-identity and starter_schema field derivation, including
// the namespace parameter (which writ.InitResult itself does not carry
// back -- see FromInitResult's own doc comment) and the omitted-vs-present
// cases for each optional field.
func TestWire_FromInitResult_IdentityAndStarterSchema(t *testing.T) {
	t.Run("person id from writ.personId", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			PersonID: "user:alice", PersonIDFromKey: true,
		}, nil, "")
		if got.PersonID != "user:alice" || got.PersonIDSource != "writ.personId" {
			t.Errorf("PersonID/PersonIDSource = %q/%q, want user:alice/writ.personId", got.PersonID, got.PersonIDSource)
		}
	})

	t.Run("person id derived from user.email", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			PersonID: "email:alice@example.com",
		}, nil, "")
		if got.PersonID != "email:alice@example.com" || got.PersonIDSource != "user.email" {
			t.Errorf("PersonID/PersonIDSource = %q/%q, want email:alice@example.com/user.email", got.PersonID, got.PersonIDSource)
		}
	})

	t.Run("person id omitted on error", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			PersonIDErr: errors.New("no person identifier"),
		}, nil, "")
		if got.PersonID != "" || got.PersonIDSource != "" {
			t.Errorf("PersonID/PersonIDSource = %q/%q, want both empty on PersonIDErr", got.PersonID, got.PersonIDSource)
		}
	})

	t.Run("signing key literal", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			SigningKey: "AAAA...", SigningKeyLiteral: true,
		}, nil, "")
		if got.SigningKey != "AAAA..." || !got.SigningKeyLiteral {
			t.Errorf("SigningKey/SigningKeyLiteral = %q/%v, want AAAA.../true", got.SigningKey, got.SigningKeyLiteral)
		}
	})

	t.Run("starter schema written carries the namespace passed in", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			StarterSchemaPath: "/repo/writ.schema", StarterSchemaWritten: true,
		}, nil, "acme")
		if got.StarterSchema == nil || !got.StarterSchema.Written || got.StarterSchema.Namespace != "acme" {
			t.Errorf("StarterSchema = %+v, want written with namespace acme", got.StarterSchema)
		}
	})

	t.Run("starter schema existed carries no namespace even when one was passed", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			StarterSchemaPath: "/repo/writ.schema", StarterSchemaExisted: true,
		}, nil, "acme")
		if got.StarterSchema == nil || !got.StarterSchema.Existed || got.StarterSchema.Namespace != "" {
			t.Errorf("StarterSchema = %+v, want existed with no namespace", got.StarterSchema)
		}
	})

	t.Run("starter schema write failure reports the error but does not touch outcome", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{
			WriterID: "w", RepoID: "r",
			StarterSchemaPath: "/repo/writ.schema", StarterSchemaErr: errors.New("permission denied"),
		}, nil, "")
		if got.StarterSchema == nil || got.StarterSchema.Error != "permission denied" {
			t.Errorf("StarterSchema = %+v, want error permission denied", got.StarterSchema)
		}
		if got.Outcome != "complete" {
			t.Errorf("Outcome = %q, want complete -- a starter-schema write failure must not change it", got.Outcome)
		}
	})

	t.Run("starter schema omitted for a bare repository", func(t *testing.T) {
		got := wire.FromInitResult(writ.InitResult{WriterID: "w", RepoID: "r"}, nil, "")
		if got.StarterSchema != nil {
			t.Errorf("StarterSchema = %+v, want nil (no work tree, StarterSchemaPath left empty)", got.StarterSchema)
		}
	})
}
