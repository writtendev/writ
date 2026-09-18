package fold

// ReachOracle provides O(1) causal happens-before ancestry checks over the restricted DAG.
type ReachOracle interface {
	IsAncestor(ancestorID, descendantID string) bool
}

type bitsetReachOracle struct {
	idToIndex map[string]int
	ancestors [][]uint64
	numWords  int
}

// BuildReachability computes the transitive ancestry bitsets over orderedOps in topological order.
//
// In total order L, all parents of an operation appear strictly before that operation.
// Ancestry is computed in a single forward pass: the ancestor set of node i is the union of
// its restricted parents and each parent's transitive ancestors.
func BuildReachability(orderedOps []OrderedOp) ReachOracle {
	n := len(orderedOps)
	if n == 0 {
		return &bitsetReachOracle{
			idToIndex: make(map[string]int),
		}
	}

	numWords := (n + 63) / 64
	idToIndex := make(map[string]int, n)
	for i, o := range orderedOps {
		idToIndex[o.Op.ID] = i
	}

	ancestors := make([][]uint64, n)
	for i := range ancestors {
		ancestors[i] = make([]uint64, numWords)
	}

	for i, o := range orderedOps {
		for _, pID := range o.Op.Parents {
			if pIdx, ok := idToIndex[pID]; ok {
				// Mark immediate parent
				ancestors[i][pIdx/64] |= (uint64(1) << (pIdx % 64))
				// Transitive closure: union parent's ancestors (already fully computed since pIdx < i)
				pWords := ancestors[pIdx]
				for w := 0; w < numWords; w++ {
					ancestors[i][w] |= pWords[w]
				}
			}
		}
	}

	return &bitsetReachOracle{
		idToIndex: idToIndex,
		ancestors: ancestors,
		numWords:  numWords,
	}
}

// IsAncestor returns true if ancestorID causally precedes descendantID (ancestorID ≺ descendantID)
// in the restricted DAG.
func (r *bitsetReachOracle) IsAncestor(ancestorID, descendantID string) bool {
	if r == nil || len(r.ancestors) == 0 {
		return false
	}
	aIdx, aOk := r.idToIndex[ancestorID]
	dIdx, dOk := r.idToIndex[descendantID]
	if !aOk || !dOk {
		return false
	}
	if aIdx >= dIdx {
		return false
	}
	return (r.ancestors[dIdx][aIdx/64] & (uint64(1) << (aIdx % 64))) != 0
}

// lazyReachOracle defers building the transitive ancestry bitset
// (BuildReachability, an n×⌈n/64⌉ allocation) until it is first asked an
// IsAncestor question. Six of the nine strategies in the catalogue — lww,
// create-once, set-union, append, lattice, keyed-lww — never call
// IsAncestor at all, so an object folded only through those never builds
// the bitset; set-observed-remove (only once an add has a same-item
// remove to check), tombstone, and multi-value still build it, on first
// use, exactly as before.
//
// No mutex guards the build. Fold drives every accumulator — Result()
// included — from a single goroutine, and this package's own import
// allowlist (imports_test.go) has no room for "sync": that absence is a
// decision, not an oversight.
type lazyReachOracle struct {
	orderedOps []OrderedOp
	built      ReachOracle
}

// newLazyReachOracle returns a ReachOracle over orderedOps that builds the
// underlying bitsetReachOracle lazily, on first use.
func newLazyReachOracle(orderedOps []OrderedOp) ReachOracle {
	return &lazyReachOracle{orderedOps: orderedOps}
}

func (l *lazyReachOracle) IsAncestor(ancestorID, descendantID string) bool {
	if l.built == nil {
		l.built = BuildReachability(l.orderedOps)
	}
	return l.built.IsAncestor(ancestorID, descendantID)
}
