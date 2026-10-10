package ui

import (
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// diag is the island's input: the atlas layout, a heat per tile for every
// lens, the marks, and the call edges the island walks to ring a selection.
type diag struct {
	At    int64            `json:"at"`
	W     float64          `json:"w"`
	H     float64          `json:"h"`
	Lens  string           `json:"lens"`
	Tiles []atlasTile      `json:"tiles"`
	Heats map[string][]int `json:"heats"`
	Marks []mark           `json:"marks"`
	Calls [][2]int         `json:"calls"` // [caller tile, callee tile], production code only
	// A view centred on one place: the tile to frame, and the tiles to
	// outline, which are its related places or the code a change touched.
	Focus string   `json:"focus,omitempty"`
	Ring  []string `json:"ring,omitempty"`
	// Exported declarations a change re-signed or removed: their callers
	// may break.
	Broke []string `json:"broke,omitempty"`
}

func (d diag) around(focus string, ring []string) diag {
	d.Focus, d.Ring = focus, ring
	return d
}

type mark struct {
	Tile    int    `json:"t"`
	Level   int    `json:"l"`
	Lens    string `json:"lens"`
	Key     string `json:"key"`
	Title   string `json:"title"`
	Summary string `json:"sum"`
	Name    string `json:"name"`
}

func diagOf(a *live.Analysis, gs []prognosis.Prognosis, lens string) diag {
	s := a.Snap
	busy := prognosis.BusyChurn(s)
	instab := instabilities(s)
	heats := map[string][]int{}
	for _, l := range diagLenses {
		heats[l.key] = nil
	}
	tile := map[string]int{}
	at := atlasOf(a, mapW, mapH, func(i int, t *atlasTile, pkg *code.Package, f *code.File, d *code.Decl) {
		var hv [4]int
		switch {
		case d != nil:
			t.ID = d.ID
			hv = prognosis.Heats(s, d, f.Churn, busy, instab[pkg.Path])
		case f != nil:
			t.ID = f.Path
		default:
			t.ID = pkg.Path
		}
		tile[t.ID] = i
		for j, l := range diagLenses {
			heats[l.key] = append(heats[l.key], hv[j])
		}
	})
	return diag{At: at.At, W: at.W, H: at.H, Lens: lens, Tiles: at.Tiles, Heats: heats, Marks: marksOf(gs, tile), Calls: callsOf(s, tile)}
}

func instabilities(s *code.Snapshot) map[string]float64 {
	out := map[string]float64{}
	for _, r := range depRows(s) {
		out[r.pkg.Path] = r.unstable
	}
	return out
}

// marksOf puts each prognosis on its target's tile.
func marksOf(gs []prognosis.Prognosis, tile map[string]int) []mark {
	out := []mark{}
	for _, g := range gs {
		if i, ok := tile[g.Target]; ok {
			out = append(out, mark{Tile: i, Level: int(g.Level), Lens: string(g.Lens), Key: g.Key, Title: g.Title, Summary: g.Summary, Name: g.Name})
		}
	}
	return out
}

// callsOf lists the call edges between tiles.
func callsOf(s *code.Snapshot, tile map[string]int) [][2]int {
	out := [][2]int{}
	for _, d := range s.Decls() {
		to, ok := tile[d.ID]
		if !ok {
			continue
		}
		for _, c := range d.Callers {
			if from, ok := tile[c]; ok && from != to {
				out = append(out, [2]int{from, to})
			}
		}
	}
	return out
}
