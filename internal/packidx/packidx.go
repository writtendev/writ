// Package packidx caches decoded pack indexes across one pass over a
// repository's history, so a caller that looks up many blobs against the
// same on-disk packs — codec's packfileObjectSize does exactly this, once
// per commit dag.Store.EnumerateSince walks — pays the cost of decoding a
// pack's whole .idx once, not on every lookup. WRIT-255 round 2 found that
// cost unbounded per call: a pack of 1,000,000 objects (a 28 MB .idx) cost
// about 40 ms and 28 MB allocated on every op.json size check, including
// repeat checks against the very same pack.
//
// It lives under engine/internal because a pack index is git plumbing —
// exactly the kind of detail the public Go API stays free of (see
// AGENTS.md's "public Go API is schema-shaped" house rule). engine/codec
// and engine/dag both import it; the "internal" boundary keeps it reachable
// from anywhere under engine/ while excluding it from api/engine.txt (see
// internal/cmd/apisurface, which skips "internal" directories outright).
package packidx

import (
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/storage"
)

// Cache caches a repository's decoded pack indexes, keyed by pack hash. A
// decoded *idxfile.MemoryIndex answers FindOffset from the structure it
// built the first time, so keeping it around costs nothing further to
// reuse.
//
// A nil *Cache is valid everywhere one is accepted: Get always misses and
// Put is a no-op, so "no cache" and "cache" share one code path.
//
// A Cache is scoped to whatever built it and must not outlive that: a fetch
// or a repack can add, remove, or replace the packs it holds indexes for,
// and a stale hit for a pack that's gone — or a miss for one that just
// arrived — would reintroduce the bug this type exists to fix. WithCache
// constructs a fresh one for each call; nothing stores it anywhere longer
// lived (dag.Store.EnumerateSince wraps its storer once per pass and lets
// the wrapper go when the pass returns). It holds no reference to anything
// outside the process (a decoded index is a plain in-memory structure, not
// an open file descriptor), so dropping it is just letting the garbage
// collector reclaim the map.
type Cache struct {
	mu      sync.Mutex
	indexes map[plumbing.Hash]*idxfile.MemoryIndex
}

func newCache() *Cache {
	return &Cache{indexes: make(map[plumbing.Hash]*idxfile.MemoryIndex)}
}

// Get is safe to call on a nil *Cache; it reports a miss.
func (c *Cache) Get(pack plumbing.Hash) (*idxfile.MemoryIndex, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.indexes[pack]
	return idx, ok
}

// Put is safe to call on a nil *Cache; it does nothing.
func (c *Cache) Put(pack plumbing.Hash, idx *idxfile.MemoryIndex) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.indexes[pack] = idx
}

// Wrapper attaches a fresh Cache to a storage.Storer, embedding it so a
// *Wrapper is itself a storage.Storer with every method the embedded
// value has. codec's packfileObjectSize type-asserts a storage.Storer to
// *Wrapper to find the Cache to use (a plain storage.Storer that was
// never wrapped just means no caching), and — deliberately — checks
// what the embedded Storer itself supports (e.g. whether it exposes a
// billy.Filesystem) rather than asking the *Wrapper. Do not add a method
// here that forwards to one the embedded storage.Storer may or may not
// have: unlike embedding, a hand-written forwarding method can't decline
// to exist, so a wrapped value that lacks the method would come out the
// other side looking like it always has one that answers with a zero
// value — exactly the bug this comment exists to keep from coming back.
type Wrapper struct {
	storage.Storer
	Cache *Cache
}

// WithCache wraps s with a Cache good for one pass over history: construct
// one per pass — dag.Store.EnumerateSince does exactly that, once per call
// — and let the pass discard it when it returns. See Cache's doc comment
// for why it must never outlive that.
func WithCache(s storage.Storer) storage.Storer {
	return &Wrapper{Storer: s, Cache: newCache()}
}
