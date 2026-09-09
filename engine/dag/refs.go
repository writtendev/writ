package dag

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/identity"
)

// objectTypeRegexp mirrors the object-type rule in spec/ref-layout.md: a
// bare segment, or two such segments joined by exactly one dot — the
// <namespace>.<type> qualified form WRIT-217 introduces so two
// independently authored schema packages can each declare a type of the
// same bare name without contending for one wire object_type. Each
// segment is capped at 64 characters in the pattern itself, so the
// qualified whole is capped at 129 (64 + '.' + 64) by construction; see
// objectTypeMaxLength. Writer-id validation lives in
// identity.ParseWriterID.
var objectTypeRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`)

// objectTypeMaxLength is object_type's own bound (spec/op-envelope.md):
// two 64-character segments plus the separating dot. Belt-and-braces
// alongside objectTypeRegexp's own per-segment {0,63} bound, not the
// grammar's only enforcement of it.
const objectTypeMaxLength = 129

// objectTypeEndsInLock reports whether objType's final "."-delimited
// segment (or the whole string, if it carries no dot) is exactly "lock" —
// the one object-type shape that is grammar-legal but ref-unwritable:
// git rejects any slash-separated ref path component ending in ".lock"
// outright (verified against real git: `git check-ref-format
// refs/writ/<writer-id>/acme.lock` fails, while `.../acme.standup`
// succeeds). A namespace of "lock" is fine ("lock.thing" is a legal, ref-
// writable object type); the exclusion is on the trailing segment alone.
func objectTypeEndsInLock(objType string) bool {
	if i := strings.LastIndexByte(objType, '.'); i >= 0 {
		return objType[i+1:] == "lock"
	}
	return objType == "lock"
}

// ChainRef represents a parsed Writ append chain reference.
type ChainRef struct {
	Name       plumbing.ReferenceName `json:"name"`
	Remote     string                 `json:"remote,omitempty"`
	WriterID   identity.WriterID      `json:"writer_id"`
	ObjectType string                 `json:"object_type"`
}

// DiscoveredChain holds a parsed ChainRef and its current tip commit hash.
type DiscoveredChain struct {
	Ref ChainRef      `json:"ref"`
	Tip plumbing.Hash `json:"tip"`
}

// ParseChainRef parses a reference name string into a ChainRef.
// It accepts local chains (refs/writ/<writer-id>/<object-type>) and
// remote-tracking chains (refs/remotes/<remote>/writ/<writer-id>/<object-type>).
func ParseChainRef(ref string) (ChainRef, error) {
	const localPrefix = "refs/writ/"
	const remotePrefix = "refs/remotes/"

	if strings.HasPrefix(ref, localPrefix) {
		rem := strings.TrimPrefix(ref, localPrefix)
		parts := strings.Split(rem, "/")
		if len(parts) != 2 {
			return ChainRef{}, fmt.Errorf("dag: invalid chain ref %q: must have exactly 2 segments after %q", ref, localPrefix)
		}
		wID, err := identity.ParseWriterID(parts[0])
		if err != nil {
			return ChainRef{}, fmt.Errorf("dag: invalid writer-id in ref %q: %w", ref, err)
		}
		objType := parts[1]
		if len(objType) == 0 || len(objType) > objectTypeMaxLength || !objectTypeRegexp.MatchString(objType) || objectTypeEndsInLock(objType) {
			return ChainRef{}, fmt.Errorf("dag: invalid object-type %q in ref %q", objType, ref)
		}
		return ChainRef{
			Name:       plumbing.ReferenceName(ref),
			Remote:     "",
			WriterID:   wID,
			ObjectType: objType,
		}, nil
	}

	if strings.HasPrefix(ref, remotePrefix) {
		rem := strings.TrimPrefix(ref, remotePrefix)
		const writMarker = "/writ/"
		idx := strings.Index(rem, writMarker)
		if idx <= 0 {
			return ChainRef{}, fmt.Errorf("dag: invalid remote chain ref %q: missing /writ/ segment", ref)
		}
		remote := rem[:idx]
		tail := rem[idx+len(writMarker):]
		parts := strings.Split(tail, "/")
		if len(parts) != 2 {
			return ChainRef{}, fmt.Errorf("dag: invalid remote chain ref %q: must have exactly 2 segments after /writ/", ref)
		}
		wID, err := identity.ParseWriterID(parts[0])
		if err != nil {
			return ChainRef{}, fmt.Errorf("dag: invalid writer-id in ref %q: %w", ref, err)
		}
		objType := parts[1]
		if len(objType) == 0 || len(objType) > objectTypeMaxLength || !objectTypeRegexp.MatchString(objType) || objectTypeEndsInLock(objType) {
			return ChainRef{}, fmt.Errorf("dag: invalid object-type %q in ref %q", objType, ref)
		}
		return ChainRef{
			Name:       plumbing.ReferenceName(ref),
			Remote:     remote,
			WriterID:   wID,
			ObjectType: objType,
		}, nil
	}

	return ChainRef{}, fmt.Errorf("dag: ref %q is not a writ chain ref", ref)
}

// LocalRefName constructs the canonical plumbing.ReferenceName for a local writer's chain.
func LocalRefName(writerID identity.WriterID, objectType string) plumbing.ReferenceName {
	return plumbing.ReferenceName(fmt.Sprintf("refs/writ/%s/%s", writerID, objectType))
}

// RemoteRefName constructs the canonical plumbing.ReferenceName for a remote-tracking chain.
func RemoteRefName(remote string, writerID identity.WriterID, objectType string) plumbing.ReferenceName {
	return plumbing.ReferenceName(fmt.Sprintf("refs/remotes/%s/writ/%s/%s", remote, writerID, objectType))
}

// Chains performs a single-pass scan of storer's references and returns all discovered
// local and remote-tracking writ chains mapped by their reference name string.
func Chains(s storage.Storer) (map[string]DiscoveredChain, error) {
	if s == nil {
		return nil, fmt.Errorf("dag: nil storer")
	}
	iter, err := s.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("dag: iter references: %w", err)
	}
	defer iter.Close()

	chains := make(map[string]DiscoveredChain)
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref == nil || ref.Type() != plumbing.HashReference {
			return nil
		}
		refName := ref.Name().String()
		chainRef, err := ParseChainRef(refName)
		if err != nil {
			return nil // Not a writ chain ref; ignore
		}
		chains[refName] = DiscoveredChain{
			Ref: chainRef,
			Tip: ref.Hash(),
		}
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return nil, fmt.Errorf("dag: iterate references: %w", err)
	}

	return chains, nil
}
