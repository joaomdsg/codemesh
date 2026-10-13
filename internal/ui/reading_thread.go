package ui

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/thread"
)

// hopCap is how many callers a hop lists before the rest wait behind a
// button: the riskiest come first, and a contract change can have dozens.
const hopCap = 8

// callContext is how many lines show either side of a call.
const callContext = 2

// active is the thread the query names, as far as the head snapshot can
// follow it: a decl that is gone, or a hop that is no caller or user of the
// one before, ends the path there.
func (p *reviewPage) active(rd *reading) thread.Thread {
	ids := p.path
	snap := rd.head.Snap
	if len(ids) == 0 || snap.Decl(ids[0]) == nil {
		return thread.Thread{}
	}
	t := thread.New(rd.scope, ids[0], rd.keyOf(ids[0]))
	for _, id := range ids[1:] {
		cands := thread.Next(snap, t)
		i := slices.IndexFunc(cands, func(c thread.Candidate) bool { return c.DeclID == id })
		if i < 0 {
			break
		}
		thread.Follow(&t, cands[i], rd.keyOf(id))
	}
	return t
}

// pathIDs is the thread's decl IDs, root first.
func pathIDs(t thread.Thread) []string {
	ids := []string{t.Root}
	for _, h := range t.Hops {
		ids = append(ids, h.DeclID)
	}
	return ids
}

// SetThread opens the thread path in arg, which opens, follows, trims or
// closes it. The browser writes the new address to the address bar.
func (p *reviewPage) SetThread(_ *via.Ctx, arg string) {
	p.path = splitPath(arg)
	p.Url.Set(p.threadURL())
}

// MoreHops lists every caller of one decl, not just the first few.
func (p *reviewPage) MoreHops(_ *via.Ctx, declID string) {
	if p.moreHops == nil {
		p.moreHops = map[string]bool{}
	}
	p.moreHops[declID] = true
}

// callers is the disclosure under a unit: who calls it. Opening it starts a
// thread at the unit; each hop followed nests the next one's callers.
func (p *reviewPage) callers(rd *reading, u *review.Unit, t thread.Thread) h.H {
	d := rd.head.Snap.Decl(u.ID)
	if d == nil || len(d.Callers)+len(d.Users) == 0 {
		return nil
	}
	open := t.Root == u.ID
	verb, glyph, arg := "Show", "▸", joinPath([]string{u.ID})
	if open {
		verb, glyph, arg = "Hide", "▾", ""
	}
	label := fmt.Sprintf("%s callers of %s", verb, u.Name)
	text := glyph + " " + callersText(rd.head.Snap, d)
	kids := []h.H{h.Button(h.Class("calls-toggle"), h.Title(label), h.Aria("label", label), on.Click(on.Bind(p.SetThread, arg)), h.Str(text))}
	if open {
		kids = append(kids, p.hops(rd, t, 0))
	}
	return h.Div(append([]h.H{h.Class("calls")}, kids...)...)
}

// callerCount is how many callers a declaration has and how many of them are
// tests, in the one wording every card uses; "" when it has none.
func callerCount(s *code.Snapshot, callers []string) string {
	if len(callers) == 0 {
		return ""
	}
	tests := 0
	for _, id := range callers {
		if c := s.Decl(id); c != nil && c.Test {
			tests++
		}
	}
	text := plural(len(callers), "caller")
	if tests > 0 {
		text += fmt.Sprintf(" (%d in tests)", tests)
	}
	return text
}

// callersText is callerCount with how many types' users reach the declaration.
func callersText(s *code.Snapshot, d *code.Decl) string {
	var parts []string
	if c := callerCount(s, d.Callers); c != "" {
		parts = append(parts, c)
	}
	if n := len(d.Users); n > 0 {
		parts = append(parts, plural(n, "type use"))
	}
	return strings.Join(parts, " · ")
}

// hops lists what reaches the thread's node at depth level: its callers and
// users, the one on the path open with its own below it.
func (p *reviewPage) hops(rd *reading, t thread.Thread, level int) h.H {
	prefix := t
	prefix.Hops = t.Hops[:level]
	cands := thread.Next(rd.head.Snap, prefix)
	if len(cands) == 0 {
		return h.P(h.Class("hint"), h.Str("Nothing calls it."))
	}
	parent := pathIDs(t)[level]
	limit := hopCap
	if p.moreHops[parent] {
		limit = len(cands)
	}
	var rows []h.H
	shown := 0
	for _, c := range cands {
		open := level < len(t.Hops) && t.Hops[level].DeclID == c.DeclID
		if shown >= limit && !open {
			continue
		}
		shown++
		rows = append(rows, p.hop(rd, t, level, c, open))
	}
	if hidden := len(cands) - shown; hidden > 0 {
		rows = append(rows, h.Button(h.Class("expand"), on.Click(on.Bind(p.MoreHops, parent)), h.Str(fmt.Sprintf("Show %d more", hidden))))
	}
	return h.Div(append([]h.H{h.Class("hops")}, rows...)...)
}

// hopBlock names a hop's code block, so a note on one of its lines finds it.
func hopBlock(parent, id string) string {
	f := fnv.New64a()
	f.Write([]byte(parent + "\x00" + id))
	return fmt.Sprintf("h%x", f.Sum64())
}

func (p *reviewPage) hop(rd *reading, t thread.Thread, level int, c thread.Candidate, open bool) h.H {
	snap := rd.head.Snap
	d, from := snap.Decl(c.DeclID), snap.Decl(c.From)
	block := hopBlock(c.From, c.DeclID)
	p.anchors[block] = anchor{file: d.File, decl: d.ID, key: rd.keyOf(d.ID)}
	line, found, ctx := p.callSite(snap, d, from)
	rows := make([]h.H, len(ctx))
	for i, l := range ctx {
		rows[i] = p.row(block, d.File, l, found && l.New == line)
	}
	name := shortPkg(rd.head, d.Package) + d.Name
	chips := []h.H{h.Span(h.Class("chip"), h.Str(fmt.Sprintf("hop %d", level+1)))}
	if key := rd.keyOf(d.ID); key != "" {
		label := "in this change"
		if rd.pr != nil {
			label = "in this PR"
		}
		chips = append(chips, h.A(h.Class("hop-pr"), h.Href("#"+unitID(key)), h.Str(label)))
	}
	if d.Test {
		chips = append(chips, h.Span(h.Class("chip"), h.Str("test")))
	}
	if c.Via == thread.User {
		chips = append(chips, h.Span(h.Class("chip"), h.Title("Reaches the type through one of its members"), h.Str("uses it")))
	}
	if c := callerCount(snap, d.Callers); c != "" {
		chips = append(chips, h.Span(h.Class("chip"), h.Str(c)))
	}

	ids := pathIDs(t)[:level+1]
	var tool h.H
	switch {
	case len(d.Callers)+len(d.Users) == 0:
		tool = h.Span(h.Class("hint"), h.Str("nothing calls it"))
	case open:
		tool = h.Button(h.Class("tool hop-follow"), h.Title("Close "+d.Name), on.Click(on.Bind(p.SetThread, joinPath(ids))), h.Str("Close"))
	default:
		tool = h.Button(h.Class("tool hop-follow"), h.Title("Follow "+d.Name), on.Click(on.Bind(p.SetThread, joinPath(append(slices.Clone(ids), d.ID)))), h.Str("Callers →"))
	}
	kids := []h.H{
		h.Class(map[bool]string{true: "hop open", false: "hop"}[open]), h.ID(block), h.TabIndex(0),
		h.Div(h.Class("hop-head"),
			group(chips),
			h.Span(h.Class("nm"), h.Str(name)),
			p.loc("loc", fmt.Sprintf("%s:%d", d.File, line), d.File, d.ID, true),
			h.Span(h.Class("hop-tools"), tool),
		),
		h.Div(h.Class("diff hop-code"), group(rows)),
	}
	if open {
		kids = append(kids, p.hops(rd, t, level+1))
	}
	return h.Div(kids...)
}

// callSite finds where caller names callee: the line, and its neighbours.
// The match is by name over the caller's own source, so it is the first line
// that reads like a call, not a proof; when none does, the context is the
// top of the caller and found is false.
func (p *reviewPage) callSite(s *code.Snapshot, caller, callee *code.Decl) (line int, found bool, ctx []review.Line) {
	src := p.lines(s, caller.File)
	if caller.Start < 1 || caller.End > len(src) {
		return caller.Start, false, nil
	}
	name := callee.Name
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name, _, _ = strings.Cut(name, "(")
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	own, _, _ := strings.Cut(caller.Name[strings.LastIndex(caller.Name, ".")+1:], "(")
	line = caller.Start
	find := func(match func(string) bool) bool {
		for n := caller.Start; n <= caller.End; n++ {
			if match(strings.TrimSpace(src[n-1])) {
				line = n
				return true
			}
		}
		return false
	}
	// A method names its type only in its receiver, so failing a call, the
	// declaration line counts, less its own name.
	found = find(func(t string) bool { return !isComment(t) && !isDeclaration(t) && re.MatchString(t) }) ||
		find(func(t string) bool { return isDeclaration(t) && re.MatchString(strings.Replace(t, own+"(", "(", 1)) })
	from, to := max(line-callContext, caller.Start), min(line+callContext, caller.End)
	if !found {
		to = min(caller.Start+2*callContext, caller.End)
	}
	for n := from; n <= to; n++ {
		ctx = append(ctx, review.Line{Op: ' ', New: n, Text: src[n-1]})
	}
	return line, found, ctx
}

func isComment(t string) bool {
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// isDeclaration is true for the line that declares a function, whose name
// would otherwise match a call to a decl of the same name.
func isDeclaration(t string) bool {
	for _, kw := range []string{"func ", "def ", "fn ", "pub fn ", "function ", "export function ", "async function ", "export async function "} {
		if strings.HasPrefix(t, kw) {
			return true
		}
	}
	return false
}

// threadBar pins the open path to the top: each pill trims the thread to it.
func (p *reviewPage) threadBar(rd *reading, t thread.Thread) h.H {
	if t.Root == "" {
		return nil
	}
	snap := rd.head.Snap
	ids := pathIDs(t)
	var pills []h.H
	for i, id := range ids {
		if i > 0 {
			pills = append(pills, h.Span(h.Class("sep"), h.Str("→")))
		}
		name := snap.Decl(id).Name
		cls := "pill"
		if i == len(ids)-1 {
			cls += " last"
		}
		pills = append(pills, h.Button(h.Class(cls), h.Title("Trim the thread to "+name), on.Click(on.Bind(p.SetThread, joinPath(ids[:i+1]))), h.Str(name)))
	}
	var keep h.H
	if p.inBasket(handoff.Thread, threadKey(t), rd.scope) {
		keep = h.Span(h.Class("ok"), h.Str("✓ In handoff"))
	} else {
		keep = h.Button(h.Class("btn small keep-thread"), h.Title("Keep this thread in the handoff"), on.Click(p.KeepThread), h.Str("Keep thread"))
	}
	var note h.H
	if len(t.Hops) > 0 {
		note = h.Button(h.Class("btn small ghost"), h.Title("Note on the last hop"), on.Click(p.NoteLast), h.Str("Note"))
	}
	return h.Nav(append([]h.H{h.Class("thread-bar"), h.Aria("label", "Thread")},
		h.Span(h.Class("hint"), h.Str("Thread")),
		group(pills),
		h.Span(h.Class("thread-tools"), keep, note,
			h.Button(h.Class("btn small ghost"), h.Title("Close the thread"), on.Click(on.Bind(p.SetThread, "")), h.Str("Close"))),
	)...)
}

func threadKey(t thread.Thread) string { return strings.Join(pathIDs(t), " > ") }

// KeepThread puts the open thread in the basket.
func (p *reviewPage) KeepThread(_ *via.Ctx) {
	rd, release, _ := p.read()
	defer release()
	if rd == nil {
		return
	}
	t := p.active(rd)
	if t.Root == "" || p.inBasket(handoff.Thread, threadKey(t), rd.scope) {
		return
	}
	snap := rd.head.Snap
	name := func(d *code.Decl) string { return shortPkg(rd.head, d.Package) + d.Name }
	root := snap.Decl(t.Root)
	names := []string{name(root)}
	var body []string
	for _, hop := range t.Hops {
		d, from := snap.Decl(hop.DeclID), snap.Decl(hop.From)
		line, _, _ := p.callSite(snap, d, from)
		hopLine := fmt.Sprintf("%s ← %s · %s:%d", name(from), name(d), repoPath(rd.head.Root, d.File), line)
		if hop.Via == thread.User {
			hopLine += " (uses it)"
		}
		body = append(body, hopLine)
		names = append(names, name(d))
	}
	if len(body) == 0 {
		body = []string{callersText(snap, root)}
	}
	p.add(handoff.Item{
		Kind: handoff.Thread, Repo: p.repo.ID, Scope: rd.scope,
		Title: "Blast radius: " + strings.Join(names, " → "), Where: fmt.Sprintf("%s:%d", repoPath(rd.head.Root, root.File), root.Start),
		Body: strings.Join(body, "\n"), Code: declLines(snap, t.Root), Key: threadKey(t),
		Steps: []string{
			"Read the changed declaration, then each caller listed, from the changed code outward.",
			"At each hop, check the caller still gets what it expects from the one before it.",
		},
		Check: "Every declaration on the path still works with the change, and the build and tests pass.",
	})
}

// NoteLast opens a note on the call line of the thread's last hop.
func (p *reviewPage) NoteLast(_ *via.Ctx) {
	rd, release, _ := p.read()
	defer release()
	if rd == nil {
		return
	}
	t := p.active(rd)
	if len(t.Hops) == 0 {
		return
	}
	snap := rd.head.Snap
	hop := t.Hops[len(t.Hops)-1]
	d := snap.Decl(hop.DeclID)
	line, _, _ := p.callSite(snap, d, snap.Decl(hop.From))
	p.noting = &noteTarget{block: hopBlock(hop.From, hop.DeclID), file: d.File, decl: d.ID, key: rd.keyOf(d.ID), side: "+", line: line}
}

// inBasket reports whether the basket already holds an item of this kind from this source.
func (p *reviewPage) inBasket(kind handoff.Kind, key, scope string) bool {
	return slices.ContainsFunc(p.app.Basket().Items, func(it handoff.Item) bool {
		return it.Kind == kind && it.Key == key && it.Repo == p.repo.ID && it.Scope == scope
	})
}

func (p *reviewPage) add(it handoff.Item) {
	if _, err := p.app.BasketAdd(it); err != nil {
		p.err = "Not added to the handoff: " + err.Error()
		return
	}
	p.err = ""
}

// lines is a file of the snapshot's tree, split into lines; a file that
// cannot be read has none.
func (p *reviewPage) lines(s *code.Snapshot, rel string) []string {
	full := filepath.Join(s.Dir, filepath.FromSlash(rel))
	if l, ok := p.files[full]; ok {
		return l
	}
	var l []string
	if data, err := os.ReadFile(full); err == nil {
		l = strings.Split(string(data), "\n")
	}
	p.files[full] = l
	return l
}
