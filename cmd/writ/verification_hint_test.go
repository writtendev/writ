package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trustHintText is the exact stderr line maybePrintTrustHint prints
// (object.go) for a non-valid verification outcome with no
// gpg.ssh.allowedSignersFile configured (WRIT-251 ruling 2).
const trustHintText = "hint: no gpg.ssh.allowedSignersFile configured; signatures cannot be verified as valid"

// createTicketObject creates one acme.ticket object via the CLI and
// returns its object id.
func createTicketObject(t *testing.T, dir string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", dir, "acme.ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created struct {
		Data struct {
			ObjectID string `json:"object_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal object create output: %v", err)
	}
	return created.Data.ObjectID
}

// TestObjectCLI_TrustHint_WrongKeyPorcelainOnly pins WRIT-251's CLI-side
// hint: initTestRepo signs with a real key but configures no
// gpg.ssh.allowedSignersFile at all, so every object's verification comes
// back wrong-key (ruling 2's unconfigured-trust-store rule). The porcelain
// forms of `object show`/`object list` must print the hint once on
// stderr; --json must never print it, since a script parsing stdout has
// no use for the hint and the outcome itself is already in the payload.
func TestObjectCLI_TrustHint_WrongKeyPorcelainOnly(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)
	objectID := createTicketObject(t, env.repoDir)

	t.Run("object show porcelain", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("object show failed with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "verification") || !strings.Contains(stdout.String(), "wrong-key") {
			t.Errorf("stdout = %q, want a verification row reporting wrong-key", stdout.String())
		}
		if !strings.Contains(stderr.String(), trustHintText) {
			t.Errorf("stderr = %q, want it to contain the trust-store hint", stderr.String())
		}
	})

	t.Run("object show --json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID, "--json"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("object show --json failed with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"verification":"wrong-key"`) {
			t.Errorf("stdout = %q, want verification:wrong-key in the JSON payload", stdout.String())
		}
		if strings.Contains(stderr.String(), trustHintText) {
			t.Errorf("stderr = %q, --json must never print the porcelain hint", stderr.String())
		}
	})

	t.Run("object list porcelain", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"object", "list", "-C", env.repoDir}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("object list failed with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "verification: wrong-key") {
			t.Errorf("stdout = %q, want a verification marker reporting wrong-key", stdout.String())
		}
		if !strings.Contains(stderr.String(), trustHintText) {
			t.Errorf("stderr = %q, want it to contain the trust-store hint", stderr.String())
		}
	})

	t.Run("object list --json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"object", "list", "-C", env.repoDir, "--json"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("object list --json failed with %d; stderr: %s", code, stderr.String())
		}
		if strings.Contains(stderr.String(), trustHintText) {
			t.Errorf("stderr = %q, --json must never print the porcelain hint", stderr.String())
		}
	})
}

// TestObjectCLI_TrustHint_AbsentWhenValid confirms the hint's other half:
// once gpg.ssh.allowedSignersFile authorizes the signing key, verification
// reports valid and the hint stops appearing at all.
func TestObjectCLI_TrustHint_AbsentWhenValid(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)
	objectID := createTicketObject(t, env.repoDir)

	keyPaths := getGitConfigAll(t, env.repoDir, "user.signingKey")
	if len(keyPaths) != 1 {
		t.Fatalf("user.signingKey = %v, want exactly one", keyPaths)
	}
	pubBytes, err := os.ReadFile(keyPaths[0] + ".pub")
	if err != nil {
		t.Fatalf("read signing public key: %v", err)
	}
	emails := getGitConfigAll(t, env.repoDir, "user.email")
	if len(emails) != 1 {
		t.Fatalf("user.email = %v, want exactly one", emails)
	}

	allowedPath := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(allowedPath, []byte(emails[0]+" "+strings.TrimSpace(string(pubBytes))+"\n"), 0o600); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}
	setGitConfig(t, env.repoDir, "gpg.ssh.allowedSignersFile", allowedPath)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object show failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "verification") || !strings.Contains(stdout.String(), "valid") {
		t.Errorf("stdout = %q, want a verification row reporting valid", stdout.String())
	}
	if strings.Contains(stderr.String(), trustHintText) {
		t.Errorf("stderr = %q, must not print the hint once verification is valid", stderr.String())
	}
}
