package sshsig_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/writtendev/writ/internal/codec/sshsig"
)

func TestAllowedSigners_ParsingAndAuthorization(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub1, _ := ssh.NewPublicKey(pub1)
	pubLine1 := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub1)))

	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub2, _ := ssh.NewPublicKey(pub2)
	pubLine2 := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub2)))

	allowedSignersContent := `
# Comment line

alice@example.com ` + pubLine1 + ` alice-comment
*@company.test namespaces="git" ` + pubLine2 + `
dev@example.com namespaces="file" ` + pubLine1 + `
temporal@example.com valid-after="20260101000000",valid-before="20260102000000" ` + pubLine1 + `
untrusted@example.com cert-authority ` + pubLine1 + `
!blocked@example.com,*@example.com ` + pubLine2 + `
`

	ts, err := sshsig.ParseAllowedSigners(strings.NewReader(allowedSignersContent))
	if err != nil {
		t.Fatalf("ParseAllowedSigners failed: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// 1. Direct match alice
	if !ts.IsAuthorized(sshPub1, "alice@example.com", "git", now) {
		t.Error("expected alice@example.com to be authorized with pub1")
	}

	// Wrong key for alice (pub3 is not in allowed_signers)
	pub3, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub3, _ := ssh.NewPublicKey(pub3)
	if ts.IsAuthorized(sshPub3, "alice@example.com", "git", now) {
		t.Error("alice@example.com should not be authorized with pub3")
	}

	// 2. Wildcard match on company.test
	if !ts.IsAuthorized(sshPub2, "bob@company.test", "git", now) {
		t.Error("expected bob@company.test to be authorized with pub2 under git")
	}
	// Wrong namespace for company.test
	if ts.IsAuthorized(sshPub2, "bob@company.test", "other", now) {
		t.Error("bob@company.test should not be authorized under namespace other")
	}

	// 3. Namespace mismatch on dev@example.com (only "file")
	if ts.IsAuthorized(sshPub1, "dev@example.com", "git", now) {
		// Note: alice@example.com rule does not match dev@example.com
		t.Error("dev@example.com should not be authorized under namespace git")
	}

	// 4. Temporal validity
	if !ts.IsAuthorized(sshPub1, "temporal@example.com", "git", now) {
		t.Error("temporal@example.com should be authorized at 2026-01-01 12:00:00")
	}
	beforeTime := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	if ts.IsAuthorized(sshPub1, "temporal@example.com", "git", beforeTime) {
		t.Error("temporal@example.com should not be authorized before valid-after")
	}
	afterTime := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if ts.IsAuthorized(sshPub1, "temporal@example.com", "git", afterTime) {
		t.Error("temporal@example.com should not be authorized after valid-before")
	}

	// 5. cert-authority line skipped
	if ts.IsAuthorized(sshPub1, "untrusted@example.com", "git", now) {
		t.Error("untrusted@example.com should not be authorized (cert-authority skipped)")
	}

	// 6. Negated pattern !blocked@example.com,*@example.com
	if !ts.IsAuthorized(sshPub2, "user@example.com", "git", now) {
		t.Error("user@example.com should match *@example.com")
	}
	if ts.IsAuthorized(sshPub2, "blocked@example.com", "git", now) {
		t.Error("blocked@example.com should be rejected due to !blocked@example.com")
	}
}

func TestAllowedSigners_CaseSensitiveMatching(t *testing.T) {
	// OpenSSH's match_pattern_list runs with dolower=0 for both the
	// principal and the namespaces= option (sshsig.c
	// check_allowed_keys_line), so matching here must be case-sensitive.
	// No existing test asserted the old (wrong) case-insensitive behaviour;
	// these are pure additions pinning the corrected comparisons in
	// matchPattern and matchNamespace. Each case below parses its own
	// TrustStore so one rule's match can't be masked by another.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	newKey := func(t *testing.T) (ssh.PublicKey, string) {
		t.Helper()
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return sshPub, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	}

	// Exact-case principal still authorized.
	t.Run("exact case still authorized", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader("alice@example.com " + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.com", "git", now) {
			t.Error("expected exact-case alice@example.com to remain authorized")
		}
	})

	// Alice@Example.COM vs an alice@example.com line is now unauthorized.
	t.Run("mixed case principal no longer authorized", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader("alice@example.com " + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "Alice@Example.COM", "git", now) {
			t.Error("Alice@Example.COM should not be authorized against an alice@example.com line")
		}
	})

	// Glob *@Example.com no longer matches bob@example.com.
	t.Run("glob pattern case no longer folds", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`*@Example.com ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "bob@example.com", "git", now) {
			t.Error("bob@example.com should not match pattern *@Example.com")
		}
		if !ts.IsAuthorized(sshPub, "bob@Example.com", "git", now) {
			t.Error("bob@Example.com should still match pattern *@Example.com")
		}
	})

	// !Blocked@example.com no longer negates blocked@example.com.
	t.Run("negation case no longer folds", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`!Blocked@example.com,*@example.com ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "blocked@example.com", "git", now) {
			t.Error("blocked@example.com should no longer be negated by !Blocked@example.com (case differs)")
		}
		if ts.IsAuthorized(sshPub, "Blocked@example.com", "git", now) {
			t.Error("Blocked@example.com should still be negated by exact-case !Blocked@example.com")
		}
	})

	// Bare "*" still matches everything, regardless of case.
	t.Run("bare wildcard still matches everything", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`* ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "Anyone@Example.COM", "git", now) {
			t.Error("bare * should authorize any principal regardless of case")
		}
	})
}

func TestAllowedSigners_NamespaceCaseSensitive(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub1, _ := ssh.NewPublicKey(pub1)
	pubLine1 := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub1)))

	allowedSignersContent := `alice@example.com namespaces="Git" ` + pubLine1 + `
`
	ts, err := sshsig.ParseAllowedSigners(strings.NewReader(allowedSignersContent))
	if err != nil {
		t.Fatalf("ParseAllowedSigners failed: %v", err)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// namespaces="Git" no longer matches the lowercase "git" namespace.
	if ts.IsAuthorized(sshPub1, "alice@example.com", "git", now) {
		t.Error("namespaces=\"Git\" should not match namespace \"git\"")
	}
	// It still matches its own exact case.
	if !ts.IsAuthorized(sshPub1, "alice@example.com", "Git", now) {
		t.Error("namespaces=\"Git\" should match namespace \"Git\"")
	}
}

func TestAllowedSigners_MalformedLines(t *testing.T) {
	malformedInputs := []string{
		"alice@example.com ssh-ed25519",                    // 2 fields, no options, missing key
		"alice@example.com namespaces=\"git\" ssh-ed25519", // 3 fields with options, missing key
		"alice@example.com",                                // 1 field
	}

	for _, input := range malformedInputs {
		_, err := sshsig.ParseAllowedSigners(strings.NewReader(input))
		if err == nil {
			t.Errorf("ParseAllowedSigners(%q) expected error, got nil", input)
		}
	}
}

// TestAllowedSigners_OpenSSHMatchSemantics pins WRIT-302: matchPattern and
// matchNamespace must implement OpenSSH match.c's match_pattern /
// match_pattern_list exactly -- not Go's path.Match plus an exact-or-"*"
// namespace check. Each subtest parses its own single-line TrustStore, the
// same shape TestAllowedSigners_CaseSensitiveMatching already uses, so one
// rule's result can't be masked by another.
//
// Every discriminating case needs a wildcard *and* the other metacharacter:
// the pre-fix matchPattern only reached path.Match when the pattern
// contained '*' or '?', and short-circuited on exact string equality
// first, so a pattern with a lone '[' or '\' and no wildcard was already
// compared literally and already agreed with OpenSSH.
func TestAllowedSigners_OpenSSHMatchSemantics(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	newKey := func(t *testing.T) (ssh.PublicKey, string) {
		t.Helper()
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return sshPub, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	}

	// 1. '*' crosses '/': path.Match's '*' will not cross a path
	// separator; OpenSSH's match_pattern has no notion of separators at
	// all, so '*' matches any byte sequence, slashes included.
	t.Run("wildcard crosses slash", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`*@example.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice/laptop@example.test", "git", now) {
			t.Error("*@example.test should match alice/laptop@example.test: '*' matches any byte sequence, including '/'")
		}
	})

	// 2. A bracket class is over-permissive under path.Match, which
	// parses "[cd]" as a character class; OpenSSH has no character
	// classes, so '[' and ']' are literal bytes that must appear
	// literally in the value.
	t.Run("bracket class is literal, not a character class", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`ali[cd]e@*.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error("ali[cd]e@*.test should not match alice@example.test: '[' and ']' are literal, not a character class")
		}
		if !ts.IsAuthorized(sshPub, "ali[cd]e@example.test", "git", now) {
			t.Error("ali[cd]e@*.test should match a value containing the literal bracket text")
		}
	})

	// 3. A backslash escape is over-permissive under path.Match, which
	// treats '\' as an escape character; OpenSSH's match_pattern has no
	// escaping, so '\' is a literal byte.
	t.Run("backslash is literal, not an escape", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alice\@*.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error(`alice\@*.test should not match alice@example.test: '\' is literal, not an escape`)
		}
		if !ts.IsAuthorized(sshPub, `alice\@example.test`, "git", now) {
			t.Error(`alice\@*.test should match a value containing the literal backslash`)
		}
	})

	// 4. An unbalanced '[' is under-permissive under path.Match, which
	// returns ErrBadPattern for it -- silently treated as no-match by the
	// pre-fix code. OpenSSH has no character classes to unbalance, so
	// this is simply a literal '[' compared byte-for-byte.
	t.Run("unbalanced bracket is not a parse error", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`ali[ce*@example.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "ali[cex@example.test", "git", now) {
			t.Error("ali[ce*@example.test should match ali[cex@example.test: an unbalanced '[' is a literal byte, not a parse error")
		}
	})

	// 5. namespaces= gains globbing: the pre-fix matchNamespace knew only
	// exact equality and a bare "*", never a partial glob.
	t.Run("namespaces= globs like a principal pattern", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alice@example.test namespaces="g*" ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error(`namespaces="g*" should match namespace "git"`)
		}
		if ts.IsAuthorized(sshPub, "alice@example.test", "ssh", now) {
			t.Error(`namespaces="g*" should not match namespace "ssh"`)
		}
	})

	// 6. namespaces= gains negation: the pre-fix matchNamespace never
	// looked for a leading '!', so "!git,*" matched "git" via the
	// trailing "*" with the negation silently ignored.
	t.Run("namespaces= negation rejects even when a later subpattern matches", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alice@example.test namespaces="!git,*" ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error(`namespaces="!git,*" should reject namespace "git" outright, regardless of the trailing "*"`)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.test", "ssh", now) {
			t.Error(`namespaces="!git,*" should still match namespace "ssh" via the trailing "*"`)
		}
	})

	// 7. Byte-wise, not rune-wise: OpenSSH's match_pattern walks a
	// NUL-terminated C string byte by byte, so '?' consumes exactly one
	// byte. "é" (U+00E9) is two UTF-8 bytes (0xC3 0xA9), so it takes two
	// '?' to match, not one -- unlike path.Match, which iterates runes.
	t.Run("byte-wise matching: '?' consumes one byte, not one rune", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alic?@example.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if ts.IsAuthorized(sshPub, "alicé@example.test", "git", now) {
			t.Error(`alic?@example.test should not match alicé@example.test: 'é' is 2 bytes, '?' consumes exactly 1`)
		}
		if !ts.IsAuthorized(sshPub, "alicX@example.test", "git", now) {
			t.Error("alic?@example.test should still match a single-byte character in that position")
		}
	})

	// Empty subpattern (e.g. from a doubled comma): match_pattern_list
	// matches it only against the empty string, per match.c; the pre-fix
	// matchPrincipal instead skipped it entirely (same observable result
	// for any non-empty principal, but a different mechanism the port
	// deliberately does not carry forward).
	t.Run("empty subpattern matches only the empty string", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alice@example.test,,bob@example.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error("alice@example.test should still match despite the empty subpattern between the commas")
		}
		if !ts.IsAuthorized(sshPub, "bob@example.test", "git", now) {
			t.Error("bob@example.test should still match despite the empty subpattern between the commas")
		}
	})

	// Positive controls, guarding against a matcher that simply rejects
	// everything passing every negative assertion above vacuously.
	t.Run("positive control: wildcard suffix still matches", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`*@example.test ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error("*@example.test should still match alice@example.test")
		}
	})

	t.Run("positive control: exact namespace still matches", func(t *testing.T) {
		sshPub, pubLine := newKey(t)
		ts, err := sshsig.ParseAllowedSigners(strings.NewReader(`alice@example.test namespaces="git" ` + pubLine + "\n"))
		if err != nil {
			t.Fatalf("ParseAllowedSigners failed: %v", err)
		}
		if !ts.IsAuthorized(sshPub, "alice@example.test", "git", now) {
			t.Error(`namespaces="git" should still match namespace "git"`)
		}
	})
}
