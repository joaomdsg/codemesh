package store_test

import (
	"path/filepath"
	"testing"

	"github.com/joaomdsg/codemesh/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirs_followXDGEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	t.Setenv("XDG_CACHE_HOME", "/x/cache")

	for name, tc := range map[string]struct {
		dir  func() (string, error)
		want string
	}{
		"config": {store.ConfigDir, "/x/config/codemesh"},
		"state":  {store.StateDir, "/x/state/codemesh"},
		"cache":  {store.CacheDir, "/x/cache/codemesh"},
	} {
		got, err := tc.dir()
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

func TestDirs_fallBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Unset and empty are the same to the XDG spec; a relative value is invalid.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "relative/state")
	t.Setenv("XDG_CACHE_HOME", "")

	for name, tc := range map[string]struct {
		dir  func() (string, error)
		want string
	}{
		"config": {store.ConfigDir, filepath.Join(home, ".config", "codemesh")},
		"state":  {store.StateDir, filepath.Join(home, ".local", "state", "codemesh")},
		"cache":  {store.CacheDir, filepath.Join(home, ".cache", "codemesh")},
	} {
		got, err := tc.dir()
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}
