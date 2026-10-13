// Package ghx lists open pull requests through the gh CLI. It never touches git.
package ghx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	// ErrNoGH means the gh binary is not on PATH.
	ErrNoGH = errors.New("gh is not installed")
	// ErrNotAuthed means gh is installed but not logged in (exit code 4).
	ErrNotAuthed = errors.New("gh is not authenticated")
)

// PR is one open pull request.
type PR struct {
	Number         int
	Title          string
	Author         string
	HeadRef        string
	BaseRef        string
	HeadOID        string
	BaseOID        string
	URL            string
	Draft          bool
	UpdatedAt      time.Time
	ChangedFiles   int
	Additions      int
	Deletions      int
	ReviewDecision string
}

// PRs lists the open pull requests of the repository at repoDir.
type PRs interface {
	List(ctx context.Context, repoDir string) ([]PR, error)
}

// CLI is PRs backed by `gh pr list`.
type CLI struct{}

const fields = "number,title,author,headRefName,baseRefName,headRefOid,baseRefOid,url,isDraft,updatedAt,changedFiles,additions,deletions,reviewDecision"

type wirePR struct {
	Number         int                    `json:"number"`
	Title          string                 `json:"title"`
	Author         struct{ Login string } `json:"author"`
	HeadRefName    string                 `json:"headRefName"`
	BaseRefName    string                 `json:"baseRefName"`
	HeadRefOid     string                 `json:"headRefOid"`
	BaseRefOid     string                 `json:"baseRefOid"`
	URL            string                 `json:"url"`
	IsDraft        bool                   `json:"isDraft"`
	UpdatedAt      time.Time              `json:"updatedAt"`
	ChangedFiles   int                    `json:"changedFiles"`
	Additions      int                    `json:"additions"`
	Deletions      int                    `json:"deletions"`
	ReviewDecision string                 `json:"reviewDecision"`
}

func (CLI) List(ctx context.Context, repoDir string) ([]PR, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return nil, ErrNoGH
	}
	cmd := exec.CommandContext(ctx, gh, "pr", "list", "--state", "open", "--json", fields)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ee.ExitCode() == 4 {
			return nil, fmt.Errorf("%w: %s", ErrNotAuthed, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("gh pr list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var wire []wirePR
	if err := json.Unmarshal(stdout.Bytes(), &wire); err != nil {
		return nil, fmt.Errorf("gh pr list: parse output: %w", err)
	}
	prs := make([]PR, len(wire))
	for i, w := range wire {
		prs[i] = PR{
			Number: w.Number, Title: w.Title, Author: w.Author.Login,
			HeadRef: w.HeadRefName, BaseRef: w.BaseRefName,
			HeadOID: w.HeadRefOid, BaseOID: w.BaseRefOid,
			URL: w.URL, Draft: w.IsDraft, UpdatedAt: w.UpdatedAt,
			ChangedFiles: w.ChangedFiles, Additions: w.Additions, Deletions: w.Deletions,
			ReviewDecision: w.ReviewDecision,
		}
	}
	return prs, nil
}
