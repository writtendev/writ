package sshsig_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/codec/sshsig"
)

func TestSSHSIG_ParseAndVerify(t *testing.T) {
	tmp := t.TempDir()
	privPath := filepath.Join(tmp, "id_ed25519")

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	dataFile := filepath.Join(tmp, "data.txt")
	message := []byte("writ pure-Go sshsig test payload\n")
	if err := os.WriteFile(dataFile, message, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-f", privPath, "-n", "git", dataFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen -Y sign unavailable or failed: %v\n%s", err, out)
	}

	sigBytes, err := os.ReadFile(dataFile + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	armored := string(sigBytes)

	// 1. Test ParseSignature
	sig, err := sshsig.ParseSignature(armored)
	if err != nil {
		t.Fatalf("ParseSignature failed: %v", err)
	}

	if sig.Namespace != "git" {
		t.Errorf("expected namespace %q, got %q", "git", sig.Namespace)
	}
	if sig.Version != 1 {
		t.Errorf("expected version 1, got %d", sig.Version)
	}

	fp := sshsig.KeyFingerprint(sig.PublicKey)
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Errorf("expected SHA256:... fingerprint, got %q", fp)
	}

	fpFromSig := sshsig.FingerprintFromSignature(armored)
	if fpFromSig != fp {
		t.Errorf("FingerprintFromSignature %q != %q", fpFromSig, fp)
	}

	// 2. Test Verify valid message
	if err := sshsig.Verify(sig, message, "git"); err != nil {
		t.Errorf("Verify valid message failed: %v", err)
	}

	// 3. Test Verify mutated message
	mutated := []byte("writ mutated payload\n")
	if err := sshsig.Verify(sig, mutated, "git"); err == nil {
		t.Error("Verify on mutated message unexpectedly succeeded")
	}

	// 4. Test Verify wrong namespace
	if err := sshsig.Verify(sig, message, "wrong-namespace"); err == nil {
		t.Error("Verify on wrong namespace unexpectedly succeeded")
	}
}

func TestSSHSIG_Corrupted(t *testing.T) {
	cases := []struct {
		name    string
		armored string
	}{
		{
			name:    "empty",
			armored: "",
		},
		{
			name:    "missing headers",
			armored: "AAAA...",
		},
		{
			name:    "invalid base64",
			armored: "-----BEGIN SSH SIGNATURE-----\n!not-base64!\n-----END SSH SIGNATURE-----",
		},
		{
			name:    "too short binary",
			armored: "-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----",
		},
		{
			name:    "invalid magic",
			armored: "-----BEGIN SSH SIGNATURE-----\nTk9NQVRDSEFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFB\n-----END SSH SIGNATURE-----",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sshsig.ParseSignature(tc.armored)
			if err == nil {
				t.Errorf("ParseSignature(%s) expected error, got nil", tc.name)
			}
			fp := sshsig.FingerprintFromSignature(tc.armored)
			if fp != "" {
				t.Errorf("FingerprintFromSignature(%s) expected empty, got %q", tc.name, fp)
			}
		})
	}
}
