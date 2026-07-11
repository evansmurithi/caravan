// Package git provides a minimal Git source that clones a repository and keeps
// a local working copy in sync with a tracked branch.
package git

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

// Options configures a Source.
type Options struct {
	URL           string
	Branch        string
	Token         string // access token for HTTP(S) auth
	SSHKey        string // path to a private key for SSH auth
	SSHPassphrase string // passphrase protecting the SSH private key
	Dir           string // local checkout directory
}

// tokenUser is the username sent alongside an access token for HTTP(S) basic
// auth. Providers such as GitHub and GitLab accept the token as the password
// with any non-empty username.
const tokenUser = "caravan"

// Source manages a local clone of a remote repository.
type Source struct {
	opts Options
	auth transport.AuthMethod
	repo *git.Repository
}

// New builds a Source and resolves the appropriate auth method.
func New(opts Options) (*Source, error) {
	if opts.URL == "" {
		return nil, errors.New("git: URL is required")
	}
	if opts.Branch == "" {
		opts.Branch = "main"
	}

	auth, err := buildAuth(opts)
	if err != nil {
		return nil, err
	}
	return &Source{opts: opts, auth: auth}, nil
}

func buildAuth(opts Options) (transport.AuthMethod, error) {
	if opts.SSHKey != "" {
		auth, err := gitssh.NewPublicKeysFromFile("git", opts.SSHKey, opts.SSHPassphrase)
		if err != nil {
			return nil, fmt.Errorf("git: loading ssh key: %w", err)
		}
		return auth, nil
	}
	if opts.Token != "" {
		return &http.BasicAuth{Username: tokenUser, Password: opts.Token}, nil
	}
	return nil, nil
}

// Sync ensures the local checkout exists and reflects the tip of the tracked
// branch. It returns the resolved commit hash.
func (s *Source) Sync() (string, error) {
	if s.repo == nil {
		if err := s.open(); err != nil {
			return "", err
		}
	}

	if err := s.fetch(); err != nil {
		return "", err
	}

	if err := s.checkout(); err != nil {
		return "", err
	}

	head, err := s.repo.Head()
	if err != nil {
		return "", fmt.Errorf("git: resolving HEAD: %w", err)
	}
	return head.Hash().String(), nil
}

// Dir returns the local checkout directory.
func (s *Source) Dir() string { return s.opts.Dir }

func (s *Source) open() error {
	// Reuse an existing checkout if one is present.
	if repo, err := git.PlainOpen(s.opts.Dir); err == nil {
		s.repo = repo
		return nil
	}

	if err := os.MkdirAll(s.opts.Dir, 0o755); err != nil {
		return fmt.Errorf("git: creating checkout dir: %w", err)
	}

	repo, err := git.PlainClone(s.opts.Dir, false, &git.CloneOptions{
		URL:           s.opts.URL,
		Auth:          s.auth,
		ReferenceName: plumbing.NewBranchReferenceName(s.opts.Branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return fmt.Errorf("git: cloning %s: %w", s.opts.URL, err)
	}
	s.repo = repo
	return nil
}

func (s *Source) fetch() error {
	err := s.repo.Fetch(&git.FetchOptions{
		Auth:     s.auth,
		RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"},
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git: fetching: %w", err)
	}
	return nil
}

func (s *Source) checkout() error {
	wt, err := s.repo.Worktree()
	if err != nil {
		return fmt.Errorf("git: worktree: %w", err)
	}

	remoteRef, err := s.repo.Reference(plumbing.NewRemoteReferenceName("origin", s.opts.Branch), true)
	if err != nil {
		return fmt.Errorf("git: resolving origin/%s: %w", s.opts.Branch, err)
	}

	if err := wt.Checkout(&git.CheckoutOptions{
		Hash:  remoteRef.Hash(),
		Force: true,
	}); err != nil {
		return fmt.Errorf("git: checkout: %w", err)
	}
	return nil
}
