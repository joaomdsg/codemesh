package ui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// excerptLines caps the code copied into a basket item: enough to find the
// place, not a whole file.
const excerptLines = 40

// handoffControl is the "Add to handoff" button for click, or a link to the
// basket once the item is in it. attrs go on the button.
func handoffControl(in bool, click h.H, attrs ...h.H) h.H {
	if in {
		return h.A(h.Class("handoff in"), h.Href("/basket"), h.Title("Open the handoff basket"), h.Str("✓ In handoff"))
	}
	return h.Button(append([]h.H{h.Class("handoff"), h.Type("button"), click, h.Str("Add to handoff")}, attrs...)...)
}

// basketError is the banner text for a failed basket write, "" when it worked.
func basketError(err error) string {
	if err == nil {
		return ""
	}
	return "Not added to the handoff: " + err.Error()
}

// basketErrorLine shows msg as a banner, if there is one.
func basketErrorLine(msg string) h.H {
	if msg == "" {
		return nil
	}
	return h.Div(h.Class("banner err"), h.Role("alert"), h.Str(msg))
}

// inBasket says whether the basket already holds an item of this kind from
// this place in this repo.
func inBasket(b handoff.Basket, kind handoff.Kind, repo, key string) bool {
	return slices.ContainsFunc(b.Items, func(it handoff.Item) bool {
		return it.Kind == kind && it.Repo == repo && it.Key == key
	})
}

// findingKey names a finding by its rule and place; the same smell keeps its
// key across analyses.
func findingKey(f smell.Finding) string {
	key := string(f.Rule) + ":" + cmp.Or(f.Decl, f.File, f.Package)
	if f.Target != "" {
		key += "->" + f.Target
	}
	return key
}

// scopeOf says which tree the analysis read, in the form handoff.Item.Scope
// takes.
func scopeOf(a *app.Analysis) string {
	if a.Rev == analysis.Worktree {
		return "wt"
	}
	return "commit:" + a.Rev[:min(len(a.Rev), 7)]
}

func findingItem(a *app.Analysis, f smell.Finding) handoff.Item {
	where := f.Subject
	if f.File != "" {
		where = repoPath(a.Root, f.File)
		if f.Line > 0 {
			where = fmt.Sprintf("%s:%d", where, f.Line)
		}
	}
	body := f.Rule.Why()
	if f.Detail != "" {
		body = f.Detail + ". " + body
	}
	if f.Target != "" {
		body += " Other package: " + f.Target + "."
	}
	return handoff.Item{
		Kind:  handoff.Finding,
		Repo:  a.Repo.ID,
		Scope: scopeOf(a),
		Title: ruleLabel(f.Rule) + ": " + f.Subject,
		Where: where,
		Body:  body,
		Check: smellCheck(f),
		Code:  declLines(a.Snap, f.Decl),
		Key:   findingKey(f),
	}
}

// smellCheck is how whoever fixes a smell can tell it is fixed, without
// running codemesh.
func smellCheck(f smell.Finding) string {
	return fmt.Sprintf("Re-read the code: the %s no longer applies to %s, and the build and tests still pass.", strings.ToLower(ruleLabel(f.Rule)), f.Subject)
}

func prognosisItem(a *app.Analysis, g prognosis.Prognosis) handoff.Item {
	where := g.Name
	if g.File != "" {
		where = fmt.Sprintf("%s:%d", repoPath(a.Root, g.File), g.Line)
	}
	body := g.Summary + " " + g.Why
	var facts []string
	for _, f := range g.Facts {
		fact := fmt.Sprintf("%s %d", f.Label, f.Value)
		if f.Limit > 0 {
			fact += fmt.Sprintf(" (limit %d)", f.Limit)
		}
		facts = append(facts, fact)
	}
	if len(facts) > 0 {
		body += "\n\nNumbers: " + strings.Join(facts, "; ") + "."
	}
	return handoff.Item{
		Kind:  handoff.Prognosis,
		Repo:  a.Repo.ID,
		Scope: scopeOf(a),
		Title: g.Title,
		Where: where,
		Body:  body,
		Steps: g.Do,
		Check: g.Check,
		Code:  declLines(a.Snap, g.Target),
		Key:   g.Key,
	}
}

// declLines is the source of the declaration with this ID, cut to
// excerptLines; nil when id names no declaration or its file cannot be read.
func declLines(s *code.Snapshot, id string) []string {
	d := s.Decl(id)
	if d == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(d.File)))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if d.End > len(lines) {
		return nil
	}
	lines = lines[d.Start-1 : d.End]
	if len(lines) > excerptLines {
		lines = append(lines[:excerptLines:excerptLines], fmt.Sprintf("… %d more lines", len(lines)-excerptLines))
	}
	return lines
}
