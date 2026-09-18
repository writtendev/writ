// Package writ provides the public Go API for Writ: git-native, signed,
// append-only, mergeable state stored under refs/writ/* in a git repository.
//
// The API is schema-shaped, never git-shaped — the shapes callers see come
// from the schema declared in the log (writ.schema), not from Go structs
// writ ships, and callers see no SHAs or refspecs unless they explicitly
// ask for one.
//
// All client layers — the CLI, downstream TUIs/viewers, and services built on
// top — consume this single interface.
//
// Trust model: verification is reported, never enforced. Every op folds
// into an object's state regardless of its signature outcome — an op
// signed with a key the trust store does not authorize, or not signed at
// all, contributes its fields exactly as a valid one does, with the
// outcome surfaced on Object.Verification and ObjectResult.Verification.
// The engine does not filter for two reasons: the fold is pure and
// deterministic (ops in, state out, no I/O), and a trust store is
// resolved locally, so dropping ops against it would make an object's
// state depend on who is reading it — two clones of the same repository
// would disagree about what the object says. A caller that renders folded
// state as authentic must check Verification itself and decide what to
// do; nothing below this API does it for them.
//
// Basic usage:
//
//	store, err := writ.Open("/path/to/repo")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer store.Close()
//
//	// Create an object of a schema-declared type. objectType and the op's
//	// Type both come from the schema in the log (writ.schema) — writ itself
//	// has no built-in notion of "widget" beyond what a schema declares.
//	objectID, err := store.Objects.Create(ctx, "widget", writ.NewOp{
//	    Type:   "create",
//	    Fields: map[string]any{"title": "First widget"},
//	})
//
//	// Fold and read an object's current state, keyed by the schema's own
//	// field or target names.
//	obj, err := store.Objects.Get(ctx, objectID)
//
//	// Query across every declared type
//	objects, err := store.Query.Objects(writ.ObjectFilter{
//	    Type: []string{"widget"},
//	})
//
//	// Synchronize with git remote
//	syncResult, err := store.Sync(ctx, "origin")
package writ
