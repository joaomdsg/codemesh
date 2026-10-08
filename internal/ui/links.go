package ui

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/joaomdsg/codemesh/internal/smell"
)

// mapURL addresses one view of the map: a lens, a scope (package or file
// path) and a selected declaration. Empty fields are left out.
type mapURL struct{ lens, in, decl string }

func (u mapURL) String() string {
	var q []string
	for _, kv := range [][2]string{{"lens", u.lens}, {"in", u.in}, {"d", u.decl}} {
		if kv[1] != "" {
			q = append(q, kv[0]+"="+url.QueryEscape(kv[1]))
		}
	}
	if len(q) == 0 {
		return "/"
	}
	return "/?" + strings.Join(q, "&")
}

// findingURL opens the map where a finding is: its declaration, else its
// file, else its package.
func findingURL(f smell.Finding, lens string) string {
	switch {
	case f.Decl != "":
		return mapURL{lens, f.File, f.Decl}.String()
	case f.File != "":
		return mapURL{lens, f.File, ""}.String()
	}
	return mapURL{lens, f.Package, ""}.String()
}

func findingRow(f smell.Finding, lens string) h.H {
	return h.Li(h.Class("finding"),
		sevMark(f.Severity),
		h.A(h.Href(findingURL(f, lens)),
			h.Span(h.Class("rule"), h.Str(ruleLabel(f.Rule))),
			h.Span(h.Class("subject"), h.Str(f.Subject)),
		),
		h.Span(h.Class("detail"), h.Title(f.Rule.Why()), h.Str(f.Detail)),
		// Names repeat across packages and receivers; the place tells them apart.
		via.When(f.Decl != "", func() h.H { return h.Span(h.Class("detail"), h.Str(fmt.Sprintf("%s:%d", f.File, f.Line))) }),
	)
}
