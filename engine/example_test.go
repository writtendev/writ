package writ_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"testing"

	"github.com/writtendev/writ/engine"
)

func ExampleOpen() {
	// Open a repository at the specified working tree or subdirectory path.
	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	// Access writer identity
	writer := store.Writer()
	fmt.Printf("Active writer: %s\n", writer.Name)
}

func ExampleObjects_Create() {
	ctx := context.Background()
	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	// Create an object of a schema-declared type. objectType and the op's
	// Type both come from the schema in the log (writ.schema) — writ has no
	// built-in notion of "review" beyond what a schema declares. Version 0
	// resolves the op's version from the installed vocabulary; it must be
	// set explicitly when a type declares more than one version of the op.
	objectID, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"title":       "Add OAuth2 authentication provider",
			"description": "Implements Google and GitHub OAuth2 flows",
		},
	})
	if err != nil {
		log.Printf("create object: %v", err)
		return
	}

	fmt.Printf("Created object: %s\n", objectID)
}

func ExampleObjects_Get() {
	ctx := context.Background()
	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	objectID, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Add OAuth2 authentication provider"},
	})
	if err != nil {
		log.Printf("create object: %v", err)
		return
	}

	// Get folds the object's state directly from the DAG. Fields is keyed
	// by the schema's TARGET key, which is not always the field name the
	// creating op wrote it under — see NewOp.Fields and Object.Fields.
	obj, err := store.Objects.Get(ctx, objectID)
	if err != nil {
		log.Printf("get object: %v", err)
		return
	}

	fmt.Printf("Object %s (%s): %v\n", obj.ObjectID, obj.ObjectType, obj.Fields["title"])
}

func ExampleStore_Watch() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	// 1. Subscribe to change events first
	events := store.Watch(ctx)

	// 2. Query initial snapshot after subscribing
	tickets, err := store.Query.Objects(writ.ObjectFilter{
		Type: []string{"ticket"},
	})
	if err != nil {
		log.Printf("query initial tickets: %v", err)
		return
	}
	fmt.Printf("Initial tickets: %d\n", len(tickets))

	// 3. React to incoming events in the background
	go func() {
		for ev := range events {
			switch ev.Kind {
			case writ.EventReset:
				// Buffer overflowed or store rebuilt; re-query all state
				log.Printf("Reset received, re-querying everything")
			case writ.EventCreated, writ.EventChanged:
				log.Printf("Object %s (%s) %s with ops %v", ev.ObjectID, ev.ObjectType, ev.Kind, ev.OpTypes)
			}
		}
	}()
}

// TestDocumentedBaseAndHeadAreOIDs guards the package's own documentation.
// Nothing here documents a Base/Head-shaped field any more — that was
// review-specific porcelain this ticket deleted along with the typed
// per-type services — but a producer that resolves a ref-shaped value into
// a commit OID before writing it stays a real trap for whatever
// schema-declared field plays that role next, and an Example without an
// "// Output:" comment compiles but never runs, so `go test` would not
// itself catch a bad doc example. So the guard stays, reading the source
// instead: any Base/Head literal these two files ever teach again must be a
// commit OID.
func TestDocumentedBaseAndHeadAreOIDs(t *testing.T) {
	literal := regexp.MustCompile(`(Base|Head):\s*"([^"]*)"`)
	oid := regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

	for _, file := range []string{"writ.go", "example_test.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		matches := literal.FindAllStringSubmatch(string(src), -1)
		if len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			if !oid.MatchString(m[2]) {
				t.Errorf("%s documents %s: %q, which is not a commit OID — a schema-declared field resolved into a commit OID by the producer refuses a ref name, so the documentation would teach a call that fails",
					file, m[1], m[2])
			}
		}
	}
}
