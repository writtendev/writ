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

func ExampleReviews_Create() {
	ctx := context.Background()
	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	// Create a new code review. Base and Head are commit OIDs, not ref names:
	// resolve the ref first, the way the CLI does with git rev-parse.
	reviewID, err := store.Reviews.Create(ctx, writ.NewReview{
		Title:       "Add OAuth2 authentication provider",
		Description: "Implements Google and GitHub OAuth2 flows",
		Base:        "e83c5163316f89bfbde7d9ab23ca2e25604af290",
		Head:        "1f7a7a472abf3dd9643fd615f6da379c4acb3e3a",
	})
	if err != nil {
		log.Printf("create review: %v", err)
		return
	}

	fmt.Printf("Created review: %s\n", reviewID)
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

func ExampleQuery_Reviews() {
	store, err := writ.Open(".")
	if err != nil {
		log.Printf("open repo: %v", err)
		return
	}
	defer store.Close()

	// Query open reviews
	reviews, err := store.Query.Reviews(writ.ReviewFilter{
		Status:  []string{"open"},
		OrderBy: writ.OrderByUpdatedAtDesc,
	})
	if err != nil {
		log.Printf("query reviews: %v", err)
		return
	}

	for _, r := range reviews {
		fmt.Printf("Review %s: %s (status: %s)\n", r.ObjectID, r.Review.Title, r.Review.Status)
	}
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
	reviews, err := store.Query.Reviews(writ.ReviewFilter{
		Status: []string{"open"},
	})
	if err != nil {
		log.Printf("query initial reviews: %v", err)
		return
	}
	fmt.Printf("Initial open reviews: %d\n", len(reviews))

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
//
// The godoc landing page and ExampleReviews_Create both passed ref names for
// base and head — an idiom the producer now refuses — and nothing
// caught it: an Example without an "// Output:" comment compiles but never
// runs, so `go test` could not have noticed. It still must not run, because it
// would append ops to whatever repository `go test` was invoked in and print a
// freshly minted, nondeterministic object id. So the guard reads the source
// instead: every Base/Head literal these two files teach must be a commit OID.
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
				t.Errorf("%s documents %s: %q, which is not a commit OID — Reviews.Create and PushRevision refuse ref names, so the documentation would teach a call that fails",
					file, m[1], m[2])
			}
		}
	}
}
