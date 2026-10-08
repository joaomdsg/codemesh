// Package ui serves codemesh's pages: the map, the dependency matrix and the
// review queue. Pages render from the latest live.Analysis and re-render when
// a new one is published.
package ui

import (
	_ "embed"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
)

//go:embed style.css
var css string

//go:embed keys.js
var keysJS string

// New returns the app's handler. origin is the URL the browser uses, such as
// http://localhost:7777; actions from any other origin are refused.
func New(src *live.Source, state *review.State, origin string) http.Handler {
	r := via.NewRouter(
		via.WithHead(via.Head{
			Lang: "en",
			Raw:  `<meta name="viewport" content="width=device-width, initial-scale=1">`,
			Assets: via.Assets{
				Styles:  []via.Style{{Inline: css}},
				Scripts: []via.Script{{Inline: keysJS}},
			},
		}),
		via.WithTrustedOrigin(origin),
	)
	shell := shell{src: src, state: state}
	via.Mount(r, "/", MapPage{shell: shell})
	via.Mount(r, "/deps", DepsPage{shell: shell})
	via.Mount(r, "/review", ReviewPage{shell: shell})
	mux := http.NewServeMux()
	// Browsers ask for a favicon on every page; answer instead of logging 404s.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/", r)
	return mux
}

// shell is the frame every page shares: the header, the live stamp, and the
// subscription that re-renders the page on a new analysis.
type shell struct {
	src   *live.Source
	state *review.State
	Stamp via.State[string]
}

func (s *shell) start(ctx *via.Ctx) {
	s.Stamp.Set(stamp(s.src.Current()))
	ctx.Listen(s.src.Updates, s.onUpdate)
}

func (s *shell) onUpdate(_ *via.Ctx, _ int64) { s.Stamp.Set(stamp(s.src.Current())) }

func stamp(a *live.Analysis) string {
	if a == nil {
		return "analysing…"
	}
	if a.Err != nil {
		return "load failed " + a.At.Format("15:04:05")
	}
	return fmt.Sprintf("analysed %s in %s", a.At.Format("15:04:05"), a.Took.Round(10*time.Millisecond))
}

type tab int

const (
	tabMap tab = iota
	tabDeps
	tabReview
)

func (s *shell) frame(active tab, a *live.Analysis, main ...h.H) h.H {
	module := ""
	if a != nil && a.Snap != nil {
		module = a.Snap.Module
	}
	link := func(t tab, href, label string, extra h.H) h.H {
		cls := "tab"
		if t == active {
			cls = "tab on"
		}
		return h.A(h.Class(cls), h.Href(href), h.Str(label), extra)
	}
	var pending h.H
	if n := s.open(a); n > 0 {
		pending = h.Span(h.Class("count"), h.Title("units not yet reviewed"), h.Str(n))
	}
	var failure h.H
	if a != nil && a.Err != nil {
		failure = h.Div(h.Class("banner err"), h.Str("Load failed: "+a.Err.Error()+". Showing the last good analysis; fix the error and it reloads."))
	}
	return h.Div(h.Class("app"),
		h.Header(h.Class("top"),
			h.A(h.Class("brand"), h.Href("/"), h.Str("codemesh")),
			h.Span(h.Class("module"), h.Str(module)),
			h.Nav(h.Class("tabs"),
				link(tabMap, "/", "Map", nil),
				link(tabDeps, "/deps", "Dependencies", nil),
				link(tabReview, "/review", "Review", pending),
			),
			h.Span(h.Class("stamp"), h.Aria("live", "polite"), s.Stamp.Display()),
		),
		failure,
		h.Main(append([]h.H{h.Class("main")}, main...)...),
	)
}

// open counts the units not yet marked reviewed.
func (s *shell) open(a *live.Analysis) int {
	if a == nil || a.Review == nil {
		return 0
	}
	n := 0
	for _, u := range a.Review.Units {
		if !s.state.Reviewed(u.Key) {
			n++
		}
	}
	return n
}

// pkgName is a package's directory, or the module's last element for the
// root package, whose directory "." says nothing.
func pkgName(a *live.Analysis, p *code.Package) string {
	if p.Rel == "." {
		return path.Base(a.Snap.Module)
	}
	return p.Rel
}

func group(kids []h.H) h.H { return via.Each(kids, func(k h.H) h.H { return k }) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
