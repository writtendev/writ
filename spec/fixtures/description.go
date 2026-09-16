// Package fixtures generates the conformance-corpus git repositories from
// declarative descriptions. See README.md in this directory for why the
// corpus is built this way rather than committed as bundles or tarballs.
package fixtures

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Description is the declarative, checked-in definition of one fixture
// repo: a set of refs, each built from one or more history generations.
// A ref with more than one generation models a force-push: every
// generation but the last represents history that was once at the ref's
// tip and later got pushed over.
type Description struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Refs        []RefDesc        `yaml:"refs"`
	Resolutions []ResolutionDesc `yaml:"resolutions,omitempty"`
}

// RefDesc describes one ref and everything ever pushed to it, oldest
// generation first. The final generation's tip is the ref's value in the
// generated repo.
type RefDesc struct {
	Name    string       `yaml:"name"`
	History []Generation `yaml:"history"`
}

// Generation is one contiguous commit chain, rooted (its first commit has
// no parent) rather than built on a previous generation — force-pushed
// history in real git need not share ancestry with what it replaces, and
// keeping generations independent keeps both the format and the generator
// simple. If a fixture needs a shared-ancestry force-push, model the
// shared commits as a literal prefix repeated in both generations; content
// addressing collapses them back onto the same objects, which is exactly
// what real git does when two histories share a base.
//
// A generation with no KeptAs is a true force-push casualty: its commits
// are written to the object store (so they're inspectable by SHA, and so
// determinism covers them) but no ref points at them in the generated
// repo, matching how an overwritten branch tip actually behaves in git
// until GC. Set KeptAs to also expose the generation under an auxiliary
// ref, e.g. for fixtures that need to assert against the pre-rewrite
// state directly (the orphaned-anchors family).
type Generation struct {
	KeptAs  string       `yaml:"keep_as,omitempty"`
	Commits []CommitDesc `yaml:"commits"`
}

// ExpectDesc specifies the declared machine-readable expectation for an op commit:
// either accept, or reject with a reason from the closed set. Verification
// is an optional accept-only refinement (WRIT-251 ruling 1): signature
// verification never gates fold, so a bad signature is no longer a reject
// reason, but an accepted commit can still pin the verification outcome
// codec.Verify must observe, defaulting to "valid" when omitted.
type ExpectDesc struct {
	Accept       bool   `yaml:"accept,omitempty"`
	Reject       string `yaml:"reject,omitempty"`
	Verification string `yaml:"verification,omitempty"`
}

func (e *ExpectDesc) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Value == "accept" {
			e.Accept = true
			e.Reject = ""
			return nil
		}
		return fmt.Errorf("invalid expect value %q: scalar must be 'accept'", value.Value)
	}
	if value.Kind == yaml.MappingNode {
		var m struct {
			Accept       bool   `yaml:"accept"`
			Reject       string `yaml:"reject"`
			Verification string `yaml:"verification"`
		}
		if err := value.Decode(&m); err != nil {
			return err
		}
		if m.Accept && m.Reject != "" {
			return fmt.Errorf("expect cannot specify both accept and reject")
		}
		if !m.Accept && m.Reject == "" {
			return fmt.Errorf("expect must specify either accept: true or reject: <reason>")
		}
		if m.Verification != "" && !m.Accept {
			return fmt.Errorf("expect.verification may only be set alongside accept: true")
		}
		e.Accept = m.Accept
		e.Reject = m.Reject
		e.Verification = m.Verification
		return nil
	}
	return fmt.Errorf("expect must be a scalar ('accept') or a mapping ({reject: <reason>})")
}

// CommitDesc is one commit: full file contents or an op block for its tree,
// an identity naming a signer from the keyring in keys/, a fixed timestamp,
// and optional parent labels, committer override, signing override, or tamper instruction.
type CommitDesc struct {
	ID          string            `yaml:"id,omitempty"`
	Parents     []string          `yaml:"parents,omitempty"`
	Author      string            `yaml:"author"`
	Committer   string            `yaml:"committer,omitempty"`
	Timestamp   time.Time         `yaml:"timestamp"`
	Message     string            `yaml:"message,omitempty"`
	Files       map[string]string `yaml:"files,omitempty"`
	Op          *OpDesc           `yaml:"op,omitempty"`
	OpJSONSize  int               `yaml:"op_json_size,omitempty"`
	SignAs      string            `yaml:"sign_as,omitempty"`
	Tamper      string            `yaml:"tamper,omitempty"`
	Unsigned    bool              `yaml:"unsigned,omitempty"`
	Expect      *ExpectDesc       `yaml:"expect,omitempty"`
	Disposition string            `yaml:"disposition,omitempty"`
}

// ResolutionDesc describes one resolution case in an orphan-anchors fixture.
type ResolutionDesc struct {
	Name   string               `yaml:"name"`
	Anchor ResolutionAnchorDesc `yaml:"anchor"`
	Target string               `yaml:"target"`
	Expect ResolutionExpectDesc `yaml:"expect"`
}

// ResolutionAnchorDesc defines the anchor source to capture.
type ResolutionAnchorDesc struct {
	At    string              `yaml:"at,omitempty"`
	Path  string              `yaml:"path,omitempty"`
	Side  string              `yaml:"side,omitempty"`  // "new" | "old" | "both" (defaults to "new")
	Range []int               `yaml:"range,omitempty"` // [start, end]
	Old   *ResolutionSideDesc `yaml:"old,omitempty"`
	New   *ResolutionSideDesc `yaml:"new,omitempty"`
}

// ResolutionSideDesc defines the side anchor source for cross-side cases.
type ResolutionSideDesc struct {
	At    string `yaml:"at"`
	Path  string `yaml:"path"`
	Range []int  `yaml:"range,omitempty"`
}

// ResolutionExpectDesc specifies the expected outcome for a resolution case.
type ResolutionExpectDesc struct {
	Outcome string                    `yaml:"outcome,omitempty"`
	Match   string                    `yaml:"match,omitempty"`
	Reason  string                    `yaml:"reason,omitempty"`
	Status  string                    `yaml:"status,omitempty"`
	Old     *ResolutionSideExpectDesc `yaml:"old,omitempty"`
	New     *ResolutionSideExpectDesc `yaml:"new,omitempty"`
}

// ResolutionSideExpectDesc specifies per-side expected outcome for cross-side cases.
type ResolutionSideExpectDesc struct {
	Outcome string `yaml:"outcome"`
	Match   string `yaml:"match,omitempty"`
	Reason  string `yaml:"reason,omitempty"`
}

var validMatchRungs = map[string]bool{
	"exact-path-blob":  true,
	"exact-blob-moved": true,
	"context-exact":    true,
	"context-fuzzy":    true,
}

var validOrphanReasons = map[string]bool{
	"path-absent":         true,
	"no-candidate":        true,
	"below-threshold":     true,
	"ambiguous":           true,
	"unsupported-version": true,
}

var validOutcomes = map[string]bool{
	"resolved":           true,
	"orphaned":           true,
	"partially-resolved": true,
}

var validRejectReasons = map[string]bool{
	"non-canonical-payload": true,
	"duplicate-key":         true,
	"lone-surrogate":        true,
	"schema-violation":      true,
	"extra-tree-entry":      true,
	"op-json-subdirectory":  true,
	"missing-op-json":       true,
	"invalid-op-json-mode":  true,
	"committer-mismatch":    true,
	"payload-too-large":     true,
}

// validVerificationOutcomes is codec.VerificationOutcome's closed set,
// mirrored here so a fixture's expect.verification is validated against
// the same vocabulary codec.Verify emits (WRIT-251 ruling 1). Signature
// outcomes were reject reasons before this ruling; they moved here because
// fold no longer treats any of them as a reason to drop the op.
var validVerificationOutcomes = map[string]bool{
	"valid":               true,
	"wrong-key":           true,
	"unsigned":            true,
	"corrupted-signature": true,
	"payload-mutated":     true,
}

// Load parses a single fixture description from YAML and validates its integrity.
func Load(data []byte) (*Description, error) {
	var d Description
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("fixtures: parse description: %w", err)
	}
	if d.Name == "" {
		return nil, fmt.Errorf("fixtures: description missing name")
	}
	if len(d.Refs) == 0 {
		return nil, fmt.Errorf("fixtures: description %q has no refs", d.Name)
	}

	seenRefNames := map[string]string{}
	seenLabels := map[string]bool{}

	for _, r := range d.Refs {
		if r.Name == "" {
			return nil, fmt.Errorf("fixtures: description %q has a ref with no name", d.Name)
		}
		if src, dup := seenRefNames[r.Name]; dup {
			return nil, fmt.Errorf("fixtures: description %q: ref %q collides with %s", d.Name, r.Name, src)
		}
		seenRefNames[r.Name] = fmt.Sprintf("ref %q", r.Name)
		if len(r.History) == 0 {
			return nil, fmt.Errorf("fixtures: description %q ref %q has no history", d.Name, r.Name)
		}
		for gi, g := range r.History {
			if len(g.Commits) == 0 {
				return nil, fmt.Errorf("fixtures: description %q ref %q generation %d has no commits", d.Name, r.Name, gi)
			}
			if g.KeptAs != "" {
				if src, dup := seenRefNames[g.KeptAs]; dup {
					return nil, fmt.Errorf("fixtures: description %q: keep_as %q (ref %q generation %d) collides with %s", d.Name, g.KeptAs, r.Name, gi, src)
				}
				seenRefNames[g.KeptAs] = fmt.Sprintf("keep_as %q (ref %q generation %d)", g.KeptAs, r.Name, gi)
			}
			for ci, c := range g.Commits {
				if c.Author == "" {
					return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d has no author", d.Name, r.Name, gi, ci)
				}
				if c.Timestamp.IsZero() {
					return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d has no timestamp", d.Name, r.Name, gi, ci)
				}
				if c.ID != "" {
					if seenLabels[c.ID] {
						return nil, fmt.Errorf("fixtures: description %q: duplicate commit id %q", d.Name, c.ID)
					}
					seenLabels[c.ID] = true
				}
				if c.Parents != nil {
					for _, p := range c.Parents {
						if !seenLabels[p] {
							return nil, fmt.Errorf("fixtures: description %q: commit references unknown or forward parent label %q", d.Name, p)
						}
					}
				}
				if c.Op != nil {
					if len(c.Files) > 0 {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d specifies both 'op' and 'files'", d.Name, r.Name, gi, ci)
					}
					if c.Message != "" {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d specifies both 'op' and 'message'", d.Name, r.Name, gi, ci)
					}
					if c.Op.ObjectID == "" {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d op missing object_id", d.Name, r.Name, gi, ci)
					}
					if c.Op.ObjectType == "" {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d op missing object_type", d.Name, r.Name, gi, ci)
					}
					if c.Op.OpType == "" {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d op missing op_type", d.Name, r.Name, gi, ci)
					}
				}
				if c.OpJSONSize != 0 {
					if c.Op == nil {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d specifies 'op_json_size' without 'op'", d.Name, r.Name, gi, ci)
					}
					if _, err := PadOpJSON(c.Op, c.OpJSONSize); err != nil {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid op_json_size: %w", d.Name, r.Name, gi, ci, err)
					}
				}
				if c.SignAs != "" {
					if _, err := lookupIdentity(c.SignAs); err != nil {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid sign_as: %w", d.Name, r.Name, gi, ci, err)
					}
				}
				if c.Tamper != "" {
					if !IsValidTamper(c.Tamper) {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid tamper %q (must be closed enum)", d.Name, r.Name, gi, ci, c.Tamper)
					}
				}
				if c.Expect != nil && c.Expect.Reject != "" {
					if !validRejectReasons[c.Expect.Reject] {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid reject reason %q (must be closed enum)", d.Name, r.Name, gi, ci, c.Expect.Reject)
					}
				}
				if c.Expect != nil && c.Expect.Verification != "" {
					if !validVerificationOutcomes[c.Expect.Verification] {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid verification outcome %q (must be closed enum)", d.Name, r.Name, gi, ci, c.Expect.Verification)
					}
				}
				if c.Disposition != "" {
					if c.Op == nil {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d specifies disposition without 'op'", d.Name, r.Name, gi, ci)
					}
					if c.Disposition != "interpretable" && c.Disposition != "opaque" {
						return nil, fmt.Errorf("fixtures: description %q ref %q generation %d commit %d invalid disposition %q (must be closed enum: 'interpretable' or 'opaque')", d.Name, r.Name, gi, ci, c.Disposition)
					}
				}
			}
		}
	}

	for _, res := range d.Resolutions {
		if res.Name == "" {
			return nil, fmt.Errorf("fixtures: description %q resolution case missing name", d.Name)
		}
		if res.Target == "" {
			return nil, fmt.Errorf("fixtures: description %q resolution %q missing target", d.Name, res.Name)
		}
		if !seenLabels[res.Target] {
			return nil, fmt.Errorf("fixtures: description %q resolution %q references unknown target commit label %q", d.Name, res.Name, res.Target)
		}

		if res.Anchor.Old != nil || res.Anchor.New != nil {
			if res.Anchor.Old != nil {
				if res.Anchor.Old.At == "" || !seenLabels[res.Anchor.Old.At] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q old anchor references unknown commit label %q", d.Name, res.Name, res.Anchor.Old.At)
				}
				if res.Anchor.Old.Path == "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q old anchor missing path", d.Name, res.Name)
				}
				if res.Anchor.Old.Range != nil {
					if len(res.Anchor.Old.Range) != 2 || res.Anchor.Old.Range[0] < 1 || res.Anchor.Old.Range[1] < res.Anchor.Old.Range[0] {
						return nil, fmt.Errorf("fixtures: description %q resolution %q old anchor range must be 1-based [start, end] with end >= start", d.Name, res.Name)
					}
				}
			}
			if res.Anchor.New != nil {
				if res.Anchor.New.At == "" || !seenLabels[res.Anchor.New.At] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q new anchor references unknown commit label %q", d.Name, res.Name, res.Anchor.New.At)
				}
				if res.Anchor.New.Path == "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q new anchor missing path", d.Name, res.Name)
				}
				if res.Anchor.New.Range != nil {
					if len(res.Anchor.New.Range) != 2 || res.Anchor.New.Range[0] < 1 || res.Anchor.New.Range[1] < res.Anchor.New.Range[0] {
						return nil, fmt.Errorf("fixtures: description %q resolution %q new anchor range must be 1-based [start, end] with end >= start", d.Name, res.Name)
					}
				}
			}
		} else {
			if res.Anchor.At == "" || !seenLabels[res.Anchor.At] {
				return nil, fmt.Errorf("fixtures: description %q resolution %q anchor references unknown commit label %q", d.Name, res.Name, res.Anchor.At)
			}
			if res.Anchor.Path == "" {
				return nil, fmt.Errorf("fixtures: description %q resolution %q anchor missing path", d.Name, res.Name)
			}
			if res.Anchor.Side != "" && res.Anchor.Side != "new" && res.Anchor.Side != "old" && res.Anchor.Side != "both" {
				return nil, fmt.Errorf("fixtures: description %q resolution %q anchor invalid side %q (must be 'new', 'old', or 'both')", d.Name, res.Name, res.Anchor.Side)
			}
			if res.Anchor.Range != nil {
				if len(res.Anchor.Range) != 2 || res.Anchor.Range[0] < 1 || res.Anchor.Range[1] < res.Anchor.Range[0] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q anchor range must be 1-based [start, end] with end >= start", d.Name, res.Name)
				}
			}
		}

		if res.Expect.Status != "" && !validOutcomes[res.Expect.Status] {
			return nil, fmt.Errorf("fixtures: description %q resolution %q invalid status %q (must be closed enum)", d.Name, res.Name, res.Expect.Status)
		}
		if res.Expect.Old != nil {
			if !validOutcomes[res.Expect.Old.Outcome] {
				return nil, fmt.Errorf("fixtures: description %q resolution %q invalid old outcome %q (must be closed enum)", d.Name, res.Name, res.Expect.Old.Outcome)
			}
			if res.Expect.Old.Outcome == "resolved" {
				if !validMatchRungs[res.Expect.Old.Match] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid old match %q (must be closed enum)", d.Name, res.Name, res.Expect.Old.Match)
				}
				if res.Expect.Old.Reason != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q resolved old expect cannot specify orphan reason", d.Name, res.Name)
				}
			} else if res.Expect.Old.Outcome == "orphaned" {
				if !validOrphanReasons[res.Expect.Old.Reason] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid old reason %q (must be closed enum)", d.Name, res.Name, res.Expect.Old.Reason)
				}
				if res.Expect.Old.Match != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q orphaned old expect cannot specify match", d.Name, res.Name)
				}
			}
		}
		if res.Expect.New != nil {
			if !validOutcomes[res.Expect.New.Outcome] {
				return nil, fmt.Errorf("fixtures: description %q resolution %q invalid new outcome %q (must be closed enum)", d.Name, res.Name, res.Expect.New.Outcome)
			}
			if res.Expect.New.Outcome == "resolved" {
				if !validMatchRungs[res.Expect.New.Match] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid new match %q (must be closed enum)", d.Name, res.Name, res.Expect.New.Match)
				}
				if res.Expect.New.Reason != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q resolved new expect cannot specify orphan reason", d.Name, res.Name)
				}
			} else if res.Expect.New.Outcome == "orphaned" {
				if !validOrphanReasons[res.Expect.New.Reason] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid new reason %q (must be closed enum)", d.Name, res.Name, res.Expect.New.Reason)
				}
				if res.Expect.New.Match != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q orphaned new expect cannot specify match", d.Name, res.Name)
				}
			}
		}
		if res.Expect.Outcome != "" {
			if !validOutcomes[res.Expect.Outcome] {
				return nil, fmt.Errorf("fixtures: description %q resolution %q invalid outcome %q (must be closed enum)", d.Name, res.Name, res.Expect.Outcome)
			}
			if res.Expect.Outcome == "resolved" {
				if !validMatchRungs[res.Expect.Match] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid match %q (must be closed enum)", d.Name, res.Name, res.Expect.Match)
				}
				if res.Expect.Reason != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q resolved expect cannot specify orphan reason", d.Name, res.Name)
				}
			} else if res.Expect.Outcome == "orphaned" {
				if !validOrphanReasons[res.Expect.Reason] {
					return nil, fmt.Errorf("fixtures: description %q resolution %q invalid reason %q (must be closed enum)", d.Name, res.Name, res.Expect.Reason)
				}
				if res.Expect.Match != "" {
					return nil, fmt.Errorf("fixtures: description %q resolution %q orphaned expect cannot specify match", d.Name, res.Name)
				}
			}
		}
	}

	return &d, nil
}
