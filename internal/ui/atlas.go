package ui

import (
	"encoding/json"
	"math"
	"path"

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
	Tiles []atlasTile `json:"tiles"`
}

type atlasTile struct {
	K          string // "p" package, "f" file, "d" declaration
	X, Y, W, H float64
	Name       string
	Unit       string // card id of the review unit for this declaration
}

// MarshalJSON writes a tile as [k, x, y, w, h, name, unit]: a large module
// has thousands of tiles, and Datastar posts every signal back on each action.
func (t atlasTile) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{t.K, t.X, t.Y, t.W, t.H, t.Name, t.Unit})
}

const atlasW, atlasH = 900.0, 600.0

// atlasOf lays the module out. units maps decl IDs to the card id of their
// review unit, so the island can light and link the touched declarations.
func atlasOf(a *live.Analysis, units map[string]string) atlas {
	out := atlas{At: a.At.UnixMilli(), W: atlasW, H: atlasH, Tiles: []atlasTile{}}
	var pkgs []treemap.Item
	byPath := map[string]*code.Package{}
	for _, p := range a.Snap.Packages {
		if n := p.Lines(); n > 0 {
			pkgs = append(pkgs, treemap.Item{ID: p.Path, Weight: float64(n)})
			byPath[p.Path] = p
		}
	}
	for _, pt := range treemap.Layout(pkgs, treemap.Rect{W: atlasW, H: atlasH}) {
		p := byPath[pt.ID]
		out.Tiles = append(out.Tiles, tileAt("p", pkgName(a, p), "", pt.Rect))
		var files []treemap.Item
		byFile := map[string]*code.File{}
		for _, f := range p.Files {
			if !f.Test && f.Lines > 0 {
				files = append(files, treemap.Item{ID: f.Path, Weight: float64(f.Lines)})
				byFile[f.Path] = f
			}
		}
		for _, ft := range treemap.Layout(files, pt.Rect.Inset(2)) {
			f := byFile[ft.ID]
			out.Tiles = append(out.Tiles, tileAt("f", path.Base(f.Path), "", ft.Rect))
			var decls []treemap.Item
			byDecl := map[string]*code.Decl{}
			for _, d := range f.Decls {
				decls = append(decls, treemap.Item{ID: d.ID, Weight: float64(max(d.Lines, 1))})
				byDecl[d.ID] = d
			}
			for _, dt := range treemap.Layout(decls, ft.Rect.Inset(1)) {
				d := byDecl[dt.ID]
				out.Tiles = append(out.Tiles, tileAt("d", d.Name, units[d.ID], dt.Rect))
			}
		}
	}
	return out
}

func tileAt(kind, name, unit string, r treemap.Rect) atlasTile {
	round := func(v float64) float64 { return math.Round(v*10) / 10 }
	return atlasTile{K: kind, X: round(r.X), Y: round(r.Y), W: round(r.W), H: round(r.H), Name: name, Unit: unit}
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
