package ui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The atlas tile's wire form is an array the island indexes by position;
// atlas.js reads [k, x, y, w, h, name, unit, heat, id, exp].
func TestAtlasTile_marshalsAsTheArrayTheIslandReads(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(atlasTile{K: "d", X: 1, Y: 2, W: 3, H: 4.5, Name: "Scale", Unit: "u1", Heat: 3, ID: "m.Scale", Exp: 1})
	require.NoError(t, err)
	assert.JSONEq(t, `["d", 1, 2, 3, 4.5, "Scale", "u1", 3, "m.Scale", 1]`, string(b))
}

func TestAtlas_drawsTestFilesWhenTheRootHoldsNothingElse(t *testing.T) {
	t.Parallel()
	b := code.NewBuilder(code.Julia, "test", "")
	p := b.Package(code.Package{Path: "test"})
	f := b.File(p, code.File{Path: "runtests.jl", Lines: 40, Test: true})
	b.Decl(f, code.Decl{ID: "test.check(Any)", Name: "check", Lines: 10})
	a := &app.Analysis{At: time.Unix(0, 0), Snap: b.Finish()}

	at := atlasOf(a, 100, 100, func(int, *atlasTile, *code.Package, *code.File, *code.Decl) {})
	var drawn []string
	for _, tl := range at.Tiles {
		if tl.W > 0 && tl.H > 0 {
			drawn = append(drawn, tl.K+":"+tl.Name)
		}
	}
	assert.Contains(t, drawn, "f:runtests.jl", "a test environment has no other code to show")
}
