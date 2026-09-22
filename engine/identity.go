package writ

import "github.com/writtendev/writ/internal/identity"

// ConfigError records a failure while reading or validating a writer's git
// configuration -- a missing, invalid, or unsupported value for a key Init
// or a signed write depends on (writ.writerId, writ.repoId, writ.personId,
// user.name, user.email, gpg.format, user.signingKey). Message returns the
// same text as Error without the "(run 'writ init' to configure)"
// remediation clause, for the one caller that remediation does not fit:
// writ init itself, which already prints the git config lines to run
// directly below each warning it emits.
type ConfigError = identity.ConfigError

var (
	// ErrMissingConfig indicates a required git configuration key is unset
	// or empty.
	ErrMissingConfig = identity.ErrMissing

	// ErrInvalidConfig indicates a git configuration key has an invalid
	// value.
	ErrInvalidConfig = identity.ErrInvalid

	// ErrUnsupportedFormat indicates a git configuration key specifies an
	// unsupported format (gpg.format set to anything but "ssh").
	ErrUnsupportedFormat = identity.ErrUnsupportedFormat
)

// PersonIDKey is the git config key that overrides a writer's derived
// person identifier (spec/identifiers.md Relationship to writer-id):
// writ.personId when set, otherwise "email:" followed by the normalized
// user.email.
const PersonIDKey = identity.PersonIDKey
