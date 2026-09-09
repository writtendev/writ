package sync_test

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/sync"
)

func TestStatus_ReachabilityComputation(t *testing.T) {
	aliceID := "0123456789abcdef"
	bobID := "fedcba9876543210"

	t.Run("nothing_appended", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		_ = dir
		ident := testIdentity(aliceID, "Alice", "alice@example.com")
		status, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus failed: %v", err)
		}
		if status.Unsynced != 0 {
			t.Errorf("Unsynced = %d, want 0", status.Unsynced)
		}
		if status.Diverged {
			t.Errorf("Diverged = true, want false")
		}
		if len(status.ByType) != 0 {
			t.Errorf("ByType len = %d, want 0", len(status.ByType))
		}
	})

	t.Run("fresh_chain_no_remote_ref", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		ident := testIdentity(aliceID, "Alice", "alice@example.com")
		store := mustOpenStore(t, dir, ident)

		appendTestOp(t, store, "widget", "w-1", "create", map[string]any{"title": "Widget 1"})
		appendTestOp(t, store, "widget", "w-1", "update", map[string]any{"description": "Description 1"})

		status, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus failed: %v", err)
		}
		if status.Unsynced != 2 {
			t.Errorf("Unsynced = %d, want 2", status.Unsynced)
		}
		if status.Diverged {
			t.Errorf("Diverged = true, want false")
		}
		if len(status.ByType) != 1 || status.ByType[0].ObjectType != "widget" || status.ByType[0].Unsynced != 2 {
			t.Errorf("ByType = %+v, want [{ObjectType: widget, Unsynced: 2}]", status.ByType)
		}
	})

	t.Run("partial_push_across_two_object_types", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		ident := testIdentity(aliceID, "Alice", "alice@example.com")
		store := mustOpenStore(t, dir, ident)

		widgetOp1 := appendTestOp(t, store, "widget", "w-1", "create", map[string]any{"title": "Widget 1"})
		_ = appendTestOp(t, store, "gadget", "g-1", "create", map[string]any{"title": "Gadget 1"})

		// Simulate that widget chain was pushed to origin, but gadget was not pushed
		widgetRef := dag.RemoteRefName("origin", ident.WriterID, "widget")
		err := repo.Storer.SetReference(plumbing.NewReferenceFromStrings(widgetRef.String(), widgetOp1))
		if err != nil {
			t.Fatalf("SetReference failed: %v", err)
		}

		status, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus failed: %v", err)
		}
		if status.Unsynced != 1 {
			t.Errorf("Unsynced = %d, want 1", status.Unsynced)
		}
		if status.Diverged {
			t.Errorf("Diverged = true, want false")
		}
		if len(status.ByType) != 2 {
			t.Fatalf("ByType len = %d, want 2", len(status.ByType))
		}
		// Expect sorted ByType: [gadget: 1, widget: 0]
		if status.ByType[0].ObjectType != "gadget" || status.ByType[0].Unsynced != 1 {
			t.Errorf("ByType[0] = %+v, want {gadget, 1}", status.ByType[0])
		}
		if status.ByType[1].ObjectType != "widget" || status.ByType[1].Unsynced != 0 {
			t.Errorf("ByType[1] = %+v, want {widget, 0}", status.ByType[1])
		}
	})

	t.Run("chain_first_op_causal_parent_is_another_writers_remote_op", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		bobIdent := testIdentity(bobID, "Bob", "bob@example.com")
		bobStore := mustOpenStore(t, dir, bobIdent)

		bobOp1 := appendTestOp(t, bobStore, "widget", "w-1", "create", map[string]any{"title": "Bob Widget"})

		// Simulate Bob's op is already on remote
		bobRemoteRef := dag.RemoteRefName("origin", bobIdent.WriterID, "widget")
		_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(bobRemoteRef.String(), bobOp1))

		// Alice creates a new chain whose causal parent includes Bob's op
		aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
		aliceStore := mustOpenStore(t, dir, aliceIdent)
		appendTestOp(t, aliceStore, "widget", "w-1", "endorse", map[string]any{
			"revision": strings.Repeat("a", 40),
			"verdict":  "yes",
		})

		// Status for Alice must report only Alice's 1 unsynced op, not Bob's op
		status, err := sync.ComputeStatus(repo.Storer, aliceIdent.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus failed: %v", err)
		}
		if status.Unsynced != 1 {
			t.Errorf("Unsynced = %d, want 1", status.Unsynced)
		}
		if status.Diverged {
			t.Errorf("Diverged = true, want false")
		}
	})

	t.Run("two_remotes_at_different_frontiers", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		ident := testIdentity(aliceID, "Alice", "alice@example.com")
		store := mustOpenStore(t, dir, ident)

		op1 := appendTestOp(t, store, "widget", "w-1", "create", map[string]any{"title": "Widget 1"})

		// Remote "origin" has op1 pushed
		originRef := dag.RemoteRefName("origin", ident.WriterID, "widget")
		_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(originRef.String(), op1))

		// Remote "upstream" has nothing pushed
		statusOrigin, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus origin failed: %v", err)
		}
		if statusOrigin.Unsynced != 0 {
			t.Errorf("origin Unsynced = %d, want 0", statusOrigin.Unsynced)
		}

		statusUpstream, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "upstream")
		if err != nil {
			t.Fatalf("ComputeStatus upstream failed: %v", err)
		}
		if statusUpstream.Unsynced != 1 {
			t.Errorf("upstream Unsynced = %d, want 1", statusUpstream.Unsynced)
		}
	})

	t.Run("rolled_back_remote_diverged_not_overcounted", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		ident := testIdentity(aliceID, "Alice", "alice@example.com")
		store := mustOpenStore(t, dir, ident)

		// Create commit R1 (simulating rolled back tip on remote)
		opR1 := appendTestOp(t, store, "widget", "w-old", "create", map[string]any{"title": "Old Widget"})
		remoteRef := dag.RemoteRefName("origin", ident.WriterID, "widget")
		_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(remoteRef.String(), opR1))

		// Now reset local chain to a new branch / commit L1 that does not have R1 as ancestor
		// By resetting local ref:
		localRef := dag.LocalRefName(ident.WriterID, "widget")
		_ = repo.Storer.RemoveReference(localRef)

		// Append new op on local (creates fresh root L1)
		store2 := mustOpenStore(t, dir, ident)
		appendTestOp(t, store2, "widget", "w-new", "create", map[string]any{"title": "New Widget"})

		status, err := sync.ComputeStatus(repo.Storer, ident.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus failed: %v", err)
		}
		if !status.Diverged {
			t.Errorf("Diverged = false, want true")
		}
		if status.Unsynced != 1 {
			t.Errorf("Unsynced = %d, want 1", status.Unsynced)
		}
	})

	t.Run("two_devices_one_email", func(t *testing.T) {
		dir, repo := initTestRepo(t)
		device1 := "1111111111111111"
		device2 := "2222222222222222"

		ident1 := testIdentity(device1, "Alice", "alice@example.com")
		ident2 := testIdentity(device2, "Alice", "alice@example.com")

		store1 := mustOpenStore(t, dir, ident1)
		store2 := mustOpenStore(t, dir, ident2)

		appendTestOp(t, store1, "widget", "w-1", "create", map[string]any{"title": "Device 1 Widget"})
		appendTestOp(t, store2, "widget", "w-2", "create", map[string]any{"title": "Device 2 Widget"})

		status1, err := sync.ComputeStatus(repo.Storer, ident1.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus device 1 failed: %v", err)
		}
		if status1.Unsynced != 1 {
			t.Errorf("Device 1 Unsynced = %d, want 1", status1.Unsynced)
		}

		status2, err := sync.ComputeStatus(repo.Storer, ident2.WriterID, "origin")
		if err != nil {
			t.Fatalf("ComputeStatus device 2 failed: %v", err)
		}
		if status2.Unsynced != 1 {
			t.Errorf("Device 2 Unsynced = %d, want 1", status2.Unsynced)
		}
	})
}
