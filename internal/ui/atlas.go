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
	b := atlasBuilder{a: a, mark: mark, out: atlas{At: a.At.UnixMilli(), W: w, H: h, Tiles: []atlasTile{}}}
	b.packages(treemap.Rect{W: w, H: h})
	return b.out
}

type atlasBuilder struct {
	a    *live.Analysis
	mark func(i int, t *atlasTile, pkg *code.Package, f *code.File, d *code.Decl)
	out  atlas
}

func (b *atlasBuilder) add(t atlasTile, pkg *code.Package, f *code.File, d *code.Decl) {
	b.mark(len(b.out.Tiles), &t, pkg, f, d)
	b.out.Tiles = append(b.out.Tiles, t)
}

func (b *atlasBuilder) packages(r treemap.Rect) {
	size := func(p *code.Package) (string, float64) { return p.Path, float64(p.Lines()) }
	for _, pt := range treemap.Place(b.a.Snap.Packages, r, size) {
		b.add(tileAt("p", pkgName(b.a, pt.Val), pt.Rect), pt.Val, nil, nil)
		b.files(pt.Val, belowLabel(pt.Rect.Inset(2), 14))
	}
}

func (b *atlasBuilder) files(p *code.Package, r treemap.Rect) {
	size := func(f *code.File) (string, float64) {
		if f.Test {
			return f.Path, 0
		}
		return f.Path, float64(f.Lines)
	}
	for _, ft := range treemap.Place(p.Files, r, size) {
		b.add(tileAt("f", path.Base(ft.Val.Path), ft.Rect), p, ft.Val, nil)
		b.decls(p, ft.Val, belowLabel(ft.Rect.Inset(1), 12))
	}
}

func (b *atlasBuilder) decls(p *code.Package, f *code.File, r treemap.Rect) {
	// A declaration with no code lines still gets a tile.
	size := func(d *code.Decl) (string, float64) { return d.ID, float64(max(d.Lines, 1)) }
	for _, dt := range treemap.Place(f.Decls, r, size) {
		t := tileAt("d", dt.Val.Name, dt.Rect)
		if dt.Val.Exported {
			t.Exp = 1
		}
		b.add(t, p, f, dt.Val)
	}
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
