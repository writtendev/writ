package dag_test

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/sshsig"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"golang.org/x/crypto/ssh"
)

// The tests in this file pin WRIT-312: an op's identity is its signed
// payload, so two commits whose signed payloads are byte-identical are one
// op however their signatures are armored. Every test builds the second
// commit ("carrier") the way an attacker with push access would: take an
// existing signed op commit, change bytes the signature does not cover
// (armor line width, an ECDSA signature's s, or the whole header), store the
// result as a new commit, and point a ref at it.

// replayKey is one throwaway ssh signing key and the trust store that
// authorizes it for alice@example.test.
type replayKey struct {
	priv   string
	signer codec.Signer
	ts     codec.TrustStore
}

func newReplayKey(t *testing.T, keyType string) replayKey {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH")
	}
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_"+keyType)
	args := []string{"-t", keyType, "-N", "", "-f", priv}
	if keyType == "ecdsa" {
		args = append(args, "-b", "256")
	}
	if out, err := exec.Command("ssh-keygen", args...).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowed, []byte("alice@example.test "+strings.TrimSpace(string(pub))+"\n"), 0o600); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}
	ts, err := sshsig.ParseAllowedSignersFile(allowed)
	if err != nil {
		t.Fatalf("ParseAllowedSignersFile: %v", err)
	}
	signer, err := codec.NewSigner(identity.SigningKey{Format: "ssh", Value: priv})
	if err != nil {
		t.Fatalf("codec.NewSigner: %v", err)
	}
	return replayKey{priv: priv, signer: signer, ts: ts}
}

// replayRef is the namespace Mallory writes her carriers under: a writer id
// that is not Alice's, which nothing in the ref layout binds to an author.
const replayRef = "refs/writ/fedcba9876543210/widget"

// signedChain appends n ops to Alice's widget/w-1 chain and returns their ids.
func signedChain(t *testing.T, dir string, key replayKey, n int) (*dag.Store, []string) {
	t.Helper()
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies(), dag.WithSigner(key.signer))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var ids []string
	for i := 0; i < n; i++ {
		op := "update"
		body := `{"title":"v2"}`
		if i == 0 {
			op, body = "create", `{"title":"v1"}`
		}
		got, err := store.Append(context.Background(), codec.Envelope{
			ObjectID: "w-1", ObjectType: "widget", OpType: op, OpVersion: 1,
			Body: json.RawMessage(body),
		}, nil)
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		ids = append(ids, got.ID)
	}
	return store, ids
}

// carrierOf stores a copy of commit h whose gpgsig header is sig ("" drops
// the header) and returns the new commit's SHA. Tree, parents, author,
// committer and message are untouched, so the signed payload is unchanged.
func carrierOf(t *testing.T, repo *git.Repository, h string, sig string) plumbing.Hash {
	t.Helper()
	c, err := repo.CommitObject(plumbing.NewHash(h))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", h, err)
	}
	clone := *c
	clone.PGPSignature = sig
	obj := repo.Storer.NewEncodedObject()
	if err := clone.Encode(obj); err != nil {
		t.Fatalf("Encode carrier: %v", err)
	}
	out, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store carrier: %v", err)
	}
	return out
}

func setRef(t *testing.T, repo *git.Repository, name string, h plumbing.Hash) {
	t.Helper()
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), h)); err != nil {
		t.Fatalf("set ref %s: %v", name, err)
	}
}

func commitSignature(t *testing.T, repo *git.Repository, h string) string {
	t.Helper()
	c, err := repo.CommitObject(plumbing.NewHash(h))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", h, err)
	}
	return c.PGPSignature
}

// rewrapArmor re-wraps an armored SSHSIG's base64 body at width columns:
// the same signature bytes, different commit bytes.
func rewrapArmor(t *testing.T, armored string, width int) string {
	t.Helper()
	var body strings.Builder
	for _, line := range strings.Split(armored, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == sshsig.ArmorHeader || line == sshsig.ArmorFooter {
			continue
		}
		body.WriteString(line)
	}
	b64 := body.String()
	var out strings.Builder
	out.WriteString(sshsig.ArmorHeader + "\n")
	for len(b64) > 0 {
		n := width
		if n > len(b64) {
			n = len(b64)
		}
		out.WriteString(b64[:n] + "\n")
		b64 = b64[n:]
	}
	out.WriteString(sshsig.ArmorFooter)
	return out.String()
}

// flipECDSAS returns armored with its ECDSA signature's s replaced by N-s,
// the other valid signature over the same payload.
func flipECDSAS(t *testing.T, armored string) string {
	t.Helper()
	raw, err := sshsig.Unarmor(armored)
	if err != nil {
		t.Fatalf("Unarmor: %v", err)
	}
	const preamble = len(sshsig.Magic) + 4
	var fields struct {
		PublicKey     []byte
		Namespace     string
		Reserved      string
		HashAlgorithm string
		Signature     []byte
	}
	if err := ssh.Unmarshal(raw[preamble:], &fields); err != nil {
		t.Fatalf("unmarshal sshsig fields: %v", err)
	}
	var inner ssh.Signature
	if err := ssh.Unmarshal(fields.Signature, &inner); err != nil {
		t.Fatalf("unmarshal inner signature: %v", err)
	}
	var rs struct{ R, S *big.Int }
	if err := ssh.Unmarshal(inner.Blob, &rs); err != nil {
		t.Fatalf("unmarshal ecdsa blob: %v", err)
	}
	rs.S = new(big.Int).Sub(elliptic.P256().Params().N, rs.S)
	inner.Blob = ssh.Marshal(rs)
	fields.Signature = ssh.Marshal(inner)

	out := append([]byte(sshsig.Magic), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[len(sshsig.Magic):], sshsig.Version)
	out = append(out, ssh.Marshal(fields)...)
	return sshsig.ArmorHeader + "\n" + base64.StdEncoding.EncodeToString(out) + "\n" + sshsig.ArmorFooter
}

// opsByID indexes res.Ops["w-1"] by op id.
func opsByID(ops []codec.Op) map[string]codec.Op {
	m := make(map[string]codec.Op, len(ops))
	for _, op := range ops {
		m[op.ID] = op
	}
	return m
}

func minSHA(shas ...string) string {
	sorted := append([]string(nil), shas...)
	sort.Strings(sorted)
	return sorted[0]
}

// requireOpWithCarriers fails unless w-1 holds exactly want's ops, that op's id is
// the lowest-sorting of its carriers, and it reports every carrier.
func requireOpWithCarriers(t *testing.T, res *dag.EnumerateResult, want []string, carriers ...string) codec.Op {
	t.Helper()
	ops := res.Ops["w-1"]
	if len(ops) != len(want) {
		t.Fatalf("w-1 has %d ops, want %d: %+v", len(ops), len(want), ops)
	}
	byID := opsByID(ops)
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Fatalf("op %s missing from %v", id, ops)
		}
	}
	if len(carriers) == 0 {
		return ops[len(ops)-1]
	}
	id := minSHA(carriers...)
	op, ok := byID[id]
	if !ok {
		t.Fatalf("op id is not the lowest-sorting carrier %s: ops %v", id, ops)
	}
	got := res.Carriers[id]
	sorted := append([]string(nil), carriers...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("Carriers[%s] = %v, want %v", id, got, sorted)
	}
	return op
}

func TestEnumerate_ArmorRewrapIsOneOp(t *testing.T) {
	key := newReplayKey(t, "ed25519")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, key, 2)

	orig := ids[1]
	rewrapped := carrierOf(t, repo, orig, rewrapArmor(t, commitSignature(t, repo, orig), 40))
	if rewrapped.String() == orig {
		t.Fatal("rewrapping the armor did not change the commit")
	}
	setRef(t, repo, replayRef, rewrapped)

	res, err := store.Enumerate(dag.WithLiveTrustStore(key.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(res.Rejections) != 0 {
		t.Fatalf("Rejections = %v, want none", res.Rejections)
	}
	survivor := minSHA(orig, rewrapped.String())
	// ids[0] is the create op; the replayed update is one op, not two.
	op := requireOpWithCarriers(t, res, []string{ids[0], survivor}, orig, rewrapped.String())
	if op.Verification.Outcome != codec.OutcomeValid {
		t.Errorf("verification = %q, want valid", op.Verification.Outcome)
	}
	if len(op.Parents) != 1 || op.Parents[0] != ids[0] {
		t.Errorf("parents = %v, want [%s]", op.Parents, ids[0])
	}
	if res.PayloadIDs[survivor] == "" || res.PayloadIDs[survivor] == res.PayloadIDs[ids[0]] {
		t.Errorf("PayloadIDs = %v, want a distinct id per op", res.PayloadIDs)
	}
}

func TestEnumerate_ECDSAMalleabilityIsOneOp(t *testing.T) {
	key := newReplayKey(t, "ecdsa")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, key, 2)

	orig := ids[1]
	flipped := carrierOf(t, repo, orig, flipECDSAS(t, commitSignature(t, repo, orig)))
	setRef(t, repo, replayRef, flipped)

	// The flipped signature must still verify on its own, or this test would
	// only be showing that a bad signature is ignored.
	only, err := store.Enumerate(dag.WithLiveTrustStore(key.ts), dag.VerifyOnly(func(codec.Op) bool { return true }))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if got := only.Ops["w-1"][len(only.Ops["w-1"])-1].Verification.Outcome; got != codec.OutcomeValid {
		t.Fatalf("verification = %q, want valid", got)
	}

	res, err := store.Enumerate(dag.WithLiveTrustStore(key.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	survivor := minSHA(orig, flipped.String())
	op := requireOpWithCarriers(t, res, []string{ids[0], survivor}, orig, flipped.String())
	if op.Verification.Outcome != codec.OutcomeValid {
		t.Errorf("verification = %q, want valid", op.Verification.Outcome)
	}
}

// TestEnumerate_DedupeIsNotADowngrade is the attack dedupe itself opens: if
// the surviving carrier's own signature decided verification, grinding an
// unsigned or garbage-signed carrier at a lower SHA would flip Alice's valid
// op to unsigned. Verification is the best outcome across carriers instead.
func TestEnumerate_DedupeIsNotADowngrade(t *testing.T) {
	key := newReplayKey(t, "ed25519")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, key, 2)
	orig := ids[1]

	// An unsigned carrier, and a garbage-signature carrier ground until it
	// sorts below the original. The unsigned one is a single fixed commit;
	// whichever side of the original it lands on, the outcome must not move.
	unsigned := carrierOf(t, repo, orig, "")
	var garbage plumbing.Hash
	for i := 0; ; i++ {
		if i > 64 {
			t.Fatal("could not grind a garbage-signature carrier below the original")
		}
		sig := sshsig.ArmorHeader + "\n" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 10+i))) + "\n" + sshsig.ArmorFooter
		garbage = carrierOf(t, repo, orig, sig)
		if garbage.String() < orig {
			break
		}
	}
	setRef(t, repo, "refs/writ/fedcba9876543210/unsigned", unsigned)
	setRef(t, repo, "refs/writ/fedcba9876543210/garbage", garbage)

	res, err := store.Enumerate(dag.WithLiveTrustStore(key.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	all := []string{orig, unsigned.String(), garbage.String()}
	survivor := minSHA(all...)
	op := requireOpWithCarriers(t, res, []string{ids[0], survivor}, all...)
	if survivor == orig {
		t.Fatalf("survivor %s is the original; the attack needs a lower carrier", survivor)
	}
	if op.Verification.Outcome != codec.OutcomeValid {
		t.Errorf("verification = %q, want valid (the lowest-SHA carrier is %s, not the signed one)", op.Verification.Outcome, survivor)
	}

	// Alone, the same carriers verify as what they are: the dedupe takes the
	// best of them, it does not launder either.
	for _, c := range []struct {
		name string
		sha  plumbing.Hash
		want codec.VerificationOutcome
	}{
		{"unsigned", unsigned, codec.OutcomeUnsigned},
		{"garbage", garbage, codec.OutcomeCorruptedSignature},
	} {
		pure := pureCommit(t, repo, c.sha)
		if got := codec.Verify(pure, key.ts).Outcome; got != c.want {
			t.Errorf("%s carrier verifies as %q, want %q", c.name, got, c.want)
		}
	}
}

// TestEnumerate_ForeignKeyCarrierDoesNotWin: Mallory re-signs Alice's exact
// payload with her own key. That carrier is wrong-key on its own; as a
// carrier of Alice's op it must not displace Alice's valid one.
func TestEnumerate_ForeignKeyCarrierDoesNotWin(t *testing.T) {
	alice := newReplayKey(t, "ed25519")
	mallory := newReplayKey(t, "ed25519")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, alice, 2)
	orig := ids[1]

	c, err := repo.CommitObject(plumbing.NewHash(orig))
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	payload := encodeWithoutSignature(t, repo, c)
	sig, err := mallory.signer.Sign(context.Background(), payload)
	if err != nil {
		t.Fatalf("mallory sign: %v", err)
	}
	resigned := carrierOf(t, repo, orig, sig)
	setRef(t, repo, replayRef, resigned)

	res, err := store.Enumerate(dag.WithLiveTrustStore(alice.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	survivor := minSHA(orig, resigned.String())
	op := requireOpWithCarriers(t, res, []string{ids[0], survivor}, orig, resigned.String())
	if op.Verification.Outcome != codec.OutcomeValid {
		t.Errorf("verification = %q, want valid", op.Verification.Outcome)
	}

	// Only Mallory's carrier on the table: it is a different, wrong-key
	// verification, reported as such, not Alice's.
	if got := codec.Verify(pureCommit(t, repo, resigned), alice.ts).Outcome; got != codec.OutcomeWrongKey {
		t.Errorf("mallory carrier verifies as %q, want wrong-key", got)
	}
}

// TestEnumerate_ChildOfDroppedCarrierIsRewritten: a commit whose parent line
// names the carrier that did not become the op id reads as naming the op id.
func TestEnumerate_ChildOfDroppedCarrierIsRewritten(t *testing.T) {
	key := newReplayKey(t, "ed25519")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, key, 2)
	orig := ids[1]

	rewrapped := carrierOf(t, repo, orig, rewrapArmor(t, commitSignature(t, repo, orig), 40))
	setRef(t, repo, replayRef, rewrapped)

	survivor := minSHA(orig, rewrapped.String())
	dropped := orig
	if dropped == survivor {
		dropped = rewrapped.String()
	}

	bob := testIdentity("1111111111111111", "Bob", "bob@example.test")
	child, err := writeRawOpCommit(context.Background(), repo.Storer, codec.Envelope{
		ObjectID: "w-1", ObjectType: "widget", OpType: "update", OpVersion: 1,
		Body: json.RawMessage(`{"title":"bob"}`),
	}, codec.Identity{Name: bob.Author.Name, Email: bob.Author.Email, When: testTime(3)}, dropped, nil)
	if err != nil {
		t.Fatalf("write child: %v", err)
	}
	setRef(t, repo, "refs/writ/1111111111111111/widget", plumbing.NewHash(child))

	res, err := store.Enumerate(dag.WithLiveTrustStore(key.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	ops := opsByID(res.Ops["w-1"])
	got, ok := ops[child]
	if !ok {
		t.Fatalf("child %s not enumerated: %v", child, res.Ops["w-1"])
	}
	if len(got.Parents) != 1 || got.Parents[0] != survivor {
		t.Errorf("child parents = %v, want [%s] (the op id, not the dropped carrier %s)", got.Parents, survivor, dropped)
	}
	if _, ok := ops[dropped]; ok {
		t.Errorf("dropped carrier %s is still an op", dropped)
	}
	if len(ops) != 3 {
		t.Errorf("w-1 has %d ops, want 3 (create, the replayed update once, bob's child)", len(ops))
	}
}

// TestEnumerate_VerifyOnlyAgreesWithFullVerify: scoped verification, including
// the backfill for an op decoded before the matching op that confirms its
// object, reaches the same per-op outcomes as verifying everything.
func TestEnumerate_VerifyOnlyAgreesWithFullVerify(t *testing.T) {
	key := newReplayKey(t, "ed25519")
	dir, repo := initTestRepo(t)
	store, ids := signedChain(t, dir, key, 3)
	orig := ids[2]

	setRef(t, repo, replayRef, carrierOf(t, repo, orig, rewrapArmor(t, commitSignature(t, repo, orig), 40)))
	setRef(t, repo, "refs/writ/fedcba9876543210/unsigned", carrierOf(t, repo, orig, ""))

	full, err := store.Enumerate(dag.WithLiveTrustStore(key.ts))
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	for name, match := range map[string]func(codec.Op) bool{
		"by object":      func(op codec.Op) bool { return op.ObjectID == "w-1" },
		"oldest op only": func(op codec.Op) bool { return op.OpType == "create" },
		"nothing":        func(codec.Op) bool { return false },
	} {
		scoped, err := store.Enumerate(dag.WithLiveTrustStore(key.ts), dag.VerifyOnly(match))
		if err != nil {
			t.Fatalf("%s: Enumerate: %v", name, err)
		}
		if len(scoped.Ops["w-1"]) != len(full.Ops["w-1"]) {
			t.Fatalf("%s: %d ops, want %d", name, len(scoped.Ops["w-1"]), len(full.Ops["w-1"]))
		}
		fullByID := opsByID(full.Ops["w-1"])
		for _, op := range scoped.Ops["w-1"] {
			want := fullByID[op.ID]
			if name == "nothing" {
				if op.Verification.Outcome != "" {
					t.Errorf("%s: op %s verified (%q) though nothing matched", name, op.ID, op.Verification.Outcome)
				}
				continue
			}
			if op.Verification.Outcome != want.Verification.Outcome || op.Verification.KeyFingerprint != want.Verification.KeyFingerprint {
				t.Errorf("%s: op %s verification = %+v, want %+v", name, op.ID, op.Verification, want.Verification)
			}
			if op.Signature != want.Signature {
				t.Errorf("%s: op %s signature differs from full verify", name, op.ID)
			}
		}
	}
}

func pureCommit(t *testing.T, repo *git.Repository, h plumbing.Hash) codec.Commit {
	t.Helper()
	c, err := repo.CommitObject(h)
	if err != nil {
		t.Fatalf("CommitObject %s: %v", h, err)
	}
	pure, err := codec.FromGitCommit(repo.Storer, c)
	if err != nil {
		t.Fatalf("FromGitCommit: %v", err)
	}
	return pure
}

func testTime(n int) time.Time {
	return time.Date(2026, 1, 1, 0, n, 0, 0, time.UTC)
}

func encodeWithoutSignature(t *testing.T, repo *git.Repository, c interface {
	EncodeWithoutSignature(plumbing.EncodedObject) error
}) []byte {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	if err := c.EncodeWithoutSignature(obj); err != nil {
		t.Fatalf("EncodeWithoutSignature: %v", err)
	}
	r, err := obj.Reader()
	if err != nil {
		t.Fatalf("payload reader: %v", err)
	}
	defer r.Close()
	payload, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return payload
}
