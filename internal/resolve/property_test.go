package resolve_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/spec"
)

func TestDeterminismShuffledMap(t *testing.T) {
	cases, err := spec.ResolutionVectors()
	if err != nil {
		t.Fatalf("loading resolution vectors: %v", err)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// ResolveRaw, not ParseAnchor+Resolve: a malformed-* vector's
			// anchor.version is not always something ParseAnchor can even
			// decode (WRIT-252), and ResolveRaw is the actual total
			// read-side entry point this determinism property must hold
			// for.
			algo := detectHashAlgo(c.Anchor)

			fileKeys := make([]string, 0, len(c.Target.Files))
			for k := range c.Target.Files {
				fileKeys = append(fileKeys, k)
			}

			var firstResult []byte

			for round := 0; round < 100; round++ {
				// Rebuild map in randomized insertion order
				rng.Shuffle(len(fileKeys), func(i, j int) {
					fileKeys[i], fileKeys[j] = fileKeys[j], fileKeys[i]
				})
				shuffledFiles := make(map[string][]byte, len(fileKeys))
				for _, k := range fileKeys {
					shuffledFiles[k] = []byte(c.Target.Files[k])
				}

				tree := resolve.NewTree(shuffledFiles, algo)
				res := resolve.ResolveRaw(c.Anchor, tree)

				resJSON, err := json.Marshal(res)
				if err != nil {
					t.Fatalf("round %d: marshal outcome: %v", round, err)
				}
				canon, err := canonicaljson.Marshal(resJSON)
				if err != nil {
					t.Fatalf("round %d: canonicaljson: %v", round, err)
				}

				if round == 0 {
					firstResult = canon
				} else if !bytes.Equal(firstResult, canon) {
					t.Fatalf("non-deterministic output on round %d:\nfirst:\n%s\ncurrent:\n%s", round, string(firstResult), string(canon))
				}
			}
		})
	}
}

func TestPurityNoInputMutation(t *testing.T) {
	cases, err := spec.ResolutionVectors()
	if err != nil {
		t.Fatalf("loading resolution vectors: %v", err)
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			rawAnchorCopy := make([]byte, len(c.Anchor))
			copy(rawAnchorCopy, c.Anchor)

			files := make(map[string][]byte, len(c.Target.Files))
			fileSnapshots := make(map[string][]byte, len(c.Target.Files))
			for k, v := range c.Target.Files {
				b := []byte(v)
				files[k] = b
				bCopy := make([]byte, len(b))
				copy(bCopy, b)
				fileSnapshots[k] = bCopy
			}

			algo := detectHashAlgo(c.Anchor)
			tree := resolve.NewTree(files, algo)

			// ResolveRaw, not ParseAnchor+Resolve — see TestDeterminismShuffledMap.
			_ = resolve.ResolveRaw(rawAnchorCopy, tree)

			// Check anchor raw bytes unchanged
			if !bytes.Equal(rawAnchorCopy, c.Anchor) {
				t.Errorf("anchor bytes mutated after ResolveRaw")
			}

			// Check all input file bytes unchanged
			for k, originalBytes := range fileSnapshots {
				currentBytes := files[k]
				if !bytes.Equal(currentBytes, originalBytes) {
					t.Errorf("file %q bytes mutated after Resolve", k)
				}
			}
		})
	}
}
