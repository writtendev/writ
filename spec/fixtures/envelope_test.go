package fixtures_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestEnvelopeFamily registers the envelope fixture family and runs all
// envelope-*.yaml descriptions through the golden test harness.
func TestEnvelopeFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "envelope",
		GoldenDir: "testdata/golden/envelope",
		Filter: func(desc *fixtures.Description) bool {
			return strings.HasPrefix(desc.Name, "envelope-")
		},
		Runner: runEnvelopeFixture,
	})
}

// EnvelopeGolden holds one entry per commit: an OpGoldenState for a commit the
// harness loaded, or an UnloadedCommitState for one whose size the reader
// refuses before loading (commit-too-large).
type EnvelopeGolden struct {
	Ops []any `json:"ops"`
}

// UnloadedCommitState records a commit rejected on its object size alone
// (spec/op-envelope.md §Reader validation rule 1): it is never loaded, so
// nothing that needs the decoded commit — parents, identities, tree entries,
// signature — can be recorded for it. The commit SHA pins all of that.
type UnloadedCommitState struct {
	Ref        string           `json:"ref"`
	Commit     string           `json:"commit"`
	CommitSize int              `json:"commit_size"`
	Expected   DispositionState `json:"expected"`
	Observed   DispositionState `json:"observed"`
}

type OpGoldenState struct {
	Ref                     string           `json:"ref"`
	Commit                  string           `json:"commit"`
	Parents                 []string         `json:"parents"`
	Author                  string           `json:"author"`
	Committer               string           `json:"committer"`
	Timestamp               string           `json:"timestamp"`
	TreeEntries             []TreeEntryState `json:"tree_entries,omitempty"`
	TreeSize                int              `json:"tree_size,omitempty"`
	Payload                 string           `json:"payload,omitempty"`
	CanonicalPayload        string           `json:"canonical_payload,omitempty"`
	PayloadSize             int              `json:"payload_size,omitempty"`
	Signed                  bool             `json:"signed"`
	SignatureKeyFingerprint string           `json:"signature_key_fingerprint,omitempty"`
	VerificationOutcome     string           `json:"verification_outcome"`
	ExpectedVerification    string           `json:"expected_verification,omitempty"`
	Expected                DispositionState `json:"expected"`
	Observed                DispositionState `json:"observed"`
}

type TreeEntryState struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
}

type DispositionState struct {
	Disposition string `json:"disposition"`
	Reason      string `json:"reason,omitempty"`
}

func runEnvelopeFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	// Refs are discovered the way a conforming reader discovers them --
	// production ref discovery (dag.Chains), not the description's ref
	// list -- so a ref a conforming reader would skip (an invalid
	// writer-id) fails the family instead of silently evaluating commits
	// nothing would ever reach. This is a discovery check only, not an
	// enumeration-coverage one: the per-commit loop below still walks
	// every commit the manifest lists by SHA, because how far a
	// production walk reaches past a rejected commit is
	// multi-writer-chains' subject (WRIT-289), not this family's.
	if err := checkChainsDiscoverable(fix); err != nil {
		return nil, err
	}

	trustStore, err := fixtures.TrustStoreFor(fix.Description)
	if err != nil {
		return nil, fmt.Errorf("create trust store: %w", err)
	}

	var golden EnvelopeGolden

	// Map manifest generation commits to descriptions
	commitDescMap := make(map[string]fixtures.CommitDesc)
	genStateMap := make(map[string]fixtures.GenerationState)
	for _, gs := range fix.Manifest.Generations {
		genStateMap[gs.Ref] = gs
	}

	commitIdx := 0
	for _, ref := range fix.Description.Refs {
		for _, gen := range ref.History {
			gs := fix.Manifest.Generations[commitIdx]
			commitIdx++
			for ci, cd := range gen.Commits {
				cState := gs.Commits[ci]
				commitDescMap[cState.SHA] = cd

				// codec.GetCommit sizes the commit before loading it, as a
				// reader does: a commit over the bound is recorded without
				// ever being read.
				commitHash := plumbing.NewHash(cState.SHA)
				commit, err := codec.GetCommit(fix.Repo.Storer, commitHash)
				if err != nil {
					state, serr := unloadedCommitState(fix, ref.Name, cState.SHA, cd, err)
					if serr != nil {
						return nil, serr
					}
					golden.Ops = append(golden.Ops, *state)
					continue
				}

				opState, err := evaluateOpCommit(t, fix, ref.Name, commit, cd, trustStore)
				if err != nil {
					return nil, err
				}
				golden.Ops = append(golden.Ops, *opState)
			}
		}
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal envelope golden: %w", err)
	}
	return append(b, '\n'), nil
}

// unloadedCommitState builds the golden record of a commit codec.GetCommit
// refused to load, after checking the refusal is the one the description
// declares: commit-too-large, at the size commit_size pins.
func unloadedCommitState(fix *fixtures.Fixture, refName, sha string, cd fixtures.CommitDesc, loadErr error) (*UnloadedCommitState, error) {
	var rej *codec.RejectError
	if !errors.As(loadErr, &rej) {
		return nil, fmt.Errorf("lookup commit %s: %w", sha, loadErr)
	}
	if cd.CommitSize == 0 {
		return nil, fmt.Errorf("fixture %s commit %s: rejected %s on load but its description pins no commit_size", fix.Name, sha, rej.Reason)
	}
	size, err := fix.Repo.Storer.EncodedObjectSize(plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("size commit %s: %w", sha, err)
	}
	if size != int64(cd.CommitSize) {
		return nil, fmt.Errorf("fixture %s commit %s: commit_size %d, commit object is %d bytes", fix.Name, sha, cd.CommitSize, size)
	}

	expected := DispositionState{Disposition: "accept"}
	if cd.Expect != nil && !cd.Expect.Accept && cd.Expect.Reject != "" {
		expected = DispositionState{Disposition: "reject", Reason: cd.Expect.Reject}
	}
	observed := DispositionState{Disposition: "reject", Reason: string(rej.Reason)}
	if expected != observed {
		return nil, fmt.Errorf("fixture %s commit %s: expected disposition %+v, observed %+v", fix.Name, sha, expected, observed)
	}
	return &UnloadedCommitState{
		Ref:        refName,
		Commit:     sha,
		CommitSize: cd.CommitSize,
		Expected:   expected,
		Observed:   observed,
	}, nil
}

// checkChainsDiscoverable asserts that every ref the fixture's manifest
// records is a chain a conforming reader finds through production ref
// discovery (dag.Chains), pointed at the tip the manifest recorded. It is
// the family's substitute for reading fix.Description.Refs directly: a
// ref with a grammar-invalid writer-id (spec/ref-layout.md) is one
// dag.Chains silently skips, so a fixture repo built from such a ref is
// not a conforming writ repository and an independent implementation
// enumerating its own refs would see nothing there at all.
func checkChainsDiscoverable(fix *fixtures.Fixture) error {
	chains, err := dag.Chains(fix.Repo.Storer)
	if err != nil {
		return fmt.Errorf("discover chains: %w", err)
	}

	for _, r := range fix.Manifest.Refs {
		chain, ok := chains[r.Name]
		if !ok {
			if _, perr := dag.ParseChainRef(r.Name); perr != nil {
				return fmt.Errorf("ref %q is not a chain a conforming reader discovers: %w", r.Name, perr)
			}
			return fmt.Errorf("ref %q is not a chain a conforming reader discovers: not found by dag.Chains", r.Name)
		}
		if chain.Tip.String() != r.Commit {
			return fmt.Errorf("ref %q: discovered tip %s does not match manifest commit %s", r.Name, chain.Tip.String(), r.Commit)
		}
	}

	if len(chains) != len(fix.Manifest.Refs) {
		return fmt.Errorf("dag.Chains discovered %d chains but the manifest lists %d refs", len(chains), len(fix.Manifest.Refs))
	}

	return nil
}

func evaluateOpCommit(t *testing.T, fix *fixtures.Fixture, refName string, commit *object.Commit, cd fixtures.CommitDesc, trustStore codec.TrustStore) (*OpGoldenState, error) {
	pureCommit, err := codec.FromGitCommit(fix.Repo.Storer, commit)
	if err != nil {
		return nil, fmt.Errorf("from git commit: %w", err)
	}

	var treeEntries []TreeEntryState
	var opBlobContent string
	var canonicalPayload string

	for _, entry := range pureCommit.Tree {
		treeEntries = append(treeEntries, TreeEntryState{
			Name: entry.Name,
			Mode: entry.Mode,
			SHA:  entry.Hash,
		})
		if entry.Name == "op.json" {
			opBlobContent = string(entry.Data)
		}
	}

	if opBlobContent != "" {
		canonBytes, canonErr := canonicaljson.Marshal([]byte(opBlobContent))
		if canonErr == nil {
			canonicalPayload = string(canonBytes)
		}
	}

	// A commit whose description pins op.json's byte length (the
	// envelope-payload-size family) records that length instead of the
	// payload text: the tree-entry blob SHA above already pins the exact
	// bytes, so the golden need not carry a megabyte of literal padding.
	payloadSize := 0
	if cd.OpJSONSize != 0 {
		payloadSize = len(opBlobContent)
		opBlobContent = ""
		canonicalPayload = ""
	}

	// Likewise a commit whose description pins its root tree object's byte
	// size (the envelope-object-size family) records that size instead of
	// the tree's entries, which carry a padding entry whose name is a
	// kilobyte of "x". A tree over the bound is never loaded, so it has no
	// entries to record in any case; commit_size's check that the size is
	// the one pinned is made here, where the tree's size is known.
	treeSize := 0
	if cd.TreeSize != 0 {
		if pureCommit.TreeSize != int64(cd.TreeSize) {
			return nil, fmt.Errorf("fixture %s commit %s: tree_size %d, root tree object is %d bytes", fix.Name, commit.Hash.String(), cd.TreeSize, pureCommit.TreeSize)
		}
		treeSize = cd.TreeSize
		treeEntries = nil
	}

	// 1. Expected disposition
	expected := DispositionState{
		Disposition: "accept",
	}
	if cd.Expect != nil && !cd.Expect.Accept && cd.Expect.Reject != "" {
		expected = DispositionState{
			Disposition: "reject",
			Reason:      cd.Expect.Reject,
		}
	}

	// 2. Observed disposition evaluation
	//
	// Disposition comes only from codec.DecodeCommit (WRIT-251 ruling 1):
	// signature verification never gates fold or acceptance, so a bad
	// signature can no longer make the observed disposition "reject" --
	// it is checked as a separate expected-vs-observed verification
	// outcome below instead.
	var observed DispositionState

	// A-C. Decode and validate commit via codec
	_, decErr := codec.DecodeCommit(pureCommit)
	if decErr != nil {
		var rej *codec.RejectError
		if errors.As(decErr, &rej) {
			observed = DispositionState{Disposition: "reject", Reason: string(rej.Reason)}
		} else {
			return nil, fmt.Errorf("unexpected decode error: %w", decErr)
		}
	} else {
		observed = DispositionState{Disposition: "accept"}
	}

	// D. Signature verification. Never part of disposition (see above):
	// computed unconditionally so every commit's outcome is pinned in the
	// golden file, but only compared against an expectation when the
	// commit was actually accepted.
	verResult := codec.Verify(pureCommit, trustStore)

	// Assert declared expectation matches observed disposition
	if expected != observed {
		t.Fatalf("fixture %s commit %s: expected disposition %+v, observed %+v (verification: %+v)",
			fix.Name, commit.Hash.String(), expected, observed, verResult)
	}

	// Assert declared (or defaulted) expected verification outcome matches
	// observed, for accepted commits only -- a rejected commit's signature
	// state is not what its expect: block is describing, and
	// expectedVerification stays "" for one so the golden never pins a
	// value that contradicts verification_outcome (the description loader
	// already refuses expect.verification on a reject expectation;
	// UnmarshalYAML above).
	var expectedVerification string
	if observed.Disposition == "accept" {
		expectedVerification = string(codec.OutcomeValid)
		if cd.Expect != nil && cd.Expect.Verification != "" {
			expectedVerification = cd.Expect.Verification
		}
		if expectedVerification != string(verResult.Outcome) {
			t.Fatalf("fixture %s commit %s: expected verification %q, observed %q",
				fix.Name, commit.Hash.String(), expectedVerification, verResult.Outcome)
		}
	}

	parents := make([]string, len(commit.ParentHashes))
	for i, p := range commit.ParentHashes {
		parents[i] = p.String()
	}

	return &OpGoldenState{
		Ref:                     refName,
		Commit:                  commit.Hash.String(),
		Parents:                 parents,
		Author:                  fmt.Sprintf("%s <%s>", commit.Author.Name, commit.Author.Email),
		Committer:               fmt.Sprintf("%s <%s>", commit.Committer.Name, commit.Committer.Email),
		Timestamp:               commit.Author.When.UTC().Format("2006-01-02T15:04:05Z07:00"),
		TreeEntries:             treeEntries,
		TreeSize:                treeSize,
		Payload:                 opBlobContent,
		CanonicalPayload:        canonicalPayload,
		PayloadSize:             payloadSize,
		Signed:                  commit.PGPSignature != "",
		SignatureKeyFingerprint: verResult.KeyFingerprint,
		VerificationOutcome:     string(verResult.Outcome),
		ExpectedVerification:    expectedVerification,
		Expected:                expected,
		Observed:                observed,
	}, nil
}
