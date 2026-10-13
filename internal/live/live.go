// Package live watches the working trees of local repos and keeps the
// engine's snapshots of them current, announcing each change on a topic.
package live

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
)

// fingerprint hashes the names, sizes and mtimes of the files analysis reads,
// plus HEAD, so commits and checkouts count as changes.
func fingerprint(dir string, repo *gitx.Repo) string {
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && unwatchedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !watched(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	if repo != nil {
		head, _ := repo.Head()
		fmt.Fprintln(h, head)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// unwatchedDir reports whether analysis skips a directory: hidden ones,
// vendored code and test data.
func unwatchedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules"
}

// watched reports whether a file's edits change the analysis: sources of any
// registered language and the files that name their dependencies.
func watched(name string) bool {
	switch name {
	case "go.mod", "go.sum", "Project.toml", "Manifest.toml",
		"package.json", "tsconfig.json", "pyproject.toml", "setup.py", "Cargo.toml", "Cargo.lock":
		return true
	}
	return slices.ContainsFunc(lang.Exts(), func(ext string) bool { return strings.HasSuffix(name, ext) })
}
