package sync_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/sync"
)

func TestClassifyGitError(t *testing.T) {
	tests := []struct {
		name          string
		remote        string
		args          []string
		inputErr      error
		stderr        string
		stdout        string
		wantKind      sync.FailureKind
		wantSentinel  error
		wantRetryable bool
		wantAdviceSub string
	}{
		{
			name:          "ssh_publickey_denied",
			remote:        "origin",
			args:          []string{"push", "--porcelain", "origin", "refs/writ/alice/*:refs/writ/alice/*"},
			inputErr:      errors.New("exit status 255"),
			stderr:        "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
			wantKind:      sync.FailureKindAuth,
			wantSentinel:  sync.ErrAuth,
			wantRetryable: false,
			wantAdviceSub: "credentials rejected by origin",
		},
		{
			name:          "https_auth_failed",
			remote:        "origin",
			args:          []string{"push", "--porcelain", "origin", "refs/writ/alice/*:refs/writ/alice/*"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: Authentication failed for 'https://github.com/writtendev/writ.git/'",
			wantKind:      sync.FailureKindAuth,
			wantSentinel:  sync.ErrAuth,
			wantRetryable: false,
			wantAdviceSub: "check your ssh agent or credential helper",
		},
		{
			name:          "username_prompt_disabled",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			wantKind:      sync.FailureKindAuth,
			wantSentinel:  sync.ErrAuth,
			wantRetryable: false,
			wantAdviceSub: "credentials rejected",
		},
		{
			name:          "http_403_forbidden",
			remote:        "origin",
			args:          []string{"push", "--porcelain", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "error: The requested URL returned error: 403 Forbidden",
			wantKind:      sync.FailureKindAuth,
			wantSentinel:  sync.ErrAuth,
			wantRetryable: false,
			wantAdviceSub: "credentials rejected",
		},
		{
			name:          "dns_resolution_failure",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: Could not resolve host: github.com",
			wantKind:      sync.FailureKindNetwork,
			wantSentinel:  sync.ErrNetwork,
			wantRetryable: true,
			wantAdviceSub: "network or host unreachable for origin",
		},
		{
			name:          "connection_refused",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: unable to access 'https://127.0.0.1:9999/repo.git/': Failed to connect to 127.0.0.1 port 9999: Connection refused",
			wantKind:      sync.FailureKindNetwork,
			wantSentinel:  sync.ErrNetwork,
			wantRetryable: true,
			wantAdviceSub: "check connection and remote URL",
		},
		{
			name:          "connection_timed_out",
			remote:        "upstream",
			args:          []string{"fetch", "upstream"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: unable to access 'https://example.com/repo.git/': Connection timed out",
			wantKind:      sync.FailureKindNetwork,
			wantSentinel:  sync.ErrNetwork,
			wantRetryable: true,
			wantAdviceSub: "network or host unreachable for upstream",
		},
		{
			name:          "ssl_certificate_problem",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: unable to access 'https://selfsigned.local/repo.git/': SSL certificate problem: self-signed certificate",
			wantKind:      sync.FailureKindNetwork,
			wantSentinel:  sync.ErrNetwork,
			wantRetryable: true,
			wantAdviceSub: "network or host unreachable",
		},
		{
			name:          "pre_receive_hook_declined",
			remote:        "origin",
			args:          []string{"push", "--porcelain", "origin", "refs/writ/alice/widget:refs/writ/alice/widget"},
			inputErr:      errors.New("exit status 1"),
			stderr:        "remote: error: hook declined to update refs/writ/alice/widget\nTo origin\n!	refs/writ/alice/widget:refs/writ/alice/widget	[remote rejected] (pre-receive hook declined)\nerror: failed to push some refs",
			wantKind:      sync.FailureKindRejected,
			wantSentinel:  sync.ErrRefRejected,
			wantRetryable: false,
			wantAdviceSub: "rejected ref update",
		},
		{
			name:          "non_fast_forward_rejected",
			remote:        "origin",
			args:          []string{"push", "--porcelain", "origin", "refs/writ/alice/widget:refs/writ/alice/widget"},
			inputErr:      errors.New("exit status 1"),
			stderr:        "To origin\n!	refs/writ/alice/widget:refs/writ/alice/widget	[rejected] (non-fast-forward)\nerror: failed to push some refs to 'origin'\nhint: Updates were rejected because the tip of your current branch is behind",
			wantKind:      sync.FailureKindRejected,
			wantSentinel:  sync.ErrNonFastForward,
			wantRetryable: false,
			wantAdviceSub: "rejected non-fast-forward update",
		},
		{
			name:          "repository_not_found",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "ERROR: Repository not found.\nfatal: Could not read from remote repository.",
			wantKind:      sync.FailureKindNotFound,
			wantSentinel:  sync.ErrUnknownRemote,
			wantRetryable: false,
			wantAdviceSub: "not found or repository does not exist",
		},
		{
			name:          "does_not_appear_to_be_git_repo",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: 'nosuchpath' does not appear to be a git repository",
			wantKind:      sync.FailureKindNotFound,
			wantSentinel:  sync.ErrUnknownRemote,
			wantRetryable: false,
			wantAdviceSub: "not found or repository does not exist",
		},
		{
			name:          "no_such_remote",
			remote:        "missing",
			args:          []string{"fetch", "missing"},
			inputErr:      errors.New("exit status 128"),
			stderr:        "fatal: No such remote 'missing'",
			wantKind:      sync.FailureKindNotFound,
			wantSentinel:  sync.ErrUnknownRemote,
			wantRetryable: false,
			wantAdviceSub: "not found or repository does not exist",
		},
		{
			name:          "context_canceled",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      context.Canceled,
			stderr:        "",
			wantKind:      sync.FailureKindCanceled,
			wantSentinel:  context.Canceled,
			wantRetryable: true,
			wantAdviceSub: "canceled",
		},
		{
			name:          "context_deadline_exceeded",
			remote:        "origin",
			args:          []string{"fetch", "origin"},
			inputErr:      context.DeadlineExceeded,
			stderr:        "",
			wantKind:      sync.FailureKindCanceled,
			wantSentinel:  context.DeadlineExceeded,
			wantRetryable: true,
			wantAdviceSub: "timed out",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gitErr := sync.ClassifyGitError(tc.remote, tc.args, tc.inputErr, []byte(tc.stderr), []byte(tc.stdout))
			if gitErr == nil {
				t.Fatalf("expected non-nil GitError")
			}
			if gitErr.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", gitErr.Kind, tc.wantKind)
			}
			if !errors.Is(gitErr, tc.wantSentinel) {
				t.Errorf("errors.Is(gitErr, %v) = false, want true (actual Err: %v)", tc.wantSentinel, gitErr.Err)
			}
			if gitErr.Retryable() != tc.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", gitErr.Retryable(), tc.wantRetryable)
			}
			if tc.wantAdviceSub != "" && !strings.Contains(gitErr.Advice, tc.wantAdviceSub) {
				t.Errorf("Advice = %q, want substring %q", gitErr.Advice, tc.wantAdviceSub)
			}
			if gitErr.Error() == "" {
				t.Errorf("Error() string is empty")
			}
		})
	}
}
