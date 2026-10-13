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

// Group is the items of one repo and kind, in the order the prompt lists them.
type Group struct {
	Repo    string // display name
	RepoID  string
	Kind    Kind
	Heading string
	Items   []Item
}

// Groups orders b the way Render writes it: by repo name, then kind, with the
// items of each in the order the user gave.
func Groups(b Basket, ctx Context) []Group {
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
		)
	})
	var out []Group
	for _, it := range items {
		if n := len(out); n > 0 && out[n-1].RepoID == it.Repo && out[n-1].Kind == it.Kind {
			out[n-1].Items = append(out[n-1].Items, it)
			continue
		}
		out = append(out, Group{Repo: name(it), RepoID: it.Repo, Kind: it.Kind, Heading: heading(it.Kind), Items: []Item{it}})
	}
	return out
}

// Render writes the prompt for b: a brief to a coding agent. Items are grouped
// as Groups says. The same input always gives the same text.
func Render(b Basket, ctx Context) string {
	if len(b.Items) == 0 {
		return "The basket is empty.\n"
	}
	groups := Groups(b, ctx)
	var repos []string
	for _, g := range groups {
		if !slices.Contains(repos, g.Repo) {
			repos = append(repos, g.Repo)
		}
	}

	var w strings.Builder
	w.WriteString("# Codemesh handoff\n\n")
	w.WriteString("Goal: " + cmp.Or(strings.TrimSpace(b.Intro), "resolve each item below.") + "\n\n")
	w.WriteString("Codemesh, a code-health and review tool, collected " + count(len(b.Items)) + " below. ")
	w.WriteString("Each says where the code is, what is wrong, the steps to take where there are any, and how to check the result. Work through them in order.\n\n")
	w.WriteString("- Repos: " + strings.Join(repos, ", ") + "\n")
	for _, f := range []struct{ label, val string }{
		{"Branch", ctx.Branch}, {"PR", ctx.PR}, {"Base", ctx.BaseSHA}, {"Head", ctx.HeadSHA},
	} {
		if f.val != "" {
			w.WriteString("- " + f.label + ": " + f.val + "\n")
		}
	}

	for i, g := range groups {
		if i == 0 || g.RepoID != groups[i-1].RepoID {
			w.WriteString("\n## " + g.Repo + "\n")
		}
		w.WriteString("\n### " + g.Heading + "\n")
		for _, it := range g.Items {
			item(&w, it)
		}
	}

	w.WriteString("\n## When you are done\n\n")
	w.WriteString("1. Verify each claim in the code before you change anything. Items come from static analysis and a reviewer's notes, so one may be wrong or already fixed. If it is, skip it and say why.\n")
	w.WriteString("2. Change only what an item calls for. Do not reformat or refactor around it.\n")
	w.WriteString("3. Run the tests and the build in each repo you changed, and fix what you broke.\n")
	w.WriteString("4. Reply with one line per item: changed, skipped (why), or needs a decision. End with the test result.\n")
	return w.String()
}

func count(n int) string {
	if n == 1 {
		return "1 item"
	}
	return strconv.Itoa(n) + " items"
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
		w.WriteString("\nCode:\n\n" + f + "\n" + strings.Join(it.Code, "\n") + "\n" + f + "\n")
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
	if rest, ok := strings.CutPrefix(s, "commit:"); ok {
		return "commit " + rest
	}
	if rest, ok := strings.CutPrefix(s, "pr:"); ok {
		n, sha, _ := strings.Cut(rest, "@")
		return "PR #" + n + " at " + sha
	}
	return s
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
