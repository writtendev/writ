package codec_test

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/packidx"
)

// oversizedPackedOpJSONBytes is well over codec.MaxPayloadBytes, matching
// the oversizedOpJSONBytes budget dag's
// TestEnumerate_RejectsOversizedPayloadWithBoundedAllocation uses for the
// loose-object case: large enough that a regression back to decoding a
// whole object representation (what go-git's own Packfile.GetSizeByOffset
// does for a delta, and what this package's readOpJSONBlob used to rely on
// through EncodedObjectSize) allocates far more than the bounded-allocation
// budget below catches.
const oversizedPackedOpJSONBytes = 8 << 20 // 8 MiB

// packObjectHeader encodes a packfile object header: type in the first
// byte's 3 type bits, size in 4 bits there and 7 bits per continuation
// byte after, exactly as packfile.Scanner.readObjectTypeAndLength decodes
// it (gitformat-pack's object header, not the delta's own size varints).
func packObjectHeader(typ plumbing.ObjectType, size int64) []byte {
	first := byte(size&0x0f) | (byte(typ) << 4)
	size >>= 4
	if size > 0 {
		first |= 0x80
	}
	out := []byte{first}
	for size > 0 {
		b := byte(size & 0x7f)
		size >>= 7
		if size > 0 {
			b |= 0x80
		}
		out = append(out, b)
	}
	return out
}

// encodeDeltaSizeVarint encodes one of the two size varints at the front
// of a packed delta representation — the mirror of this package's own
// decodeDeltaSizeVarint.
func encodeDeltaSizeVarint(n uint64) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n > 0 {
			out = append(out, b|0x80)
			continue
		}
		out = append(out, b)
		return out
	}
}

// insertOnlyDelta builds a packed delta representation — <base size>
// <target size><instructions> — whose instructions are pure "insert N
// literal bytes" opcodes reproducing target exactly, never referencing
// baseSize's content at all. This is the worst case for decoding a
// delta's size by materializing the whole representation: the
// representation is as large as target itself, byte for byte, the same
// shape the WRIT-255 round-1 review found (a reviewer-built 229 KB pack
// holding a 64 MiB blob as an insert-only delta).
func insertOnlyDelta(baseSize int, target []byte) []byte {
	var out []byte
	out = append(out, encodeDeltaSizeVarint(uint64(baseSize))...)
	out = append(out, encodeDeltaSizeVarint(uint64(len(target)))...)
	for len(target) > 0 {
		n := len(target)
		if n > 127 {
			n = 127
		}
		out = append(out, byte(n))
		out = append(out, target[:n]...)
		target = target[n:]
	}
	return out
}

func deflate(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

// buildInsertOnlyDeltaPack hand-assembles a valid packfile holding
// exactly two objects: a small base blob, and an insert-only REF_DELTA
// object that reconstructs target. It then runs go-git's own
// packfile.Parser over those bytes — the same resolution real git's
// `git index-pack` performs on a pack it receives — to compute the
// matching index, so the index this test writes to disk is exactly as
// trustworthy as one a real push would leave behind.
func buildInsertOnlyDeltaPack(t *testing.T, target []byte) (packBytes []byte, idxBytes []byte, packHash, targetHash plumbing.Hash) {
	t.Helper()

	baseContent := []byte("B")
	baseHash := plumbing.ComputeHash(plumbing.BlobObject, baseContent)
	targetHash = plumbing.ComputeHash(plumbing.BlobObject, target)

	var body bytes.Buffer
	body.WriteString("PACK")
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 2) // version
	body.Write(hdr[:])
	binary.BigEndian.PutUint32(hdr[:], 2) // object count
	body.Write(hdr[:])

	body.Write(packObjectHeader(plumbing.BlobObject, int64(len(baseContent))))
	body.Write(deflate(t, baseContent))

	delta := insertOnlyDelta(len(baseContent), target)
	body.Write(packObjectHeader(plumbing.REFDeltaObject, int64(len(delta))))
	body.Write(baseHash[:])
	body.Write(deflate(t, delta))

	sum := sha1.Sum(body.Bytes())
	body.Write(sum[:])
	packBytes = body.Bytes()

	scanner := packfile.NewScanner(bytes.NewReader(packBytes))
	idxWriter := new(idxfile.Writer)
	parser, err := packfile.NewParser(scanner, idxWriter)
	if err != nil {
		t.Fatalf("packfile.NewParser: %v", err)
	}
	checksum, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse handwritten pack: %v", err)
	}
	idx, err := idxWriter.Index()
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	if _, err := idx.FindOffset(targetHash); err != nil {
		t.Fatalf("index does not resolve target hash %s: %v", targetHash, err)
	}

	var idxBuf bytes.Buffer
	if _, err := idxfile.NewEncoder(&idxBuf).Encode(idx); err != nil {
		t.Fatalf("encode index: %v", err)
	}
	return packBytes, idxBuf.Bytes(), checksum, targetHash
}

// writeOpCommitOverBlob writes a tree with a single op.json entry
// pointing at blobHash — which need not exist as a loose object, e.g.
// when the caller has already placed it in a handwritten pack, or in an
// alternate object store — plus the commit over that tree, into repo.
func writeOpCommitOverBlob(t *testing.T, repo *git.Repository, blobHash plumbing.Hash) *object.Commit {
	t.Helper()

	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "op.json", Mode: filemode.Regular, Hash: blobHash},
	}}
	treeObj := repo.Storer.NewEncodedObject()
	treeObj.SetType(plumbing.TreeObject)
	if err := tree.Encode(treeObj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		t.Fatalf("store tree: %v", err)
	}

	sig := object.Signature{Name: "Alice", Email: "alice@example.test", When: time.Now().UTC()}
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   "writ: create widget/packed\n",
		TreeHash:  treeHash,
	}
	commitObj := repo.Storer.NewEncodedObject()
	commitObj.SetType(plumbing.CommitObject)
	if err := commit.Encode(commitObj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}

	gitCommit, err := object.GetCommit(repo.Storer, commitHash)
	if err != nil {
		t.Fatalf("get commit: %v", err)
	}
	return gitCommit
}

// TestFromGitCommit_PackedDeltaOversizedPayloadBoundedAllocation pins the
// packfile side of WRIT-255 round 2: go-git's own EncodedObjectSize is
// not cheap for a packed delta object — it fully decompresses the delta
// representation into memory to answer a size query — so an op.json
// blob stored as an insert-only delta must still be recognized as
// oversized without allocating anywhere near its own size. Reverting
// FromGitCommit's size check to plain EncodedObjectSize (WRIT-255 round
// 1) makes this test fail on the allocation budget, not the rejection
// itself: go-git decodes the whole ~8 MiB delta representation before
// this package ever gets to look at its length.
func TestFromGitCommit_PackedDeltaOversizedPayloadBoundedAllocation(t *testing.T) {
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	target := bytes.Repeat([]byte("x"), oversizedPackedOpJSONBytes)
	packBytes, idxBytes, packHash, targetHash := buildInsertOnlyDeltaPack(t, target)

	packDir := filepath.Join(repoDir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	base := "pack-" + packHash.String()
	if err := os.WriteFile(filepath.Join(packDir, base+".pack"), packBytes, 0o644); err != nil {
		t.Fatalf("write pack: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packDir, base+".idx"), idxBytes, 0o644); err != nil {
		t.Fatalf("write idx: %v", err)
	}

	gitCommit := writeOpCommitOverBlob(t, repo, targetHash)

	// buildInsertOnlyDeltaPack above already resolved this exact delta
	// once, while validating the handwritten pack through go-git's own
	// packfile.Parser, and go-git pools the buffers that decoding a
	// delta uses (utils/sync's GetBytesBuffer/PutBytesBuffer). Without
	// forcing that pool to drop its now-8-MiB-capacity buffer first, the
	// measurement below would silently reuse it for any further delta
	// decode and see no new allocation at all — hiding the very
	// regression this test exists to catch. A buffer Put into a
	// sync.Pool survives one full GC cycle as a "victim" and is dropped
	// on the next, so two full cycles guarantee it is gone before the
	// baseline is captured.
	runtime.GC()
	runtime.GC()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pureCommit, err := codec.FromGitCommit(repo.Storer, gitCommit)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("FromGitCommit failed: %v", err)
	}

	if len(pureCommit.Tree) != 1 || pureCommit.Tree[0].Name != "op.json" {
		t.Fatalf("Tree = %+v, want a single op.json entry", pureCommit.Tree)
	}
	if got := len(pureCommit.Tree[0].Data); got != codec.MaxPayloadBytes+1 {
		t.Errorf("op.json data length = %d, want %d (the oversized placeholder)", got, codec.MaxPayloadBytes+1)
	}

	if delta := after.TotalAlloc - before.TotalAlloc; delta > 4*codec.MaxPayloadBytes {
		t.Errorf("FromGitCommit allocated %d bytes, want well under the %d-byte delta's target size (budget 4x MaxPayloadBytes = %d)",
			delta, oversizedPackedOpJSONBytes, 4*codec.MaxPayloadBytes)
	}
}

// TestFromGitCommit_AlternatesOversizedPayloadBoundedAllocation pins the
// second half of WRIT-255 round 2: a blob that exists only in an
// alternate object store (as with a --shared or --reference clone) must
// still be sized cheaply, not by falling back to a full, unbounded
// fetch the moment the main repository's own EncodedObjectSize comes up
// empty (it never searches alternates at all — only the full
// EncodedObject fetch does).
//
// The alternate lives inside the main repository's own .git directory
// (.git/alt, holding just an objects/ tree) rather than as a genuinely
// separate repository elsewhere on disk. That is not how a real
// --reference clone is laid out, but it is the layout go-git's own
// dotgit.Alternates can resolve at all through the default,
// chroot-bound filesystem internal/gitdir.OpenStorage constructs (a
// relative alternates entry pointing outside that boundary hits
// billy's ErrCrossedBoundary before writ's own code ever runs) — so
// this is the honest way to exercise the same dg.Alternates() call
// packfileObjectSize makes, and the same recursive search, without
// depending on go-git behavior this package does not control.
func TestFromGitCommit_AlternatesOversizedPayloadBoundedAllocation(t *testing.T) {
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	altDir := filepath.Join(repoDir, ".git", "alt")
	if err := os.MkdirAll(filepath.Join(altDir, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir alternate objects dir: %v", err)
	}
	altStorage := filesystem.NewStorage(osfs.New(altDir), cache.NewObjectLRUDefault())

	target := bytes.Repeat([]byte("y"), oversizedPackedOpJSONBytes)
	blobObj := altStorage.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	w, err := blobObj.Writer()
	if err != nil {
		t.Fatalf("alternate blob writer: %v", err)
	}
	if _, err := w.Write(target); err != nil {
		t.Fatalf("write alternate blob: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close alternate blob: %v", err)
	}
	targetHash, err := altStorage.SetEncodedObject(blobObj)
	if err != nil {
		t.Fatalf("store alternate blob: %v", err)
	}

	// A relative entry here is resolved by go-git relative to the main
	// repository's own .git directory (not, as real git does, relative
	// to objects/info/ — a documented go-git simplification), so
	// "alt/objects" means .git/alt/objects: exactly where the blob
	// above was just written, and nowhere the main repository's own
	// loose or packed storage will ever look.
	alternatesPath := filepath.Join(repoDir, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternatesPath), 0o755); err != nil {
		t.Fatalf("mkdir objects/info: %v", err)
	}
	if err := os.WriteFile(alternatesPath, []byte("alt/objects\n"), 0o644); err != nil {
		t.Fatalf("write alternates file: %v", err)
	}

	gitCommit := writeOpCommitOverBlob(t, repo, targetHash)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pureCommit, err := codec.FromGitCommit(repo.Storer, gitCommit)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("FromGitCommit failed: %v", err)
	}

	if len(pureCommit.Tree) != 1 || pureCommit.Tree[0].Name != "op.json" {
		t.Fatalf("Tree = %+v, want a single op.json entry", pureCommit.Tree)
	}
	if got := len(pureCommit.Tree[0].Data); got != codec.MaxPayloadBytes+1 {
		t.Errorf("op.json data length = %d, want %d (the oversized placeholder)", got, codec.MaxPayloadBytes+1)
	}

	if delta := after.TotalAlloc - before.TotalAlloc; delta > 4*codec.MaxPayloadBytes {
		t.Errorf("FromGitCommit allocated %d bytes, want well under the %d-byte alternate blob's size (budget 4x MaxPayloadBytes = %d)",
			delta, oversizedPackedOpJSONBytes, 4*codec.MaxPayloadBytes)
	}
}

// buildLargePackedRepo populates repo with n small, distinct blobs, packs
// all of them into a single pack via `git pack-objects` — the same tool
// a real push leaves behind, producing a real .idx rather than a
// handwritten one — and removes their loose copies, leaving every one of
// them reachable only through that pack's index. It returns their
// hashes in creation order.
func buildLargePackedRepo(t *testing.T, dir string, repo *git.Repository, n int) []plumbing.Hash {
	t.Helper()

	hashes := make([]plumbing.Hash, n)
	var stdin bytes.Buffer
	for i := 0; i < n; i++ {
		obj := repo.Storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			t.Fatalf("blob writer: %v", err)
		}
		if _, err := fmt.Fprintf(w, "writ-bench-blob-%d", i); err != nil {
			t.Fatalf("write blob: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close blob: %v", err)
		}
		h, err := repo.Storer.SetEncodedObject(obj)
		if err != nil {
			t.Fatalf("store blob: %v", err)
		}
		hashes[i] = h
		fmt.Fprintln(&stdin, h.String())
	}

	packDir := filepath.Join(dir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	cmd := exec.Command("git", "pack-objects", "--quiet", filepath.Join(packDir, "pack"))
	cmd.Dir = dir
	cmd.Stdin = &stdin
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-objects: %v\n%s", err, out)
	}

	loose, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "[0-9a-f][0-9a-f]"))
	if err != nil {
		t.Fatalf("glob loose objects: %v", err)
	}
	for _, l := range loose {
		if err := os.RemoveAll(l); err != nil {
			t.Fatalf("remove loose objects: %v", err)
		}
	}

	return hashes
}

// TestPackfileObjectSize_CachedIndexAvoidsPerCallRedecode pins the fix
// for WRIT-255 round 2's major finding: packfileObjectSize used to list
// the pack directory and decode a fresh idxfile.MemoryIndex from a
// pack's whole .idx on every single call, with nothing cached across
// calls — turning dag.Store.EnumerateSince's per-commit loop into a cost
// proportional to the whole repository's packs, once per commit,
// instead of once per pass. A storer wrapped once with
// packidx.WithCache and reused across calls must make every call after
// the first cheap; a plain, never-wrapped storer must still pay the
// decode in full on every call, both to prove the shared cache — not
// something else about a later lookup — is what makes the difference,
// and because a plain storage.Storer remains a valid, supported way to
// opt out of caching.
func TestPackfileObjectSize_CachedIndexAvoidsPerCallRedecode(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	const n = 10000
	hashes := buildLargePackedRepo(t, dir, repo, n)
	target := hashes[n-1]

	alloc := func(s storage.Storer) uint64 {
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		size, found, err := codec.PackfileObjectSize(s, target)
		runtime.ReadMemStats(&after)
		if err != nil || !found || size <= 0 {
			t.Fatalf("PackfileObjectSize: size=%d found=%v err=%v", size, found, err)
		}
		return after.TotalAlloc - before.TotalAlloc
	}

	// A plain, never-wrapped storer, twice: both calls decode the pack's
	// .idx from scratch, so both cost about the same — round 1's
	// behavior, which a plain storage.Storer must still provide.
	uncached1 := alloc(repo.Storer)
	uncached2 := alloc(repo.Storer)

	// One storer wrapped with packidx.WithCache, reused across three
	// calls: only the first should pay to decode; the rest reuse what
	// it already built.
	shared := packidx.WithCache(repo.Storer)
	firstWithCache := alloc(shared)
	secondWithCache := alloc(shared)
	thirdWithCache := alloc(shared)

	t.Logf("uncached: %d, %d bytes; shared cache: %d, %d, %d bytes",
		uncached1, uncached2, firstWithCache, secondWithCache, thirdWithCache)

	// A 10,000-entry .idx decodes to well over this; a cache hit
	// allocates nothing close to it. If the uncached calls don't clear
	// this bar, the benchmark's pack isn't exercising a real decode and
	// the rest of this test can't be trusted.
	const minDecodeCost = 200 << 10 // 200 KiB
	if uncached1 < minDecodeCost || uncached2 < minDecodeCost {
		t.Fatalf("uncached calls allocated %d and %d bytes, want at least %d", uncached1, uncached2, minDecodeCost)
	}

	const budget = minDecodeCost / 4
	if secondWithCache > budget {
		t.Errorf("second call against a warm cache allocated %d bytes, want under %d (an uncached decode allocated %d)", secondWithCache, budget, uncached1)
	}
	if thirdWithCache > budget {
		t.Errorf("third call against a warm cache allocated %d bytes, want under %d (an uncached decode allocated %d)", thirdWithCache, budget, uncached1)
	}
}

// TestPackedObjectSize_SkipsPackWithoutIdx pins WRIT-255 round 2's minor
// finding: git renames a pack's .pack file into place before its .idx
// during a fetch or a repack, so a pack-<hash>.pack with no matching
// .idx is common, transient state on a live repository, not corruption.
// dg.ObjectPacks() lists that file like any other, so packedObjectSize
// must skip it and keep searching the rest, the way git itself
// tolerates this — not fail the whole lookup closed the moment it hits
// the one pack that isn't fully written yet.
func TestPackedObjectSize_SkipsPackWithoutIdx(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	hashes := buildLargePackedRepo(t, dir, repo, 10)
	target := hashes[len(hashes)-1]

	packDir := filepath.Join(dir, ".git", "objects", "pack")
	realPacks, err := filepath.Glob(filepath.Join(packDir, "pack-*.pack"))
	if err != nil || len(realPacks) != 1 {
		t.Fatalf("glob real pack: %v (matches: %v)", err, realPacks)
	}
	realBase := strings.TrimSuffix(realPacks[0], ".pack")

	// dg.ObjectPacks() walks packs in the pack directory's sorted
	// listing order, so renaming the real pack+idx and the stray pack
	// below to fixed, maximally-far-apart names forces the stray one to
	// be searched first, deterministically. Without that, this test
	// would pass or fail depending on where the two pack hashes
	// happened to sort relative to each other — sometimes never
	// exercising the fix at all. strayName must not be the all-zero
	// hash: go-git's own dg.ObjectPacks() (objectPacks in
	// storage/filesystem/dotgit) silently skips any pack-<hash>.pack
	// whose hash is all zero as a "badly-formatted name" before this
	// package ever sees it, so a zero-hash stray pack never reaches
	// packedObjectSize's own idx-less-pack skip at all and this test
	// would still pass with that skip logic deleted.
	const realName = "pack-ffffffffffffffffffffffffffffffffffffffff"
	const strayName = "pack-0000000000000000000000000000000000000001"
	if err := os.Rename(realBase+".pack", filepath.Join(packDir, realName+".pack")); err != nil {
		t.Fatalf("rename pack: %v", err)
	}
	if err := os.Rename(realBase+".idx", filepath.Join(packDir, realName+".idx")); err != nil {
		t.Fatalf("rename idx: %v", err)
	}

	// Drop in a stray pack-<hash>.pack with no matching .idx at all —
	// exactly the mid-fetch/mid-repack state git leaves (it renames a
	// pack's .pack into place before its .idx). packedObjectSize must
	// never get far enough to read this file's content — it fails on
	// the missing .idx first — so the content itself doesn't need to be
	// a valid pack.
	strayContent := []byte("not a real pack, and that must not matter")
	if err := os.WriteFile(filepath.Join(packDir, strayName+".pack"), strayContent, 0o644); err != nil {
		t.Fatalf("write stray pack: %v", err)
	}
	// Deliberately no strayName+".idx".

	size, found, err := codec.PackfileObjectSize(repo.Storer, target)
	if err != nil {
		t.Fatalf("PackfileObjectSize returned an error instead of skipping the idx-less pack: %v", err)
	}
	if !found {
		t.Fatalf("PackfileObjectSize did not find %s past the idx-less pack", target)
	}
	if size <= 0 {
		t.Errorf("size = %d, want > 0", size)
	}
}
