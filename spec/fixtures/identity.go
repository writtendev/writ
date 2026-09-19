package fixtures

import "fmt"

// identity is a fixed, fixture-only author/signer. Timestamps come from
// the description, never from these — identity only fixes name, email,
// and signing key.
type identity struct {
	Name    string
	Email   string
	KeyFile string // basename under keys/, e.g. "alice_ed25519"
}

// identities is the fixed cast of fixture authors. Every description in
// this repo must reference one of these by name; the set is intentionally
// small and closed so the keyring in keys/ never needs to grow silently.
var identities = map[string]identity{
	"alice": {Name: "Alice Example", Email: "alice@example.test", KeyFile: "alice_ed25519"},
	"bob":   {Name: "Bob Example", Email: "bob@example.test", KeyFile: "bob_ed25519"},

	// alice_upper reuses alice's key rather than growing the keyring
	// (WRIT-278): AllowedSignersContent derives its principal lines from
	// keys/*.pub filenames, not from this map, so the generated trust
	// store still contains only the lowercase alice@example.test line —
	// this identity exists purely to author a commit whose author email
	// case-mismatches that line while still signing with alice's key.
	"alice_upper": {Name: "Alice Example", Email: "Alice@Example.TEST", KeyFile: "alice_ed25519"},

	// alice_slash and alice_bracket exist for the envelope-signer-patterns
	// fixture (WRIT-302), which needs author emails path.Match's old
	// semantics treated specially but OpenSSH's match_pattern does not:
	// a '/' that a glob must be able to cross, and a literal '[' that
	// must NOT open a character class. Both reuse alice's key, same as
	// alice_upper, and both are only ever referenced from that
	// description's own trust_store: override, never from
	// AllowedSignersContent's generated default.
	"alice_slash":   {Name: "Alice Example", Email: "alice/laptop@example.test", KeyFile: "alice_ed25519"},
	"alice_bracket": {Name: "Alice Example", Email: "ali[cex@example.net", KeyFile: "alice_ed25519"},
}

func lookupIdentity(name string) (identity, error) {
	id, ok := identities[name]
	if !ok {
		return identity{}, fmt.Errorf("fixtures: unknown identity %q (see identity.go for the fixed cast)", name)
	}
	return id, nil
}
