package fold

import (
	"encoding/json"
	"sort"

	"github.com/writtendev/writ/engine/codec"
)

// CommentRules is the v1 comment rule table, mirroring spec/testdata/comments/field-rules.json.
var CommentRules = []Rule{
	{OpType: "create", OpVersion: 1, Field: "subject", Strategy: "create-once"},
	{OpType: "create", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
	{OpType: "create", OpVersion: 1, Field: "in_reply_to", Strategy: "create-once", ValueType: "object-ref"},
	{OpType: "create", OpVersion: 1, Field: "anchor", Strategy: "create-once", ValueType: "anchor"},
	{OpType: "edit", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
	{OpType: "delete", OpVersion: 1, Field: "deleted", Strategy: "tombstone", ValueType: "bool"},
	{OpType: "resolve", OpVersion: 1, Field: "resolved", Strategy: "lww", ValueType: "bool"},
	{OpType: "resolve", OpVersion: 1, Field: "resolved_by", Strategy: "lww", ValueType: "person-ref"},
}

// CommentFold represents the folded state of a comment collaborative object before
// high-level type decoding (e.g. resolve.Anchor parsing).
type CommentFold struct {
	ObjectID   string
	ObjectType string
	TotalOrder []OpRef
	UnknownOps []UnknownOp
	SubjectRaw []byte
	Text       string
	InReplyTo  string
	AnchorRaw  []byte
	Deleted    bool
	Resolved   *bool
	ResolvedBy string
}

// CommentNode represents a node in the comment reply forest.
type CommentNode struct {
	ObjectID string
	Comment  CommentFold
	Replies  []CommentNode
}

// FoldComment executes deterministic fold reduction for a single comment object.
// Subject and anchor payloads are extracted as raw JSON bytes to preserve byte-identical payloads.
func FoldComment(ops []codec.Op) (CommentFold, error) {
	res, err := Fold(ops, CommentRules)
	if err != nil {
		return CommentFold{}, err
	}

	cf := CommentFold{
		ObjectID:   res.ObjectID,
		ObjectType: res.ObjectType,
		TotalOrder: res.TotalOrder,
		UnknownOps: res.UnknownOps,
	}

	if t, ok := res.State["text"].(string); ok {
		cf.Text = t
	}
	if raw, ok := res.State["in_reply_to"].(json.RawMessage); ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			cf.InReplyTo = s
		}
	} else if p, ok := res.State["in_reply_to"].(string); ok {
		cf.InReplyTo = p
	}
	if d, ok := res.State["deleted"].(bool); ok {
		cf.Deleted = d
	}
	if r, ok := res.State["resolved"].(bool); ok {
		cf.Resolved = &r
	}
	if a, ok := res.State["resolved_by"].(string); ok {
		cf.ResolvedBy = a
	}
	if raw, ok := res.State["subject"].(json.RawMessage); ok && len(raw) > 0 && string(raw) != "null" {
		cf.SubjectRaw = append([]byte(nil), raw...)
	}
	if raw, ok := res.State["anchor"].(json.RawMessage); ok && len(raw) > 0 && string(raw) != "null" {
		cf.AnchorRaw = append([]byte(nil), raw...)
	}

	return cf, nil
}

// FoldComments groups operations across multiple comment objects, folds each comment,
// and constructs the reply forest with deterministic sibling ordering.
func FoldComments(ops []codec.Op) ([]CommentNode, error) {
	if len(ops) == 0 {
		return nil, nil
	}

	// Group ops by object_id
	grouped := make(map[string][]codec.Op)
	for _, op := range ops {
		grouped[op.ObjectID] = append(grouped[op.ObjectID], op)
	}

	foldedMap := make(map[string]CommentFold, len(grouped))
	type commentMeta struct {
		fold        CommentFold
		createTStar int64
		createSHA   string
	}
	metaMap := make(map[string]commentMeta, len(grouped))

	for objID, objOps := range grouped {
		cf, err := FoldComment(objOps)
		if err != nil {
			return nil, err
		}
		foldedMap[objID] = cf

		// Determine creation order key (t*, commit SHA)
		var createTStar int64
		var createSHA string
		createRule := Rule{OpType: "create", OpVersion: 1}

		// Find the winning create op in total order
		for _, ref := range cf.TotalOrder {
			for _, o := range objOps {
				if o.ID == ref.Commit && opMatchesRule(o, createRule) {
					createTStar = ref.TStar
					createSHA = ref.Commit
					break
				}
			}
			if createSHA != "" {
				break
			}
		}
		// If no create op found, fall back to first op in total order
		if createSHA == "" && len(cf.TotalOrder) > 0 {
			createTStar = cf.TotalOrder[0].TStar
			createSHA = cf.TotalOrder[0].Commit
		}

		metaMap[objID] = commentMeta{
			fold:        cf,
			createTStar: createTStar,
			createSHA:   createSHA,
		}
	}

	// Build parent -> children map and identify roots
	parentMap := make(map[string]string, len(foldedMap)) // child -> parent
	for objID, cf := range foldedMap {
		if cf.InReplyTo != "" && cf.InReplyTo != objID {
			if _, parentExists := foldedMap[cf.InReplyTo]; parentExists {
				parentMap[objID] = cf.InReplyTo
			}
		}
	}

	// Detect cycles in parentMap:
	visitState := make(map[string]int, len(foldedMap))
	inCycle := make(map[string]bool)

	for objID := range foldedMap {
		if visitState[objID] != 0 {
			continue
		}
		var path []string
		curr := objID
		for curr != "" {
			if visitState[curr] == 1 {
				// Cycle detected: all nodes in path from curr onwards are in a cycle
				cycleStart := false
				for _, nodeID := range path {
					if nodeID == curr {
						cycleStart = true
					}
					if cycleStart {
						inCycle[nodeID] = true
					}
				}
				break
			}
			if visitState[curr] == 2 {
				break
			}
			visitState[curr] = 1
			path = append(path, curr)
			curr = parentMap[curr]
		}
		for _, nodeID := range path {
			visitState[nodeID] = 2
		}
	}

	// Clear parent for all nodes in cycle (promoting them to roots)
	for objID := range inCycle {
		delete(parentMap, objID)
	}

	// Build children map
	childrenMap := make(map[string][]string, len(foldedMap))
	var rootIDs []string
	for objID := range foldedMap {
		parentID, hasParent := parentMap[objID]
		if hasParent {
			childrenMap[parentID] = append(childrenMap[parentID], objID)
		} else {
			rootIDs = append(rootIDs, objID)
		}
	}

	// Sort helper: order by (createTStar, createSHA, objectID)
	sortIDs := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			mI := metaMap[ids[i]]
			mJ := metaMap[ids[j]]
			if mI.createTStar != mJ.createTStar {
				return mI.createTStar < mJ.createTStar
			}
			if mI.createSHA != mJ.createSHA {
				return mI.createSHA < mJ.createSHA
			}
			return ids[i] < ids[j]
		})
	}

	sortIDs(rootIDs)

	var buildTree func(id string) CommentNode
	buildTree = func(id string) CommentNode {
		chIDs := childrenMap[id]
		sortIDs(chIDs)
		replies := make([]CommentNode, 0, len(chIDs))
		for _, chID := range chIDs {
			replies = append(replies, buildTree(chID))
		}
		return CommentNode{
			ObjectID: id,
			Comment:  foldedMap[id],
			Replies:  replies,
		}
	}

	roots := make([]CommentNode, 0, len(rootIDs))
	for _, rootID := range rootIDs {
		roots = append(roots, buildTree(rootID))
	}

	return roots, nil
}
