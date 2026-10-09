package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChangedDecls_landsOnTheDeclarationsTheLinesFallIn(t *testing.T) {
	ds := []*code.Decl{{ID: "A", Start: 1, End: 5}, {ID: "B", Start: 7, End: 12}}

	// Lines 8 and 9 replaced by three lines, with a line of context each side.
	c := fix.Change{Start: 7, Old: "ctx\nold8\nold9\nctx", New: "ctx\nn1\nn2\nn3\nctx"}
	assert.Equal(t, map[string]int{"B": 2 + 3}, changedDecls(ds, c))

	// Four lines inserted after line 3, inside A.
	c = fix.Change{Start: 2, Old: "l2\nl3\nl4", New: "l2\nl3\na\nb\nc\nd\nl4"}
	assert.Equal(t, map[string]int{"A": 4}, changedDecls(ds, c))

	// Lines added between A and B count for A, the declaration before them.
	c = fix.Change{Start: 5, Old: "l5\nl6", New: "l5\nx\nl6"}
	assert.Equal(t, map[string]int{"A": 1}, changedDecls(ds, c))
}

func TestBroken_listsExportedDeclarationsRemovedOrResigned(t *testing.T) {
	t.Parallel()
	load := func(src string) *code.Snapshot {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.27\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "m.go"), []byte("package m\n\n"+src), 0o644))
		s, err := code.Load(dir)
		require.NoError(t, err)
		return s
	}
	a := load("func Open(a int) {}\nfunc Gone() {}\nfunc Same() {}\nfunc keep(a int) {}\n")
	b := load("func Open(a, b int) {}\nfunc Same() { _ = 1 }\nfunc keep(a, b int) {}\n")

	gone, resigned := broken(a, b)
	assert.Equal(t, []string{"m.Gone"}, gone)
	assert.Equal(t, []string{"m.Open"}, resigned, "a body change keeps the contract; keep is not exported")
}
