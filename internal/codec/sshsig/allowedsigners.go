package sshsig

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SignerRule represents a single allowed signer record from an allowed_signers file.
type SignerRule struct {
	Principals    []string
	Namespaces    []string
	ValidAfter    time.Time
	ValidBefore   time.Time
	PublicKey     ssh.PublicKey
	CertAuthority bool
}

// TrustStore stores allowed signer rules for signature verification.
type TrustStore struct {
	rules []SignerRule
}

// NewTrustStore creates an empty TrustStore.
func NewTrustStore() *TrustStore {
	return &TrustStore{}
}

// ParseAllowedSigners reads an allowed_signers file from r and constructs a TrustStore.
func ParseAllowedSigners(r io.Reader) (*TrustStore, error) {
	ts := &TrustStore{}
	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		rule, err := parseAllowedSignersLine(line)
		if err != nil {
			return nil, fmt.Errorf("sshsig: allowed_signers line %d: %w", lineNum, err)
		}
		if rule != nil {
			ts.rules = append(ts.rules, *rule)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sshsig: read allowed_signers: %w", err)
	}

	return ts, nil
}

// ParseAllowedSignersFile reads an allowed_signers file from path.
func ParseAllowedSignersFile(filePath string) (*TrustStore, error) {
	if filePath == "" {
		return nil, errors.New("sshsig: empty allowed_signers file path")
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("sshsig: open allowed_signers %s: %w", filePath, err)
	}
	defer f.Close()

	return ParseAllowedSigners(f)
}

func parseAllowedSignersLine(line string) (*SignerRule, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return nil, errors.New("insufficient fields")
	}

	principalsField := fields[0]
	principals := splitPatternList(principalsField)

	var optionsStr string
	var keyType string
	var keyBase64 string

	// Determine whether fields[1] is options or key type.
	// Options contain '=' or equal "cert-authority".
	if strings.Contains(fields[1], "=") || fields[1] == "cert-authority" {
		optionsStr = fields[1]
		if len(fields) < 4 {
			return nil, errors.New("missing public key after options")
		}
		keyType = fields[2]
		keyBase64 = fields[3]
	} else {
		if len(fields) < 3 {
			return nil, errors.New("missing public key")
		}
		keyType = fields[1]
		keyBase64 = fields[2]
	}

	rule := &SignerRule{
		Principals: principals,
	}

	if optionsStr != "" {
		opts := splitOptions(optionsStr)
		for _, opt := range opts {
			opt = strings.TrimSpace(opt)
			if opt == "cert-authority" {
				rule.CertAuthority = true
			} else if strings.HasPrefix(opt, "namespaces=") {
				val := strings.Trim(strings.TrimPrefix(opt, "namespaces="), `"`)
				rule.Namespaces = splitPatternList(val)
			} else if strings.HasPrefix(opt, "valid-after=") {
				val := strings.Trim(strings.TrimPrefix(opt, "valid-after="), `"`)
				t, err := parseTimeOpt(val)
				if err != nil {
					return nil, fmt.Errorf("invalid valid-after %q: %w", val, err)
				}
				rule.ValidAfter = t
			} else if strings.HasPrefix(opt, "valid-before=") {
				val := strings.Trim(strings.TrimPrefix(opt, "valid-before="), `"`)
				t, err := parseTimeOpt(val)
				if err != nil {
					return nil, fmt.Errorf("invalid valid-before %q: %w", val, err)
				}
				rule.ValidBefore = t
			}
		}
	}

	// cert-authority is not supported in v1 and is skipped as untrusted per spec
	if rule.CertAuthority {
		return nil, nil
	}

	pubKeyBytes := []byte(keyType + " " + keyBase64)
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(pubKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rule.PublicKey = pubKey

	return rule, nil
}

// splitPatternList splits a comma-separated subpattern list (an
// allowed_signers Principals or namespaces= field) the way match.c's
// match_pattern_list walks it, not the way strings.Split does on its
// own. match_pattern_list's loop condition is `i < len(pattern)`: each
// subpattern is delimited by a comma or the end of the string, and a
// comma found is then skipped before the loop re-checks that condition.
// A leading or interior empty subpattern (from ",x" or "x,,y") is real
// and gets matched, because the loop still has bytes left after
// skipping that comma. But when the list ends in a comma, skipping it
// lands the index exactly at len(pattern), the loop condition is now
// false, and no further subpattern -- empty or otherwise -- is ever
// started. strings.Split has no such stopping condition: it always
// synthesizes one trailing "" for a trailing separator. Left alone,
// that extra element is a subpattern match_pattern_list never evaluates,
// and for a Principals field it lets an allowed_signers line ending in
// a comma authorize an op whose author.email is empty.
func splitPatternList(s string) []string {
	parts := strings.Split(s, ",")
	if len(parts) > 1 && strings.HasSuffix(s, ",") {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func splitOptions(s string) []string {
	var opts []string
	var cur strings.Builder
	inQuote := false

	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case ',':
			if inQuote {
				cur.WriteRune(r)
			} else {
				opts = append(opts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		opts = append(opts, cur.String())
	}
	return opts
}

func parseTimeOpt(val string) (time.Time, error) {
	val = strings.TrimSuffix(val, "Z")
	val = strings.ReplaceAll(val, "T", "")

	switch len(val) {
	case 8: // YYYYMMDD
		return time.ParseInLocation("20060102", val, time.UTC)
	case 14: // YYYYMMDDHHMMSS
		return time.ParseInLocation("20060102150405", val, time.UTC)
	default:
		return time.Time{}, fmt.Errorf("unsupported date/time format: %s", val)
	}
}

// AddRule adds a SignerRule to the trust store.
func (ts *TrustStore) AddRule(rule SignerRule) {
	if ts != nil && !rule.CertAuthority && rule.PublicKey != nil {
		ts.rules = append(ts.rules, rule)
	}
}

// IsAuthorized returns true if pubKey is authorized for principal under namespace at time when.
func (ts *TrustStore) IsAuthorized(pubKey ssh.PublicKey, principal, namespace string, when time.Time) bool {
	if ts == nil || pubKey == nil || len(ts.rules) == 0 {
		return false
	}

	targetKeyBytes := pubKey.Marshal()

	for _, rule := range ts.rules {
		if rule.CertAuthority || rule.PublicKey == nil {
			continue
		}

		if !bytes.Equal(rule.PublicKey.Marshal(), targetKeyBytes) {
			continue
		}

		// Check principal match
		if !matchPrincipal(rule.Principals, principal) {
			continue
		}

		// Check namespace match
		if len(rule.Namespaces) > 0 && !matchNamespace(rule.Namespaces, namespace) {
			continue
		}

		// Check time validity
		if !rule.ValidAfter.IsZero() && when.Before(rule.ValidAfter) {
			continue
		}
		if !rule.ValidBefore.IsZero() && when.After(rule.ValidBefore) {
			continue
		}

		return true
	}

	return false
}

// matchPrincipal reports whether principal is authorized by rule's
// Principals list, per matchPatternList's tri-state: a negated subpattern
// rejects the principal outright regardless of any positive subpattern
// elsewhere in the list (spec/signing.md "Pattern Matching").
func matchPrincipal(patterns []string, principal string) bool {
	return matchPatternList(principal, patterns) == 1
}

// Principal and namespace matching follow OpenSSH sshsig.c's
// check_allowed_keys_line, which runs match_pattern_list(x, list, 0) over
// both the principal and the namespaces= option -- the trailing 0 is
// match.c's dolower, so neither comparison folds case, and both use the
// same glob-and-negation semantics matchPatternList implements (see
// spec/signing.md "Pattern Matching"). Do not "fix" this back to
// case-insensitive or path.Match-style globbing: an allowed_signers file
// is OpenSSH's format, not writ's, and writ does not get to normalize it
// the way engine/internal/person deliberately case-folds writ's own
// person-refs.
func matchNamespace(allowed []string, ns string) bool {
	return matchPatternList(ns, allowed) == 1
}

// matchPatternList runs OpenSSH match.c's match_pattern_list over s: a
// list of comma-separated matchPattern subpatterns (already split by the
// allowed_signers line parser), each optionally prefixed with '!' to
// negate. It returns -1 the moment s matches a negated subpattern (a
// negative match wins immediately, without considering the rest of the
// list, exactly as match_pattern_list does), 1 if s matched at least one
// non-negated subpattern and no negated one matched first, or 0
// otherwise. Both matchPrincipal and matchNamespace require == 1, the
// same test sshsig.c's check_allowed_keys_line applies to both lists.
//
// OpenSSH's match_pattern_list copies each subpattern into a 1024-byte
// buffer and aborts the entire list -- discarding even an
// already-recorded match from an earlier subpattern -- once a subpattern
// reaches 1023 bytes. That is a match.c buffer-size artifact, not a
// matching semantic, and this port deliberately does not replicate it:
// every subpattern is matched in full regardless of length, and one
// oversized subpattern never voids the rest of the list (spec/signing.md
// "Pattern Matching" states this divergence normatively; conforming
// verifiers MUST NOT impose a subpattern length limit). An empty
// subpattern (from a doubled comma, or an empty Principals/Namespaces
// element) is not special-cased: it falls out of matchPattern's own base
// case, which matches only the empty string, exactly as
// match_pattern_list's C loop does for a zero-length subpattern.
func matchPatternList(s string, patterns []string) int {
	ret := 0
	for _, pat := range patterns {
		negated := false
		if strings.HasPrefix(pat, "!") {
			negated = true
			pat = pat[1:]
		}
		if matchPattern(s, pat) {
			if negated {
				return -1
			}
			ret = 1
		}
	}
	return ret
}

// matchPattern reports whether s matches pattern, matching the semantics
// of the NFA match.c has used for match_pattern since OpenSSH rev 1.46
// (2026-05-31) -- the algorithm real `ssh-keygen -Y verify` runs today:
// '*' matches any run of bytes including none, '?' matches exactly one
// byte, and every other byte -- '[', ']', and '\' included -- compares
// literally (match.c knows only '*' and '?'; it never enters a character
// class or an escape). Matching is byte-wise, not rune-wise, mirroring a
// NUL-terminated C string: '?' consumes one byte of a multi-byte UTF-8
// sequence, not one rune, and a literal byte in the pattern must match
// the corresponding byte of s.
//
// A run of two or more consecutive '*' is not special-cased; it falls
// out of trying the star's zero-byte expansion -- matching the rest of
// the pattern against s unchanged, including when s is already "" --
// before requiring s to be non-empty to try consuming a byte. That
// ordering matters: the recursive matcher match.c shipped before rev
// 1.46 only tried a star's expansion while s was still non-empty, so a
// residual pattern tail of two or more '*' against an already-exhausted
// s wrongly failed to match there. This is the one class where the two
// algorithms diverge (see spec/signing.md "Pattern Matching"); matching
// s == "" here is what closes it.
func matchPattern(s, pattern string) bool {
	for {
		if pattern == "" {
			return s == ""
		}

		if pattern[0] == '*' {
			pattern = pattern[1:]
			if pattern == "" {
				return true
			}

			// Not a semantic requirement (the loop below already tries
			// every position), only match.c's own optimization: when the
			// next pattern byte is a fixed literal, skip s ahead to its
			// first occurrence before recursing at each remaining
			// position.
			if pattern[0] != '?' && pattern[0] != '*' {
				for s != "" && s[0] != pattern[0] {
					s = s[1:]
				}
			}

			// Try the star consuming zero bytes first -- matching
			// pattern against s as-is, which includes s == "" and is
			// required for a residual tail that can itself match empty
			// (i.e. one or more further '*') -- then one byte, two
			// bytes, and so on until s itself is exhausted.
			for {
				if matchPattern(s, pattern) {
					return true
				}
				if s == "" {
					return false
				}
				s = s[1:]
			}
		}

		if s == "" {
			return false
		}
		if pattern[0] != '?' && pattern[0] != s[0] {
			return false
		}

		s = s[1:]
		pattern = pattern[1:]
	}
}
