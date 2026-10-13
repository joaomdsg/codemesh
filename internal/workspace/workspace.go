// Package workspace is the registry of repositories the user pointed codemesh
// at: local checkouts, and git URLs cloned into the cache.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/store"
)

const version = 1

// Root is one analysable root inside a repository.
type Root struct {
	Dir  string `json:"dir"`
	Lang string `json:"lang"`
}

// Repo is one registry entry. A local repo has Path; a cloned one has URL and
// ClonePath.
type Repo struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path,omitempty"`
	URL         string    `json:"url,omitempty"`
	ClonePath   string    `json:"clonePath,omitempty"`
	DefaultBase string    `json:"defaultBase,omitempty"`
	Roots       []Root    `json:"roots,omitempty"`
	AddedAt     time.Time `json:"addedAt"`
}

// Dir is the directory holding the repository's files.
func (r Repo) Dir() string {
	if r.Path != "" {
		return r.Path
	}
	return r.ClonePath
}

type registry struct {
	Version int    `json:"version"`
	Repos   []Repo `json:"repos"`
}

// Workspace reads and writes the registry file under the user's config
// directory. It keeps nothing in memory, so separate values see each other's
// changes.
type Workspace struct {
	file   string
	clones string
	add    sync.Mutex // one Add at a time, so a repo is never cloned twice
	mu     sync.Mutex // guards the registry file
}

// Open returns the workspace for the current XDG environment.
func Open() (*Workspace, error) {
	cfg, err := store.ConfigDir()
	if err != nil {
		return nil, err
	}
	cache, err := store.CacheDir()
	if err != nil {
		return nil, err
	}
	return &Workspace{file: filepath.Join(cfg, "workspace.json"), clones: filepath.Join(cache, "clones")}, nil
}

// Repos lists the registered repositories in the order they were added.
func (w *Workspace) Repos() ([]Repo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	reg, err := w.load()
	return reg.Repos, err
}

func (w *Workspace) load() (registry, error) {
	var reg registry
	if err := store.ReadJSON(w.file, &reg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return registry{}, err
	}
	return reg, nil
}

// findRepo finds the entry for r: one with its ID, or one whose clone sits
// where r's would, as another spelling of one URL does.
func (w *Workspace) findRepo(r Repo) (Repo, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	reg, err := w.load()
	if err != nil {
		return Repo{}, false, err
	}
	i := slices.IndexFunc(reg.Repos, func(have Repo) bool {
		return have.ID == r.ID || r.ClonePath != "" && have.ClonePath == r.ClonePath
	})
	if i < 0 {
		return Repo{}, false, nil
	}
	return reg.Repos[i], true, nil
}

// Add registers a local path or a git URL and returns the entry. A URL is
// cloned into the cache first. Adding what is already registered changes
// nothing and returns the existing entry.
func (w *Workspace) Add(arg string) (Repo, error) {
	w.add.Lock()
	defer w.add.Unlock()

	var r Repo
	if u, ok := parseURL(arg); ok {
		if slices.Contains([]string{u.host, u.owner, u.name}, "..") {
			return Repo{}, fmt.Errorf("not a usable git URL: %s", arg)
		}
		r = Repo{ID: id(u.name, u.url), Name: u.name, URL: u.url, ClonePath: filepath.Join(w.clones, u.host, u.owner, u.name)}
	} else {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return Repo{}, err
		}
		repo, err := gitx.Open(abs)
		if err != nil {
			return Repo{}, err
		}
		name := filepath.Base(repo.Dir)
		r = Repo{ID: id(name, repo.Dir), Name: name, Path: repo.Dir}
	}
	if have, ok, err := w.findRepo(r); err != nil || ok {
		return have, err
	}
	if r.URL != "" {
		if _, err := gitx.Clone(r.URL, r.ClonePath); err != nil {
			return Repo{}, err
		}
	}
	r.AddedAt = time.Now().UTC()

	w.mu.Lock()
	defer w.mu.Unlock()
	reg, err := w.load()
	if err != nil {
		return Repo{}, err
	}
	reg.Version = version
	reg.Repos = append(reg.Repos, r)
	return r, store.WriteJSON(w.file, reg)
}

// Remove forgets a repository and deletes its clone, if it has one. A local
// checkout is never touched. An unknown id is a no-op.
func (w *Workspace) Remove(id string) error {
	w.add.Lock()
	defer w.add.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()

	reg, err := w.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(reg.Repos, func(r Repo) bool { return r.ID == id })
	if i < 0 {
		return nil
	}
	gone := reg.Repos[i]
	reg.Version = version
	reg.Repos = slices.Delete(reg.Repos, i, i+1)
	if err := store.WriteJSON(w.file, reg); err != nil {
		return err
	}
	if gone.ClonePath == "" {
		return nil
	}
	if rel, err := filepath.Rel(w.clones, gone.ClonePath); err != nil || rel == "." || !filepath.IsLocal(rel) {
		return fmt.Errorf("clone path %s is outside %s: left alone", gone.ClonePath, w.clones)
	}
	return os.RemoveAll(gone.ClonePath)
}

// id is the slug of name plus six hex characters of a hash of key, so two
// repos with one name still differ.
func id(name, key string) string {
	sum := sha256.Sum256([]byte(key))
	slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		slug = "repo"
	}
	return slug + "-" + hex.EncodeToString(sum[:])[:6]
}

type remote struct{ url, host, owner, name string }

var scp = regexp.MustCompile(`^[^@/\s]+@([^:/\s]+):(.+)$`)

// IsURL reports whether arg is a git URL rather than a local path.
func IsURL(arg string) bool {
	_, ok := parseURL(arg)
	return ok
}

// parseURL reports whether arg is a git URL: scheme://..., or the scp form
// user@host:owner/name. Everything else is a local path.
func parseURL(arg string) (remote, bool) {
	var host, p string
	if strings.Contains(arg, "://") {
		u, err := url.Parse(arg)
		if err != nil {
			return remote{}, false
		}
		host, p = u.Hostname(), u.Path
	} else if m := scp.FindStringSubmatch(arg); m != nil {
		host, p = m[1], m[2]
	} else {
		return remote{}, false
	}
	p = strings.TrimSuffix(strings.TrimRight(p, "/"), ".git")
	name := path.Base(p)
	if name == "." || name == "/" {
		return remote{}, false
	}
	owner := path.Base(path.Dir(p))
	if owner == "." || owner == "/" {
		owner = "_"
	}
	if host == "" {
		host = "local"
	}
	return remote{url: arg, host: host, owner: owner, name: name}, true
}
