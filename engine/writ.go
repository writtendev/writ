// Package writ provides the public Go API for Writ: git-native, signed,
// append-only, mergeable state stored under refs/writ/* in a git repository.
//
// The API is schema-shaped, never git-shaped — the shapes callers see come
// from the schema declared in the log (writ.schema), not from Go structs
// writ ships, and callers see no SHAs or refspecs unless they explicitly
// ask for one.
//
// All client layers — CLI, downstream TUIs/viewers, GitHub bridges, and hosted services — build
// on this single interface.
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
//	// has no built-in notion of "review" beyond what a schema declares.
//	objectID, err := store.Objects.Create(ctx, "review", writ.NewOp{
//	    Type:   "create",
//	    Fields: map[string]any{"title": "Add OAuth2 authentication provider"},
//	})
//
//	// Fold and read an object's current state, keyed by the schema's own
//	// field or target names.
//	obj, err := store.Objects.Get(ctx, objectID)
//
//	// Query across every declared type
//	objects, err := store.Query.Objects(writ.ObjectFilter{
//	    Type: []string{"review"},
//	})
//
//	// Synchronize with git remote
//	syncResult, err := store.Sync(ctx, "origin")
package writ
