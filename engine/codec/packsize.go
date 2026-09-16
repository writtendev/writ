package codec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// PackIndexCache caches a repository's decoded pack indexes across
// repeated packfileObjectSize lookups, so a caller that looks up many
// blobs against the same on-disk packs pays the cost of decoding a
// pack's whole .idx once, not on every lookup. WRIT-255 round 2 found
// that cost unbounded per call: a pack of 1,000,000 objects (a 28 MB
// .idx) cost about 40 ms and 28 MB allocated on every op.json size
// check, including repeat checks against the very same pack — meaning
// dag.Store.EnumerateSince's per-commit loop paid for the whole
// repository's pack set once per commit it walked, not once per pass.
// A decoded *idxfile.MemoryIndex answers FindOffset from the structure
// it built the first time, so keeping it around costs nothing further
// to reuse.
//
// A nil *PackIndexCache is valid everywhere one is accepted: it simply
// disables caching, decoding fresh on every call, the way
// packfileObjectSize always did before this type existed.
//
// A cache is scoped to whatever built it and must not outlive that: a
// fetch or a repack can add, remove, or replace the packs it holds
// indexes for, and a stale hit for a pack that's gone — or a miss for
// one that just arrived — would reintroduce the same kind of bug this
// type exists to fix. Construct a fresh one for each pass over
// history; dag.Store.EnumerateSince does exactly that, once per call,
// never storing it on the Store itself, so it never survives past the
// pass that built it. It holds no reference to anything outside the
// process (a decoded index is a plain in-memory structure, not an open
// file descriptor), so dropping it is just letting the garbage
// collector reclaim the map.
type PackIndexCache struct {
	mu      sync.Mutex
	indexes map[plumbing.Hash]*idxfile.MemoryIndex
}

// NewPackIndexCache returns an empty PackIndexCache, ready to pass to
// FromGitCommitCached.
func NewPackIndexCache() *PackIndexCache {
	return &PackIndexCache{indexes: make(map[plumbing.Hash]*idxfile.MemoryIndex)}
}

// get and put are safe to call on a nil *PackIndexCache — every caller
// in this file goes through them rather than touching the map
// directly, so "no cache" (nil) and "cache" share one code path.
func (c *PackIndexCache) get(pack plumbing.Hash) (*idxfile.MemoryIndex, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.indexes[pack]
	return idx, ok
}

func (c *PackIndexCache) put(pack plumbing.Hash, idx *idxfile.MemoryIndex) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.indexes[pack] = idx
}

// filesystemStorer is implemented by *filesystem.Storage — the on-disk
// storer writ opens repositories with (internal/gitdir.OpenStorage).
// Depending on this narrow interface, rather than importing
// storage/filesystem for a concrete type assertion, is enough to reach the
// billy.Filesystem backing the repository, which is all packfileObjectSize
// needs.
type filesystemStorer interface {
	Filesystem() billy.Filesystem
}

// maxAlternateDepth bounds how many "objects/info/alternates" hops
// sizeFromDotGit follows, matching the depth git itself enforces, so a
// cyclic or absurdly long alternates chain cannot recurse forever.
const maxAlternateDepth = 5

// deltaHeaderPrefix is how many decompressed bytes of a packed delta's
// zlib stream deltaTargetSize reads to recover the two leb128 size
// varints at the front of the delta representation (a delta's on-disk
// shape is <base size><target size><copy/insert instructions>, per
// gitformat-pack). Each varint needs at most 10 bytes to hold a 64-bit
// length, so 32 bytes leaves generous room without ever decompressing
// into the instructions that follow — bounding the cost of reading a
// delta's size to a small constant, never the target object's own size.
const deltaHeaderPrefix = 32

// packfileObjectSize determines the plaintext size of the object with the
// given hash while decompressing no more than deltaHeaderPrefix bytes of
// any one packed object, regardless of that object's own size. This
// covers every shape a blob can take on disk: loose, packed as a plain
// object, or packed as a delta (OFS or REF, any chain depth, in this
// object store's own packs or one reached through
// "objects/info/alternates", e.g. a --shared or --reference clone).
//
// found reports whether the object was located at all by this scan. When
// found is false and err is nil, the object was not located here — the
// caller's normal lookup takes over, and will itself report the object
// missing if it truly does not exist (a different, pre-existing failure
// mode than the one this function exists to guard against). When err is
// non-nil, the object was found but its size could not be determined
// cheaply (a corrupt or unsupported pack entry, or a filesystem error
// while searching); the caller must fail closed rather than fetch the
// object in full to find out.
//
// s must expose its backing billy.Filesystem (as *filesystem.Storage,
// writ's real on-disk storer, does) for this to apply at all; for any
// other storage.Storer, found is always false and err is always nil, and
// the caller falls back to its own, already-cheap sizing.
//
// cache, if non-nil, is consulted and filled for every pack index this
// call would otherwise decode from scratch — see PackIndexCache's doc
// comment for its scope. A nil cache costs nothing extra: it decodes
// exactly as it always did.
func packfileObjectSize(s storage.Storer, hash plumbing.Hash, cache *PackIndexCache) (size int64, found bool, err error) {
	fss, ok := s.(filesystemStorer)
	if !ok {
		return 0, false, nil
	}
	return sizeFromDotGit(dotgit.New(fss.Filesystem()), hash, maxAlternateDepth, cache)
}

// sizeFromDotGit searches dg's loose objects, then its packs, then —
// while altDepth allows — its alternates, in that order, stopping at the
// first place the hash is found.
func sizeFromDotGit(dg *dotgit.DotGit, hash plumbing.Hash, altDepth int, cache *PackIndexCache) (int64, bool, error) {
	if size, found, err := looseObjectSize(dg, hash); found || err != nil {
		return size, found, err
	}

	if size, found, err := packedObjectSize(dg, hash, cache); found || err != nil {
		return size, found, err
	}

	if altDepth <= 0 {
		return 0, false, nil
	}

	// A missing or unreadable alternates file just means "no alternates
	// to search" — the same tolerance go-git's own EncodedObject/
	// getFromPackfile gives this error before it looks at alternates.
	alternates, err := dg.Alternates()
	if err != nil {
		return 0, false, nil
	}

	for _, alt := range alternates {
		if size, found, err := sizeFromDotGit(alt, hash, altDepth-1, cache); found || err != nil {
			return size, found, err
		}
	}

	return 0, false, nil
}

// looseObjectSize reads a loose object's zlib-wrapped header (type and
// size) without decompressing its content, the same cheap operation
// go-git's own EncodedObjectSize performs for a loose object.
func looseObjectSize(dg *dotgit.DotGit, hash plumbing.Hash) (int64, bool, error) {
	f, err := dg.Object(hash)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, true, fmt.Errorf("codec: open loose object %s: %w", hash, err)
	}
	defer f.Close()

	r, err := objfile.NewReader(f)
	if err != nil {
		return 0, true, fmt.Errorf("codec: read loose object %s header: %w", hash, err)
	}
	defer r.Close()

	_, size, err := r.Header()
	if err != nil {
		return 0, true, fmt.Errorf("codec: read loose object %s header: %w", hash, err)
	}
	return size, true, nil
}

// packedObjectSize searches every packfile dg holds for hash, via each
// pack's own index, and sizes it in place once found.
func packedObjectSize(dg *dotgit.DotGit, hash plumbing.Hash, cache *PackIndexCache) (int64, bool, error) {
	packs, err := dg.ObjectPacks()
	if err != nil {
		return 0, true, fmt.Errorf("codec: list packfiles: %w", err)
	}

	for _, pack := range packs {
		idx, err := loadPackIndex(dg, pack, cache)
		if err != nil {
			if errors.Is(err, dotgit.ErrPackfileNotFound) {
				// dg.ObjectPacks() lists every pack-<hash>.pack file it
				// sees, whether or not a matching .idx exists yet: git
				// renames a pack's .pack into place before its .idx
				// during a fetch or a repack, so a pack with no .idx is
				// common, transient state, not corruption. Treat it the
				// way git itself does — as if this pack weren't here
				// yet — and keep searching the rest, instead of failing
				// the whole lookup closed over a pack that never had
				// the object to begin with (or will, once the .idx
				// lands).
				continue
			}
			return 0, true, err
		}

		offset, err := idx.FindOffset(hash)
		if err != nil {
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				continue
			}
			return 0, true, fmt.Errorf("codec: search pack %s index: %w", pack, err)
		}

		size, err := packedObjectSizeAt(dg, pack, offset)
		if err != nil {
			return 0, true, err
		}
		return size, true, nil
	}

	return 0, false, nil
}

func loadPackIndex(dg *dotgit.DotGit, pack plumbing.Hash, cache *PackIndexCache) (*idxfile.MemoryIndex, error) {
	if idx, ok := cache.get(pack); ok {
		return idx, nil
	}

	f, err := dg.ObjectPackIdx(pack)
	if err != nil {
		return nil, fmt.Errorf("codec: open pack %s index: %w", pack, err)
	}
	defer f.Close()

	idx := idxfile.NewMemoryIndex()
	if err := idxfile.NewDecoder(f).Decode(idx); err != nil {
		return nil, fmt.Errorf("codec: decode pack %s index: %w", pack, err)
	}

	cache.put(pack, idx)
	return idx, nil
}

// packedObjectSizeAt reads the header of the object at offset in pack,
// returning its size directly for a plain object, or — for a delta —
// only after reading just enough of it to answer that (see
// deltaTargetSize).
func packedObjectSizeAt(dg *dotgit.DotGit, pack plumbing.Hash, offset int64) (int64, error) {
	f, err := dg.ObjectPack(pack)
	if err != nil {
		return 0, fmt.Errorf("codec: open pack %s: %w", pack, err)
	}
	defer f.Close()

	scanner := packfile.NewScanner(f)
	header, err := scanner.SeekObjectHeader(offset)
	if err != nil {
		return 0, fmt.Errorf("codec: read pack %s object header at %d: %w", pack, offset, err)
	}

	switch header.Type {
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
		// A plain object's header already declares its plaintext size;
		// nothing further needs decompressing.
		return header.Length, nil
	case plumbing.OFSDeltaObject, plumbing.REFDeltaObject:
		size, err := deltaTargetSize(scanner)
		if err != nil {
			return 0, fmt.Errorf("codec: read delta target size in pack %s at %d: %w", pack, offset, err)
		}
		return size, nil
	default:
		return 0, fmt.Errorf("codec: unexpected object type %v in pack %s at %d", header.Type, pack, offset)
	}
}

// deltaTargetSize recovers a packed delta's target size — the size of
// the object it produces once applied to its base, which is the final
// object's size regardless of chain depth or the base's own type or
// location — by decompressing only the delta representation's first
// deltaHeaderPrefix bytes. That prefix holds the two leb128 varints
// every delta starts with (base size, then target size); the
// copy/insert instructions that follow are never touched. This is the
// bounded-memory replacement for what go-git's own
// Packfile.GetSizeByOffset does for a delta object: fully decompressing
// it into a buffer sized to the whole delta representation before
// reading the same two varints back out of the front of it.
func deltaTargetSize(scanner *packfile.Scanner) (int64, error) {
	rc, err := scanner.ReadObject()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	prefix := make([]byte, deltaHeaderPrefix)
	n, err := io.ReadFull(rc, prefix)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}
	prefix = prefix[:n]

	_, rest, err := decodeDeltaSizeVarint(prefix)
	if err != nil {
		return 0, fmt.Errorf("delta base size: %w", err)
	}
	target, _, err := decodeDeltaSizeVarint(rest)
	if err != nil {
		return 0, fmt.Errorf("delta target size: %w", err)
	}
	return int64(target), nil
}

// decodeDeltaSizeVarint decodes one of the two size varints at the front
// of a packed delta representation — 7 payload bits per byte, low-order
// chunk first, continued while the high bit is set — and returns the
// bytes remaining after it.
func decodeDeltaSizeVarint(b []byte) (uint64, []byte, error) {
	var val uint64
	var shift uint
	for i, c := range b {
		if shift >= 64 {
			return 0, nil, errors.New("varint overflow")
		}
		val |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return val, b[i+1:], nil
		}
		shift += 7
	}
	return 0, nil, io.ErrUnexpectedEOF
}
