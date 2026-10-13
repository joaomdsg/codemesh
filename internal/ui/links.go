package ui

import (
	"fmt"
	"net/url"

	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// mapURL addresses one view of a repo's map: a lens, a scope (package or
// file path) and a selected declaration. Empty fields are left out.
type mapURL struct{ repo, root, lens, in, decl string }

// The view is one path segment, a query string, so that it reaches the live
// page: /r/{repo}/map/lens=churn&in=calc.
func (u mapURL) String() string {
	q := url.Values{}
	for _, kv := range [][2]string{{"lens", u.lens}, {"in", u.in}, {"d", u.decl}} {
		if kv[1] != "" {
			q.Set(kv[0], kv[1])
		}
	}
	base := repoBase(u.repo, u.root)
	if len(q) == 0 {
		return base
	}
	return base + "map/" + url.PathEscape(q.Encode())
}

// at is u pointed at another scope and declaration.
func (u mapURL) at(in, decl string) mapURL {
	u.in, u.decl = in, decl
	return u
}

// findingURL opens the map where a finding is: its declaration, else its
// file, else its package. m names the repo, root and lens.
func findingURL(m mapURL, f smell.Finding) string {
	switch {
	case f.Decl != "":
		return m.at(f.File, f.Decl).String()
	case f.File != "":
		return m.at(f.File, "").String()
	}
	return m.at(f.Package, "").String()
}

// findingRow lists f with its link to the map; extra controls follow it.
func findingRow(m mapURL, f smell.Finding, extra ...h.H) h.H {
	return h.Li(append([]h.H{h.Class("finding"),
		sevMark(f.Severity),
		h.A(h.Href(findingURL(m, f)),
			h.Span(h.Class("rule"), h.Str(ruleLabel(f.Rule))),
			h.Span(h.Class("subject"), h.Str(f.Subject)),
		),
		h.Span(h.Class("detail"), h.Title(f.Rule.Why()), h.Str(f.Detail+where(f))),
	}, extra...)...)
}

// where names a declaration finding's file and line: names repeat across
// packages and receivers, the place tells them apart.
func where(f smell.Finding) string {
	if f.Decl == "" {
		return ""
	}
	return fmt.Sprintf(" · %s:%d", f.File, f.Line)
}
