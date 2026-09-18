// Package sync implements refspec management and system-git transport
// for Writ append chains.
//
// Following the settled architecture decisions (ARCHITECTURE.md §Language),
// Writ takes a hybrid approach: go-git for local object I/O, and system git
// for all network transport. This ensures SSH agents, credential helpers,
// gitconfig, proxies, and enterprise auth setups work automatically with
// zero credential code in Writ.
//
// Callers who also use the standard library sync package should alias
// this package (e.g. `import writsync "github.com/writtendev/writ/internal/sync"`).
package sync

import (
	"fmt"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/internal/gitdir"
	"github.com/writtendev/writ/internal/identity"
)

// Option configures a Client instance.
type Option func(*Client)

// WithGitBinary configures the git executable binary path (defaults to "git").
func WithGitBinary(gitBin string) Option {
	return func(c *Client) {
		if gitBin != "" {
			c.gitBin = gitBin
		}
	}
}

// WithEnv configures additional environment variables for git subprocess invocations.
func WithEnv(env []string) Option {
	return func(c *Client) {
		c.env = env
	}
}

// Client manages git refspecs and performs network transport (fetch and push)
// for Writ operations using system git.
type Client struct {
	repoDir  string
	storer   storage.Storer
	identity identity.Identity
	gitBin   string
	env      []string
	mu       sync.Mutex
}

// Open opens a git repository at repoDir and returns an initialized Client.
func Open(repoDir string, ident identity.Identity, opts ...Option) (*Client, error) {
	info, err := gitdir.Resolve(repoDir)
	if err != nil {
		return nil, fmt.Errorf("sync: open repo %s: %w", repoDir, err)
	}
	storer, err := gitdir.OpenStorage(info)
	if err != nil {
		return nil, fmt.Errorf("sync: open repo %s: %w", repoDir, err)
	}
	return OpenStorage(storer, repoDir, ident, opts...)
}

// OpenStorage initializes a Client with a storage.Storer.
func OpenStorage(s storage.Storer, repoDir string, ident identity.Identity, opts ...Option) (*Client, error) {
	if s == nil {
		return nil, fmt.Errorf("sync: nil storer")
	}
	c := &Client{
		repoDir:  repoDir,
		storer:   s,
		identity: ident,
		gitBin:   "git",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// OpenRepo initializes a Client with an existing go-git repository instance.
func OpenRepo(repo *git.Repository, repoDir string, ident identity.Identity, opts ...Option) (*Client, error) {
	if repo == nil {
		return nil, fmt.Errorf("sync: nil repo")
	}
	return OpenStorage(repo.Storer, repoDir, ident, opts...)
}

// RepoDir returns the repository root directory.
func (c *Client) RepoDir() string {
	return c.repoDir
}

// Storer returns the underlying storage.Storer.
func (c *Client) Storer() storage.Storer {
	return c.storer
}

// Identity returns the configured writer identity.
func (c *Client) Identity() identity.Identity {
	return c.identity
}

// WriterID returns the writer ID of the configured identity.
func (c *Client) WriterID() identity.WriterID {
	return c.identity.WriterID
}

// GitBinary returns the configured git executable binary path.
func (c *Client) GitBinary() string {
	if c == nil || c.gitBin == "" {
		return "git"
	}
	return c.gitBin
}
