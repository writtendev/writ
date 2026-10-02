package fixtures

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// timeLayout is RFC 3339 in UTC — the only form commit timestamps are
// ever recorded in, so two generations of the same description always
// agree on how a given instant is spelled.
const timeLayout = time.RFC3339

// Generate builds a bare git repository at outDir from desc and returns
// the manifest describing what it produced. outDir must not already
// exist. Every commit is signed by its author's fixture identity (see
// identity.go), so generation requires ssh-keygen (OpenSSH 8.2+) on PATH.
func Generate(desc *Description, outDir string) (*Manifest, error) {
	repo, err := git.PlainInit(outDir, true)
	if err != nil {
		return nil, fmt.Errorf("fixtures: init repo at %s: %w", outDir, err)
	}

	sgnr, err := newSigner()
	if err != nil {
		return nil, err
	}
	defer sgnr.close()

	manifest := &Manifest{}
	labels := make(map[string]plumbing.Hash)

	for _, ref := range desc.Refs {
		for gi, gen := range ref.History {
			var parent plumbing.Hash
			var parents []plumbing.Hash
			state := GenerationState{Ref: ref.Name, Index: gi, KeptAs: gen.KeptAs}

			for _, cd := range gen.Commits {
				if cd.Parents != nil {
					parents = make([]plumbing.Hash, len(cd.Parents))
					for pi, pLabel := range cd.Parents {
						pHash, ok := labels[pLabel]
						if !ok {
							return nil, fmt.Errorf("fixtures: ref %q generation %d: unknown parent label %q", ref.Name, gi, pLabel)
						}
						parents[pi] = pHash
					}
				} else if !parent.IsZero() {
					parents = []plumbing.Hash{parent}
				} else {
					parents = nil
				}

				commit, hash, err := buildCommit(repo, sgnr, cd, parents)
				if err != nil {
					return nil, fmt.Errorf("fixtures: ref %q generation %d: %w", ref.Name, gi, err)
				}
				if cd.ID != "" {
					labels[cd.ID] = hash
				}
				parent = hash
				state.Commits = append(state.Commits, commitState(commit, hash, cd.CommitSize != 0))
			}

			isLast := gi == len(ref.History)-1
			if gen.KeptAs != "" {
				if err := setRef(repo, gen.KeptAs, parent); err != nil {
					return nil, err
				}
				manifest.Refs = append(manifest.Refs, RefState{Name: gen.KeptAs, Commit: parent.String()})
			}
			if isLast {
				if err := setRef(repo, ref.Name, parent); err != nil {
					return nil, err
				}
				manifest.Refs = append(manifest.Refs, RefState{Name: ref.Name, Commit: parent.String()})
			}
			manifest.Generations = append(manifest.Generations, state)
		}
	}

	// PlainInit leaves HEAD pointing at the conventional refs/heads/master,
	// which none of these fixtures populate — left alone, a plain `git
	// clone` of the generated repo checks out nothing and prints a
	// "remote HEAD refers to nonexistent ref" warning. Point it at the
	// description's first ref so the repo is inspectable the obvious way.
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName(desc.Refs[0].Name))); err != nil {
		return nil, fmt.Errorf("fixtures: set HEAD: %w", err)
	}

	return manifest, nil
}

func setRef(repo *git.Repository, name string, hash plumbing.Hash) error {
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), hash)); err != nil {
		return fmt.Errorf("fixtures: set ref %s: %w", name, err)
	}
	return nil
}

// buildCommit writes files or canonical op.json as a tree, constructs a commit object with the
// given parents, signs it, applies any requested post-signing tamper, and stores it.
func buildCommit(repo *git.Repository, sgnr *signer, cd CommitDesc, parents []plumbing.Hash) (*object.Commit, plumbing.Hash, error) {
	files, err := commitFiles(cd)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	cd.Files = files
	if cd.Op != nil {
		cd.Message = DeriveMessage(cd.Op)
	}

	authorId, err := lookupIdentity(cd.Author)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}

	signerId := authorId
	if cd.SignAs != "" {
		signerId, err = lookupIdentity(cd.SignAs)
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
	}

	authorSig := object.Signature{Name: authorId.Name, Email: authorId.Email, When: cd.Timestamp.UTC()}
	committerSig := authorSig
	if cd.Committer != "" {
		committerId, err := lookupIdentity(cd.Committer)
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
		committerSig = object.Signature{Name: committerId.Name, Email: committerId.Email, When: cd.Timestamp.UTC()}
	}

	treeHash, err := buildTree(repo.Storer, cd.Files)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}

	commit := &object.Commit{
		Author:       authorSig,
		Committer:    committerSig,
		Message:      cd.Message,
		TreeHash:     treeHash,
		ParentHashes: parents,
	}

	if cd.CommitSize != 0 {
		if err := padCommitMessage(repo.Storer, sgnr, signerId, commit, cd.Unsigned, cd.CommitSize); err != nil {
			return nil, plumbing.ZeroHash, err
		}
	}

	if !cd.Unsigned {
		armored, err := signCommit(repo.Storer, sgnr, signerId, commit)
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
		commit.PGPSignature = armored
	}

	if cd.CommitSize != 0 {
		if got, err := commitObjectSize(repo.Storer, commit); err != nil {
			return nil, plumbing.ZeroHash, err
		} else if got != int64(cd.CommitSize) {
			return nil, plumbing.ZeroHash, fmt.Errorf("fixtures: commit_size %d: signed commit object is %d bytes", cd.CommitSize, got)
		}
	}

	if cd.Tamper != "" {
		if err := applyTamper(repo.Storer, sgnr, signerId, commit, cd.Files, cd.Tamper); err != nil {
			return nil, plumbing.ZeroHash, err
		}
	}

	commitObj := repo.Storer.NewEncodedObject()
	commitObj.SetType(plumbing.CommitObject)
	if err := commit.Encode(commitObj); err != nil {
		return nil, plumbing.ZeroHash, fmt.Errorf("encode signed commit: %w", err)
	}
	hash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		return nil, plumbing.ZeroHash, fmt.Errorf("store commit: %w", err)
	}

	return commit, hash, nil
}

// commitObjectSize returns the size of commit's encoded object, signature
// included.
func commitObjectSize(store storer.EncodedObjectStorer, commit *object.Commit) (int64, error) {
	obj := store.NewEncodedObject()
	obj.SetType(plumbing.CommitObject)
	if err := commit.Encode(obj); err != nil {
		return 0, fmt.Errorf("fixtures: encode commit: %w", err)
	}
	return obj.Size(), nil
}

// padCommitMessage appends filler to commit's message so the signed commit
// object is exactly size bytes (commit_size). The signature's armored length
// does not depend on what it signs — fixture keys are ed25519 and an SSHSIG
// over one is fixed-size — so the commit is measured once with a signature
// in place and the padding computed from that; buildCommit signs the padded
// commit afterwards and checks the result is exactly size.
func padCommitMessage(store storer.EncodedObjectStorer, sgnr *signer, id identity, commit *object.Commit, unsigned bool, size int) error {
	if !unsigned {
		armored, err := signCommit(store, sgnr, id, commit)
		if err != nil {
			return err
		}
		commit.PGPSignature = armored
	}
	base, err := commitObjectSize(store, commit)
	if err != nil {
		return err
	}
	if base > int64(size) {
		return fmt.Errorf("fixtures: commit_size %d is smaller than the unpadded signed commit (%d bytes)", size, base)
	}
	commit.Message += strings.Repeat("x", size-int(base))
	commit.PGPSignature = ""
	return nil
}

// signCommit returns an armored SSH signature, by id's key, over commit's
// signed payload: the commit as encoded without its signature header.
func signCommit(store storer.EncodedObjectStorer, sgnr *signer, id identity, commit *object.Commit) (string, error) {
	payloadObj := store.NewEncodedObject()
	if err := commit.EncodeWithoutSignature(payloadObj); err != nil {
		return "", fmt.Errorf("encode commit payload: %w", err)
	}
	r, err := payloadObj.Reader()
	if err != nil {
		return "", fmt.Errorf("read commit payload: %w", err)
	}
	payload, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read commit payload: %w", err)
	}
	return sgnr.sign(id, payload)
}

// commitState summarizes a generated commit for the manifest. A commit padded
// to a pinned size (commit_size) records the byte length of its message in
// MessageSize in place of the message, which is nothing but padding: the SHA
// already pins the exact bytes, so the golden need not carry kilobytes of "x".
func commitState(c *object.Commit, hash plumbing.Hash, padded bool) CommitState {
	parents := make([]string, len(c.ParentHashes))
	for i, p := range c.ParentHashes {
		parents[i] = p.String()
	}
	state := CommitState{
		SHA:       hash.String(),
		Tree:      c.TreeHash.String(),
		Parents:   parents,
		Author:    fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email),
		Timestamp: c.Author.When.Format(timeLayout),
		Message:   c.Message,
		Signed:    c.PGPSignature != "",
	}
	if padded {
		state.Message = ""
		state.MessageSize = len(c.Message)
	}
	return state
}
