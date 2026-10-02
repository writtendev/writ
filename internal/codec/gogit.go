package codec

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
)

// FromGitCommit converts a go-git object.Commit into a pure, repository-independent Commit
// value by reading its tree entries and op.json blob data.
//
// When s is wrapped with engine/internal/packidx.WithCache, pack indexes
// FromGitCommit needs along the way are decoded through the attached
// cache instead of fresh on every call — dag.Store.EnumerateSince does
// this, wrapping its storer once per pass over history and letting the
// wrapper go when the pass ends. A plain storage.Storer, never wrapped,
// decodes fresh on every call, exactly as it always did.
func FromGitCommit(s storage.Storer, commit *object.Commit) (Commit, error) {
	return fromGitCommit(s, commit)
}

func fromGitCommit(s storage.Storer, commit *object.Commit) (Commit, error) {
	if commit == nil {
		return Commit{}, errors.New("codec: nil git commit")
	}

	// Rule 1 sizes the root tree object before reading any of it
	// (spec/op-envelope.md §Reader validation): go-git copies a loose
	// object wholly into memory to learn its type, and again to probe a
	// type mismatch, so a tree hash naming a giant object — of any type —
	// must never reach commit.Tree(). Above MaxTreeBytes the tree is left
	// unloaded and TreeSize carries the verdict to DecodeCommit.
	var treeSize int64
	tree := &object.Tree{}
	if s != nil {
		size, known, err := objectSize(s, commit.TreeHash)
		if err != nil {
			return Commit{}, fmt.Errorf("codec: determine commit tree size: %w", err)
		}
		if known {
			treeSize = size
		}
	}
	if treeSize <= MaxTreeBytes {
		var err error
		tree, err = commit.Tree()
		if err != nil {
			if s == nil || !errors.Is(err, plumbing.ErrObjectNotFound) || !objectPresentButWrongType(s, commit.TreeHash) {
				return Commit{}, fmt.Errorf("codec: commit tree: %w", err)
			}
			// commit.TreeHash names a present object, just not a tree: there
			// are no tree entries to report, which DecodeCommit's rule 1 turns
			// into missing-op-json — the same reason this shape reported
			// before this typed lookup's failure was ever probed (WRIT-271
			// round 2 review: commit.Tree() was one of two typed lookups still
			// misreporting a present-but-wrong-type object as
			// object-unavailable).
			tree = &object.Tree{}
		}
	}

	// Only the root tree's own entries are read: a directory entry is
	// reported as an entry and never descended into, so what the entries
	// themselves retain is bounded by MaxTreeBytes, however many entries
	// name the same large subtree. A blob is read only once the tree's
	// shape is known to be a single regular-mode op.json entry — rule 1
	// checks shape before it reads any blob, and DecodeCommit rejects every
	// other shape (missing-op-json, extra-tree-entry, invalid-op-json-mode)
	// without looking at the blob. Reading per entry named op.json instead
	// would let a tree of duplicates, each naming the same large blob (a
	// tree that fails fsck, but not every host runs it), retain one blob per
	// entry. So the most this retains beyond the entries is one blob,
	// capped at MaxPayloadBytes+1.
	readOpJSON := len(tree.Entries) == 1 && tree.Entries[0].Name == "op.json" && tree.Entries[0].Mode == filemode.Regular
	var treeEntries []TreeEntry
	for _, entry := range tree.Entries {
		te := TreeEntry{
			Name: entry.Name,
			Mode: entry.Mode.String(),
			Hash: entry.Hash.String(),
		}
		if readOpJSON {
			data, err := readOpJSONBlob(s, entry.Hash, func() (*object.File, error) {
				return tree.TreeEntryFile(&entry)
			})
			if err != nil {
				return Commit{}, fmt.Errorf("codec: read op.json blob: %w", err)
			}
			te.Data = data
		} else if entry.Name == "op.json" {
			// Not read, but still a blob this clone may not have: an
			// op.json entry whose object is absent has always surfaced as
			// plumbing.ErrObjectNotFound (dag: object-unavailable), whatever
			// else is wrong with the tree, and sizing is all it takes to
			// tell absent from present — a header read, never the content.
			if _, known, err := objectSize(s, entry.Hash); err != nil {
				return Commit{}, fmt.Errorf("codec: read op.json blob: determine op.json blob size: %w", err)
			} else if !known {
				return Commit{}, fmt.Errorf("codec: read op.json blob: open op.json blob: %w", plumbing.ErrObjectNotFound)
			}
		}
		treeEntries = append(treeEntries, te)
	}

	parents := make([]string, len(commit.ParentHashes))
	for i, p := range commit.ParentHashes {
		parents[i] = p.String()
	}

	var payload []byte
	payloadObj := &plumbing.MemoryObject{}
	if err := commit.EncodeWithoutSignature(payloadObj); err == nil {
		if r, err := payloadObj.Reader(); err == nil {
			payload, _ = io.ReadAll(r)
			_ = r.Close()
		}
	}

	return Commit{
		ID:      commit.Hash.String(),
		Parents: parents,
		Author: Identity{
			Name:  commit.Author.Name,
			Email: commit.Author.Email,
			When:  commit.Author.When,
		},
		Committer: Identity{
			Name:  commit.Committer.Name,
			Email: commit.Committer.Email,
			When:  commit.Committer.When,
		},
		Message:   commit.Message,
		Signature: commit.PGPSignature,
		Payload:   payload,
		Tree:      treeEntries,
		TreeSize:  treeSize,
	}, nil
}

// GetCommit loads the commit object hash names from s, after sizing it
// (spec/op-envelope.md §Reader validation rule 1): a commit object above
// MaxCommitBytes is never loaded and is reported as a *RejectError with
// reason commit-too-large — go-git copies a loose object wholly into memory
// to learn its type, so a size check after the load protects nothing. The
// size is the object's declared size whatever its type, so a hash naming a
// giant blob is refused the same way. A hash absent from s fails exactly as
// object.GetCommit fails it (plumbing.ErrObjectNotFound); a size that
// cannot be determined fails closed.
//
// s is passed straight through to packfileObjectSize, which consults its
// attached packidx.Cache when s is wrapped with packidx.WithCache — see
// FromGitCommit's doc comment.
func GetCommit(s storage.Storer, hash plumbing.Hash) (*object.Commit, error) {
	size, known, err := objectSize(s, hash)
	if err != nil {
		return nil, fmt.Errorf("codec: determine commit %s size: %w", hash, err)
	}
	if known && size > MaxCommitBytes {
		return nil, &RejectError{Reason: RejectCommitTooLarge, Err: fmt.Errorf("commit object is %d bytes, exceeds %d", size, MaxCommitBytes)}
	}
	return object.GetCommit(s, hash)
}

// GetTree is GetCommit for a tree object: one above MaxTreeBytes is never
// loaded and is reported as a *RejectError with reason tree-too-large. It
// serves a caller that reads a tree off the op path (dag.Append checking a
// causal parent) and so has no FromGitCommit to size it; FromGitCommit
// sizes the root tree itself because it needs the size too.
func GetTree(s storage.Storer, hash plumbing.Hash) (*object.Tree, error) {
	size, known, err := objectSize(s, hash)
	if err != nil {
		return nil, fmt.Errorf("codec: determine tree %s size: %w", hash, err)
	}
	if known && size > MaxTreeBytes {
		return nil, &RejectError{Reason: RejectTreeTooLarge, Err: fmt.Errorf("tree object is %d bytes, exceeds %d", size, MaxTreeBytes)}
	}
	return object.GetTree(s, hash)
}

// readOpJSONBlob reads an op.json blob's content, capped at
// MaxPayloadBytes+1 bytes (spec/op-envelope.md §Reader validation rule 1).
// s is passed straight through to packfileObjectSize, which consults
// its attached packidx.Cache when s is wrapped with packidx.WithCache —
// see FromGitCommit's doc comment.
// go-git's filesystem storer fully decodes a loose object's content into
// memory the moment it is fetched — before a caller ever gets a Reader to
// limit — so capping io.ReadAll alone does not bound the cost of an
// oversized blob; open still has to be called to get anything at all.
// packfileObjectSize determines the size first, from no more than a
// small, size-independent amount of memory for every on-disk shape a
// blob can take: loose, packed as a plain object, or packed as a delta
// (OFS or REF, any chain depth, in this pack or an alternate). That is
// not true of go-git's own EncodedObjectSize for a packed delta object —
// it fully decompresses the delta's representation into a buffer sized
// to that representation before reading the object's declared size back
// out of it, which costs memory proportional to the object, the same
// bug this function used to have. Skipping the fetch entirely once the
// blob is already known to be oversized is what actually keeps memory
// bounded. When the size can't be determined this way at all — the
// object was found but something about its pack entry was unreadable —
// readOpJSONBlob fails closed with an error rather than falling back to
// a full, unbounded fetch. The returned placeholder is never read as
// content — DecodeCommit rejects on its length alone — so its bytes
// don't matter, only that there are exactly MaxPayloadBytes+1 of them,
// the same length a real oversized blob would be capped to below.
//
// open, file.Reader, and io.ReadAll failures below are returned, not
// swallowed: the object.Tree.TreeEntryFile call open closes over reaches
// s.EncodedObject, which surfaces plumbing.ErrObjectNotFound verbatim when
// the op.json blob is genuinely absent from this clone (a partial clone,
// most commonly). Wrapping with %w lets dag.EnumerateSince (WRIT-271)
// distinguish that case — reason object-unavailable, engine-local, not a
// reader-validation rejection — from a malformed op. Before WRIT-271 these
// three failures returned nil, nil, which handed DecodeCommit an empty
// payload it reported as non-canonical-payload: an absent-object error
// wearing a malformed-payload's name.
//
// open's underlying EncodedObject lookup is typed (it wants a blob), and
// go-git reports plumbing.ErrObjectNotFound for a type mismatch on a
// present object the same way it reports genuine absence (WRIT-271 round
// 1 review: an op.json entry naming a present tree — wrong mode, or a
// mode-100644 entry whose hash happens to name a tree — hit this and was
// misreported object-unavailable instead of its pre-existing malformed-op
// reason). When s is available, an open failure is checked against a
// plumbing.AnyObject probe before being propagated: genuinely absent
// still returns the error for dag to classify object-unavailable;
// present-but-wrong-type falls back to the pre-WRIT-271 nil, nil so
// DecodeCommit's own tree-shape and payload rules apply exactly as they
// did before this ticket.
func readOpJSONBlob(s storage.Storer, hash plumbing.Hash, open func() (*object.File, error)) ([]byte, error) {
	if s != nil {
		size, known, err := objectSize(s, hash)
		if err != nil {
			return nil, fmt.Errorf("codec: determine op.json blob size: %w", err)
		}
		if known && size > MaxPayloadBytes {
			return make([]byte, MaxPayloadBytes+1), nil
		}
	}

	file, err := open()
	if err != nil {
		if s != nil && errors.Is(err, plumbing.ErrObjectNotFound) && objectPresentButWrongType(s, hash) {
			// hash names an object, just not a blob: a malformed op,
			// not an object missing from this clone.
			return nil, nil
		}
		return nil, fmt.Errorf("codec: open op.json blob: %w", err)
	}
	r, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("codec: read op.json blob: %w", err)
	}
	defer r.Close()

	data, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("codec: read op.json blob: %w", err)
	}
	return data, nil
}

// objectPresentButWrongType reports whether hash names some object in s
// that just isn't the type a typed lookup wanted — go-git's typed lookups
// (object.GetCommit, object.GetTree, object.GetBlob, and the commit.Tree()
// and Tree.TreeEntryFile methods built on them) report
// plumbing.ErrObjectNotFound for that case the same way they do for
// genuine absence, because filesystem.ObjectStorage's EncodedObject
// returns that same sentinel when it finds the object but its type
// doesn't match the one requested. Every typed lookup fromGitCommit makes
// — commit.Tree() and the op.json blob open in readOpJSONBlob — is expected to call this on an
// ErrObjectNotFound before deciding the referenced object is missing from
// this clone (dag.RejectObjectUnavailable, WRIT-271): a caller that
// skips the probe misclassifies a present-but-wrong-type object the same
// way round 1 and round 2 of WRIT-271's review each found one call site
// doing. The probe itself asks with plumbing.AnyObject, which skips the
// type check entirely, so it succeeds on any object present under hash
// regardless of its actual type.
func objectPresentButWrongType(s storage.Storer, hash plumbing.Hash) bool {
	_, err := s.EncodedObject(plumbing.AnyObject, hash)
	return !errors.Is(err, plumbing.ErrObjectNotFound)
}

// ToGitCommit converts a pure, repository-independent Commit into a go-git object.Commit,
// reconstructing the tree and blob objects in memory to derive the TreeHash and commit Hash.
func ToGitCommit(c Commit) (*object.Commit, error) {
	treeHash, err := buildTreeFromEntries(c.Tree, nil)
	if err != nil {
		return nil, fmt.Errorf("codec: build commit tree: %w", err)
	}

	parents := make([]plumbing.Hash, len(c.Parents))
	for i, p := range c.Parents {
		parents[i] = plumbing.NewHash(p)
	}

	gitCommit := &object.Commit{
		Author: object.Signature{
			Name:  c.Author.Name,
			Email: c.Author.Email,
			When:  c.Author.When,
		},
		Committer: object.Signature{
			Name:  c.Committer.Name,
			Email: c.Committer.Email,
			When:  c.Committer.When,
		},
		Message:      c.Message,
		TreeHash:     treeHash,
		ParentHashes: parents,
		PGPSignature: c.Signature,
	}

	if c.ID != "" {
		gitCommit.Hash = plumbing.NewHash(c.ID)
	} else {
		cObj := &plumbing.MemoryObject{}
		cObj.SetType(plumbing.CommitObject)
		if err := gitCommit.Encode(cObj); err == nil {
			gitCommit.Hash = cObj.Hash()
		}
	}

	return gitCommit, nil
}

// buildTreeFromEntries derives the tree hash for entries. When s is non-nil
// the blob and tree objects are also persisted into it, so a caller that writes
// objects and a caller that only computes hashes agree by construction.
func buildTreeFromEntries(entries []TreeEntry, s storage.Storer) (plumbing.Hash, error) {
	tree := &object.Tree{}

	for _, entry := range entries {
		var mode filemode.FileMode
		if entry.Mode != "" {
			if parsedMode, err := filemode.New(entry.Mode); err == nil {
				mode = parsedMode
			}
		}
		if mode == 0 {
			mode = filemode.Regular
		}

		var hash plumbing.Hash
		if entry.Data != nil {
			blobObj := &plumbing.MemoryObject{}
			blobObj.SetType(plumbing.BlobObject)
			blobObj.SetSize(int64(len(entry.Data)))
			w, err := blobObj.Writer()
			if err != nil {
				return plumbing.ZeroHash, err
			}
			if _, err := w.Write(entry.Data); err != nil {
				_ = w.Close()
				return plumbing.ZeroHash, err
			}
			if err := w.Close(); err != nil {
				return plumbing.ZeroHash, err
			}
			hash = blobObj.Hash()
			if s != nil {
				if _, err := s.SetEncodedObject(blobObj); err != nil {
					return plumbing.ZeroHash, fmt.Errorf("store blob: %w", err)
				}
			}
		} else if entry.Hash != "" {
			hash = plumbing.NewHash(entry.Hash)
		}

		tree.Entries = append(tree.Entries, object.TreeEntry{
			Name: entry.Name,
			Mode: mode,
			Hash: hash,
		})
	}

	treeObj := &plumbing.MemoryObject{}
	treeObj.SetType(plumbing.TreeObject)
	if err := tree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	if s != nil {
		if _, err := s.SetEncodedObject(treeObj); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("store tree: %w", err)
		}
	}
	return treeObj.Hash(), nil
}

// WriteCommit persists a Commit — its blobs, tree, and the commit object — into s,
// signing it first when signer is non-nil. The written commit's hash is recorded in
// commit.ID and returned. Object hashes are derived by the same builder ToGitCommit
// uses, so a written commit and a computed one agree.
func WriteCommit(ctx context.Context, s storage.Storer, commit *Commit, signer Signer) (plumbing.Hash, error) {
	if s == nil {
		return plumbing.ZeroHash, errors.New("codec: nil storer")
	}
	if commit == nil {
		return plumbing.ZeroHash, errors.New("codec: nil commit")
	}

	if _, err := buildTreeFromEntries(commit.Tree, s); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("codec: write commit tree: %w", err)
	}

	if signer != nil {
		if err := SignCommit(ctx, signer, commit); err != nil {
			return plumbing.ZeroHash, err
		}
	}

	gitCommit, err := ToGitCommit(*commit)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("codec: build commit object: %w", err)
	}
	commitObj := s.NewEncodedObject()
	if err := gitCommit.Encode(commitObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("codec: encode commit: %w", err)
	}
	// The same bound GetCommit holds a reader to, so a producer never
	// writes a commit its own readers refuse (spec/op-envelope.md
	// §Producer validation). Refused before SetEncodedObject: nothing
	// over the bound is ever stored.
	if size := commitObj.Size(); size > MaxCommitBytes {
		return plumbing.ZeroHash, &RejectError{Reason: RejectCommitTooLarge, Err: fmt.Errorf("commit object is %d bytes, exceeds %d", size, MaxCommitBytes)}
	}
	commitHash, err := s.SetEncodedObject(commitObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("codec: store commit: %w", err)
	}

	commit.ID = commitHash.String()
	return commitHash, nil
}
