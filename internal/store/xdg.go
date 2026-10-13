// Package store locates codemesh's per-user directories and writes its JSON
// files atomically.
package store

import (
	"os"
	"path/filepath"
)

// ConfigDir holds what the user chose: the repo registry.
func ConfigDir() (string, error) { return dir("XDG_CONFIG_HOME", ".config") }

// StateDir holds what the user produced: notes, threads, the basket.
func StateDir() (string, error) { return dir("XDG_STATE_HOME", ".local", "state") }

// CacheDir holds what can be rebuilt: clones and worktrees.
func CacheDir() (string, error) { return dir("XDG_CACHE_HOME", ".cache") }

// dir applies the XDG rules: an unset, empty or relative variable is ignored.
func dir(env string, fallback ...string) (string, error) {
	if base := os.Getenv(env); filepath.IsAbs(base) {
		return filepath.Join(base, "codemesh"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append(append([]string{home}, fallback...), "codemesh")...), nil
}
