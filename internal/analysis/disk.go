package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/smell"
	"github.com/joaomdsg/codemesh/internal/store"
)

// Store keeps snapshots across restarts.
type Store interface {
	// Load returns key's snapshot, or an error satisfying
	// errors.Is(err, fs.ErrNotExist) when there is none to use.
	Load(key Key) (*Saved, error)
	Save(key Key, s *Saved) error
	// Forget removes every snapshot of repo.
	Forget(repo string) error
}

// Saved is a snapshot as stored, with the basis it was read from.
type Saved struct {
	Basis    string
	Snap     *code.Snapshot
	Findings []smell.Finding
	Took     time.Duration
	At       time.Time
}

// keptCommits is how many commit snapshots of one root a Disk keeps, the
// ones read or written last: the default branch and the review base, with
// room for a pull request or a branch switch.
const keptCommits = 4

// Disk is a Store of one file per repo, root and rev under a directory. A
// file written by another build of codemesh reads as none, since loaders
// and smells change what a snapshot holds; a build that cannot hash its
// executable reads and writes none.
type Disk struct {
	dir     string
	version string
}

// NewDisk returns a Store under dir.
func NewDisk(dir string) *Disk { return &Disk{dir: dir, version: buildVersion()} }

type savedFile struct {
	Version string
	Saved
}

func (d *Disk) Load(key Key) (*Saved, error) {
	if d.version == "" {
		return nil, fmt.Errorf("unknown build: %w", fs.ErrNotExist)
	}
	var f savedFile
	if err := store.ReadJSON(d.path(key), &f); err != nil {
		return nil, err
	}
	if f.Version != d.version || f.Snap == nil {
		return nil, fmt.Errorf("snapshot of another build: %w", fs.ErrNotExist)
	}
	// prune keeps what was used last.
	now := time.Now()
	_ = os.Chtimes(d.path(key), now, now)
	f.Snap.Reindex()
	return &f.Saved, nil
}

func (d *Disk) Save(key Key, s *Saved) error {
	if d.version == "" {
		return nil
	}
	if err := store.WriteJSON(d.path(key), savedFile{Version: d.version, Saved: *s}); err != nil {
		return err
	}
	if key.Rev != Worktree {
		d.prune(key)
	}
	return nil
}

func (d *Disk) Forget(repo string) error { return os.RemoveAll(filepath.Join(d.dir, repo)) }

// path is <dir>/<repo>/<root>@<rev>.json, the root's slashes escaped so
// nested roots stay one level down.
func (d *Disk) path(key Key) string {
	root := strings.NewReplacer("%", "%25", "/", "%2F").Replace(key.Root)
	if root == "" {
		root = "."
	}
	return filepath.Join(d.dir, key.Repo, root+"@"+key.Rev+".json")
}

// prune removes all but the keptCommits commit snapshots of key's root read
// or written last.
func (d *Disk) prune(key Key) {
	worktree := filepath.Base(d.path(Key{Repo: key.Repo, Rev: Worktree, Root: key.Root}))
	prefix := strings.TrimSuffix(worktree, Worktree+".json")
	des, err := os.ReadDir(filepath.Join(d.dir, key.Repo))
	if err != nil {
		return
	}
	type file struct {
		name string
		mod  time.Time
	}
	var commits []file
	for _, de := range des {
		n := de.Name()
		if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".json") || n == worktree || strings.Contains(strings.TrimPrefix(n, prefix), "@") {
			continue
		}
		if info, err := de.Info(); err == nil {
			commits = append(commits, file{n, info.ModTime()})
		}
	}
	slices.SortFunc(commits, func(a, b file) int { return b.mod.Compare(a.mod) })
	for _, f := range commits[min(len(commits), keptCommits):] {
		os.Remove(filepath.Join(d.dir, key.Repo, f.name))
	}
}

var buildVersion = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})
