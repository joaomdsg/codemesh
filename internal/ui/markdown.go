package ui

import (
	"strings"

	"github.com/go-via/via/h"
)

// markdown renders the little markdown an agent's summary uses: paragraphs,
// "- " bullets, **bold** and `code`. Anything else stays as text.
func markdown(text string) h.H {
	var out, items []h.H
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, h.P(inline(strings.Join(para, " "))))
			para = nil
		}
		if len(items) > 0 {
			out = append(out, h.Ul(items...))
			items = nil
		}
	}
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			if len(para) > 0 {
				out = append(out, h.P(inline(strings.Join(para, " "))))
				para = nil
			}
			items = append(items, h.Li(inline(t[2:])))
		default:
			if len(items) > 0 {
				flush()
			}
			para = append(para, t)
		}
	}
	flush()
	return group(out)
}

func inline(s string) h.H {
	var out []h.H
	for i, part := range strings.Split(s, "`") {
		if i%2 == 1 {
			out = append(out, h.Code(h.Str(part)))
			continue
		}
		for j, b := range strings.Split(part, "**") {
			if j%2 == 1 {
				out = append(out, h.Strong(h.Str(b)))
			} else if b != "" {
				out = append(out, h.Str(b))
			}
		}
	}
	return group(out)
}
