package identity_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	// dag is the consumer that parses a writer id out of a ref path rather
	// than out of git config, and the reason the config remedy is attached
	// where the key is read. Importing it here is legal — this is an external
	// test package, so dag importing identity is not a cycle — and it makes
	// the layering assertion a real call into the real consumer.
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping test")
	}
}

type testEnv struct {
	repoDir       string
	globalCfgPath string
}

func setupTestEnv(t *testing.T) testEnv {
	t.Helper()
	requireGit(t)

	tempDir := t.TempDir()
	globalCfgPath := filepath.Join(tempDir, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}

	repoDir := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoDir, 0755); err != nil {
		t.Fatalf("creating repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, string(out))
	}

	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	return testEnv{
		repoDir:       repoDir,
		globalCfgPath: globalCfgPath,
	}
}

func setGitConfig(t *testing.T, dir, key, val string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, val)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s %s failed: %v (%s)", key, val, err, string(out))
	}
}

func setFileConfig(t *testing.T, filePath, key, val string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--file", filePath, key, val)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config --file %s %s %s failed: %v (%s)", filePath, key, val, err, string(out))
	}
}

func populateValidLocalConfig(t *testing.T, repoDir string) {
	t.Helper()
	setGitConfig(t, repoDir, "writ.writerId", "0123456789abcdef")
	setGitConfig(t, repoDir, "user.name", "Alice Example")
	setGitConfig(t, repoDir, "user.email", "alice@example.test")
	setGitConfig(t, repoDir, "gpg.format", "ssh")
	setGitConfig(t, repoDir, "user.signingKey", "/path/to/id_ed25519")
}

func TestLoad_ValidPathKey(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "gpg.ssh.allowedSignersFile", "/path/to/allowed_signers")

	id, err := identity.Load(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("Load unexpected error: %v", err)
	}

	if id.WriterID != "0123456789abcdef" {
		t.Errorf("id.WriterID = %q, want %q", id.WriterID, "0123456789abcdef")
	}
	if id.Author.Name != "Alice Example" {
		t.Errorf("id.Author.Name = %q, want %q", id.Author.Name, "Alice Example")
	}
	if id.Author.Email != "alice@example.test" {
		t.Errorf("id.Author.Email = %q, want %q", id.Author.Email, "alice@example.test")
	}
	if id.Key.Format != "ssh" {
		t.Errorf("id.Key.Format = %q, want %q", id.Key.Format, "ssh")
	}
	if id.Key.Value != "/path/to/id_ed25519" {
		t.Errorf("id.Key.Value = %q, want %q", id.Key.Value, "/path/to/id_ed25519")
	}
	if id.Key.Literal != false {
		t.Errorf("id.Key.Literal = %v, want false", id.Key.Literal)
	}
	if id.AllowedSigners != "/path/to/allowed_signers" {
		t.Errorf("id.AllowedSigners = %q, want %q", id.AllowedSigners, "/path/to/allowed_signers")
	}
}

func TestLoad_ValidLiteralKey(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	const literalKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyBlob"
	setGitConfig(t, env.repoDir, "user.signingKey", "key::"+literalKey)

	id, err := identity.Load(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("Load unexpected error: %v", err)
	}

	if id.Key.Literal != true {
		t.Errorf("id.Key.Literal = %v, want true", id.Key.Literal)
	}
	if id.Key.Value != literalKey {
		t.Errorf("id.Key.Value = %q, want %q", id.Key.Value, literalKey)
	}
	if id.Key.Format != "ssh" {
		t.Errorf("id.Key.Format = %q, want %q", id.Key.Format, "ssh")
	}
	if id.AllowedSigners != "" {
		t.Errorf("id.AllowedSigners = %q, want empty string when unset", id.AllowedSigners)
	}
}

// TestLoad_WhitespaceOnlyAuthor pins the guard on user.name and user.email.
// git config stores a whitespace-only value verbatim, so a raw == "" check let
// one through as a configured identity — and that identity is written into the
// commit author of every appended op and used as the principal signature
// verification matches against allowed-signers. Whitespace-only is unset.
func TestLoad_WhitespaceOnlyAuthor(t *testing.T) {
	values := []struct {
		name  string
		value string
	}{
		{"spaces", "   "},
		{"tab", "\t"},
	}

	for _, key := range []string{"user.name", "user.email"} {
		for _, v := range values {
			t.Run(key+"/"+v.name, func(t *testing.T) {
				env := setupTestEnv(t)
				populateValidLocalConfig(t, env.repoDir)
				setGitConfig(t, env.repoDir, key, v.value)

				// The value really did survive the round trip through git;
				// otherwise this test would pass for the wrong reason.
				cfg, err := identity.ReadGitConfig(context.Background(), env.repoDir)
				if err != nil {
					t.Fatalf("ReadGitConfig: %v", err)
				}
				if got := cfg[strings.ToLower(key)]; got != v.value {
					t.Fatalf("git stored %s as %q, want %q verbatim", key, got, v.value)
				}

				_, err = identity.Load(context.Background(), env.repoDir)
				if err == nil {
					t.Fatalf("Load accepted whitespace-only %s", key)
				}
				if !errors.Is(err, identity.ErrMissing) {
					t.Errorf("Load error = %v, want errors.Is ErrMissing", err)
				}
				var cfgErr *identity.ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load error is %T, want *identity.ConfigError", err)
				}
				if cfgErr.Key != key {
					t.Errorf("cfgErr.Key = %q, want %q", cfgErr.Key, key)
				}
			})
		}
	}
}

// TestLoad_TrimsAuthorPadding is the other half of the guard above: a padded
// value is configured, so Load succeeds, but the padding does not travel into
// the commit author or the verification principal.
func TestLoad_TrimsAuthorPadding(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "user.name", "  Alice Example  ")
	setGitConfig(t, env.repoDir, "user.email", "  alice@example.test\t")

	id, err := identity.Load(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("Load unexpected error: %v", err)
	}
	if id.Author.Name != "Alice Example" {
		t.Errorf("id.Author.Name = %q, want %q", id.Author.Name, "Alice Example")
	}
	if id.Author.Email != "alice@example.test" {
		t.Errorf("id.Author.Email = %q, want %q", id.Author.Email, "alice@example.test")
	}
	// The person identifier already trimmed; the author now agrees with it.
	if id.PersonID != "email:alice@example.test" {
		t.Errorf("id.PersonID = %q, want %q", id.PersonID, "email:alice@example.test")
	}
}

// TestLoad_WhitespaceOnlyWriterID pins the same guard on writ.writerId, which
// was the last key in Load still tested raw.
//
// It is a test of its own rather than a row in the sweep above, because the
// sweep asserts which key the error names and both spellings of this failure
// name writ.writerId. What changed is the problem class, and the problem class
// is what the user reads: ErrInvalid renders "invalid git config" and, by
// design, carries no general "run 'writ init'" remedy, so a whitespace-only
// value was reported as a malformed writer id — while writ init, whose
// presence test on this key has always trimmed, read the same key as absent
// and minted over it. Load and EnsureWriterID disagreeing about whether a key
// is set is the disagreement WRIT-143 is about, one file apart.
//
// Only the presence test. What EnsureWriterID parses has always been the raw
// string, and TestLoad_PaddedWriterIDStaysInvalid below is the guard on the
// other end of that: the two functions agree that whitespace is absence and
// agree that padding is not an id.
func TestLoad_WhitespaceOnlyWriterID(t *testing.T) {
	for _, value := range []string{" ", "   ", "\t"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)
			setGitConfig(t, env.repoDir, "writ.writerId", value)

			// The value really did survive the round trip through git;
			// otherwise this test would pass for the wrong reason.
			cfg, err := identity.ReadGitConfig(context.Background(), env.repoDir)
			if err != nil {
				t.Fatalf("ReadGitConfig: %v", err)
			}
			if got := cfg["writ.writerid"]; got != value {
				t.Fatalf("git stored writ.writerId as %q, want %q verbatim", got, value)
			}

			_, err = identity.Load(context.Background(), env.repoDir)
			if err == nil {
				t.Fatal("Load accepted a whitespace-only writ.writerId")
			}
			if !errors.Is(err, identity.ErrMissing) {
				t.Errorf("Load error = %v, want errors.Is ErrMissing: a set key with nothing in it is nothing configured", err)
			}
			if errors.Is(err, identity.ErrInvalid) {
				t.Errorf("Load error = %v, want ErrMissing rather than ErrInvalid: nobody typed whitespace as a writer id", err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("Load error is %T, want *identity.ConfigError", err)
			}
			if cfgErr.Key != "writ.writerId" {
				t.Errorf("cfgErr.Key = %q, want %q", cfgErr.Key, "writ.writerId")
			}
			// The remedy the ErrMissing arm appends is the one that works:
			// writ init reads this key as absent and mints into it.
			if !strings.Contains(err.Error(), "run 'writ init'") {
				t.Errorf("Load error = %q, want the writ init remedy", err.Error())
			}
		})
	}
}

// TestLoad_PaddedWriterIDStaysInvalid is the guard on the trim above. The
// emptiness check trims and the parse does not, and the gap between those two
// is a ref namespace.
//
// Trimming the value Load parses would make ` 0123456789abcdef ` a writer id
// here while EnsureWriterID — which trims only its presence test and parses
// the raw string — went on refusing it. git stores the padding verbatim, so
// the repository would keep writing ops under refs/writ/0123456789abcdef/
// while writ init could never run in it again, and the remedy init printed
// would mint a second id: one device, two namespaces. Which strings name a
// writer namespace is one decision, and both functions have to make it the
// same way.
func TestLoad_PaddedWriterIDStaysInvalid(t *testing.T) {
	for _, value := range []string{" 0123456789abcdef ", "0123456789abcdef\n", "\t0123456789abcdef"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)
			setGitConfig(t, env.repoDir, "writ.writerId", value)

			// git keeps the padding, which is the whole reason this matters.
			cfg, err := identity.ReadGitConfig(context.Background(), env.repoDir)
			if err != nil {
				t.Fatalf("ReadGitConfig: %v", err)
			}
			if got := cfg["writ.writerid"]; got != value {
				t.Fatalf("git stored writ.writerId as %q, want %q verbatim", got, value)
			}

			ident, loadErr := identity.Load(context.Background(), env.repoDir)
			if loadErr == nil {
				t.Fatalf("Load accepted a padded writ.writerId as %q: EnsureWriterID refuses it, and the two must agree on what a writer id is", ident.WriterID)
			}
			if !errors.Is(loadErr, identity.ErrInvalid) {
				t.Errorf("Load error = %v, want errors.Is ErrInvalid: padding is a value the user typed, not an absent key", loadErr)
			}

			// The other side of the pair, in the same repository.
			_, minted, ensureErr := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
			if ensureErr == nil {
				t.Fatalf("EnsureWriterID accepted a padded writ.writerId (minted=%v)", minted)
			}
			if !errors.Is(ensureErr, identity.ErrInvalid) {
				t.Errorf("EnsureWriterID error = %v, want errors.Is ErrInvalid", ensureErr)
			}
		})
	}
}

// TestWriterIDRemedyIsAttachedWhereTheValueIsRead pins the layer the remedy
// lives at.
//
// An invalid writ.writerId needs a next step: ConfigError appends no
// "run 'writ init'" to an ErrInvalid, correctly — init never overwrites a
// value that is already there — so "invalid configuration" on its own leaves
// a reader with a broken repository and nothing to do about it. The step that
// works is to clear the key first, because EnsureWriterID mints only into an
// absent one.
//
// That advice cannot live in ParseWriterID, which has a second kind of caller.
// engine/dag runs the writer-id segment of a ref path through it, and a
// malformed segment is typically someone else's id arriving over a fetch —
// telling that reader to unset their own correct writ.writerId and re-mint
// would split their device's ops across two namespaces, which is the harm
// this whole change is about. So the parser describes the format, and the two
// functions that read the value out of git config attach the remedy.
func TestWriterIDRemedyIsAttachedWhereTheValueIsRead(t *testing.T) {
	const malformed = "nothexadecimal!"

	t.Run("the parser names the format and no remedy", func(t *testing.T) {
		_, err := identity.ParseWriterID(malformed)
		if err == nil {
			t.Fatal("ParseWriterID accepted a malformed writer id")
		}
		msg := err.Error()
		if !strings.Contains(msg, "expected 16 lowercase hex characters") {
			t.Errorf("ParseWriterID error = %q, want it to say what the value had to look like", msg)
		}
		for _, unwanted := range []string{"unset", "writ init"} {
			if strings.Contains(msg, unwanted) {
				t.Errorf("ParseWriterID error = %q, want no %q: this parser also parses ref path segments, where the writer id is someone else's and unsetting your own would split your namespace", msg, unwanted)
			}
		}
	})

	t.Run("a ref parse inherits no config remedy", func(t *testing.T) {
		// The consumer the paragraph above is about, tested through it. An
		// external test package may import a package that imports the package
		// under test, so this is the real call and not a restatement of it.
		_, err := dag.ParseChainRef("refs/remotes/origin/writ/" + malformed + "/comment")
		if err == nil {
			t.Fatal("ParseChainRef accepted a malformed writer-id segment")
		}
		for _, unwanted := range []string{"unset", "writ init"} {
			if strings.Contains(err.Error(), unwanted) {
				t.Errorf("ParseChainRef error = %q, want no %q: this ref names a remote writer, and the reader's own writ.writerId is fine", err.Error(), unwanted)
			}
		}
	})

	t.Run("the config readers attach it", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		setGitConfig(t, env.repoDir, "writ.writerId", malformed)

		_, loadErr := identity.Load(context.Background(), env.repoDir)
		if loadErr == nil {
			t.Fatal("Load accepted a malformed writ.writerId")
		}
		_, _, ensureErr := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
		if ensureErr == nil {
			t.Fatal("EnsureWriterID accepted a malformed writ.writerId")
		}
		for name, err := range map[string]error{"Load": loadErr, "EnsureWriterID": ensureErr} {
			for _, want := range []string{"unset", "writ init"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error = %q, want it to contain %q: an invalid value the user typed needs a remedy the user can apply", name, err.Error(), want)
				}
			}
		}
	})
}

// TestRepoIDInvalidNamesAWorkingRemedy is the same defect one key over, which
// the writ.writerId fix left standing: `writ.repoId = notahexrepoid` failed
// with "invalid configuration" and no next step at all. EnsureRepoID mints
// only into an absent key, so the remedy is the same one — clear it first —
// and it belongs at the same layer.
func TestRepoIDInvalidNamesAWorkingRemedy(t *testing.T) {
	const malformed = "notahexrepoid"

	_, parseErr := identity.ParseRepoID(malformed)
	if parseErr == nil {
		t.Fatal("ParseRepoID accepted a malformed repo id")
	}
	if !strings.Contains(parseErr.Error(), "expected 32 lowercase hex characters") {
		t.Errorf("ParseRepoID error = %q, want it to say what the value had to look like", parseErr.Error())
	}
	for _, unwanted := range []string{"unset", "writ init"} {
		if strings.Contains(parseErr.Error(), unwanted) {
			t.Errorf("ParseRepoID error = %q, want no %q: the remedy belongs where the key is read", parseErr.Error(), unwanted)
		}
	}

	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "writ.repoId", malformed)

	_, loadErr := identity.LoadRepoID(context.Background(), env.repoDir)
	if loadErr == nil {
		t.Fatal("LoadRepoID accepted a malformed writ.repoId")
	}
	_, _, ensureErr := identity.EnsureRepoID(context.Background(), env.repoDir)
	if ensureErr == nil {
		t.Fatal("EnsureRepoID accepted a malformed writ.repoId")
	}
	for name, err := range map[string]error{"LoadRepoID": loadErr, "EnsureRepoID": ensureErr} {
		for _, want := range []string{"unset", "writ init"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s error = %q, want it to contain %q", name, err.Error(), want)
			}
		}
	}
}

// TestLoad_EmptySigningKeyLiteralNamesAWorkingRemedy covers the third
// ErrInvalid emitter that named nothing. `user.signingKey = key::` is the
// right form with no content, and "invalid configuration" alone left a reader
// looking at a key they could see was set.
//
// It asserts the remedy text, not just the key:: substring. The substring is
// ConfigError.Value, which this error prints whether or not a Remedy is set:
// deleting the Remedy left this test green under a name that promised it read
// one. The repoId counterpart next door reads its remedy, and so does this.
//
// The Message half is writ init's. init renders identity configuration through
// ConfigError.Message and prints the git config lines to run underneath, so a
// Remedy has to be stripped there for exactly the reason initHint is — and
// that keeps init's key:: warning byte-identical to what it printed before
// this field existed.
func TestLoad_EmptySigningKeyLiteralNamesAWorkingRemedy(t *testing.T) {
	const remedy = "put the public key after key::, or set the path to a key file instead"

	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "user.signingKey", "key::")

	_, err := identity.Load(context.Background(), env.repoDir)
	if err == nil {
		t.Fatal("Load accepted user.signingKey = key:: with nothing after it")
	}
	if !strings.Contains(err.Error(), "key::") {
		t.Errorf("Load error = %q, want it to name the form that is missing its content", err.Error())
	}

	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("Load error = %v (%T), want a *identity.ConfigError", err, err)
	}
	if cfgErr.Remedy != remedy {
		t.Errorf("Remedy = %q, want %q: this arm is an ErrInvalid, which takes no initHint, so the Remedy is the only next step the error carries", cfgErr.Remedy, remedy)
	}
	if !strings.Contains(err.Error(), remedy) {
		t.Errorf("Load error = %q, want it to say which half of the form to supply", err.Error())
	}
	if strings.Contains(err.Error(), "writ init") {
		t.Errorf("Load error = %q, want no \"writ init\": init never writes user.signingKey for anyone, so naming it is advice that does not work", err.Error())
	}
	if got := cfgErr.Message(); strings.Contains(got, remedy) {
		t.Errorf("Message() = %q, want the remedy stripped: writ init prints the signing-key configuration lines itself", got)
	}
}

func TestLoad_WriterIDPrecedence(t *testing.T) {
	t.Run("local_only", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		setGitConfig(t, env.repoDir, "writ.writerId", "1111111111111111")

		id, err := identity.Load(context.Background(), env.repoDir)
		if err != nil {
			t.Fatalf("Load unexpected error: %v", err)
		}
		if id.WriterID != "1111111111111111" {
			t.Errorf("id.WriterID = %q, want \"1111111111111111\"", id.WriterID)
		}
	})

	t.Run("global_only", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		// Remove local writerId and set in global config
		cmd := exec.Command("git", "config", "--unset", "writ.writerId")
		cmd.Dir = env.repoDir
		_ = cmd.Run()

		setFileConfig(t, env.globalCfgPath, "writ.writerId", "2222222222222222")

		id, err := identity.Load(context.Background(), env.repoDir)
		if err != nil {
			t.Fatalf("Load unexpected error: %v", err)
		}
		if id.WriterID != "2222222222222222" {
			t.Errorf("id.WriterID = %q, want \"2222222222222222\"", id.WriterID)
		}
	})

	t.Run("both_differing_local_wins", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		setFileConfig(t, env.globalCfgPath, "writ.writerId", "2222222222222222")
		setGitConfig(t, env.repoDir, "writ.writerId", "3333333333333333")

		id, err := identity.Load(context.Background(), env.repoDir)
		if err != nil {
			t.Fatalf("Load unexpected error: %v", err)
		}
		if id.WriterID != "3333333333333333" {
			t.Errorf("id.WriterID = %q, want local value \"3333333333333333\"", id.WriterID)
		}
	})
}

func TestLoad_IncludePath(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)

	// Remove local writerId
	cmd := exec.Command("git", "config", "--unset", "writ.writerId")
	cmd.Dir = env.repoDir
	_ = cmd.Run()

	includedPath := filepath.Join(t.TempDir(), "included_gitconfig")
	setFileConfig(t, includedPath, "writ.writerId", "4444444444444444")
	setFileConfig(t, env.globalCfgPath, "include.path", includedPath)

	id, err := identity.Load(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("Load unexpected error with include.path: %v", err)
	}
	if id.WriterID != "4444444444444444" {
		t.Errorf("id.WriterID = %q, want included value \"4444444444444444\"", id.WriterID)
	}
}

func TestLoad_MissingConfig(t *testing.T) {
	keys := []struct {
		configKey string
		lookupKey string
	}{
		{"writ.writerId", "writ.writerId"},
		{"user.name", "user.name"},
		{"user.email", "user.email"},
		{"user.signingKey", "user.signingKey"},
	}

	for _, k := range keys {
		t.Run(k.configKey, func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)

			cmd := exec.Command("git", "config", "--unset", k.configKey)
			cmd.Dir = env.repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git config --unset %s failed: %v (%s)", k.configKey, err, string(out))
			}

			_, err := identity.Load(context.Background(), env.repoDir)
			if err == nil {
				t.Fatalf("Load succeeded despite missing %s", k.configKey)
			}
			if !errors.Is(err, identity.ErrMissing) {
				t.Errorf("Load error = %v, want errors.Is ErrMissing", err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("Load error is %T, want *identity.ConfigError", err)
			}
			if cfgErr.Key != k.lookupKey {
				t.Errorf("cfgErr.Key = %q, want %q", cfgErr.Key, k.lookupKey)
			}
			errMsg := err.Error()
			if !strings.Contains(errMsg, k.lookupKey) {
				t.Errorf("errMsg %q does not name key %q", errMsg, k.lookupKey)
			}
			if !strings.Contains(errMsg, "writ init") {
				t.Errorf("errMsg %q does not mention 'writ init'", errMsg)
			}
		})
	}
}

func TestLoad_InvalidWriterID(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"uppercase", "0123456789ABCDEF"},
		{"too_short", "0123456789abcde"},
		{"non_hex", "0123456789abcdeg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)
			setGitConfig(t, env.repoDir, "writ.writerId", tc.value)

			_, err := identity.Load(context.Background(), env.repoDir)
			if err == nil {
				t.Fatalf("Load succeeded with invalid writer-id %q", tc.value)
			}
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("Load error = %v, want errors.Is ErrInvalid", err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("Load error is %T, want *identity.ConfigError", err)
			}
			if cfgErr.Key != "writ.writerId" {
				t.Errorf("cfgErr.Key = %q, want \"writ.writerId\"", cfgErr.Key)
			}
			if cfgErr.Value != tc.value {
				t.Errorf("cfgErr.Value = %q, want %q", cfgErr.Value, tc.value)
			}
		})
	}
}

func TestLoad_InvalidSigningKey_EmptyLiteral(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "user.signingKey", "key::")

	_, err := identity.Load(context.Background(), env.repoDir)
	if err == nil {
		t.Fatal("Load succeeded with empty literal signing key 'key::'")
	}
	if !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("Load error = %v, want errors.Is ErrInvalid", err)
	}
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("Load error is %T, want *identity.ConfigError", err)
	}
	if cfgErr.Key != "user.signingKey" {
		t.Errorf("cfgErr.Key = %q, want \"user.signingKey\"", cfgErr.Key)
	}
	if cfgErr.Value != "key::" {
		t.Errorf("cfgErr.Value = %q, want \"key::\"", cfgErr.Value)
	}
}

// TestLoad_GPGFormat covers all three states of gpg.format. Unset and
// unsupported used to collapse into one error, so a repo with no signing
// configuration at all was told its configuration was unsupported — writ
// reporting something broken where the user had merely not configured
// anything, on the first screen a new user sees.
func TestLoad_GPGFormat(t *testing.T) {
	t.Run("unset is missing, not unsupported", func(t *testing.T) {
		absent := map[string]string{
			"unset":      "",
			"whitespace": "   ",
		}
		for name, value := range absent {
			t.Run(name, func(t *testing.T) {
				env := setupTestEnv(t)
				populateValidLocalConfig(t, env.repoDir)
				if value == "" {
					cmd := exec.Command("git", "config", "--unset", "gpg.format")
					cmd.Dir = env.repoDir
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git config --unset gpg.format: %v (%s)", err, out)
					}
				} else {
					setGitConfig(t, env.repoDir, "gpg.format", value)
				}

				_, err := identity.Load(context.Background(), env.repoDir)
				if err == nil {
					t.Fatal("Load succeeded with no gpg.format configured")
				}
				if !errors.Is(err, identity.ErrMissing) {
					t.Errorf("Load error = %v, want errors.Is ErrMissing", err)
				}
				if errors.Is(err, identity.ErrUnsupportedFormat) {
					t.Errorf("Load error = %v, want absence reported as absence, not as an unsupported format", err)
				}
				var cfgErr *identity.ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load error is %T, want *identity.ConfigError", err)
				}
				if cfgErr.Key != "gpg.format" {
					t.Errorf("cfgErr.Key = %q, want \"gpg.format\"", cfgErr.Key)
				}
				if cfgErr.Value != "" {
					t.Errorf("cfgErr.Value = %q, want empty: there is no value to quote back", cfgErr.Value)
				}
				if msg := err.Error(); !strings.Contains(msg, "missing") || strings.Contains(msg, "unsupported") {
					t.Errorf("message %q should read as missing and not as unsupported", msg)
				}
			})
		}
	})

	t.Run("configured but unsupported", func(t *testing.T) {
		for _, format := range []string{"openpgp", "x509", "custom-crypto"} {
			t.Run(format, func(t *testing.T) {
				env := setupTestEnv(t)
				populateValidLocalConfig(t, env.repoDir)
				setGitConfig(t, env.repoDir, "gpg.format", format)

				_, err := identity.Load(context.Background(), env.repoDir)
				if err == nil {
					t.Fatalf("Load succeeded with gpg.format = %q", format)
				}
				if !errors.Is(err, identity.ErrUnsupportedFormat) {
					t.Errorf("Load error = %v, want errors.Is ErrUnsupportedFormat", err)
				}
				if errors.Is(err, identity.ErrMissing) {
					t.Errorf("Load error = %v, want a configured value reported as configured", err)
				}
				var cfgErr *identity.ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load error is %T, want *identity.ConfigError", err)
				}
				if cfgErr.Key != "gpg.format" {
					t.Errorf("cfgErr.Key = %q, want \"gpg.format\"", cfgErr.Key)
				}
				// A deliberate choice writ is asking the user to reconsider,
				// so the message quotes the choice back and says what writ
				// signs with instead.
				if cfgErr.Value != format {
					t.Errorf("cfgErr.Value = %q, want %q", cfgErr.Value, format)
				}
				msg := err.Error()
				if !strings.Contains(msg, format) {
					t.Errorf("message %q does not quote the configured value back", msg)
				}
				if !strings.Contains(msg, "ssh") {
					t.Errorf("message %q does not say what writ signs with", msg)
				}
			})
		}
	})

	t.Run("configured correctly", func(t *testing.T) {
		for _, format := range []string{"ssh", "SSH", " ssh "} {
			t.Run(format, func(t *testing.T) {
				env := setupTestEnv(t)
				populateValidLocalConfig(t, env.repoDir)
				setGitConfig(t, env.repoDir, "gpg.format", format)

				id, err := identity.Load(context.Background(), env.repoDir)
				if err != nil {
					t.Fatalf("Load with gpg.format = %q: %v", format, err)
				}
				if id.Key.Value != "/path/to/id_ed25519" {
					t.Errorf("id.Key.Value = %q, want the configured path", id.Key.Value)
				}
				// Load accepts any spelling, so it must hand on one. It
				// carried the user's spelling through, and engine/open.go
				// compared the field against "ssh": gpg.format = SSH loaded
				// clean, reported a signing key, and then had no signer.
				if id.Key.Format != "ssh" {
					t.Errorf("id.Key.Format = %q for gpg.format = %q, want the canonical %q", id.Key.Format, format, "ssh")
				}
			})
		}
	})
}

// TestLoad_WhitespaceOnlySigningKey is WRIT-131's defect three keys further
// down the same function. git stores a whitespace-only user.signingKey
// verbatim, a raw guard accepted it as a key path, and the repository then
// reported itself configured and died at the first signed write with a
// subprocess error naming a file called "   ".
func TestLoad_WhitespaceOnlySigningKey(t *testing.T) {
	for _, value := range []string{" ", "   ", "\t"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)
			setGitConfig(t, env.repoDir, "user.signingKey", value)

			// git really does store it verbatim, so the test cannot pass by
			// git having normalised the value away.
			cmd := exec.Command("git", "config", "--get", "user.signingKey")
			cmd.Dir = env.repoDir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git config --get user.signingKey: %v", err)
			}
			if got := strings.TrimRight(string(out), "\n"); got != value {
				t.Fatalf("git stored user.signingKey as %q, want %q verbatim", got, value)
			}

			_, err = identity.Load(context.Background(), env.repoDir)
			if err == nil {
				t.Fatalf("Load accepted a whitespace-only user.signingKey %q as a key path", value)
			}
			if !errors.Is(err, identity.ErrMissing) {
				t.Errorf("Load error = %v, want errors.Is ErrMissing: a set key with nothing in it is nothing configured", err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("Load error is %T, want *identity.ConfigError", err)
			}
			if cfgErr.Key != "user.signingKey" {
				t.Errorf("cfgErr.Key = %q, want \"user.signingKey\"", cfgErr.Key)
			}
		})
	}

	t.Run("literal key with nothing after the prefix", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		setGitConfig(t, env.repoDir, "user.signingKey", "key::   ")

		_, err := identity.Load(context.Background(), env.repoDir)
		if err == nil {
			t.Fatal("Load accepted \"key::   \" as a literal signing key")
		}
		if !errors.Is(err, identity.ErrInvalid) {
			t.Errorf("Load error = %v, want errors.Is ErrInvalid", err)
		}
	})

	t.Run("padding around a real key path is trimmed", func(t *testing.T) {
		env := setupTestEnv(t)
		populateValidLocalConfig(t, env.repoDir)
		setGitConfig(t, env.repoDir, "user.signingKey", "  /path/to/id_ed25519  ")

		id, err := identity.Load(context.Background(), env.repoDir)
		if err != nil {
			t.Fatalf("Load with a padded user.signingKey: %v", err)
		}
		if id.Key.Value != "/path/to/id_ed25519" {
			t.Errorf("id.Key.Value = %q, want the padding trimmed off the path", id.Key.Value)
		}
	})
}

// TestLoad_WhitespaceOnlyAllowedSigners covers the last untrimmed key in
// Load. It is optional configuration, so the only thing whitespace can buy is
// a trust-store load against a path made of spaces.
func TestLoad_WhitespaceOnlyAllowedSigners(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)
	setGitConfig(t, env.repoDir, "gpg.ssh.allowedSignersFile", "   ")

	id, err := identity.Load(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if id.AllowedSigners != "" {
		t.Errorf("id.AllowedSigners = %q, want empty: a set key with nothing in it is nothing configured", id.AllowedSigners)
	}
}

// TestConfigErrorMessage pins the two renderings of a ConfigError. Error
// carries a remediation clause; Message is the same text with that clause
// dropped and nothing else changed. The clause is dropped for writ init, which
// prints these errors itself: any advice to run writ init is circular there,
// and implies init failed at something it never attempts.
//
// Message can only strip what it renders, which is why the per-key Remedy is a
// field rather than something an emitter wraps into Problem. Wrapping it there
// put "unset the key and run 'writ init' to mint a new one" inside the text
// Message returns verbatim, so writ init printed advice to run writ init and
// this test's own contract — "the clause is all that may differ" — quietly
// stopped holding.
func TestConfigErrorMessage(t *testing.T) {
	const hint = " (run 'writ init' to configure)"

	cases := []struct {
		name string
		err  *identity.ConfigError
		// wantClause is the parenthesized remediation Error appends and
		// Message drops. Empty means Error and Message must be identical.
		wantClause string
	}{
		{
			name:       "missing with a key",
			err:        &identity.ConfigError{Key: "gpg.format", Problem: identity.ErrMissing},
			wantClause: hint,
		},
		{
			name:       "missing with wrapped guidance",
			err:        &identity.ConfigError{Key: "writ.personId", Problem: fmt.Errorf("%w: set it to user:alice", identity.ErrMissing)},
			wantClause: hint,
		},
		{
			name:       "missing with no key at all",
			err:        &identity.ConfigError{Problem: identity.ErrMissing},
			wantClause: hint,
		},
		{
			name:       "unsupported with a value",
			err:        &identity.ConfigError{Key: "gpg.format", Value: "openpgp", Problem: fmt.Errorf("%w: writ signs with ssh", identity.ErrUnsupportedFormat)},
			wantClause: hint,
		},
		{
			// Invalid never carries the general hint: a wrong value is not
			// fixed by running init, which never overwrites one.
			name: "invalid",
			err:  &identity.ConfigError{Key: "writ.writerId", Value: "nope", Problem: identity.ErrInvalid},
		},
		{
			// An invalid value with a remedy the emitter knows. It renders in
			// Error and, because it goes through the same clause mechanism as
			// the hint, drops out of Message.
			name: "invalid with a remedy",
			err: &identity.ConfigError{
				Key:     "writ.writerId",
				Value:   "nope",
				Problem: identity.ErrInvalid,
				Remedy:  "unset the key and run 'writ init' to mint a new one",
			},
			wantClause: " (unset the key and run 'writ init' to mint a new one)",
		},
		{
			name: "some other problem",
			err:  &identity.ConfigError{Key: "user.name", Problem: errors.New("git exploded")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr, gotMsg := tc.err.Error(), tc.err.Message()
			if strings.Contains(gotMsg, "writ init") {
				t.Errorf("Message() = %q, want no advice to run writ init", gotMsg)
			}
			if tc.wantClause != "" {
				if !strings.HasSuffix(gotErr, tc.wantClause) {
					t.Errorf("Error() = %q, want it to end with %q", gotErr, tc.wantClause)
				}
				if want := strings.TrimSuffix(gotErr, tc.wantClause); gotMsg != want {
					t.Errorf("Message() = %q, want %q: the clause is all that may differ", gotMsg, want)
				}
				return
			}
			if strings.Contains(gotErr, "writ init") {
				t.Errorf("Error() = %q, want no advice to run writ init", gotErr)
			}
			if gotMsg != gotErr {
				t.Errorf("Message() = %q, want it identical to Error() = %q", gotMsg, gotErr)
			}
		})
	}
}

// TestLoad_EmptyPersonIDAlwaysExplained pins WRIT-143's invariant: an Identity
// whose PersonID is empty always carries a non-nil PersonIDErr saying why, and
// an Identity whose PersonID is set never carries one.
//
// Load used to return a bare Identity{} on each of its early returns, and a
// bare Identity{} is PersonID == "" with PersonIDErr == nil — the exact shape
// of "writ.personId is unset and there is no user.email to derive one from".
// A caller with nothing but the Identity in hand could not tell that apart
// from "this repository has no identity at all", so it diagnosed the first:
// a write on a clone whose owner never ran `writ init` named
// writ.personId and user.email, both of which were already correct.
//
// The sweep aims at every one of Load's ten returns — nine failures and the
// success — because an invariant with an exception is not one. The
// unreadable-config path is a separate subtest below, since it is not reached
// by mutating config.
//
// It aims by hand, though, and hand-enumeration is the blind spot that put the
// defect in Load in the first place. This table shipped one case short: nothing
// reached the key:: empty-literal return, so that return could have been
// mutated to `return Identity{}, ...` with the suite still green.
// TestLoadReturnsCarryTheReason is the guard against the next omission — it
// reads Load's returns out of the AST rather than trusting a list — and a new
// return in Load wants a row here as well as a shape that test accepts.
//
// Reaching every return is necessary and was not sufficient. The five rows
// that return baseIdent all resolved a PersonID, so nothing here observed a
// baseIdent whose PersonIDErr was the only thing explaining an empty
// identifier, and `baseIdent.PersonIDErr = nil` after the literal passed. The
// writ.personId-unresolvable-and-gpg.format-unset row below is that pairing;
// the AST check grew a left-hand-side arm for the same mutation.
func TestLoad_EmptyPersonIDAlwaysExplained(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, repoDir string)
		// wantPersonID is the identifier Load must resolve, "" when it cannot.
		wantPersonID string
		// wantPersonIDErrKey is the config key PersonIDErr must name; "" means
		// PersonIDErr must be nil.
		wantPersonIDErrKey string
		// wantLoadErrKey is the config key the returned error must name; ""
		// means Load must succeed.
		wantLoadErrKey string
		// samePointer asserts PersonIDErr is the very error Load returned,
		// which is what makes the two diagnoses impossible to disagree.
		samePointer bool
	}{
		{
			name:         "valid configuration",
			mutate:       func(*testing.T, string) {},
			wantPersonID: "email:alice@example.test",
		},
		{
			name: "writ.writerId unset",
			mutate: func(t *testing.T, dir string) {
				unsetGitConfig(t, dir, "writ.writerId")
			},
			wantPersonIDErrKey: "writ.writerId",
			wantLoadErrKey:     "writ.writerId",
			samePointer:        true,
		},
		{
			name: "writ.writerId malformed",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "writ.writerId", "nothexadecimal!!")
			},
			wantPersonIDErrKey: "writ.writerId",
			wantLoadErrKey:     "writ.writerId",
			samePointer:        true,
		},
		{
			name: "user.name unset",
			mutate: func(t *testing.T, dir string) {
				unsetGitConfig(t, dir, "user.name")
			},
			wantPersonIDErrKey: "user.name",
			wantLoadErrKey:     "user.name",
			samePointer:        true,
		},
		{
			// The sharp one. writ.personId and user.email are untouched and
			// valid, so any message naming them names something correct.
			name: "user.name whitespace-only",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "user.name", "   ")
			},
			wantPersonIDErrKey: "user.name",
			wantLoadErrKey:     "user.name",
			samePointer:        true,
		},
		{
			name: "user.email unset",
			mutate: func(t *testing.T, dir string) {
				unsetGitConfig(t, dir, "user.email")
			},
			wantPersonIDErrKey: "user.email",
			wantLoadErrKey:     "user.email",
			samePointer:        true,
		},
		{
			name: "user.email whitespace-only",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "user.email", "\t ")
			},
			wantPersonIDErrKey: "user.email",
			wantLoadErrKey:     "user.email",
			samePointer:        true,
		},
		{
			// The case PersonIDErr was introduced for, and the reason the
			// invariant cannot simply be "PersonIDErr is nil unless Load
			// failed": here Load succeeds and PersonIDErr is set.
			name: "writ.personId set to something that is not a person identifier",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "writ.personId", "alice")
			},
			wantPersonIDErrKey: identity.PersonIDKey,
		},
		{
			// Both halves of the invariant at once, and the row this table
			// was missing: Load fails past the person derivation, so it
			// returns baseIdent, and the identifier did not resolve, so
			// baseIdent is the value carrying the reason. Every other
			// baseIdent row resolves a PersonID, which is what let
			// `baseIdent.PersonIDErr = nil` — one line after the literal —
			// break the invariant with this whole table green.
			name: "writ.personId unresolvable and gpg.format unset",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "writ.personId", "alice")
				unsetGitConfig(t, dir, "gpg.format")
			},
			wantPersonIDErrKey: identity.PersonIDKey,
			wantLoadErrKey:     "gpg.format",
		},
		{
			// The converse: Load fails past the person derivation, so the
			// identifier resolved and PersonIDErr must stay nil. Reporting a
			// person problem to a user whose signing configuration is what is
			// wrong is the same defect pointed the other way.
			name: "gpg.format unset",
			mutate: func(t *testing.T, dir string) {
				unsetGitConfig(t, dir, "gpg.format")
			},
			wantPersonID:   "email:alice@example.test",
			wantLoadErrKey: "gpg.format",
		},
		{
			name: "gpg.format unsupported",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "gpg.format", "openpgp")
			},
			wantPersonID:   "email:alice@example.test",
			wantLoadErrKey: "gpg.format",
		},
		{
			name: "user.signingKey unset",
			mutate: func(t *testing.T, dir string) {
				unsetGitConfig(t, dir, "user.signingKey")
			},
			wantPersonID:   "email:alice@example.test",
			wantLoadErrKey: "user.signingKey",
		},
		{
			// The tenth return, and the one this sweep was a case short of:
			// the key:: form with nothing after the prefix. It is a separate
			// branch from the unset row above — that one is guarded on the
			// whole value, this one on what survives the prefix — and it was
			// the only return in Load no case reached. Mutating it to
			// `return Identity{}, ...` broke the invariant with the whole
			// suite still green, which is the exact shape of the defect
			// WRIT-143 is about, in the test that is supposed to catch it.
			name: "user.signingKey key:: with an empty literal",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "user.signingKey", "key::")
			},
			wantPersonID:   "email:alice@example.test",
			wantLoadErrKey: "user.signingKey",
		},
		{
			name: "user.signingKey key:: with a whitespace-only literal",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "user.signingKey", "key::   ")
			},
			wantPersonID:   "email:alice@example.test",
			wantLoadErrKey: "user.signingKey",
		},
		{
			// writ.writerId set to whitespace. Until this change it fell
			// through to ParseWriterID, so it reached the malformed return
			// above rather than the missing one — a different return, a
			// different message and, at the CLI, a different remedy.
			name: "writ.writerId whitespace-only",
			mutate: func(t *testing.T, dir string) {
				setGitConfig(t, dir, "writ.writerId", "   ")
			},
			wantPersonIDErrKey: "writ.writerId",
			wantLoadErrKey:     "writ.writerId",
			samePointer:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestEnv(t)
			populateValidLocalConfig(t, env.repoDir)
			tc.mutate(t, env.repoDir)

			id, err := identity.Load(context.Background(), env.repoDir)

			// The invariant itself, asserted before anything specific to the
			// case: an empty identifier is always explained, and a resolved
			// one is never accompanied by an explanation of its absence.
			if id.PersonID == "" && id.PersonIDErr == nil {
				t.Errorf("PersonID is empty with a nil PersonIDErr: nothing says why (Load error: %v)", err)
			}
			if id.PersonID != "" && id.PersonIDErr != nil {
				t.Errorf("PersonID = %q alongside PersonIDErr = %v: a resolved identifier explains itself", id.PersonID, id.PersonIDErr)
			}

			if id.PersonID != tc.wantPersonID {
				t.Errorf("PersonID = %q, want %q", id.PersonID, tc.wantPersonID)
			}

			if tc.wantPersonIDErrKey == "" {
				if id.PersonIDErr != nil {
					t.Errorf("PersonIDErr = %v, want nil", id.PersonIDErr)
				}
			} else {
				var cfgErr *identity.ConfigError
				if !errors.As(id.PersonIDErr, &cfgErr) {
					t.Fatalf("PersonIDErr = %v (%T), want a *identity.ConfigError", id.PersonIDErr, id.PersonIDErr)
				}
				if cfgErr.Key != tc.wantPersonIDErrKey {
					t.Errorf("PersonIDErr names %q, want %q: the message must send the user to the key that is actually at fault", cfgErr.Key, tc.wantPersonIDErrKey)
				}
			}

			if tc.wantLoadErrKey == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("Load succeeded, want a failure naming %q", tc.wantLoadErrKey)
				}
				var cfgErr *identity.ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("Load error = %v (%T), want a *identity.ConfigError", err, err)
				}
				if cfgErr.Key != tc.wantLoadErrKey {
					t.Errorf("Load error names %q, want %q", cfgErr.Key, tc.wantLoadErrKey)
				}
			}

			if tc.samePointer && id.PersonIDErr != err {
				t.Errorf("PersonIDErr = %v, want the identical error Load returned (%v)", id.PersonIDErr, err)
			}
		})
	}

	// The remaining failure path: git config could not be read at all. It
	// carries no ConfigError key of its own, but it must still explain the
	// empty identifier rather than leave a caller to guess.
	t.Run("git config unreadable", func(t *testing.T) {
		requireGit(t)
		id, err := identity.Load(context.Background(), t.TempDir())
		if err == nil {
			t.Fatal("Load on a non-repo directory succeeded, want a failure")
		}
		if id.PersonID != "" {
			t.Errorf("PersonID = %q, want empty", id.PersonID)
		}
		if id.PersonIDErr == nil {
			t.Fatal("PersonID is empty with a nil PersonIDErr: nothing says why")
		}
		if id.PersonIDErr != err {
			t.Errorf("PersonIDErr = %v, want the identical error Load returned (%v)", id.PersonIDErr, err)
		}
	})
}

func unsetGitConfig(t *testing.T, dir, key string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--unset", key)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config --unset %s failed: %v (%s)", key, err, string(out))
	}
}

func TestLoad_NonRepoDirectory(t *testing.T) {
	requireGit(t)
	nonRepoDir := t.TempDir()

	_, err := identity.Load(context.Background(), nonRepoDir)
	if err == nil {
		t.Fatal("Load on non-repo directory succeeded, expected error")
	}
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("Load on non-repo dir returned %T (%v), want *identity.ConfigError", err, err)
	}
}

func TestLoad_GitNotOnPath(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	_, err := identity.Load(context.Background(), emptyDir)
	if err == nil {
		t.Fatal("Load with git not on PATH succeeded, expected error")
	}
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("Load with git missing returned %T (%v), want *identity.ConfigError", err, err)
	}
}

func TestLoad_ContextCancelled(t *testing.T) {
	env := setupTestEnv(t)
	populateValidLocalConfig(t, env.repoDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := identity.Load(ctx, env.repoDir)
	if err == nil {
		t.Fatal("Load with cancelled context succeeded, expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Load error = %v, want errors.Is context.Canceled", err)
	}
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("Load error is %T, want *identity.ConfigError", err)
	}
}
