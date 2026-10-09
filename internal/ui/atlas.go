package ui

import (
	"encoding/json"
	"maps"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/treemap"
)

// atlas is the whole module laid out for the D3 island: packages holding
// files holding declarations. The server owns the layout; the browser only
// draws, zooms and links it.
type atlas struct {
	At    int64       `json:"at"` // analysis time, so the island redraws only on a new layout
	W     float64     `json:"w"`
	H     float64     `json:"h"`
	Lens  string      `json:"lens,omitempty"` // map only: the lens the heats were taken with
	Tiles []atlasTile `json:"tiles"`
}

type atlasTile struct {
	K          string // "p" package, "f" file, "d" declaration
	X, Y, W, H float64
	Name       string
	Unit       string // review: card id of the unit for this declaration
	Heat       int    // map: lens bucket, 0 to 5
	ID         string // map: package path, file path or decl ID
	Exp        int    // 1 for an exported declaration
}

// MarshalJSON writes a tile as [k, x, y, w, h, name, unit, heat, id, exp]: a
// large module has thousands of tiles, and Datastar posts every signal back
// on each action.
func (t atlasTile) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{t.K, t.X, t.Y, t.W, t.H, t.Name, t.Unit, t.Heat, t.ID, t.Exp})
}

// atlasOf lays the module out in a w × h space and lets mark fill in each
// tile's page-specific fields; exactly one of pkg, f, d is the tile's own
// level, the others are its parents.
func atlasOf(a *live.Analysis, w, h float64, mark func(i int, t *atlasTile, pkg *code.Package, f *code.File, d *code.Decl)) atlas {
	out := atlas{At: a.At.UnixMilli(), W: w, H: h, Tiles: []atlasTile{}}
	add := func(t atlasTile, pkg *code.Package, f *code.File, d *code.Decl) {
		mark(len(out.Tiles), &t, pkg, f, d)
		out.Tiles = append(out.Tiles, t)
	}
	var pkgs []treemap.Item
	byPath := map[string]*code.Package{}
	for _, p := range a.Snap.Packages {
		if n := p.Lines(); n > 0 {
			pkgs = append(pkgs, treemap.Item{ID: p.Path, Weight: float64(n)})
			byPath[p.Path] = p
		}
	}
	for _, pt := range treemap.Layout(pkgs, treemap.Rect{W: w, H: h}) {
		p := byPath[pt.ID]
		add(tileAt("p", pkgName(a, p), pt.Rect), p, nil, nil)
		var files []treemap.Item
		byFile := map[string]*code.File{}
		for _, f := range p.Files {
			if !f.Test && f.Lines > 0 {
				files = append(files, treemap.Item{ID: f.Path, Weight: float64(f.Lines)})
				byFile[f.Path] = f
			}
		}
		for _, ft := range treemap.Layout(files, belowLabel(pt.Rect.Inset(2), 14)) {
			f := byFile[ft.ID]
			add(tileAt("f", path.Base(f.Path), ft.Rect), p, f, nil)
			var decls []treemap.Item
			byDecl := map[string]*code.Decl{}
			for _, d := range f.Decls {
				decls = append(decls, treemap.Item{ID: d.ID, Weight: float64(max(d.Lines, 1))})
				byDecl[d.ID] = d
			}
			for _, dt := range treemap.Layout(decls, belowLabel(ft.Rect.Inset(1), 12)) {
				d := byDecl[dt.ID]
				t := tileAt("d", d.Name, dt.Rect)
				if d.Exported {
					t.Exp = 1
				}
				add(t, p, f, d)
			}
		}
	}
	return out
}

// belowLabel keeps a strip at the top of a frame for its label, when the
// frame is tall enough to spare it, so a frame's name never sits on its kids'.
func belowLabel(r treemap.Rect, strip float64) treemap.Rect {
	if r.H < 3*strip {
		return r
	}
	return treemap.Rect{X: r.X, Y: r.Y + strip, W: r.W, H: r.H - strip}
}

func tileAt(kind, name string, r treemap.Rect) atlasTile {
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	return atlasTile{K: kind, X: round(r.X), Y: round(r.Y), W: round(r.W), H: round(r.H), Name: name}
}

// feed hands server values to client-only signals: a data-signals attribute
// per value on a hidden element, which Datastar applies on load and again on
// every morph. A SignalCS is never posted back, so the layout stays in the
// browser instead of riding along on each action.
func feed(vals map[expr.Expr]any) h.H {
	kids := []h.H{h.Class("atlas-feed"), h.Hidden(true)}
	for _, ref := range slices.Sorted(maps.Keys(vals)) {
		b, err := json.Marshal(vals[ref])
		if err != nil {
			panic(err)
		}
		kids = append(kids, h.Data("signals:"+strings.TrimPrefix(string(ref), "$"), string(b)))
	}
	return h.Div(kids...)
}

// reviewAtlas lights each declaration that has a review unit with its card id.
func reviewAtlas(a *live.Analysis) atlas {
	units := unitCards(a.Review)
	return atlasOf(a, 900, 600, func(_ int, t *atlasTile, _ *code.Package, _ *code.File, d *code.Decl) {
		if d != nil {
			t.Unit = units[d.ID]
		}
	})
}

// unitCards maps each declaration unit's decl ID to its card id.
func unitCards(rev *review.Review) map[string]string {
	out := map[string]string{}
	for _, u := range rev.Units {
		if u.Kind != "" {
			out[u.ID] = unitID(u.Key)
		}
	}
	return out
}

// reviewedCards lists the card ids of reviewed declaration units. It is its
// own signal, so a mark resends a short list, not the whole layout.
func reviewedCards(rev *review.Review, st *review.State) []string {
	out := []string{}
	for _, u := range rev.Units {
		if u.Kind != "" && st.Reviewed(u.Key) {
			out = append(out, unitID(u.Key))
		}
	}
	return out
}
