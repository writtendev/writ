package fold

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"

	"github.com/writtendev/writ/internal/codec"
)

var (
	// ErrCycle is returned when the operation graph contains a directed cycle.
	ErrCycle = errors.New("fold: cycle detected in op graph")

	// ErrDuplicateOpID is returned when the input set contains duplicate operation IDs.
	ErrDuplicateOpID = errors.New("fold: duplicate op id")

	// ErrMixedObjects is returned when the input set spans multiple object IDs.
	ErrMixedObjects = errors.New("fold: input ops span multiple object IDs")
)

// OrderedOp pairs a codec.Op with its causality-monotone effective timestamp t*.
type OrderedOp struct {
	Op    codec.Op
	TStar int64
}

type readyItem struct {
	op    codec.Op
	tStar int64
}

type readyHeap []readyItem

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	if h[i].tStar != h[j].tStar {
		return h[i].tStar < h[j].tStar
	}
	return h[i].op.ID < h[j].op.ID
}
func (h readyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any) {
	*h = append(*h, x.(readyItem))
}
func (h *readyHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

type node struct {
	op             codec.Op
	inDegree       int
	children       []string
	maxParentTStar int64
	hasParentTStar bool
}

// Order computes the deterministic total order L of an object's operations set S
// using Kahn's algorithm with a (t*, id) min-heap priority queue per spec/fold.md §4.
//
// Parent edges are restricted to IDs present in the input set S; missing parent commits
// or foreign object commits are omitted from ancestry without error.
//
// Order returns ErrMixedObjects if ops contains operations for more than one ObjectID,
// ErrDuplicateOpID if any op ID is repeated, or ErrCycle if a causal cycle is detected.
// If ops is empty, Order returns (nil, nil). The input slice is never mutated.
func Order(ops []codec.Op) ([]codec.Op, error) {
	orderedOps, err := OrderWithTStar(ops)
	if err != nil {
		return nil, err
	}
	if len(orderedOps) == 0 {
		return nil, nil
	}
	res := make([]codec.Op, len(orderedOps))
	for i, o := range orderedOps {
		res[i] = o.Op
	}
	return res, nil
}

// OrderWithTStar computes the deterministic total order L and returns OrderedOp
// elements carrying each operation alongside its computed effective timestamp t*.
func OrderWithTStar(ops []codec.Op) ([]OrderedOp, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	firstObjID := ops[0].ObjectID
	inSet := make(map[string]int, len(ops))
	for i, op := range ops {
		if op.ObjectID != firstObjID {
			return nil, fmt.Errorf("%w: %q and %q", ErrMixedObjects, firstObjID, op.ObjectID)
		}
		if _, exists := inSet[op.ID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateOpID, op.ID)
		}
		inSet[op.ID] = i
	}

	nodes := make(map[string]*node, len(ops))
	for _, op := range ops {
		nodes[op.ID] = &node{
			op: op,
		}
	}

	for _, op := range ops {
		currNode := nodes[op.ID]
		for _, pID := range op.Parents {
			if _, ok := inSet[pID]; ok {
				currNode.inDegree++
				nodes[pID].children = append(nodes[pID].children, op.ID)
			}
		}
	}

	h := &readyHeap{}
	heap.Init(h)

	for _, op := range ops {
		n := nodes[op.ID]
		if n.inDegree == 0 {
			tStar := op.Author.When.UTC().Unix()
			heap.Push(h, readyItem{op: op, tStar: tStar})
		}
	}

	ordered := make([]OrderedOp, 0, len(ops))

	for h.Len() > 0 {
		item := heap.Pop(h).(readyItem)
		ordered = append(ordered, OrderedOp{Op: item.op, TStar: item.tStar})

		n := nodes[item.op.ID]
		for _, childID := range n.children {
			ch := nodes[childID]
			if !ch.hasParentTStar || item.tStar > ch.maxParentTStar {
				ch.maxParentTStar = item.tStar
				ch.hasParentTStar = true
			}
			ch.inDegree--
			if ch.inDegree == 0 {
				childTime := ch.op.Author.When.UTC().Unix()
				tStar := childTime
				if ch.hasParentTStar && ch.maxParentTStar > tStar {
					tStar = ch.maxParentTStar
				}
				heap.Push(h, readyItem{op: ch.op, tStar: tStar})
			}
		}
	}

	if len(ordered) != len(ops) {
		emitted := make(map[string]bool, len(ordered))
		for _, o := range ordered {
			emitted[o.Op.ID] = true
		}
		var cycleIDs []string
		for _, op := range ops {
			if !emitted[op.ID] {
				cycleIDs = append(cycleIDs, op.ID)
			}
		}
		sort.Strings(cycleIDs)
		return nil, fmt.Errorf("%w: un-emitted ops %v", ErrCycle, cycleIDs)
	}

	return ordered, nil
}
