// Package gitdir provides helpers for discovering git repository paths
// and initializing filesystem-backed storage instances without relying on
// go-git's porcelain repository loader.
package gitdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/cache"
	cfgformat "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// ErrUnsupportedRepository reports a repository whose on-disk layout writ
// cannot read or write correctly.
var ErrUnsupportedRepository = errors.New("gitdir: unsupported repository format")

// ErrNotRepository reports that a path is not inside a git repository
// (walking up to the mount point turned up neither a .git entry nor a bare
// repository layout).
var ErrNotRepository = errors.New("gitdir: not a git repository")

// Info contains resolved git repository directory paths.
type Info struct {
	// WorkTree is the root of the working directory, or empty for bare repositories.
	WorkTree string

	// GitDir is the repository metadata directory (.git or worktree gitdir).
	GitDir string

	// CommonDir is the common .git directory shared across all linked worktrees.
	CommonDir string
}

// Resolve locates the git directory and common directory for a given path
// purely in Go without invoking external git processes.
func Resolve(path string) (Info, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Info{}, fmt.Errorf("gitdir: resolve absolute path %q: %w", path, err)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return Info{}, fmt.Errorf("gitdir: stat path %q: %w", path, err)
	}

	startDir := absPath
	if !fi.IsDir() {
		startDir = filepath.Dir(absPath)
	}

	// 1. Walk up looking for .git
	curr := startDir
	for {
		gitEntry := filepath.Join(curr, ".git")
		entryInfo, err := os.Stat(gitEntry)
		if err == nil {
			if entryInfo.IsDir() {
				// Standard working tree
				gitDir := gitEntry
				commonDir := readCommonDir(gitDir)
				return Info{
					WorkTree:  curr,
					GitDir:    gitDir,
					CommonDir: commonDir,
				}, nil
			}

			// Linked worktree (.git is a file containing "gitdir: <path>")
			data, err := os.ReadFile(gitEntry)
			if err != nil {
				return Info{}, fmt.Errorf("gitdir: read .git file %q: %w", gitEntry, err)
			}

			content := strings.TrimSpace(string(data))
			const gitdirPrefix = "gitdir:"
			if strings.HasPrefix(content, gitdirPrefix) {
				rawTarget := strings.TrimSpace(content[len(gitdirPrefix):])
				var gitDir string
				if filepath.IsAbs(rawTarget) {
					gitDir = filepath.Clean(rawTarget)
				} else {
					gitDir = filepath.Clean(filepath.Join(curr, rawTarget))
				}

				commonDir := readCommonDir(gitDir)
				return Info{
					WorkTree:  curr,
					GitDir:    gitDir,
					CommonDir: commonDir,
				}, nil
			}
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	// 2. Check if startDir or any parent is a bare repository (contains HEAD, objects, refs)
	curr = startDir
	for {
		if isBareRepo(curr) {
			gitDir := curr
			commonDir := readCommonDir(gitDir)
			return Info{
				WorkTree:  "",
				GitDir:    gitDir,
				CommonDir: commonDir,
			}, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}

	return Info{}, fmt.Errorf("%w (or any parent up to mount point): %s", ErrNotRepository, path)
}

func readCommonDir(gitDir string) string {
	commondirFile := filepath.Join(gitDir, "commondir")
	data, err := os.ReadFile(commondirFile)
	if err != nil {
		return gitDir
	}
	relCommon := strings.TrimSpace(string(data))
	if relCommon == "" {
		return gitDir
	}
	if filepath.IsAbs(relCommon) {
		return filepath.Clean(relCommon)
	}
	return filepath.Clean(filepath.Join(gitDir, relCommon))
}

// OpenStorage initializes a filesystem-backed Storage storer for the given git info.
//
// We deliberately bypass go-git's porcelain repository loader, whose extension
// verification rejects repositories carrying standard v0 extensions such as
// extensions.worktreeConfig (WRIT-93). Bypassing that check is only safe for
// extensions that leave the on-disk layout alone, so OpenStorage still refuses
// the extensions that move objects or refs somewhere this storer cannot see.
func OpenStorage(info Info) (*filesystem.Storage, error) {
	dot := osfs.New(info.GitDir)
	var repositoryFs billy.Filesystem = dot
	if info.CommonDir != "" && info.CommonDir != info.GitDir {
		commonDot := osfs.New(info.CommonDir)
		repositoryFs = dotgit.NewRepositoryFilesystem(dot, commonDot)
	}
	s := filesystem.NewStorage(repositoryFs, cache.NewObjectLRUDefault())
	if err := verifyLayout(s); err != nil {
		return nil, err
	}
	return s, nil
}

// verifyLayout rejects repositories whose object or ref storage does not live
// where the filesystem storer looks for it. Without this check writ opens such
// a repository, reads no refs, and writes ops into loose refs that system git
// never sees — so `writ sync` silently pushes nothing.
func verifyLayout(s *filesystem.Storage) error {
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("gitdir: read repository config: %w", err)
	}

	switch cfg.Core.RepositoryFormatVersion {
	case "", cfgformat.Version_0, cfgformat.Version_1:
	default:
		return fmt.Errorf("%w: core.repositoryformatversion %q is not supported",
			ErrUnsupportedRepository, cfg.Core.RepositoryFormatVersion)
	}

	if cfg.Raw == nil || !cfg.Raw.HasSection("extensions") {
		return nil
	}
	for _, opt := range cfg.Raw.Section("extensions").Options {
		name := strings.ToLower(opt.Key)
		value := strings.ToLower(opt.Value)
		switch name {
		case "objectformat":
			if value != "" && value != "sha1" {
				return fmt.Errorf("%w: extensions.objectFormat %q (writ reads git objects directly and only understands sha1)",
					ErrUnsupportedRepository, opt.Value)
			}
		case "refstorage":
			if value != "" && value != "files" {
				return fmt.Errorf("%w: extensions.refStorage %q (writ reads refs directly and only understands the files backend)",
					ErrUnsupportedRepository, opt.Value)
			}
		}
	}
	return nil
}

func isBareRepo(dir string) bool {
	headPath := filepath.Join(dir, "HEAD")
	objectsPath := filepath.Join(dir, "objects")
	refsPath := filepath.Join(dir, "refs")

	if _, err := os.Stat(headPath); err != nil {
		return false
	}
	if fi, err := os.Stat(objectsPath); err != nil || !fi.IsDir() {
		return false
	}
	if fi, err := os.Stat(refsPath); err != nil || !fi.IsDir() {
		return false
	}
	return true
}
