package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/notes"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// anchor is the unit a code block belongs to, so a note written on one of
// its lines knows which declaration it is about.
type anchor struct{ file, decl, key string }

// noteTarget is the line a note is being written on.
type noteTarget struct {
	block, file, decl, key, side string
	line                         int
}

func lineKey(file, side string, line int) string { return fmt.Sprintf("%s|%s|%d", file, side, line) }

// Pick opens the note box on the line the browser named, as block:side+line.
func (p *reviewPage) Pick(_ *via.Ctx) {
	tok := p.Anchor.Get()
	p.Anchor.Set("")
	block, at, ok := strings.Cut(tok, ":")
	if !ok || len(at) < 2 {
		return
	}
	line, err := strconv.Atoi(at[1:])
	a, known := p.anchors[block]
	if err != nil || !known {
		return
	}
	p.noting = &noteTarget{block: block, file: a.file, decl: a.decl, key: a.key, side: at[:1], line: line}
}

// SaveNote writes the note in the box, and puts it in the handoff when the
// box says so.
func (p *reviewPage) SaveNote(_ *via.Ctx) {
	text := strings.TrimSpace(p.NoteText.Get())
	if p.noting == nil || text == "" {
		return
	}
	rd, release, _ := p.read()
	defer release()
	if rd == nil {
		return
	}
	ns, err := p.app.Notes(p.repo.ID)
	if err != nil {
		p.err = "Note not saved: " + err.Error()
		return
	}
	n := p.noting
	saved, err := ns.Add(notes.Note{
		Repo: p.repo.ID, Scope: rd.scope, File: n.file, DeclID: n.decl, UnitKey: n.key,
		Side: n.side, Line: n.line, Body: text, Created: time.Now().UTC(),
	})
	if err != nil {
		p.err = "Note not saved: " + err.Error()
		return
	}
	p.err = ""
	p.noting = nil
	p.NoteText.Set("")
	if p.InHandoff.Get() {
		p.add(p.noteItem(rd, saved))
	}
}

// CancelNote closes the note box.
func (p *reviewPage) CancelNote(_ *via.Ctx) {
	p.noting = nil
	p.NoteText.Set("")
}

// ToggleNote puts a note in the handoff, or takes it out.
func (p *reviewPage) ToggleNote(_ *via.Ctx, id string) {
	for _, it := range p.app.Basket().Items {
		if it.Kind == handoff.Note && it.Key == id && it.Repo == p.repo.ID {
			if err := p.app.BasketRemove(it.ID); err != nil {
				p.err = "Not taken out of the handoff: " + err.Error()
			}
			return
		}
	}
	rd, release, _ := p.read()
	defer release()
	if rd == nil {
		return
	}
	ns, err := p.app.Notes(p.repo.ID)
	if err != nil {
		return
	}
	for _, n := range ns.All() {
		if n.ID == id {
			p.add(p.noteItem(rd, n))
		}
	}
}

// noteItem is a note as the prompt carries it: the line it is on, as written.
func (p *reviewPage) noteItem(rd *reading, n notes.Note) handoff.Item {
	snap := rd.head.Snap
	if n.Side == "-" && rd.base != nil {
		snap = rd.base.Snap
	}
	var lines []string
	if src := p.lines(snap, n.File); n.Line >= 1 && n.Line <= len(src) {
		lines = []string{src[n.Line-1]}
	}
	title, _, _ := strings.Cut(n.Body, "\n")
	if r := []rune(title); len(r) > 70 {
		title = string(r[:69]) + "…"
	}
	body := n.Body
	if body == title {
		body = ""
	}
	return handoff.Item{
		Kind: handoff.Note, Repo: p.repo.ID, Scope: n.Scope, Title: title,
		Where: fmt.Sprintf("%s:%d", n.File, n.Line), Body: body, Code: lines, Key: n.ID,
	}
}

// notesByLine is the reading's notes by the line they are on.
func (p *reviewPage) notesByLine(rd *reading) map[string][]notes.Note {
	out := map[string][]notes.Note{}
	ns, err := p.app.Notes(p.repo.ID)
	if err != nil {
		return out
	}
	for _, n := range ns.All() {
		if n.Scope == rd.scope && !n.Resolved {
			k := lineKey(n.File, n.Side, n.Line)
			out[k] = append(out[k], n)
		}
	}
	return out
}

// lineExtras is what sits under a code line: its notes, and the note box
// when one is being written there.
func (p *reviewPage) lineExtras(block, file, side string, line int) h.H {
	var kids []h.H
	for _, n := range p.notesAt[lineKey(file, side, line)] {
		if !p.shown[n.ID] {
			kids = append(kids, p.noteRow(n))
		}
	}
	if t := p.noting; t != nil && t.block == block && t.side == side && t.line == line {
		kids = append(kids, p.composer())
	}
	if len(kids) == 0 {
		return nil
	}
	return group(kids)
}

func (p *reviewPage) noteRow(n notes.Note) h.H {
	p.shown[n.ID] = true
	cls, label, action := "margin-note", "Add this note to the handoff", "Add to handoff"
	if p.inBasket(handoff.Note, n.ID, n.Scope) {
		cls += " kept"
		label, action = "Take this note out of the handoff", "In handoff ✓"
	}
	var stale h.H
	if notes.Outdated(n, p.unitKeys) {
		stale = h.Span(h.Class("chip chip-smell"), h.Title("The code under this note changed"), h.Str("outdated"))
	}
	return h.Div(h.Class(cls),
		h.Span(h.Class("mn-body"), h.Str(n.Body)), stale,
		h.Button(h.Class("tool"), h.Title(label), on.Click(on.Bind(p.ToggleNote, n.ID)), h.Str(action)))
}

func (p *reviewPage) composer() h.H {
	return h.Div(h.Class("note-form"), h.Role("group"), h.Aria("label", "Note"),
		h.Input(h.Type("text"), h.Class("note-text"), h.Placeholder("Write a note on this line"), h.Aria("label", "Note text"), p.NoteText.Bind()),
		h.Label(h.Input(h.Type("checkbox"), p.InHandoff.Bind()), h.Str(" In the handoff")),
		h.Button(h.Class("btn small note-save"), h.Title("Save the note"), on.Click(p.SaveNote), h.Str("Save")),
		h.Button(h.Class("btn small ghost note-cancel"), h.Title("Drop the note box"), on.Click(p.CancelNote), h.Str("Cancel")),
	)
}

// strayNotes lists the notes whose line is not on the page: their diff is
// folded or their unit has since left the change.
func (p *reviewPage) strayNotes(_ *reading) h.H {
	var rows []h.H
	for _, ns := range p.notesAt {
		for _, n := range ns {
			if !p.shown[n.ID] {
				rows = append(rows, h.Div(h.Class("stray"), h.Span(h.Class("loc"), h.Str(fmt.Sprintf("%s:%d", n.File, n.Line))), p.noteRow(n)))
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return h.Section(h.Class("lane"), h.Div(h.Class("lane-bar"), h.H3(h.Str("Notes off the page"))), group(rows))
}

// smellKey identifies one finding across renders.
func smellKey(f smell.Finding) string {
	return strings.Join([]string{string(f.Rule), f.Decl, f.File, f.Subject}, "|")
}

// smells lists the findings this change brings and the ones it fixes, a line
// each. It folds once everything it brings is in the handoff.
func (p *reviewPage) smells(rd *reading) h.H {
	g := rd.graph
	if g == nil || len(g.Introduced)+len(g.Fixed) == 0 {
		return nil
	}
	var rows []h.H
	pending := 0
	for _, f := range g.Introduced {
		in := p.inBasket(handoff.Introduced, smellKey(f), rd.scope)
		if !in {
			pending++
		}
		var name h.H = h.Span(h.Class("smell-name"), h.Str(f.Subject))
		if key := rd.keyOf(f.Decl); key != "" {
			name = h.A(h.Class("smell-name"), h.Href("#"+unitID(key)), h.Str(f.Subject))
		}
		text := []h.H{h.Class("smell-text"), h.Str(ruleLabel(f.Rule) + " · "), name}
		if f.Detail != "" {
			text = append(text, h.Str(" — "+f.Detail))
		}
		rows = append(rows, h.Li(h.Class("finding smell"), sevMark(f.Severity), h.Span(text...),
			handoffControl(in, on.Click(on.Bind(p.AddSmell, smellKey(f))), h.Title("Add "+f.Subject+" to the handoff"))))
	}
	for _, f := range g.Fixed {
		rows = append(rows, h.Li(h.Class("finding smell fixed"), h.Span(h.Class("ok"), h.Str("✓")),
			h.Span(h.Class("smell-text"), h.Str("Fixed: "+ruleLabel(f.Rule)+" · "+f.Subject))))
	}
	head := []h.H{h.Str(fmt.Sprintf("Smells: %d new, %d fixed", len(g.Introduced), len(g.Fixed)))}
	switch {
	case len(g.Introduced) > 0 && pending == 0:
		head = append(head, h.Span(h.Class("ok"), h.Str("✓ all in handoff")))
	case pending > 0 && len(g.Introduced) > 1:
		head = append(head, h.Button(h.Class("btn small"), h.Type("button"), h.Title("Add every introduced smell to the handoff"),
			on.Click(p.AddAllSmells, on.WithPrevent()), h.Str("Add all")))
	}
	return h.Details(h.Class("smells"), h.Open(len(g.Introduced) == 0 || pending > 0),
		h.Summary(head...),
		h.Ul(append([]h.H{h.Class("findings")}, rows...)...))
}

// AddSmell puts one introduced smell in the handoff.
func (p *reviewPage) AddSmell(_ *via.Ctx, key string) { p.addSmells(key) }

// AddAllSmells puts every introduced smell in the handoff.
func (p *reviewPage) AddAllSmells(_ *via.Ctx) { p.addSmells("") }

// addSmells adds the introduced smell with this key, or all of them when key
// is empty. One already in the handoff stays as it is.
func (p *reviewPage) addSmells(key string) {
	rd, release, _ := p.read()
	defer release()
	if rd == nil || rd.graph == nil {
		return
	}
	for _, f := range rd.graph.Introduced {
		if k := smellKey(f); (key == "" || k == key) && !p.inBasket(handoff.Introduced, k, rd.scope) {
			p.add(smellItem(p.repo.ID, rd.scope, f))
		}
	}
}

func smellItem(repo, scope string, f smell.Finding) handoff.Item {
	where := f.File
	if f.Line > 0 {
		where = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	body := f.Rule.Why()
	if f.Detail != "" {
		body = f.Detail + ". " + body
	}
	return handoff.Item{
		Kind: handoff.Introduced, Repo: repo, Scope: scope,
		Title: "New " + strings.ToLower(ruleLabel(f.Rule)) + ": " + f.Subject,
		Where: where, Body: body,
		Check: fmt.Sprintf("The %q smell for %s is gone when the code is analysed again, and the tests still pass.", ruleLabel(f.Rule), f.Subject),
		Key:   smellKey(f),
	}
}
