package dag_test

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

func generateRandomDAG(rng *rand.Rand) []codec.Op {
	numWriters := 1 + rng.Intn(5) // 1 to 5 writer chains
	numOps := 1 + rng.Intn(50)    // 1 to 50 ops

	writerTips := make([]string, numWriters)
	allOps := make([]codec.Op, 0, numOps)
	seenIDs := make(map[string]bool)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	for i := 0; i < numOps; i++ {
		var id string
		for {
			b := make([]byte, 20)
			rng.Read(b)
			id = hex.EncodeToString(b)
			if !seenIDs[id] {
				seenIDs[id] = true
				break
			}
		}

		w := rng.Intn(numWriters)
		var parents []string
		if writerTips[w] != "" {
			parents = append(parents, writerTips[w])
		}

		// 0 to 2 causal parents from earlier ops (acyclic by construction)
		numCausal := rng.Intn(3)
		if len(allOps) > 0 && numCausal > 0 {
			for c := 0; c < numCausal; c++ {
				randIdx := rng.Intn(len(allOps))
				pID := allOps[randIdx].ID
				already := false
				for _, existing := range parents {
					if existing == pID {
						already = true
						break
					}
				}
				if !already {
					parents = append(parents, pID)
				}
			}
		}

		writerTips[w] = id

		// Timestamps with clock skew / inversions
		opTime := baseTime + int64(rng.Intn(2000)-1000)
		subSecondNs := int64(rng.Intn(1_000_000_000))

		op := codec.Op{
			ID:      id,
			Parents: parents,
			Author: codec.Identity{
				Name:  fmt.Sprintf("Writer-%d", w),
				Email: fmt.Sprintf("w%d@example.test", w),
				When:  time.Unix(opTime, subSecondNs).UTC(),
			},
			Envelope: codec.Envelope{
				ObjectID:   "obj-property-test",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
			},
		}
		allOps = append(allOps, op)
	}

	// Drop a fraction (0% to 30%) of ops to exercise truncated ancestry
	dropFraction := rng.Float64() * 0.3
	var finalOps []codec.Op
	for _, op := range allOps {
		if rng.Float64() >= dropFraction {
			finalOps = append(finalOps, op)
		}
	}

	if len(finalOps) == 0 {
		finalOps = allOps
	}

	return finalOps
}

func cloneOps(ops []codec.Op) []codec.Op {
	res := make([]codec.Op, len(ops))
	for i, op := range ops {
		res[i] = op
		if op.Parents != nil {
			res[i].Parents = append([]string(nil), op.Parents...)
		}
	}
	return res
}

func computeOracleTStar(ops []codec.Op, inSet map[string]bool) map[string]int64 {
	tStar := make(map[string]int64, len(inSet))
	opMap := make(map[string]codec.Op, len(ops))
	for _, op := range ops {
		opMap[op.ID] = op
	}

	var getTStar func(id string) int64
	getTStar = func(id string) int64 {
		if t, ok := tStar[id]; ok {
			return t
		}
		op := opMap[id]
		res := op.Author.When.Unix()
		for _, p := range op.Parents {
			if inSet[p] {
				pt := getTStar(p)
				if pt > res {
					res = pt
				}
			}
		}
		tStar[id] = res
		return res
	}

	for id := range inSet {
		getTStar(id)
	}
	return tStar
}

func TestProperty_RandomDAGs(t *testing.T) {
	seed := time.Now().UnixNano()
	rng := rand.New(rand.NewSource(seed))
	t.Logf("property test random seed: %d", seed)

	const iterations = 50

	for iter := 0; iter < iterations; iter++ {
		ops := generateRandomDAG(rng)
		inputCopy := cloneOps(ops)

		ordered, err := dag.Order(ops)
		if err != nil {
			t.Fatalf("seed %d (iter %d): Order failed: %v", seed, iter, err)
		}

		// Property 5: Purity (input slice and its Parents slices not mutated)
		if !reflect.DeepEqual(ops, inputCopy) {
			t.Fatalf("seed %d (iter %d): Property 5 (Purity) violated: input ops slice was mutated", seed, iter)
		}
		orderedAgain, err := dag.Order(ops)
		if err != nil {
			t.Fatalf("seed %d (iter %d): consecutive Order failed: %v", seed, iter, err)
		}
		if !reflect.DeepEqual(ordered, orderedAgain) {
			t.Fatalf("seed %d (iter %d): Property 5 (Purity) violated: consecutive calls gave different results", seed, iter)
		}

		// Property 1: Totality (|L| = |S|, every op exactly once)
		if len(ordered) != len(ops) {
			t.Fatalf("seed %d (iter %d): Property 1 (Totality) violated: len(ordered)=%d, len(ops)=%d", seed, iter, len(ordered), len(ops))
		}
		inSet := make(map[string]bool, len(ops))
		for _, op := range ops {
			inSet[op.ID] = true
		}
		seenInOrder := make(map[string]bool, len(ordered))
		pos := make(map[string]int, len(ordered))
		for i, op := range ordered {
			if !inSet[op.ID] {
				t.Fatalf("seed %d (iter %d): Property 1 (Totality) violated: unknown op %s in output", seed, iter, op.ID)
			}
			if seenInOrder[op.ID] {
				t.Fatalf("seed %d (iter %d): Property 1 (Totality) violated: duplicate op %s in output", seed, iter, op.ID)
			}
			seenInOrder[op.ID] = true
			pos[op.ID] = i
		}

		// Property 2: Parents before children
		for _, op := range ops {
			childPos := pos[op.ID]
			for _, p := range op.Parents {
				if inSet[p] {
					parentPos := pos[p]
					if parentPos >= childPos {
						t.Fatalf("seed %d (iter %d): Property 2 (Parents before children) violated: parent %s (pos %d) >= child %s (pos %d)",
							seed, iter, p, parentPos, op.ID, childPos)
					}
				}
			}
		}

		// Property 3: Determinism under permutation (shuffle 100x -> byte-identical order)
		for perm := 0; perm < 100; perm++ {
			shuffled := cloneOps(ops)
			rng.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})
			shuffledOrdered, err := dag.Order(shuffled)
			if err != nil {
				t.Fatalf("seed %d (iter %d, perm %d): Order on shuffled input failed: %v", seed, iter, perm, err)
			}
			if !reflect.DeepEqual(shuffledOrdered, ordered) {
				t.Fatalf("seed %d (iter %d, perm %d): Property 3 (Determinism under permutation) violated", seed, iter, perm)
			}
		}

		// Property 4: Greedy minimality (recomputing ready set independently and verifying each step)
		oracleTStar := computeOracleTStar(ops, inSet)
		opMap := make(map[string]codec.Op, len(ops))
		for _, op := range ops {
			opMap[op.ID] = op
		}

		emitted := make(map[string]bool, len(ops))
		for step, op := range ordered {
			var ready []string
			for id, o := range opMap {
				if emitted[id] {
					continue
				}
				allParentsEmitted := true
				for _, p := range o.Parents {
					if inSet[p] && !emitted[p] {
						allParentsEmitted = false
						break
					}
				}
				if allParentsEmitted {
					ready = append(ready, id)
				}
			}

			if len(ready) == 0 {
				t.Fatalf("seed %d (iter %d, step %d): ready set unexpectedly empty", seed, iter, step)
			}

			bestID := ready[0]
			bestT := oracleTStar[bestID]
			for _, candID := range ready[1:] {
				candT := oracleTStar[candID]
				if candT < bestT || (candT == bestT && candID < bestID) {
					bestID = candID
					bestT = candT
				}
			}

			if op.ID != bestID {
				t.Fatalf("seed %d (iter %d, step %d): Property 4 (Greedy minimality) violated: emitted %s (t*=%d), oracle expected %s (t*=%d)",
					seed, iter, step, op.ID, oracleTStar[op.ID], bestID, bestT)
			}

			emitted[op.ID] = true
		}
	}
}
