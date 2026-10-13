package handoff

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Context is what the prompt says about where the items come from. Repos maps
// a repo ID to its display name; an ID with no entry is shown as is. The
// remaining fields are left out of the prompt when empty.
type Context struct {
	Repos   map[string]string
	Branch  string
	BaseSHA string
	HeadSHA string
	PR      string
}

// kinds is the order items appear in within a repo, with their headings.
var kinds = []struct {
	kind    Kind
	heading string
}{
	{Introduced, "Introduced by this change"},
	{Finding, "Findings"},
	{Prognosis, "Prognoses"},
	{Note, "Review notes"},
	{Thread, "Threads"},
}

// Render writes the prompt for b. Items are grouped by repo, then kind, then
// file; within a file they keep the basket's order. The same input always
// gives the same text.
func Render(b Basket, ctx Context) string {
	if len(b.Items) == 0 {
		return "The basket is empty.\n"
	}
	name := func(it Item) string {
		if n := ctx.Repos[it.Repo]; n != "" {
			return n
		}
		return it.Repo
	}
	items := slices.Clone(b.Items)
	slices.SortStableFunc(items, func(a, b Item) int {
		return cmp.Or(
			cmp.Compare(name(a), name(b)),
			cmp.Compare(a.Repo, b.Repo),
			cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(file(a), file(b)),
		)
	})

	var repos []string
	for _, it := range items {
		if n := name(it); !slices.Contains(repos, n) {
			repos = append(repos, n)
		}
	}

	var w strings.Builder
	w.WriteString("# Codemesh handoff\n\n")
	w.WriteString("Work through the items below. Each says where the code is, what is wrong and how to check the fix. Change only what an item calls for.\n\n")
	w.WriteString("- Repos: " + strings.Join(repos, ", ") + "\n")
	for _, f := range []struct{ label, val string }{
		{"Branch", ctx.Branch}, {"PR", ctx.PR}, {"Base", ctx.BaseSHA}, {"Head", ctx.HeadSHA},
	} {
		if f.val != "" {
			w.WriteString("- " + f.label + ": " + f.val + "\n")
		}
	}

	for i, it := range items {
		newRepo := i == 0 || it.Repo != items[i-1].Repo
		if newRepo {
			w.WriteString("\n## " + name(it) + "\n")
		}
		if newRepo || it.Kind != items[i-1].Kind {
			w.WriteString("\n### " + heading(it.Kind) + "\n")
		}
		item(&w, it)
	}
	return w.String()
}

func item(w *strings.Builder, it Item) {
	w.WriteString("\n#### " + it.Title + "\n\n")
	if it.Where != "" {
		w.WriteString("Where: `" + it.Where + "`\n")
	}
	if it.Scope != "" {
		w.WriteString("Scope: " + scope(it.Scope) + "\n")
	}
	if it.Body != "" {
		w.WriteString("\n" + it.Body + "\n")
	}
	if len(it.Steps) > 0 {
		w.WriteString("\nSteps:\n")
		for i, s := range it.Steps {
			w.WriteString(strconv.Itoa(i+1) + ". " + s + "\n")
		}
	}
	if it.Check != "" {
		w.WriteString("\nCheck: " + it.Check + "\n")
	}
	if len(it.Code) > 0 {
		f := fence(it.Code)
		w.WriteString("\n" + f + "\n" + strings.Join(it.Code, "\n") + "\n" + f + "\n")
	}
}

func kindRank(k Kind) int {
	for i, e := range kinds {
		if e.kind == k {
			return i
		}
	}
	return len(kinds)
}

func heading(k Kind) string {
	if r := kindRank(k); r < len(kinds) {
		return kinds[r].heading
	}
	return string(k)
}

func scope(s string) string {
	if s == "wt" {
		return "working tree"
	}
	return s
}

// file is the path part of an item's file:line.
func file(it Item) string {
	f, _, _ := strings.Cut(it.Where, ":")
	return f
}

// fence is a backtick run longer than any inside lines.
func fence(lines []string) string {
	n := 2
	for _, l := range lines {
		run := 0
		for _, c := range l {
			if c == '`' {
				run++
				n = max(n, run)
			} else {
				run = 0
			}
		}
	}
	return strings.Repeat("`", n+1)
}
