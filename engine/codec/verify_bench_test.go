package codec_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/identity"
)

// BenchmarkVerify_RealSignature isolates codec.Verify's real per-op cost
// (WRIT-251 round 2 perf finding) away from decode/disk-I/O noise: one
// real ed25519-signed commit, verified repeatedly against a matching
// trust store. Round-1's Objects.Get paid this cost once per op in the
// WHOLE repo on every call; the fix pays it only for the requested
// object's own ops.
func BenchmarkVerify_RealSignature(b *testing.B) {
	tmp := b.TempDir()
	privPath := filepath.Join(tmp, "id_ed25519")
	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		b.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}
	pubBytes, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		b.Fatal(err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	signer, err := codec.NewSigner(identity.SigningKey{Format: "ssh", Value: privPath})
	if err != nil {
		b.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	author := codec.Identity{Name: "Alice Example", Email: "alice@example.test", When: now}
	env := codec.Envelope{
		ObjectID: "w-01", ObjectType: "widget", OpType: "create", OpVersion: 1,
		Body: json.RawMessage(`{"title":"Initial"}`),
	}
	commit, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
	if err != nil {
		b.Fatal(err)
	}
	if err := codec.SignCommit(context.Background(), signer, commit); err != nil {
		b.Fatal(err)
	}

	ts, err := sshsig.ParseAllowedSigners(strings.NewReader("alice@example.test " + pubLine + "\n"))
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if v := codec.Verify(*commit, ts); v.Outcome != codec.OutcomeValid {
			b.Fatalf("unexpected outcome: %+v", v)
		}
	}
}
