// Package lang is the registry of language frontends: which one reads a
// directory, which roots a repository holds and which files count as source.
package lang

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/lang/golang"
	"github.com/joaomdsg/codemesh/internal/lang/julia"
	"github.com/joaomdsg/codemesh/internal/lang/treesitter"
)

// ErrNoProject means no frontend detects a project in the directory.
var ErrNoProject = errors.New("no analysable project")

var frontends = []struct {
	code.Frontend
	exts []string
}{
	{golang.Frontend{}, []string{".go"}},
	{julia.Frontend{}, []string{".jl"}},
	{treesitter.TypeScript, treesitter.TypeScript.Exts()},
	{treesitter.Python, treesitter.Python.Exts()},
	{treesitter.Rust, treesitter.Rust.Exts()},
}

// Load analyses the project rooted at dir with the first frontend that
// detects one there.
func Load(dir string) (*code.Snapshot, error) { return LoadContext(context.Background(), dir) }

// Close stops the processes frontends keep between loads.
func Close() {
	for _, f := range frontends {
		if c, ok := f.Frontend.(interface{ Close() }); ok {
			c.Close()
		}
	}
}

// LoadContext is Load that stops when ctx ends: a frontend that runs child
// processes kills them.
func LoadContext(ctx context.Context, dir string) (*code.Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range frontends {
		if f.Detect(abs) {
			return f.Load(ctx, abs)
		}
	}
	return nil, fmt.Errorf("%w in %s", ErrNoProject, abs)
}

// Exts returns the source file extensions of every frontend, sorted.
func Exts() []string {
	var out []string
	for _, f := range frontends {
		out = append(out, f.exts...)
	}
	slices.Sort(out)
	return out
}
