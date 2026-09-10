package identity

import (
	"fmt"
	"strings"

	"github.com/writtendev/writ/engine/internal/person"
)

// PersonIDKey is the git config key that overrides the derived person
// identifier. Reading git config lowercases keys; this is the spelling a user
// types.
const PersonIDKey = "writ.personId"

// DerivePersonID returns the local writer's person identifier from git config,
// per spec/identifiers.md §Relationship to writer-id: writ.personId when set,
// otherwise "email:" followed by the normalized user.email.
//
// The derivation is deliberately not a guess. A person identifier carries a
// scheme, and the only scheme a git config can supply on its own is email:, so
// that is the only one derived. A workspace that identifies people by handle
// sets writ.personId explicitly — which is also the escape hatch for anyone
// who does not want their address in a public, unretractable op log.
//
// The result is normalized and checked. An identifier the schema would reject
// is an error here rather than a truncation or a repair: an op body is written
// once into a signed commit and never rewritten, so the only place to stop a
// malformed identifier is before the write.
func DerivePersonID(cfg map[string]string) (string, error) {
	// Trim before the emptiness check, exactly as the user.email branch below
	// does and for the same reason. git config stores a whitespace-only value
	// verbatim, so a raw != "" guard entered this branch, normalized to the
	// empty string, failed the check and refused with a malformed-identifier
	// error — telling a user their identifier was missing its scheme when what
	// they had typed was nothing at all, and taking down every write path on a
	// repository whose user.email would have derived a perfectly good
	// identifier. A set key with nothing in it is nothing configured; the two
	// keys had disagreed about that.
	//
	// Only the guard trims. A value that survives it is normalized from the
	// raw string, and a value that fails the check is reported raw: the error
	// quotes what the user typed.
	if raw := cfg["writ.personid"]; strings.TrimSpace(raw) != "" {
		norm := person.NormalizePerson(raw)
		if p := person.Check(norm); p != person.Valid {
			problem := fmt.Errorf("%w: %s", ErrInvalid, p)
			if p == person.ForbiddenCodePoint {
				if r, ok := person.FirstForbidden(norm); ok {
					problem = fmt.Errorf("%w: %s (U+%04X)", ErrInvalid, p, r)
				}
			}
			return "", &ConfigError{
				Key:     PersonIDKey,
				Value:   raw,
				Problem: problem,
			}
		}
		return norm, nil
	}

	// Trim before the emptiness check. A whitespace-only user.email is a set
	// key with nothing in it; treating it as configured would derive "email:",
	// an identifier with an empty value, and report it as a bad address rather
	// than as an address that was never given.
	email := strings.TrimSpace(cfg["user.email"])
	if email == "" {
		return "", &ConfigError{
			Key: PersonIDKey,
			Problem: fmt.Errorf("%w: set %s (for example %q), or set user.email so it can be derived as email:<user.email>",
				ErrMissing, PersonIDKey, "user:alice"),
		}
	}

	norm := person.NormalizePerson("email:" + email)
	if p := person.Check(norm); p != person.Valid {
		problem := fmt.Errorf("%w: derived person identifier is not conforming: %s (set %s to override)", ErrInvalid, p, PersonIDKey)
		if p == person.ForbiddenCodePoint {
			if r, ok := person.FirstForbidden(norm); ok {
				problem = fmt.Errorf("%w: derived person identifier is not conforming: %s (U+%04X) (set %s to override)", ErrInvalid, p, r, PersonIDKey)
			}
		}
		return "", &ConfigError{
			Key:     "user.email",
			Value:   email,
			Problem: problem,
		}
	}
	return norm, nil
}
