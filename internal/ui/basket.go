package ui

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/expr"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/handoff"
)

// basketPage is what the user collected for a coding agent: the items by
// repo and kind, the goal line, and the prompt they add up to.
type basketPage struct {
	app   *app.App
	Intro via.Signal[string]
	err   string
}

func (p *basketPage) OnInit(ctx *via.Ctx) error {
	p.Intro.Set(p.app.Basket().Intro)
	ctx.Listen(p.app.Events, func(*via.Ctx, app.Event) {})
	return nil
}

func (p *basketPage) PageMeta() via.Meta { return via.Meta{Title: "Handoff · codemesh"} }

// SetIntro saves the goal line.
func (p *basketPage) SetIntro(_ *via.Ctx) {
	p.fail(p.app.BasketIntro(strings.TrimSpace(p.Intro.Get())))
}

// Remove drops an item.
func (p *basketPage) Remove(_ *via.Ctx, id string) { p.fail(p.app.BasketRemove(id)) }

// Up swaps an item with the one before it in its group.
func (p *basketPage) Up(_ *via.Ctx, id string) { p.move(id, -1) }

// Down swaps an item with the one after it in its group.
func (p *basketPage) Down(_ *via.Ctx, id string) { p.move(id, 1) }

// move puts the item where its neighbour in the same repo and kind is. The
// prompt lists a group's items in basket order, so only such a swap shows.
func (p *basketPage) move(id string, dir int) {
	items := p.app.Basket().Items
	i := slices.IndexFunc(items, func(it handoff.Item) bool { return it.ID == id })
	if i < 0 {
		return
	}
	for j := i + dir; j >= 0 && j < len(items); j += dir {
		if items[j].Repo == items[i].Repo && items[j].Kind == items[i].Kind {
			p.fail(p.app.BasketMove(id, j))
			return
		}
	}
}

func (p *basketPage) fail(err error) {
	p.err = ""
	if err != nil {
		p.err = "The basket was not saved: " + err.Error()
	}
}

func (p *basketPage) View() h.H {
	groups := p.app.BasketGroups()
	var body h.H = h.P(h.Class("empty"), h.Str("Nothing here yet. Add a finding from a repo's map or health page. Review notes and kept threads land here too."))
	if len(groups) > 0 {
		body = h.Div(h.Class("bk"), p.items(groups), p.prompt())
	}
	var errLine h.H
	if p.err != "" {
		errLine = h.Div(h.Class("banner err"), h.Role("alert"), h.Str(p.err))
	}
	return h.Div(h.Class("app"),
		h.Header(h.Class("top"),
			h.A(h.Class("brand"), h.Href("/"), h.Str("codemesh")),
			h.Span(h.Class("module"), h.Str("handoff")),
			basketLink(p.app),
		),
		errLine,
		h.Main(h.Class("main"), body),
	)
}

func (p *basketPage) items(groups []handoff.Group) h.H {
	var kids []h.H
	for i, g := range groups {
		if i == 0 || g.RepoID != groups[i-1].RepoID {
			kids = append(kids, h.H2(h.Class("bk-repo"), h.Str(g.Repo)))
		}
		var rows []h.H
		for j, it := range g.Items {
			rows = append(rows, p.row(it, j == 0, j == len(g.Items)-1))
		}
		kids = append(kids, h.H3(h.Class("bk-kind"), h.Str(g.Heading)), h.Ul(append([]h.H{h.Class("bk-list")}, rows...)...))
	}
	return h.Section(append([]h.H{h.Class("bk-items")}, kids...)...)
}

func (p *basketPage) row(it handoff.Item, first, last bool) h.H {
	var where []h.H
	if it.Where != "" {
		where = append(where, h.Span(h.Class("loc"), h.Str(it.Where)))
	}
	if it.Scope != "" {
		where = append(where, h.Span(h.Class("chip"), h.Str(scopeLabel(it.Scope))))
	}
	var detail h.H
	if it.Body != "" {
		detail = h.P(h.Class("bk-body"), h.Str(it.Body))
	}
	return h.Li(h.Class("bk-item"),
		h.Div(h.Class("bk-main"),
			h.Span(h.Class("bk-title"), h.Str(it.Title)),
			h.Div(append([]h.H{h.Class("bk-where")}, where...)...),
			detail,
		),
		h.Div(h.Class("bk-acts"),
			h.Button(h.Class("icon"), h.Type("button"), h.Aria("label", "Move up"), h.Title("Move up"), h.Disabled(first), moveBind(first, p.Up, it.ID), h.Str("↑")),
			h.Button(h.Class("icon"), h.Type("button"), h.Aria("label", "Move down"), h.Title("Move down"), h.Disabled(last), moveBind(last, p.Down, it.ID), h.Str("↓")),
			h.Button(h.Class("icon"), h.Type("button"), h.Aria("label", "Remove from handoff"), h.Title("Remove from handoff"), on.Click(on.Bind(p.Remove, it.ID)), h.Str("✕")),
		),
	)
}

// scopeLabel writes an item's scope the way a person says it.
func scopeLabel(scope string) string {
	switch {
	case scope == "wt":
		return "working tree"
	case strings.HasPrefix(scope, "pr:"):
		n, sha, _ := strings.Cut(strings.TrimPrefix(scope, "pr:"), "@")
		return fmt.Sprintf("PR #%s @%s", n, short(sha))
	case strings.HasPrefix(scope, "commit:"):
		return "commit " + strings.TrimPrefix(scope, "commit:")
	}
	return scope
}

// moveBind binds a move, or nothing where the item cannot move that way.
func moveBind(edge bool, fn func(*via.Ctx, string), id string) h.H {
	if edge {
		return nil
	}
	return on.Click(on.Bind(fn, id))
}

// copyPrompt copies the prompt, or selects it for the user to copy where the
// clipboard is off limits (a page not served from localhost, an old browser).
//
// The goal field saves on blur, which the click on Copy causes, so the prompt
// may not hold it yet: Copy waits, for up to two seconds, for the preview to
// be built from the field's text.
var copyPrompt = expr.Raw(`const t = document.getElementById('prompt'), g = document.getElementById('goal'); ` +
	`const done = () => { el.textContent = 'Copied'; setTimeout(() => { el.textContent = 'Copy' }, 1500) }; ` +
	`const saved = async () => { for (let i = 0; i < 20 && t.dataset.goal !== g.value.trim(); i++) await new Promise((r) => setTimeout(r, 100)) }; ` +
	`saved().then(() => navigator.clipboard ? navigator.clipboard.writeText(t.value) : Promise.reject()).then(done, () => { t.select(); document.execCommand('copy') && done() })`)

func (p *basketPage) prompt() h.H {
	return h.Aside(h.Class("bk-prompt"),
		h.Div(h.Class("bk-goal"),
			h.Label(h.Str("Goal"), h.For("goal")),
			h.Input(p.Intro.Bind(), on.Change(p.SetIntro), h.ID("goal"), h.Class("intro"), h.Type("text"), h.AutoComplete("off"),
				h.Placeholder("What should the agent get done?")),
		),
		h.Div(h.Class("bk-copy"),
			h.H2(h.Str("Prompt")),
			h.Button(h.Class("btn"), h.Type("button"), h.Aria("live", "polite"), on.ClickCS(copyPrompt), h.Str("Copy")),
			h.A(h.Class("hint"), h.Href("/basket.txt"), h.Str("as text")),
		),
		h.Textarea(h.ID("prompt"), h.Class("prompt"), h.Data("goal", strings.TrimSpace(p.app.Basket().Intro)), h.ReadOnly(true), h.Aria("label", "Prompt"), h.Rows(28), h.Str(p.app.BasketText())),
	)
}

// serveBasketText answers with the prompt as plain text.
func serveBasketText(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, a.BasketText())
	}
}
