package lang

import (
	"cmp"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/code"
)

// Root is a directory one frontend reads as a project.
type Root struct {
	Dir  string // slash-separated, relative to the repository; "." for the repository itself
	Lang code.Lang
}

// Roots finds every project root below repoDir, sorted by Dir. It skips
// hidden, underscore, testdata, vendor and node_modules directories, as the
// go command does.
func Roots(repoDir string) ([]Root, error) {
	var out []Root
	err := filepath.WalkDir(repoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == repoDir {
				return err
			}
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		if name := d.Name(); p != repoDir && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
			name == "testdata" || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(repoDir, p)
		if err != nil {
			return err
		}
		for _, f := range frontends {
			if f.Detect(p) {
				out = append(out, Root{Dir: filepath.ToSlash(rel), Lang: f.Lang()})
			}
		}
		return nil
	})
	slices.SortStableFunc(out, func(a, b Root) int { return cmp.Compare(a.Dir, b.Dir) })
	return out, err
}

// projectFiles are the names the frontends look for to detect a root.
var projectFiles = []string{"go.mod", "Project.toml", "package.json", "tsconfig.json", "pyproject.toml", "setup.py", "Cargo.toml"}

// RootsIn is Roots for a revision that is not checked out: files are its
// slash-separated paths, as git ls-tree lists them. Detection runs on a scratch
// tree holding only the project files, so it agrees with Roots.
func RootsIn(files []string) ([]Root, error) {
	dir, err := os.MkdirTemp("", "codemesh-roots-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, f := range files {
		if !slices.Contains(projectFiles, path.Base(f)) {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			return nil, err
		}
	}
	return Roots(dir)
}
