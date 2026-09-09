package dag_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
)

type refVectorsDoc struct {
	Valid []struct {
		Ref        string `json:"ref"`
		WriterID   string `json:"writer_id"`
		ObjectType string `json:"object_type"`
	} `json:"valid"`
	Invalid []struct {
		Ref    string `json:"ref"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

func loadVectors(t *testing.T) refVectorsDoc {
	t.Helper()
	// Find spec/testdata/ref-names/vectors.json relative to repo root
	path := filepath.Join("..", "..", "spec", "testdata", "ref-names", "vectors.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var doc refVectorsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal vectors: %v", err)
	}
	return doc
}

func TestParseChainRef_Vectors(t *testing.T) {
	doc := loadVectors(t)

	for _, v := range doc.Valid {
		v := v
		t.Run("valid_"+v.Ref, func(t *testing.T) {
			cr, err := dag.ParseChainRef(v.Ref)
			if err != nil {
				t.Fatalf("ParseChainRef(%q) unexpected error: %v", v.Ref, err)
			}
			if string(cr.WriterID) != v.WriterID {
				t.Errorf("writer_id = %q, want %q", cr.WriterID, v.WriterID)
			}
			if cr.ObjectType != v.ObjectType {
				t.Errorf("object_type = %q, want %q", cr.ObjectType, v.ObjectType)
			}
			if cr.Remote != "" {
				t.Errorf("remote = %q, want empty", cr.Remote)
			}
			if cr.Name.String() != v.Ref {
				t.Errorf("name = %q, want %q", cr.Name, v.Ref)
			}
		})
	}

	for _, v := range doc.Invalid {
		v := v
		t.Run("invalid_"+v.Ref, func(t *testing.T) {
			_, err := dag.ParseChainRef(v.Ref)
			if err == nil {
				t.Fatalf("ParseChainRef(%q) expected error for reason: %s", v.Ref, v.Reason)
			}
		})
	}
}

func TestParseChainRef_RemoteTracking(t *testing.T) {
	tests := []struct {
		ref            string
		wantRemote     string
		wantWriterID   string
		wantObjectType string
		wantErr        bool
	}{
		{
			ref:            "refs/remotes/origin/writ/0123456789abcdef/widget",
			wantRemote:     "origin",
			wantWriterID:   "0123456789abcdef",
			wantObjectType: "widget",
			wantErr:        false,
		},
		{
			ref:            "refs/remotes/upstream/writ/fedcba9876543210/waypoint",
			wantRemote:     "upstream",
			wantWriterID:   "fedcba9876543210",
			wantObjectType: "waypoint",
			wantErr:        false,
		},
		{
			ref:     "refs/remotes/origin/heads/main",
			wantErr: true,
		},
		{
			ref:     "refs/remotes//writ/0123456789abcdef/widget",
			wantErr: true,
		},
		{
			ref:     "refs/remotes/origin/writ/invalid-id/widget",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			cr, err := dag.ParseChainRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cr.Remote != tt.wantRemote {
				t.Errorf("remote = %q, want %q", cr.Remote, tt.wantRemote)
			}
			if string(cr.WriterID) != tt.wantWriterID {
				t.Errorf("writer_id = %q, want %q", cr.WriterID, tt.wantWriterID)
			}
			if cr.ObjectType != tt.wantObjectType {
				t.Errorf("object_type = %q, want %q", cr.ObjectType, tt.wantObjectType)
			}
		})
	}
}

func TestChains(t *testing.T) {
	s := memory.NewStorage()
	h1 := plumbing.NewHash("1111111111111111111111111111111111111111")
	h2 := plumbing.NewHash("2222222222222222222222222222222222222222")
	h3 := plumbing.NewHash("3333333333333333333333333333333333333333")

	// Set writ refs and unrelated refs
	_ = s.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/writ/0123456789abcdef/widget"), h1))
	_ = s.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/remotes/origin/writ/fedcba9876543210/waypoint"), h2))
	_ = s.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), h3))
	_ = s.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.0.0"), h3))

	chains, err := dag.Chains(s)
	if err != nil {
		t.Fatalf("Chains failed: %v", err)
	}

	if len(chains) != 2 {
		t.Fatalf("len(chains) = %d, want 2", len(chains))
	}

	c1, ok := chains["refs/writ/0123456789abcdef/widget"]
	if !ok || c1.Tip != h1 || c1.Ref.ObjectType != "widget" || c1.Ref.Remote != "" {
		t.Errorf("unexpected chain c1: %+v", c1)
	}

	c2, ok := chains["refs/remotes/origin/writ/fedcba9876543210/waypoint"]
	if !ok || c2.Tip != h2 || c2.Ref.ObjectType != "waypoint" || c2.Ref.Remote != "origin" {
		t.Errorf("unexpected chain c2: %+v", c2)
	}
}

func TestRefConstructors(t *testing.T) {
	wID := identity.WriterID("0123456789abcdef")
	local := dag.LocalRefName(wID, "widget")
	if local != "refs/writ/0123456789abcdef/widget" {
		t.Errorf("LocalRefName = %q", local)
	}

	remote := dag.RemoteRefName("origin", wID, "waypoint")
	if remote != "refs/remotes/origin/writ/0123456789abcdef/waypoint" {
		t.Errorf("RemoteRefName = %q", remote)
	}
}

func TestNilRepoChains(t *testing.T) {
	_, err := dag.Chains(nil)
	if err == nil {
		t.Fatal("expected error for nil storer")
	}
	_, err = dag.OpenRepo(nil, identity.Identity{})
	if err == nil {
		t.Fatal("expected error for nil repo")
	}
	_, err = dag.Open(t.TempDir()+"/nonexistent", identity.Identity{})
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
