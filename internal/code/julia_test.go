package code

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadShapes(t *testing.T) *Snapshot {
	t.Helper()
	if _, err := exec.LookPath("julia"); err != nil {
		t.Skip("julia is not installed")
	}
	s, err := Load("testdata/jl")
	require.NoError(t, err)
	return s
}

func TestLoadJulia_mapsModulesFilesAndEachMethod(t *testing.T) {
	t.Parallel()
	s := loadShapes(t)

	assert.Equal(t, Julia, s.Lang)
	assert.Equal(t, "Shapes", s.Module)
	require.Len(t, s.Packages, 1, "Units lives inside Shapes.jl, so it stays in Shapes' package")
	var files []string
	for _, f := range s.Packages[0].Files {
		files = append(files, f.Path)
	}
	assert.Equal(t, []string{"src/Shapes.jl", "src/circle.jl", "src/square.jl", "test/runtests.jl"}, files, "include() decides the files")

	sq, ci := s.Decl("Shapes.area(Square)"), s.Decl("Shapes.area(Circle)")
	require.NotNil(t, sq, "each method is a declaration of its own")
	require.NotNil(t, ci)
	assert.True(t, sq.Exported)
	assert.Equal(t, 5, sq.Start, "the docstring is part of it")
	assert.Equal(t, "src/square.jl", sq.File)

	checked := s.Decl("Shapes._checked(Any)")
	require.NotNil(t, checked)
	assert.False(t, checked.Exported)
	assert.Equal(t, 3, checked.Complexity, "one if and one &&")
	assert.Equal(t, []string{"Shapes.area(Circle)", "Shapes.area(Square)"}, checked.Callers)

	assert.Equal(t, Method, s.Decl("Shapes.Base.show(IO, Circle)").Kind, "a method of Base's function, called by dispatch")
	assert.NotNil(t, s.Decl("Shapes.Units.metres(Any)"), "a submodule's names are qualified")
}

func TestLoadJulia_matchesTestCallsByNameAndThroughAliases(t *testing.T) {
	t.Parallel()
	s := loadShapes(t)

	tests := func(id string) (n int) {
		for _, c := range s.Decl(id).Callers {
			if s.Decl(c).Test {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, tests("Shapes.area(Square)"), "the square test set calls area")
	assert.Equal(t, 1, tests("Shapes.area(Circle)"), "by name, a call reaches every method")
	assert.Equal(t, 1, tests("Shapes.Units.metres(Any)"), "S.Units.metres goes through const S = Shapes")
}
