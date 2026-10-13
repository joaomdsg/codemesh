package lang

import (
	"cmp"
	"io/fs"
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
			return err
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
