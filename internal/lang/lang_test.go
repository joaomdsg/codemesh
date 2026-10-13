package lang_test

import (
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

func TestLoad_picksTheFrontendThatDetectsTheDir(t *testing.T) {
	t.Parallel()

	s, err := lang.Load("testdata/mixed")
	require.NoError(t, err)
	assert.Equal(t, code.Go, s.Lang)
	assert.Equal(t, "example.com/mixed", s.Module)

	_, err = lang.Load("testdata/mixed/empty")
	assert.Error(t, err, "no frontend reads a dir without a project file")
}

func TestExts_listsTheSourceExtensionsOfEveryFrontend(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{".cjs", ".cts", ".go", ".jl", ".js", ".jsx", ".mjs", ".mts", ".py", ".rs", ".ts", ".tsx"}, lang.Exts())
}
