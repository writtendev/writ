package fixtures

// Manifest is the byte-deterministic summary of one generated fixture
// repo. It's what golden files capture: not the .git directory itself
// (which can't be committed), but every SHA generation produced, so a
// regeneration that disagrees with the golden file has either drifted
// from its description or found a determinism bug.
type Manifest struct {
	// Refs is the generated repo's actual ref set, as `git ls-remote`
	// would report it: one entry per ref name in the description, plus
	// one per KeptAs.
	Refs []RefState `json:"refs"`

	// Generations covers every generation of every ref, including ones
	// with no KeptAs — commits that exist in the object store but that no
	// ref in the repo points at, exactly like a force-pushed-over branch
	// tip before GC. This is what lets a fixture that needs the
	// pre-rewrite SHA find it without needing a ref for every generation.
	Generations []GenerationState `json:"generations"`
}

type RefState struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

type GenerationState struct {
	Ref     string        `json:"ref"`
	Index   int           `json:"index"`
	KeptAs  string        `json:"kept_as,omitempty"`
	Commits []CommitState `json:"commits"` // oldest first
}

type CommitState struct {
	SHA       string   `json:"sha"`
	Tree      string   `json:"tree"`
	Parents   []string `json:"parents"`
	Author    string   `json:"author"`
	Timestamp string   `json:"timestamp"`
	Message   string   `json:"message"`
	// MessageSize is the message's byte length, recorded in place of the
	// message for a commit padded to a pinned size (commit_size).
	MessageSize int  `json:"message_size,omitempty"`
	Signed      bool `json:"signed"`
}
