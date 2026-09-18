package sshsig

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
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
	principals := strings.Split(principalsField, ",")

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
				rule.Namespaces = strings.Split(val, ",")
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

func matchPrincipal(patterns []string, principal string) bool {
	principal = strings.TrimSpace(principal)
	matched := false

	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}

		negated := false
		if strings.HasPrefix(pat, "!") {
			negated = true
			pat = strings.TrimPrefix(pat, "!")
		}

		m := matchPattern(pat, principal)
		if m {
			if negated {
				return false
			}
			matched = true
		}
	}

	return matched
}

// Principal and namespace matching is deliberately case-sensitive, mirroring
// OpenSSH sshsig.c's check_allowed_keys_line, which calls
// match_pattern_list(x, list, 0) for both the principal and the namespaces=
// option -- the trailing 0 is match.c's dolower, so neither comparison folds
// case. Do not "fix" this back to case-insensitive: an allowed_signers file
// is OpenSSH's format, not writ's, and writ does not get to normalize it the
// way engine/internal/person deliberately case-folds writ's own person-refs.
func matchNamespace(allowed []string, ns string) bool {
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "*" || a == ns {
			return true
		}
	}
	return false
}

func matchPattern(pat, val string) bool {
	if pat == "*" {
		return true
	}
	if pat == val {
		return true
	}
	if strings.ContainsAny(pat, "*?") {
		matched, err := path.Match(pat, val)
		if err == nil && matched {
			return true
		}
	}
	return false
}
