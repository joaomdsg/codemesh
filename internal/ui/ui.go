// Package ui serves codemesh's pages: the workspace, and per repo the map,
// the dependency matrix, the review queue, the pull requests and the health
// diagnosis. Pages render from app.Analysis and re-render on each app.Event.
package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/workspace"
)

//go:embed style.css
var css string

//go:embed keys.js
var keysJS string

// assets are files served as they are: D3 and the island that drives it.
//
//go:embed assets
var assets embed.FS

// assetURL names a served asset with a hash of its bytes, so it can be cached
// forever and still change with each build.
func assetURL(name string) string {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return "/_codemesh/" + name + "?v=" + hex.EncodeToString(sum[:6])
}

var (
	d3URL       = assetURL("d3.min.js")
	atlasURL    = assetURL("atlas.js")
	diagnoseURL = assetURL("diagnose.js")
)

func serveAssets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.StripPrefix("/_codemesh", http.FileServerFS(assetsRoot)).ServeHTTP(w, r)
}

var assetsRoot = func() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}()

// New returns the app's handler. origin is the URL the browser uses, such as
// http://localhost:7777; actions from any other origin are refused.
func New(a *app.App, origin string) http.Handler {
	r := via.NewRouter(
		via.WithHead(via.Head{
			Lang: "en",
			Raw:  `<meta name="viewport" content="width=device-width, initial-scale=1">`,
			Assets: via.Assets{
				Styles: []via.Style{{Inline: css}},
				// D3 and the island load before Datastar runs any effect.
				Scripts: []via.Script{{Src: d3URL}, {Src: atlasURL}, {Src: diagnoseURL}, {Inline: keysJS}},
			},
		}),
		via.WithTrustedOrigin(origin),
	)
	sh := shell{app: a}
	via.Mount(r, "/", workspacePage{app: a})
	// A page's state is in its path: a live page's stream and actions are
	// addressed by the mount pattern alone, and see no query. A repo's pages
	// are at /r/{repo}/ for its first root, and at /r/{repo}/in/{root}/ for
	// another.
	for _, at := range []string{"/r/{repo}", "/r/{repo}/in/{root}"} {
		via.Mount(r, at+"/", mapPage{shell: sh})
		via.Mount(r, at+"/map/{view}", mapPage{shell: sh})
		via.Mount(r, at+"/deps", depsPage{shell: sh})
		via.Mount(r, at+"/review", reviewPage{shell: sh})
		via.Mount(r, at+"/review/{thread}", reviewPage{shell: sh})
		via.Mount(r, at+"/pr/{n}", reviewPage{shell: sh})
		via.Mount(r, at+"/pr/{n}/{thread}", reviewPage{shell: sh})
		via.Mount(r, at+"/prs", prsPage{shell: sh})
		via.Mount(r, at+"/health", diagnosePage{shell: sh})
		via.Mount(r, at+"/health/{lens}", diagnosePage{shell: sh})
		via.Mount(r, at+"/prognosis/{key}", diagnosePage{shell: sh})
	}
	via.Mount(r, "/basket", basketPage{app: a})
	mux := http.NewServeMux()
	// Browsers ask for a favicon on every page; answer instead of logging 404s.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /_codemesh/", serveAssets)
	mux.HandleFunc("GET /basket.txt", serveBasketText(a))
	mux.Handle("/", r)
	return onlyHosts(mux, origin)
}

// onlyHosts refuses requests addressed to a name other than the origin's or a
// loopback one. A page on any other name that resolves to this machine passes
// the origin checks as same-origin, and could then drive every action.
func onlyHosts(next http.Handler, origin string) http.Handler {
	own := ""
	if u, err := url.Parse(origin); err == nil {
		own = strings.ToLower(u.Hostname())
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(name); err == nil {
			name = h
		}
		name = strings.Trim(name, "[]")
		if name != own && name != "localhost" && !net.ParseIP(name).IsLoopback() {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// shell is the frame every repo page shares: the header, the stamp, and the
// subscription that re-renders the page on each app event.
type shell struct {
	app   *app.App
	repo  workspace.Repo
	root  string // the root in the path, "" for the repo's first
	state *review.State
	Stamp via.State[string]
}

func (s *shell) start(ctx *via.Ctx) error {
	r, ok := s.app.Repo(ctx.Request().PathValue("repo"))
	if !ok {
		return via.ErrNotFound
	}
	st, err := s.app.Reviewed(r.ID)
	if err != nil {
		return err
	}
	s.repo, s.state = r, st
	s.root = ctx.Request().PathValue("root")
	s.Stamp.Set(s.stamp())
	ctx.Listen(s.app.Events, s.onEvent)
	return nil
}

func (s *shell) onEvent(_ *via.Ctx, _ app.Event) { s.Stamp.Set(s.stamp()) }

func (s *shell) stamp() string {
	return "analysed " + s.app.Updated(s.repo.ID).Format("15:04:05")
}

// analysis is the repo's current root at its default revision.
func (s *shell) analysis() *app.Analysis { return s.app.Analyse(s.repo.ID, s.root, "") }

// page renders the analysis with body, or says why there is none.
func (s *shell) page(active tab, body func(*app.Analysis) h.H) h.H {
	a := s.analysis()
	defer a.Release()
	if a.Snap == nil {
		return s.unavailable(active, a)
	}
	return body(a)
}

// withSnap runs fn on the analysis, if there is one to act on.
func (s *shell) withSnap(fn func(*app.Analysis)) {
	a := s.analysis()
	defer a.Release()
	if a.Snap != nil {
		fn(a)
	}
}

// base is the URL prefix of the repo's pages in the root being viewed.
func (s *shell) base() string { return repoBase(s.repo.ID, s.root) }

// repoBase is the URL prefix of a repo's pages in root. The repo's top
// directory, "." in a path, is the first root and needs no prefix.
func repoBase(repo, root string) string {
	b := "/r/" + url.PathEscape(repo) + "/"
	if root == "" || root == "." {
		return b
	}
	return b + "in/" + url.PathEscape(root) + "/"
}

// href is the repo page at path, in the root being viewed.
func (s *shell) href(path string) string { return s.base() + path }

func (s *shell) mapURL(lens, in, decl string) mapURL {
	return mapURL{repo: s.repo.ID, root: s.root, lens: lens, in: in, decl: decl}
}

type tab int

const (
	tabMap tab = iota
	tabDeps
	tabReview
	tabPRs
	tabHealth
)

func (s *shell) frame(active tab, a *app.Analysis, main ...h.H) h.H {
	module := s.repo.Name
	if a != nil && a.Snap != nil {
		module = a.Snap.Module
	}
	link := func(t tab, path, label string, extra h.H) h.H {
		cls := "tab"
		if t == active {
			cls = "tab on"
		}
		return h.A(h.Class(cls), h.Href(s.href(path)), h.Str(label), extra)
	}
	var pending h.H
	if n := s.app.Unreviewed(s.repo.ID); n > 0 {
		pending = h.Span(h.Class("count"), h.Title("units not yet reviewed"), h.Str(n))
	}
	var banner h.H
	switch {
	case a != nil && a.Err != nil:
		banner = h.Div(h.Class("banner err"), h.Role("alert"), h.Str("Load failed: "+a.Err.Error()+". Fix the error and the page reloads."))
	case a != nil && a.Snap != nil:
		banner = typeErrors(a)
		// The review lists nested modules' files; the map and matrix lack them.
		if active != tabReview {
			banner = group([]h.H{banner, nestedModules(a)})
		}
	}
	return h.Div(h.Class("app"),
		h.Header(h.Class("top"),
			h.A(h.Class("brand"), h.Href("/"), h.Str("codemesh")),
			s.switcher(),
			h.Span(h.Class("module"), h.Str(module)),
			s.rootSwitcher(a),
			h.Nav(h.Class("tabs"),
				link(tabMap, "", "Map", nil),
				link(tabDeps, "deps", "Dependencies", nil),
				link(tabReview, "review", "Review", pending),
				link(tabPRs, "prs", "Pull requests", nil),
				link(tabHealth, "health", "Health", nil),
			),
			basketLink(s.app),
			h.Span(h.Class("stamp"), h.Aria("live", "polite"), s.Stamp.Display()),
		),
		banner,
		h.Main(append([]h.H{h.Class("main")}, main...)...),
	)
}

// switcher lists the workspace's repos, so one click moves between them.
func (s *shell) switcher() h.H {
	repos, _ := s.app.Repos()
	var rows []h.H
	for _, r := range repos {
		cls := ""
		if r.ID == s.repo.ID {
			cls = "on"
		}
		rows = append(rows, h.Li(h.A(h.Class(cls), h.Href("/r/"+url.PathEscape(r.ID)+"/"), h.Str(r.Name))))
	}
	rows = append(rows, h.Li(h.Class("switch-all"), h.A(h.Href("/"), h.Str("All repos"))))
	return h.Details(h.Class("switch"),
		h.Summary(h.Title("Switch repo"), h.Str(s.repo.Name)),
		h.Ul(append([]h.H{h.Class("switch-list")}, rows...)...),
	)
}

// rootLabel names a root: its directory, or for the repo's own root the last
// element of its module path when a, the analysis on screen, is that root. The
// repo's name stands in for a root the page has not loaded.
func (s *shell) rootLabel(dir string, a *app.Analysis) string {
	switch {
	case dir != ".":
		return dir
	case a != nil && a.Snap != nil && a.Root == ".":
		return path.Base(a.Snap.Module)
	}
	return s.repo.Name
}

// rootSwitcher offers the repo's other projects, when it has several.
func (s *shell) rootSwitcher(a *app.Analysis) h.H {
	roots := s.app.Roots(s.repo.ID)
	if len(roots) < 2 {
		return nil
	}
	var kids []h.H
	for _, r := range roots {
		cls := "seg"
		if a != nil && r.Dir == a.Root {
			cls = "seg on"
		}
		kids = append(kids, h.A(h.Class(cls), h.Href(repoBase(s.repo.ID, r.Dir)), h.Title(string(r.Lang)), h.Str(s.rootLabel(r.Dir, a))))
	}
	return h.Nav(append([]h.H{h.Class("segs roots"), h.Aria("label", "Project")}, kids...)...)
}

// basketLink shows how many items wait in the handoff basket.
func basketLink(a *app.App) h.H {
	return h.A(h.Class("tab basket"), h.Href("/basket"), h.Str("Basket"), h.Span(h.Class("count"), h.Str(len(a.Basket().Items))))
}

// unavailable is the page body while there is no snapshot to show.
func (s *shell) unavailable(active tab, a *app.Analysis) h.H {
	if errors.Is(a.Err, app.ErrNoRoot) {
		return s.frame(active, a, h.P(h.Class("empty"), h.Str("No such project in this repo: "+s.root+".")))
	}
	if a.Err != nil {
		return s.frame(active, a, h.P(h.Class("empty"), h.Str("Nothing to show until the code loads.")))
	}
	return s.frame(active, a, h.P(h.Class("empty"), h.Str("Analysing the repo…")))
}

// pkgName is a package's directory, or the module's last element for the
// root package, whose directory "." says nothing.
func pkgName(a *app.Analysis, p *code.Package) string {
	if p.Rel == "." {
		return path.Base(a.Snap.Module)
	}
	return p.Rel
}

// typeErrors warns of what the loader could not read: files the parser
// rejected, which codemesh is to blame for, and packages that did not
// type-check, which are the code's. Either way the references through them
// are missing, so callers and smells undercount.
func typeErrors(a *app.Analysis) h.H {
	var broken, unparsed []string
	first, firstParse := "", ""
	for _, p := range a.Snap.Packages {
		typed := false
		for _, e := range p.Errors {
			if loc, ok := strings.CutSuffix(e, ": syntax errors"); ok {
				unparsed = append(unparsed, loc)
				if firstParse == "" {
					firstParse = loc
				}
				continue
			}
			if !typed {
				broken, typed = append(broken, pkgName(a, p)), true
			}
			if first == "" {
				first = e
			}
		}
	}
	var banners []h.H
	if len(unparsed) > 0 {
		file, line, _ := strings.Cut(firstParse, ":")
		what := fmt.Sprintf("%s (line %s)", file, line)
		if len(unparsed) > 1 {
			what = fmt.Sprintf("%d files, the first %s", len(unparsed), what)
		}
		banners = append(banners, h.Div(h.Class("banner warn"), h.Role("status"),
			h.Str("▲ codemesh could not parse "+what+", so its callers and smells are missing. The rest of the repo is analysed.")))
	}
	if len(broken) > 0 {
		banners = append(banners, h.Div(h.Class("banner warn"), h.Role("status"),
			h.Str(fmt.Sprintf("▲ %s did not type-check, so callers and smells there may be missing until it builds. First error: %s",
				plural(len(broken), "package"), first))))
	}
	return group(banners)
}

// nestedModules says where the modules inside this one went: Go loads each as
// a module of its own, which the root switcher offers.
func nestedModules(a *app.Analysis) h.H {
	var text string
	switch n := a.Snap.Nested; len(n) {
	case 0:
		return nil
	case 1:
		text = fmt.Sprintf("Nested module %s/ is a separate root: pick it in the header to map it.", n[0])
	default:
		text = fmt.Sprintf("%d nested modules are separate roots: %s/. Pick one in the header to map it.", len(n), strings.Join(n, "/, "))
	}
	return h.Div(h.Class("banner warn"), h.Role("status"), h.Str("▲ "+text))
}

// okLine is the quiet success line: a green check, grey text.
func okLine(text string) h.H {
	return h.P(h.Class("hint"), h.Span(h.Class("ok"), h.Str("✓ ")), h.Str(text))
}

// delta writes "+3 −1", leaving out a zero side.
func delta(added, deleted int) h.H {
	var kids []h.H
	if added > 0 {
		kids = append(kids, h.Span(h.Class("add"), h.Str(fmt.Sprintf("+%d", added))))
	}
	if deleted > 0 {
		kids = append(kids, h.Span(h.Class("del"), h.Str(fmt.Sprintf("−%d", deleted))))
	}
	return group(kids)
}

func group(kids []h.H) h.H { return via.Each(kids, func(k h.H) h.H { return k }) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
