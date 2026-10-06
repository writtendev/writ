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
			fileKeys := make([]string, 0, len(c.Target.Contents))
			for k := range c.Target.Contents {
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
					shuffledFiles[k] = c.Target.Contents[k]
				}

				tree := resolve.NewTree(shuffledFiles)
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

			files := make(map[string][]byte, len(c.Target.Contents))
			fileSnapshots := make(map[string][]byte, len(c.Target.Contents))
			for k, v := range c.Target.Contents {
				b := v
				files[k] = b
				bCopy := make([]byte, len(b))
				copy(bCopy, b)
				fileSnapshots[k] = bCopy
			}

			tree := resolve.NewTree(files)

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
