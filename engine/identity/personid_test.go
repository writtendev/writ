package identity_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/identity"
)

func TestDerivePersonID(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want string
	}{
		{
			name: "derived from user.email",
			cfg:  map[string]string{"user.email": "Alice@Example.COM"},
			want: "email:alice@example.com",
		},
		{
			name: "writ.personId overrides",
			cfg: map[string]string{
				"user.email":    "alice@example.com",
				"writ.personid": "  User:Alice  ",
				"user.name":     "Alice",
				"writ.writerid": "0123456789abcdef",
				"gpg.format":    "ssh",
			},
			want: "user:alice",
		},
		{
			name: "an unknown scheme is honoured, not second-guessed",
			cfg:  map[string]string{"writ.personid": "keybase:alice"},
			want: "keybase:alice",
		},
		{
			// WRIT-144. The override used to be gated on the raw value, so a
			// key holding only spaces entered the branch, normalized to
			// nothing, and refused with a malformed-identifier error — telling
			// a user their identifier had no scheme when they had typed
			// nothing, and doing it on a repository whose user.email derives
			// perfectly well. The two keys now agree that a set key with
			// nothing in it is nothing configured.
			name: "whitespace-only writ.personId falls back to user.email",
			cfg: map[string]string{
				"writ.personid": "   ",
				"user.email":    "Alice@Example.COM",
			},
			want: "email:alice@example.com",
		},
		{
			name: "writ.personId of tabs and newlines falls back too",
			cfg: map[string]string{
				"writ.personid": "\t\n ",
				"user.email":    "alice@example.com",
			},
			want: "email:alice@example.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.DerivePersonID(tc.cfg)
			if err != nil {
				t.Fatalf("DerivePersonID: %v", err)
			}
			if got != tc.want {
				t.Errorf("DerivePersonID = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDerivePersonIDErrors covers the two ways derivation fails. Both name the
// config key to set, because the only thing a user can do about either is set
// one — and neither may fall back to something that is not a person identifier.
func TestDerivePersonIDErrors(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]string
		wantKey string
		wantErr error
	}{
		{
			name:    "nothing to derive from",
			cfg:     map[string]string{"user.name": "Alice"},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrMissing,
		},
		{
			name:    "whitespace user.email derives an empty value",
			cfg:     map[string]string{"user.email": "   "},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrMissing,
		},
		{
			name:    "writ.personId with no scheme",
			cfg:     map[string]string{"writ.personid": "alice@example.com"},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrInvalid,
		},
		{
			name:    "writ.personId with an over-long scheme",
			cfg:     map[string]string{"writ.personid": strings.Repeat("a", 33) + ":alice"},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrInvalid,
		},
		{
			name:    "user.email too long to bound",
			cfg:     map[string]string{"user.email": strings.Repeat("a", 321)},
			wantKey: "user.email",
			wantErr: identity.ErrInvalid,
		},
		{
			name:    "writ.personId with a forbidden code point",
			cfg:     map[string]string{"writ.personid": "user:ali" + string(rune(0x202E)) + "ce"},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrInvalid,
		},
		{
			name:    "user.email with a forbidden code point",
			cfg:     map[string]string{"user.email": "ali" + string(rune(0x202E)) + "ce@example.com"},
			wantKey: "user.email",
			wantErr: identity.ErrInvalid,
		},
		{
			// WRIT-144, the other half: with the override blank and nothing to
			// fall back to, the refusal is about configuration that is
			// missing, not about an identifier that is malformed. It used to
			// be ErrInvalid — "your identifier has no scheme" — for a user who
			// had typed no identifier at all.
			name: "whitespace-only writ.personId with nothing to fall back to",
			cfg: map[string]string{
				"writ.personid": "   ",
				"user.name":     "Alice",
			},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrMissing,
		},
		{
			name: "whitespace-only writ.personId and whitespace-only user.email",
			cfg: map[string]string{
				"writ.personid": " ",
				"user.email":    "   ",
			},
			wantKey: identity.PersonIDKey,
			wantErr: identity.ErrMissing,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.DerivePersonID(tc.cfg)
			if err == nil {
				t.Fatalf("DerivePersonID = %q, want an error", got)
			}
			if got != "" {
				t.Errorf("DerivePersonID returned %q alongside an error; a failed derivation must yield nothing, never a repaired identifier", got)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want one wrapping %v", err, tc.wantErr)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("error = %v, want a *identity.ConfigError", err)
			}
			if cfgErr.Key != tc.wantKey {
				t.Errorf("ConfigError.Key = %q, want %q", cfgErr.Key, tc.wantKey)
			}
		})
	}
}

// TestDerivePersonIDWhitespaceEmailIsNotSilentlyEmpty guards the specific shape
// of bug this derivation invites: "   " is a non-empty config value that
// normalizes to nothing, so a raw != "" check would produce "email:" — a
// schema-invalid identifier written into a signed, permanent op.
func TestDerivePersonIDWhitespaceEmailIsNotSilentlyEmpty(t *testing.T) {
	got, err := identity.DerivePersonID(map[string]string{"user.email": "   "})
	if err == nil {
		t.Fatalf("DerivePersonID = %q, want an error", got)
	}
	if strings.HasPrefix(got, "email:") {
		t.Errorf("DerivePersonID = %q, want no identifier at all", got)
	}
}

// TestDerivePersonIDInvalidOverrideQuotesRawValue pins the other half of
// WRIT-144's trim. Only the emptiness guard trims: a value that survives it
// and then fails the check is still reported exactly as the user typed it,
// padding included, because an error that quotes something the user cannot
// find in their config file is an error they cannot act on.
func TestDerivePersonIDInvalidOverrideQuotesRawValue(t *testing.T) {
	const raw = "  alice  "

	got, err := identity.DerivePersonID(map[string]string{
		"writ.personid": raw,
		// A usable address the trim must not reach for: the override is set to
		// something real, so it is honoured and refused, not skipped.
		"user.email": "alice@example.com",
	})
	if err == nil {
		t.Fatalf("DerivePersonID = %q, want an error: %q has no scheme", got, raw)
	}
	if got != "" {
		t.Errorf("DerivePersonID = %q, want nothing alongside the error", got)
	}

	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error = %v, want a *identity.ConfigError", err)
	}
	if !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("error = %v, want one wrapping ErrInvalid: the value is set and malformed, not absent", err)
	}
	if cfgErr.Key != identity.PersonIDKey {
		t.Errorf("ConfigError.Key = %q, want %q", cfgErr.Key, identity.PersonIDKey)
	}
	if cfgErr.Value != raw {
		t.Errorf("ConfigError.Value = %q, want the raw configured value %q", cfgErr.Value, raw)
	}
}

// TestDerivePersonIDForbiddenCodePointNamed pins WRIT-137's acceptance
// criterion for the producer-side repertoire rule -- a person identifier
// carrying a forbidden code point is refused with a message naming it --
// for both DerivePersonID arms: an explicit writ.personId override, and the
// email:<user.email> derivation.
func TestDerivePersonIDForbiddenCodePointNamed(t *testing.T) {
	_, err := identity.DerivePersonID(map[string]string{
		"writ.personid": "user:ali" + string(rune(0x202E)) + "ce",
	})
	if err == nil {
		t.Fatal("DerivePersonID: want an error")
	}
	if !strings.Contains(err.Error(), "U+202E") {
		t.Errorf("error = %v, want it to name U+202E", err)
	}

	_, err = identity.DerivePersonID(map[string]string{
		"user.email": "ali" + string(rune(0x202E)) + "ce@example.com",
	})
	if err == nil {
		t.Fatal("DerivePersonID: want an error")
	}
	if !strings.Contains(err.Error(), "U+202E") {
		t.Errorf("error = %v, want it to name U+202E", err)
	}
}
