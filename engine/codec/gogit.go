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

	tree, err := commit.Tree()
	if err != nil {
		return Commit{}, fmt.Errorf("codec: commit tree: %w", err)
	}

	var treeEntries []TreeEntry
	for _, entry := range tree.Entries {
		te := TreeEntry{
			Name: entry.Name,
			Mode: entry.Mode.String(),
			Hash: entry.Hash.String(),
		}
		if entry.Name == "op.json" {
			data, err := readOpJSONBlob(s, entry.Hash, func() (*object.File, error) {
				return tree.TreeEntryFile(&entry)
			})
			if err != nil {
				return Commit{}, fmt.Errorf("codec: read op.json blob: %w", err)
			}
			te.Data = data
		}
		if entry.Mode == filemode.Dir && s != nil {
			subTree, err := object.GetTree(s, entry.Hash)
			if err == nil {
				for _, subEntry := range subTree.Entries {
					subTe := TreeEntry{
						Name: subEntry.Name,
						Mode: subEntry.Mode.String(),
						Hash: subEntry.Hash.String(),
					}
					if subEntry.Name == "op.json" {
						data, err := readOpJSONBlob(s, subEntry.Hash, func() (*object.File, error) {
							return subTree.TreeEntryFile(&subEntry)
						})
						if err != nil {
							return Commit{}, fmt.Errorf("codec: read op.json blob: %w", err)
						}
						subTe.Data = data
					}
					te.Entries = append(te.Entries, subTe)
				}
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
	}, nil
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
func readOpJSONBlob(s storage.Storer, hash plumbing.Hash, open func() (*object.File, error)) ([]byte, error) {
	if s != nil {
		size, found, err := packfileObjectSize(s, hash)
		if err != nil {
			return nil, fmt.Errorf("codec: determine op.json blob size: %w", err)
		}
		if found {
			if size > MaxPayloadBytes {
				return make([]byte, MaxPayloadBytes+1), nil
			}
		} else if size, err := s.EncodedObjectSize(hash); err == nil && size > MaxPayloadBytes {
			return make([]byte, MaxPayloadBytes+1), nil
		}
	}

	file, err := open()
	if err != nil {
		return nil, nil
	}
	r, err := file.Reader()
	if err != nil {
		return nil, nil
	}
	defer r.Close()

	data, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
	if err != nil {
		return nil, nil
	}
	return data, nil
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
		if len(entry.Entries) > 0 {
			subHash, err := buildTreeFromEntries(entry.Entries, s)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			hash = subHash
			mode = filemode.Dir
		} else if entry.Data != nil {
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
	commitHash, err := s.SetEncodedObject(commitObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("codec: store commit: %w", err)
	}

	commit.ID = commitHash.String()
	return commitHash, nil
}
