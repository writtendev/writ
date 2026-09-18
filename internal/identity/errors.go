package identity

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrMissing indicates a required git configuration key is unset or empty.
	ErrMissing = errors.New("missing configuration")

	// ErrInvalid indicates a git configuration key has an invalid value.
	ErrInvalid = errors.New("invalid configuration")

	// ErrUnsupportedFormat indicates a git configuration key specifies an unsupported format.
	ErrUnsupportedFormat = errors.New("unsupported format")
)

// ConfigError records a failure while reading or validating git configuration.
type ConfigError struct {
	Key     string
	Value   string
	Problem error
	// Remedy is the next step for this particular misconfiguration, when the
	// general "run 'writ init' to configure" is not it. Error appends it in
	// parentheses and Message omits it, exactly as they treat initHint — so
	// every remediation this type prints goes through one mechanism and
	// Message can strip all of it.
	//
	// It is set by the code that read the value out of git config, never by a
	// parser. ParseWriterID is why: engine/dag parses the writer-id segment of
	// a ref path through it, and a malformed segment there is usually someone
	// else's writer id arriving over a fetch. Telling that reader to unset
	// their own — correct — writ.writerId and mint a new one would split their
	// device's ops across two ref namespaces. The parser says what the value
	// had to look like; only a caller that knows the value came from a config
	// key can say what to do about it. See withRemedy.
	Remedy string
}

// initHint is the remediation ConfigError.Error appends to the two problems a
// user fixes by configuring something. It is correct for every emitter except
// one: writ init prints these errors itself, and telling the reader to run the
// command they are running is circular. Worse, it implies init failed at
// something it never attempts — writ does not write signing configuration for
// anyone; it prints the git config lines and expects the user to run them.
// Message renders the same text without this clause for that caller.
//
// ErrInvalid is the third problem and never gets initHint, deliberately. writ
// init never overwrites a value that is already there: EnsureWriterID reuses a
// writ.writerId it can parse and mints only into an absent one, EnsureRepoID
// does the same for writ.repoId, and init reads writ.personId without ever
// writing it. So for every key that reports ErrInvalid the user typed the
// offending value and only the user can retype it — "run 'writ init'" on its
// own would be a remedy that does not work.
//
// An ErrInvalid that has a working next step names it in Remedy instead. Three
// do today, all three set where the key is read rather than where it is
// parsed: writ.writerId and writ.repoId say to unset the key first, because
// init mints only into an absent one, and the user.signingKey key:: arm — set
// in Load, at the read — says what the form needs. DerivePersonID's two arms
// carry person.Check's diagnosis in Problem and no Remedy — what is wrong with
// the identifier is the whole of what there is to say, and only the user knows
// what they meant to type.
const initHint = " (run 'writ init' to configure)"

// remintRemedy is the next step for a writ.writerId or writ.repoId that does
// not parse. Both are minted by writ init into an absent key and never over a
// present one, so the key has to be cleared before init can replace it.
//
// Clearing it is safe precisely because the value is invalid: no ops can have
// been written under a writer id that never parsed, so there is no namespace
// to strand. That is not true of a valid id, which is why writ init refuses to
// overwrite one and why this advice is never attached to anything but an
// ErrInvalid.
const remintRemedy = "unset the key and run 'writ init' to mint a new one"

// withRemedy returns err with remedy attached, for a *ConfigError raised by a
// parser that does not know its input came from git config.
//
// The value is otherwise carried through unchanged, including Problem, so
// errors.Is still sees the same sentinel.
func withRemedy(err error, remedy string) error {
	cfgErr, ok := err.(*ConfigError)
	if !ok {
		return err
	}
	return &ConfigError{
		Key:     cfgErr.Key,
		Value:   cfgErr.Value,
		Problem: cfgErr.Problem,
		Remedy:  remedy,
	}
}

func (e *ConfigError) Error() string {
	return e.message(e.hint())
}

// Message returns the same text as Error without the remediation clause —
// neither "run 'writ init' to configure" nor a per-key Remedy. It is for writ
// init, which is the one emitter no remediation can help: it prints the git
// config lines to run directly below, and it is already the command any of
// them would name. Every other caller wants Error.
func (e *ConfigError) Message() string {
	return e.message("")
}

// hint is the remediation clause Error appends: the emitter's own Remedy when
// it set one, the general initHint when the problem is one writ init fixes,
// and nothing for an invalid value nobody but the user can retype.
func (e *ConfigError) hint() string {
	if e.Remedy != "" {
		return " (" + e.Remedy + ")"
	}
	if errors.Is(e.Problem, ErrInvalid) {
		return ""
	}
	return initHint
}

func (e *ConfigError) message(hint string) string {
	if errors.Is(e.Problem, ErrMissing) {
		// Carry e.Problem's detail through, the way the ErrInvalid and
		// ErrUnsupportedFormat arms below already do. Short-circuiting on the
		// sentinel discarded whatever the caller wrapped into it, which made
		// guidance like "set writ.personId (for example user:alice)"
		// unreachable at exactly the moment a user needed it.
		detail := strings.TrimSpace(strings.TrimPrefix(e.Problem.Error(), ErrMissing.Error()))
		detail = strings.TrimPrefix(detail, ":")
		detail = strings.TrimSpace(detail)
		if e.Key != "" {
			if detail != "" {
				return fmt.Sprintf("identity: missing git config %q: %s%s", e.Key, detail, hint)
			}
			return fmt.Sprintf("identity: missing git config %q%s", e.Key, hint)
		}
		if detail != "" {
			return fmt.Sprintf("identity: missing git configuration: %s%s", detail, hint)
		}
		return "identity: missing git configuration" + hint
	}
	if errors.Is(e.Problem, ErrUnsupportedFormat) {
		if e.Key != "" && e.Value != "" {
			return fmt.Sprintf("identity: unsupported git config %q=%q: %v%s", e.Key, e.Value, e.Problem, hint)
		}
		if e.Key != "" {
			return fmt.Sprintf("identity: unsupported git config %q: %v%s", e.Key, e.Problem, hint)
		}
		return fmt.Sprintf("identity: unsupported format: %v%s", e.Problem, hint)
	}
	if errors.Is(e.Problem, ErrInvalid) {
		if e.Key != "" && e.Value != "" {
			return fmt.Sprintf("identity: invalid git config %q=%q: %v%s", e.Key, e.Value, e.Problem, hint)
		}
		if e.Key != "" {
			return fmt.Sprintf("identity: invalid git config %q: %v%s", e.Key, e.Problem, hint)
		}
		if e.Value != "" {
			return fmt.Sprintf("identity: invalid value %q: %v%s", e.Value, e.Problem, hint)
		}
		return fmt.Sprintf("identity: invalid configuration: %v%s", e.Problem, hint)
	}
	// Neither arm below takes the hint. What lands here is not one of the
	// three config problems — a cancelled context, a git subprocess failure —
	// and none of the remediations this type carries applies to it.
	if e.Key != "" {
		return fmt.Sprintf("identity: git config error for %q: %v", e.Key, e.Problem)
	}
	return fmt.Sprintf("identity: %v", e.Problem)
}

func (e *ConfigError) Unwrap() error {
	return e.Problem
}
