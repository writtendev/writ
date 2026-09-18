package state

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var repoDesignatorRegexp = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ErrInvalidReference is returned when a reference string violates the reference grammar.
var ErrInvalidReference = errors.New("writ: invalid reference format")

// ParseReference parses a reference string into its repository designator (empty for bare local references)
// and target object ID, validating syntax against spec/identifiers.md §Reference grammar.
func ParseReference(ref string) (designator string, objectID string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("%w: reference cannot be empty", ErrInvalidReference)
	}

	for _, r := range ref {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", "", fmt.Errorf("%w: reference cannot contain whitespace or control characters", ErrInvalidReference)
		}
	}

	parts := strings.Split(ref, "#")
	switch len(parts) {
	case 1:
		// Bare reference
		if parts[0] == "" {
			return "", "", fmt.Errorf("%w: object id cannot be empty", ErrInvalidReference)
		}
		return "", parts[0], nil

	case 2:
		// Fully-qualified reference: <repo-id>#<object-id>
		des := parts[0]
		objID := parts[1]

		if des == "" {
			return "", "", fmt.Errorf("%w: repo designator cannot be empty when '#' is present", ErrInvalidReference)
		}
		if !repoDesignatorRegexp.MatchString(des) {
			return "", "", fmt.Errorf("%w: repo designator must be 32 lowercase hex characters", ErrInvalidReference)
		}
		if objID == "" {
			return "", "", fmt.Errorf("%w: object id cannot be empty", ErrInvalidReference)
		}

		return des, objID, nil

	default:
		return "", "", fmt.Errorf("%w: reference cannot contain multiple '#' separators", ErrInvalidReference)
	}
}
