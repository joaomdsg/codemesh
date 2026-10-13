package ui

import (
	"fmt"
	"time"

	"github.com/go-via/via"
	"github.com/go-via/via/h"
	"github.com/go-via/via/on"
	"github.com/joaomdsg/codemesh/internal/ghx"
)

// prsPage lists the repo's open pull requests, as gh reports them.
type prsPage struct{ shell }

func (p *prsPage) OnInit(ctx *via.Ctx) error {
	if err := p.start(ctx); err != nil {
		return err
	}
	// A render asks gh again once the list is a minute old.
	ctx.Tick(time.Minute, func(*via.Ctx) {})
	return nil
}

func (p *prsPage) PageMeta() via.Meta { return via.Meta{Title: "Pull requests · codemesh"} }

// Refresh asks gh for the list now.
func (p *prsPage) Refresh(_ *via.Ctx) { p.app.RefreshPRs(p.repo.ID) }

func (p *prsPage) View() h.H {
	list := p.app.PRs(p.repo.ID)
	var body h.H
	switch {
	case list.Loading:
		body = h.P(h.Class("empty"), h.Str("Asking gh for the open pull requests…"))
	case list.Err != nil:
		body = h.P(h.Class("empty"), h.Title(list.Err.Error()), h.Str(list.Hint()))
	case len(list.PRs) == 0:
		body = h.P(h.Class("empty"), h.Str("No open pull requests."))
	default:
		body = p.table(list.PRs)
	}
	return p.frame(tabPRs, nil,
		h.Div(h.Class("prs"),
			h.Div(h.Class("lane-bar"),
				h.H2(h.Str("Open pull requests")),
				h.Button(h.Class("btn"), on.Click(p.Refresh), h.Str("Refresh")),
			),
			body,
		),
	)
}

func (p *prsPage) table(prs []ghx.PR) h.H {
	var rows []h.H
	for _, pr := range prs {
		status := pr.ReviewDecision
		if pr.Draft {
			status = "draft"
		}
		rows = append(rows, h.Tr(
			h.Td(h.Class("num"), h.A(h.Href(fmt.Sprintf("%spr/%d", p.base(), pr.Number)), h.Str("#"+fmt.Sprint(pr.Number)))),
			h.Td(h.A(h.Href(fmt.Sprintf("%spr/%d", p.base(), pr.Number)), h.Str(pr.Title))),
			h.Td(h.Str(pr.Author)),
			h.Td(h.Class("loc"), h.Str(pr.HeadRef+" → "+pr.BaseRef)),
			h.Td(h.Class("num"), delta(pr.Additions, pr.Deletions)),
			h.Td(h.Class("num"), h.Str(pr.ChangedFiles)),
			h.Td(h.Str(pr.UpdatedAt.Local().Format("2 Jan 15:04"))),
			h.Td(h.Str(status)),
		))
	}
	return h.Table(h.Class("prs-table"),
		h.Thead(h.Tr(h.Th(h.Str("#")), h.Th(h.Str("Title")), h.Th(h.Str("Author")), h.Th(h.Str("Branch")),
			h.Th(h.Str("Lines")), h.Th(h.Str("Files")), h.Th(h.Str("Updated")), h.Th(h.Str("Status")))),
		h.Tbody(rows...),
	)
}
