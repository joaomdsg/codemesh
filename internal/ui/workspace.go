package ui

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/prognosis"
)

// workspacePage is the home page: add a repo, the changes waiting to be read
// across every repo, and one card per repo.
type workspacePage struct {
	app   *app.App
	Input via.Signal[string]
	err   string
	// Live is set once the page's stream opens.
	Live via.State[bool]
}

func (p *workspacePage) OnInit(ctx *via.Ctx) error {
	// Every event re-renders the page, which is all a listener has to do.
	ctx.Listen(p.app.Events, func(*via.Ctx, app.Event) {})
	// An event between the first render and the stream's connect reaches
	// nobody. via pushes at connect only a render that moved off the
	// connect's own, so the connect moves it.
	ctx.OnConnect(func() { p.Live.Set(true) })
	return nil
}

func (p *workspacePage) PageMeta() via.Meta { return via.Meta{Title: "codemesh"} }

// Add registers the path or git URL in the input.
func (p *workspacePage) Add(_ *via.Ctx) {
	arg := strings.TrimSpace(p.Input.Get())
	if arg == "" {
		p.err = "Enter a path or a git URL."
		return
	}
	if err := p.app.AddRepo(arg); err != nil {
		p.err = "Could not add " + arg + ": " + err.Error()
		return
	}
	p.err = ""
	p.Input.Set("")
}

// Remove forgets a repo.
func (p *workspacePage) Remove(_ *via.Ctx, id string) {
	if err := p.app.RemoveRepo(id); err != nil {
		p.err = "Could not remove the repo: " + err.Error()
	}
}

// Dismiss clears a failed clone.
func (p *workspacePage) Dismiss(_ *via.Ctx, arg string) { p.app.Dismiss(arg) }

func (p *workspacePage) View() h.H {
	cards := p.app.Cards()
	var inbox h.H
	if len(cards) > 0 {
		inbox = p.inbox(p.app.Inbox())
	}
	var grid h.H
	if len(cards) > 0 {
		grid = h.Div(h.Class("ws-grid"), inbox, p.repos(cards))
	}
	var live h.H
	if p.Live.Get() {
		live = h.Data("live", "")
	}
	return h.Div(h.Class("app"), live,
		h.Header(h.Class("top"),
			h.A(h.Class("brand"), h.Href("/"), h.Str("codemesh")),
			h.Span(h.Class("module"), h.Str("workspace")),
			basketLink(p.app),
		),
		h.Main(h.Class("main ws"),
			p.addForm(len(cards) == 0),
			grid,
		),
	)
}

func (p *workspacePage) addForm(empty bool) h.H {
	var errLine h.H
	if p.err != "" {
		errLine = h.Div(h.Class("banner err"), h.Role("alert"), h.Str(p.err))
	}
	var pending []h.H
	for _, c := range p.app.Pending() {
		if c.Err != nil {
			pending = append(pending, h.Li(h.Class("clone fail"),
				h.Span(h.Str("Clone of "+c.Arg+" failed: "+c.Err.Error())),
				h.Button(h.Class("btn ghost small"), on.Click(on.Bind(p.Dismiss, c.Arg)), h.Str("Dismiss"))))
			continue
		}
		pending = append(pending, h.Li(h.Class("clone"), h.Str("Cloning "+c.Arg+"…")))
	}
	return h.Section(h.Class("ws-add"),
		h.Form(h.Class("add"), on.Submit(p.Add, on.WithPrevent()),
			h.Input(p.Input.Bind(), h.Type("text"), h.Placeholder("Path or git URL"), h.Aria("label", "Repo path or git URL"), h.AutoComplete("off")),
			h.Button(h.Class("btn"), h.Type("submit"), h.Str("Add repo")),
		),
		via.When(empty, func() h.H {
			return h.P(h.Class("hint"), h.Str("Add a repo by path or git URL, e.g. ~/src/app or https://github.com/org/repo"))
		}),
		errLine,
		via.When(len(pending) > 0, func() h.H { return h.Ul(append([]h.H{h.Class("clones")}, pending...)...) }),
	)
}

// inbox groups the changes waiting to be read: those with Contract or Logic
// units still unreviewed first, the rest to skim.
func (p *workspacePage) inbox(items []app.InboxItem) h.H {
	var read, skim []h.H
	for _, it := range items {
		if it.Reading.NeedsReading() {
			read = append(read, inboxRow(it))
		} else {
			skim = append(skim, inboxRow(it))
		}
	}
	if len(items) == 0 {
		return h.Section(h.H2(h.Str("Inbox")), h.P(h.Class("hint"), h.Str("Nothing to read. Open pull requests and uncommitted changes show up here.")))
	}
	var rows []h.H
	for _, g := range []struct {
		title string
		items []h.H
	}{{"Needs reading", read}, {"Skim", skim}} {
		if len(g.items) > 0 {
			rows = append(rows, h.Div(h.Class("inbox-h"), h.Str(g.title)))
			rows = append(rows, g.items...)
		}
	}
	return h.Section(h.H2(h.Str("Inbox")), h.Div(h.Class("inbox"), group(rows)))
}

func inboxRow(it app.InboxItem) h.H {
	rd := it.Reading
	base := "/r/" + url.PathEscape(it.Repo.ID) + "/"
	href, n, title := base+"review", "tree", "Working tree against the base branch"
	var by h.H
	var chips []h.H
	if pr := it.PR; pr != nil {
		href, n, title = fmt.Sprintf("%spr/%d", base, pr.Number), fmt.Sprintf("#%d", pr.Number), pr.Title
		if pr.Author != "" {
			by = h.Span(h.Class("by"), h.Str(" · "+pr.Author))
		}
		if pr.Draft {
			chips = append(chips, h.Span(h.Class("chip"), h.Str("draft")))
		}
	}
	switch {
	case rd.Err != nil:
		chips = append(chips, h.Span(h.Class("chip chip-warn"), h.Title(rd.Err.Error()), h.Str("could not be read")))
	case !rd.Ready:
		chips = append(chips, h.Span(h.Class("chip"), h.Str("analysing…")))
	default:
		for _, c := range []struct {
			n    int
			word string
			cls  string
		}{{rd.Contract, "contract", ""}, {rd.Logic, "logic", ""}, {rd.Introduced, "new smells", "chip-bad"}} {
			if c.n > 0 {
				chips = append(chips, h.Span(h.Class("chip "+c.cls), h.Str(fmt.Sprintf("%s %d", c.word, c.n))))
			}
		}
		if rd.MaxRisk > 0 {
			chips = append(chips, h.Span(h.Class("chip"), h.Title("the riskiest unit's score"), h.Str(fmt.Sprintf("risk %d", rd.MaxRisk))))
		}
	}
	size := []h.H{h.Class("inbox-lines")}
	if added, deleted := changeLines(rd, it.PR); added+deleted > 0 {
		size = append(size, delta(added, deleted))
	}
	if rd.Ready {
		size = append(size, h.Str(" · "+plural(rd.Units, "unit")))
	}
	progress := h.Span(h.Class("hint"), h.Str("—"))
	if rd.Ready && rd.Units > 0 {
		progress = group([]h.H{
			h.Meter(h.Class("progress"), h.RawAttr("min", "0"), h.RawAttr("max", fmt.Sprint(rd.Units)), h.RawAttr("value", fmt.Sprint(rd.Done)), h.Aria("label", "Units reviewed")),
			h.Span(h.Class("hint"), h.Str(fmt.Sprintf("%d/%d", rd.Done, rd.Units))),
		})
	}
	return h.Div(h.Class("inbox-row"),
		h.Span(h.Class("repo"), h.Str(it.Repo.Name)),
		h.Span(h.Class("title"), h.Span(h.Class("n"), h.Str(n)), h.A(h.Class("title-link"), h.Href(href), h.Str(title)), by, h.Br(), h.Span(h.Class("chips"), group(chips))),
		h.Span(size...),
		h.Span(h.Class("read-progress"), progress),
	)
}

func (p *workspacePage) repos(cards []app.Card) h.H {
	if len(cards) == 0 {
		return nil
	}
	var kids []h.H
	for _, c := range cards {
		kids = append(kids, p.card(c))
	}
	return h.Aside(h.Class("ws-repos"), h.H2(h.Str("Repos")), h.Div(append([]h.H{h.Class("cards")}, kids...)...))
}

func (p *workspacePage) card(c app.Card) h.H {
	r := c.Repo
	base := "/r/" + url.PathEscape(r.ID) + "/"
	var langs []h.H
	for _, l := range c.Langs {
		langs = append(langs, h.Span(h.Class("chip"), h.Title(precisionHelp[l.Precision]), h.Str(l.Name+" · "+l.Precision)))
	}
	where := r.Path
	if r.URL != "" {
		where = r.URL
	}
	// The inbox analyses a local repo's working tree; a clone waits to be opened.
	pending := "Not analysed yet."
	if r.Path == "" {
		pending += " Open it to analyse."
	}
	var health h.H = h.P(h.Class("hint"), h.Str(pending))
	if c.Ready {
		health = census(c.Census)
	}
	return h.Article(h.Class("card"),
		h.Div(h.Class("card-head"), h.A(h.Class("card-name"), h.Href(base), h.Str(r.Name)), h.Span(append([]h.H{h.Class("chips")}, langs...)...)),
		h.P(h.Class("loc"), h.Str(where)),
		health,
		prLine(base, c.PRs),
		h.Div(h.Class("card-foot"),
			h.Span(h.Class("hint"), h.Str("changed "+c.Updated.Format("15:04:05"))),
			h.Button(h.Class("btn ghost small"), on.Click(on.Bind(p.Remove, r.ID)),
				h.Title("Forget this repo. A cloned copy is deleted; a local checkout is not touched."), h.Str("Remove")),
		),
	)
}

var precisionHelp = map[string]string{
	"typed": "calls and references are resolved with types",
	"named": "calls and references are matched by name, so callers are estimates",
}

// census counts the places that need attention by urgency.
func census(n [3]int) h.H {
	var kids []h.H
	for i, words := range []string{"fix first", "fix soon", "when convenient"} {
		if n[i] > 0 {
			kids = append(kids, h.Span(h.Class("dx-count"), levelMark(prognosis.Level(3-i)), h.Str(fmt.Sprintf("%d %s", n[i], words))))
		}
	}
	if len(kids) == 0 {
		return okLine("Nothing needs attention.")
	}
	return h.P(append([]h.H{h.Class("dx-census")}, kids...)...)
}

// prLine is the card's line about pull requests: the count, or the one quiet
// line saying what to do when gh cannot list them.
func prLine(base string, l app.PRList) h.H {
	switch {
	case l.Loading:
		return h.P(h.Class("hint"), h.Str("Pull requests: asking gh…"))
	case l.Err != nil:
		return h.P(h.Class("hint"), h.Title(l.Err.Error()), h.Str(l.Hint()))
	}
	return h.P(h.Class("hint"), h.A(h.Href(base+"prs"), h.Str(plural(len(l.PRs), "open pull request"))))
}
