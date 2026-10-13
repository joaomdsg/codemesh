package lang_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoots_findsEveryLanguageRootAndSkipsVendoredDirs(t *testing.T) {
	t.Parallel()

	got, err := lang.Roots("testdata/mixed")
	require.NoError(t, err)

	assert.Equal(t, []lang.Root{
		{Dir: ".", Lang: code.Go},
		{Dir: "julia", Lang: code.Julia},
		{Dir: "tools", Lang: code.Go},
	}, got, "the Go module, the Julia package and the nested module; not testdata, vendor, node_modules or hidden dirs")
}

func TestRoots_anUnreadableDirDoesNotHideTheRootsAfterIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, d := range []string{"a", "b", "z"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	for _, d := range []string{"a", "z"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, d, "go.mod"), []byte("module m\n"), 0o644))
	}
	require.NoError(t, os.Chmod(filepath.Join(dir, "b"), 0))
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "b"), 0o755) })

	got, err := lang.Roots(dir)

	require.NoError(t, err)
	assert.Equal(t, []lang.Root{{Dir: "a", Lang: code.Go}, {Dir: "z", Lang: code.Go}}, got)
}

func TestLoad_picksTheFrontendThatDetectsTheDir(t *testing.T) {
	t.Parallel()

	s, err := lang.Load("testdata/mixed")
	require.NoError(t, err)
	assert.Equal(t, code.Go, s.Lang)
	assert.Equal(t, "example.com/mixed", s.Module)

	_, err = lang.Load("testdata/mixed/empty")
	assert.Error(t, err, "no frontend reads a dir without a project file")
}

func TestLoadContext_aCancelledContextStopsTheLoad(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := lang.LoadContext(ctx, "testdata/mixed")

	assert.Error(t, err)
}

func TestExts_listsTheSourceExtensionsOfEveryFrontend(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{".cjs", ".cts", ".go", ".jl", ".js", ".jsx", ".mjs", ".mts", ".py", ".rs", ".ts", ".tsx"}, lang.Exts())
}
