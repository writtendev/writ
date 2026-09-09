package writ_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	writsync "github.com/writtendev/writ/engine/sync"
)

func TestOpenWithRepositoryExtensions(t *testing.T) {
	extensionScenarios := []struct {
		name       string
		configArgs [][]string
	}{
		{
			name: "worktreeConfig (sparse-checkout)",
			configArgs: [][]string{
				{"core.repositoryformatversion", "0"},
				{"extensions.worktreeConfig", "true"},
			},
		},
		{
			name: "partialClone",
			configArgs: [][]string{
				{"core.repositoryformatversion", "0"},
				{"extensions.partialClone", "origin"},
			},
		},
		{
			name: "preciousObjects",
			configArgs: [][]string{
				{"core.repositoryformatversion", "0"},
				{"extensions.preciousObjects", "true"},
			},
		},
		{
			name: "all standard extensions combined",
			configArgs: [][]string{
				{"core.repositoryformatversion", "0"},
				{"extensions.worktreeConfig", "true"},
				{"extensions.partialClone", "origin"},
				{"extensions.preciousObjects", "true"},
			},
		},
	}

	for _, tc := range extensionScenarios {
		t.Run(tc.name, func(t *testing.T) {
			repoDir, _ := setupConfiguredRepo(t)

			// Apply git config extensions
			for _, cfg := range tc.configArgs {
				runGitCmd(t, repoDir, "config", cfg[0], cfg[1])
			}

			ctx := context.Background()

			// 1. writ.Open
			s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
			if err != nil {
				t.Fatalf("writ.Open failed on repo with %s: %v", tc.name, err)
			}
			defer s.Close()

			applyCoreSchema(t, ctx, s)

			// 2. Create a gadget
			gadgetID, err := s.Objects.Create(ctx, "gadget", writ.NewOp{
				Type: "create",
				Fields: map[string]any{
					"title":       "Test gadget with extensions",
					"description": "Testing repository extensions compatibility",
				},
			})
			if err != nil {
				t.Fatalf("Objects.Create(gadget) failed: %v", err)
			}
			if gadgetID == "" {
				t.Fatal("expected non-empty gadget ID")
			}

			// 3. Create a widget
			widgetID, err := s.Objects.Create(ctx, "widget", writ.NewOp{
				Type:   "create",
				Fields: map[string]any{"title": "Test widget with extensions"},
			})
			if err != nil {
				t.Fatalf("Objects.Create(widget) failed: %v", err)
			}
			if widgetID == "" {
				t.Fatal("expected non-empty widget ID")
			}

			// 4. Create a note against the widget
			noteID, err := s.Objects.Create(ctx, "note", writ.NewOp{
				Type: "create",
				Fields: map[string]any{
					"text": "Looks good to me!",
					"subject": map[string]string{
						"object_type": "widget",
						"object_id":   widgetID,
					},
				},
			})
			if err != nil {
				t.Fatalf("Objects.Create(note) failed: %v", err)
			}
			if noteID == "" {
				t.Fatal("expected non-empty note ID")
			}

			// 5. Query
			gadgets, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"gadget"}})
			if err != nil {
				t.Fatalf("Query.Objects(gadget) failed: %v", err)
			}
			if len(gadgets) != 1 || gadgets[0].ObjectID != gadgetID {
				t.Fatalf("unexpected gadgets query result: %+v", gadgets)
			}

			widgets, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"widget"}})
			if err != nil {
				t.Fatalf("Query.Objects(widget) failed: %v", err)
			}
			if len(widgets) != 1 || widgets[0].ObjectID != widgetID {
				t.Fatalf("unexpected widgets query result: %+v", widgets)
			}

			notes, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"note"}})
			if err != nil {
				t.Fatalf("Query.Objects(note) failed: %v", err)
			}
			if len(notes) != 1 || notes[0].ObjectID != noteID {
				t.Fatalf("unexpected notes query result: %+v", notes)
			}

			// 6. Refresh projection
			stats, err := s.Refresh(ctx)
			if err != nil {
				t.Fatalf("Refresh failed: %v", err)
			}
			if stats.ObjectsTouched < 0 {
				t.Fatalf("unexpected stats: %+v", stats)
			}

			// 7. Verify dag.Open directly
			ident := identity.Identity{
				WriterID: "0123456789abcdef",
				Author:   identity.Author{Name: "Alice Test", Email: "alice@example.com"},
			}
			dagStore, err := dag.Open(repoDir, ident)
			if err != nil {
				t.Fatalf("dag.Open failed on repo with %s: %v", tc.name, err)
			}
			chains, err := dag.Chains(dagStore.Storer())
			if err != nil {
				t.Fatalf("dag.Chains failed: %v", err)
			}
			if len(chains) == 0 {
				t.Fatal("expected non-empty chains")
			}

			// 8. Verify sync.Open directly
			syncClient, err := writsync.Open(repoDir, ident)
			if err != nil {
				t.Fatalf("sync.Open failed on repo with %s: %v", tc.name, err)
			}
			if syncClient.RepoDir() != repoDir {
				t.Fatalf("syncClient.RepoDir = %s, want %s", syncClient.RepoDir(), repoDir)
			}
		})
	}
}

func TestOpenSparseCheckoutRepository(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	// Run git sparse-checkout init (sets core.repositoryformatversion 0 and extensions.worktreeConfig true)
	runGitCmd(t, repoDir, "sparse-checkout", "init")

	ctx := context.Background()
	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed on sparse-checkout repo: %v", err)
	}
	defer s.Close()

	applyCoreSchema(t, ctx, s)

	gadgetID, err := s.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Sparse checkout test gadget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) failed: %v", err)
	}
	if gadgetID == "" {
		t.Fatal("expected non-empty gadget ID")
	}

	gadgets, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"gadget"}})
	if err != nil {
		t.Fatalf("Query.Objects(gadget) failed: %v", err)
	}
	if len(gadgets) != 1 || gadgets[0].ObjectID != gadgetID {
		t.Fatalf("unexpected gadgets query result: %+v", gadgets)
	}
}

func TestOpenLinkedWorktreeWithExtensions(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	// Configure extensions on main repo
	runGitCmd(t, repoDir, "config", "core.repositoryformatversion", "0")
	runGitCmd(t, repoDir, "config", "extensions.worktreeConfig", "true")

	// Create linked worktree
	wtDir := filepath.Join(t.TempDir(), "wt-ext")
	runGitCmd(t, repoDir, "worktree", "add", wtDir, "-b", "wt-ext-branch")

	// Create annotated tag in main repo
	runGitCmd(t, repoDir, "tag", "-a", "v1.0.0", "-m", "Release 1.0.0")

	ctx := context.Background()
	s, err := writ.Open(wtDir, writ.WithSigner(dummySigner()), writ.WithTargetRefs("v1.0.0"))
	if err != nil {
		t.Fatalf("writ.Open failed on linked worktree with extensions: %v", err)
	}
	defer s.Close()

	applyCoreSchema(t, ctx, s)

	gadgetID, err := s.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Linked worktree with extensions gadget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) failed: %v", err)
	}
	if gadgetID == "" {
		t.Fatal("expected non-empty gadget ID")
	}

	gadgets, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"gadget"}})
	if err != nil {
		t.Fatalf("Query.Objects(gadget) failed: %v", err)
	}
	if len(gadgets) != 1 || gadgets[0].ObjectID != gadgetID {
		t.Fatalf("unexpected gadgets query result: %+v", gadgets)
	}
}

