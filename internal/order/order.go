// Package order implements fractional indexing as a shared ordering primitive
// for user-controlled ordering that survives concurrent edits across distributed
// actors.
//
// Positions are represented as lexicographically ordered, byte-comparable base-62
// strings with strict canonical validation (forbidding trailing '0'). Duplicate
// positions arising from concurrent inserts are deterministically resolved by an
// op-id tiebreak.
//
// Normative specification: spec/ordering.md.
package order

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// Alphabet is the base-62 character set in ASCII collation order.
	Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// MinChar is the minimum character in the alphabet ('0').
	MinChar = '0'

	// MaxChar is the maximum character in the alphabet ('z').
	MaxChar = 'z'

	// MidChar is the midpoint character of the alphabet ('V', index 31).
	MidChar = 'V'
)

var (
	// ErrEmptyKey is returned when a position key is empty.
	ErrEmptyKey = errors.New("order: position key must not be empty")

	// ErrInvalidCharacter is returned when a position key contains a character
	// outside the base-62 Alphabet.
	ErrInvalidCharacter = errors.New("order: position key contains invalid character")

	// ErrTrailingZero is returned when a position key ends with '0', violating
	// canonical form.
	ErrTrailingZero = errors.New("order: position key must not end with '0'")

	// ErrInvalidRange is returned when before is greater than or equal to after in Between.
	ErrInvalidRange = errors.New("order: before must be less than after")
)

// Validate checks that key is a canonical fractional index position:
// non-empty, composed strictly of base-62 characters from Alphabet, and
// not ending with '0'.
func Validate(key string) error {
	if key == "" {
		return ErrEmptyKey
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if charIndex(c) < 0 {
			return fmt.Errorf("%w: %q at byte %d", ErrInvalidCharacter, c, i)
		}
	}
	if key[len(key)-1] == '0' {
		return ErrTrailingZero
	}
	return nil
}

// Between returns a canonical position key strictly between before and after.
// If before is empty (""), the key is placed before after (or at MidChar if after is also empty).
// If after is empty (""), the key is placed after before.
// If both are non-empty, before must be strictly less than after in byte order.
func Between(before, after string) (string, error) {
	if before != "" {
		if err := Validate(before); err != nil {
			return "", fmt.Errorf("invalid before key: %w", err)
		}
	}
	if after != "" {
		if err := Validate(after); err != nil {
			return "", fmt.Errorf("invalid after key: %w", err)
		}
	}

	if before != "" && after != "" {
		if before >= after {
			return "", fmt.Errorf("%w: before (%q) >= after (%q)", ErrInvalidRange, before, after)
		}
	}

	return between(before, after), nil
}

func between(before, after string) string {
	if before == "" && after == "" {
		return string(MidChar)
	}

	if before == "" {
		// Insert before after.
		// Find first non-zero character in after.
		// after is canonical, so it cannot be all zeros.
		k := 0
		for k < len(after) && after[k] == '0' {
			k++
		}
		d := after[k]
		idx := charIndex(d)
		if idx > 1 {
			mid := idx / 2
			return strings.Repeat("0", k) + string(Alphabet[mid])
		}
		// idx == 1 (character '1'):
		// Append k+1 zeros followed by MidChar ('V').
		return strings.Repeat("0", k+1) + string(MidChar)
	}

	if after == "" {
		// Insert after before.
		// Count leading 'z's.
		m := 0
		for m < len(before) && before[m] == 'z' {
			m++
		}
		if m == len(before) {
			// before is all 'z's (e.g. "z", "zz")
			return before + string(MidChar)
		}
		// Character before[m] < 'z'
		idx := charIndex(before[m])
		mid := (idx + len(Alphabet)) / 2
		return before[:m] + string(Alphabet[mid])
	}

	// Both before and after are non-empty, before < after.
	p := 0
	for p < len(before) && p < len(after) && before[p] == after[p] {
		p++
	}

	if p == len(before) {
		// before is a strict prefix of after.
		// We need a key between before and after.
		return before + between("", after[p:])
	}

	// At index p, before[p] < after[p].
	i1 := charIndex(before[p])
	i2 := charIndex(after[p])

	if i2-i1 > 1 {
		mid := i1 + (i2-i1)/2
		return before[:p] + string(Alphabet[mid])
	}

	// i2 - i1 == 1. No character between before[p] and after[p].
	// We take prefix before[:p+1] and append a key > before[p+1:].
	if len(before) == p+1 {
		return before[:p+1] + string(MidChar)
	}
	return before[:p+1] + between(before[p+1:], "")
}

// Compare compares two (position, op_id) pairs deterministically:
// first by position (byte comparison / ASCII lexicographic order),
// and on tie by op_id (commit SHA byte comparison).
// It returns -1 if a < b, 0 if a == b, and 1 if a > b.
func Compare(posA, opIDA, posB, opIDB string) int {
	if posA < posB {
		return -1
	}
	if posA > posB {
		return 1
	}
	if opIDA < opIDB {
		return -1
	}
	if opIDA > opIDB {
		return 1
	}
	return 0
}

// Less reports whether (posA, opIDA) is ordered strictly before (posB, opIDB).
func Less(posA, opIDA, posB, opIDB string) bool {
	return Compare(posA, opIDA, posB, opIDB) < 0
}

func charIndex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return int(c - 'A' + 10)
	case c >= 'a' && c <= 'z':
		return int(c - 'a' + 36)
	default:
		return -1
	}
}
