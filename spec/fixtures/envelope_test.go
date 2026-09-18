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

type EnvelopeGolden struct {
	Ops []OpGoldenState `json:"ops"`
}

type OpGoldenState struct {
	Ref                     string           `json:"ref"`
	Commit                  string           `json:"commit"`
	Parents                 []string         `json:"parents"`
	Author                  string           `json:"author"`
	Committer               string           `json:"committer"`
	Timestamp               string           `json:"timestamp"`
	TreeEntries             []TreeEntryState `json:"tree_entries"`
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

	trustStore, err := fixtures.NewTrustStore()
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

				commitHash := plumbing.NewHash(cState.SHA)
				commit, err := fix.Repo.CommitObject(commitHash)
				if err != nil {
					return nil, fmt.Errorf("lookup commit %s: %w", cState.SHA, err)
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
