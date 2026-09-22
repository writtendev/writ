package writ

import (
	"context"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/identity"
)

// TrustStoreStatus classifies a repository's trust-store configuration, as
// reported by CheckTrustStore.
type TrustStoreStatus string

const (
	// TrustStoreOK means a gpg.ssh.allowedSignersFile is configured and
	// its file reads and parses: a non-valid verification outcome here is
	// a real wrong-key/unsigned/etc., not an unconfigured-trust-store
	// artifact.
	TrustStoreOK TrustStoreStatus = "ok"

	// TrustStoreUnconfigured means no gpg.ssh.allowedSignersFile key is
	// set at all.
	TrustStoreUnconfigured TrustStoreStatus = "unconfigured"

	// TrustStoreUnreadable means a gpg.ssh.allowedSignersFile key is set,
	// but the file it names could not be read or parsed. Open treats
	// that the same as unconfigured -- wrong-key everywhere, for exactly
	// the same underlying reason as the unconfigured case, just a
	// different cause to name.
	TrustStoreUnreadable TrustStoreStatus = "unreadable"
)

// CheckTrustStore classifies path's repository's trust-store
// configuration, for a caller deciding whether a non-valid verification
// outcome is a real wrong-key/unsigned/etc. failure or an artifact of no
// trust store being configured at all (cmd/writ's object show/list
// stderr hint).
//
// It resolves the git directory independently of Open -- the same way
// open.go's resolveTrustSignersPath itself resolves repoDir before
// calling identity.AllowedSignersFile -- rather than reading it off an
// already-open *Store: identity.Load's early returns (a config error, a
// missing key) make a Store's already-loaded identity unreliable for
// this, so a caller cannot simply consult a Store it already has. An
// unresolvable git directory reports TrustStoreOK, so a caller's hint
// stays quiet rather than firing on a path that is not a repository at
// all.
func CheckTrustStore(ctx context.Context, path string) TrustStoreStatus {
	gitInfo, err := ResolveGitDir(path)
	if err != nil {
		return TrustStoreOK
	}
	repoDir := gitInfo.WorkTree
	if repoDir == "" {
		repoDir = gitInfo.GitDir
	}
	signersPath, err := identity.AllowedSignersFile(ctx, repoDir)
	if err != nil || signersPath == "" {
		return TrustStoreUnconfigured
	}
	if _, err := codec.LoadTrustStore(signersPath); err != nil {
		return TrustStoreUnreadable
	}
	return TrustStoreOK
}
