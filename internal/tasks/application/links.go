package application

import (
	"context"
	"net/mail"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/tasks"
)

func LinksOf(ctx context.Context, q db.Querier, t tasks.Task) ([]tasks.Link, error) {
	if t.SourceKind == "" {
		return []tasks.Link{}, nil
	}
	title, err := linkTitle(ctx, q, t.SourceKind, t.SourceID)
	if err != nil {
		return nil, err
	}
	return []tasks.Link{{Kind: t.SourceKind, ID: t.SourceID, Title: title}}, nil
}

func linkTitle(ctx context.Context, q db.Querier, kind, id string) (*string, error) {
	if kind == tasks.LinkPerson {
		if a, err := mail.ParseAddress(id); err != nil || a.Address != id {
			return nil, nil
		}
		return &id, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return nil, nil
	}
	switch kind {
	case tasks.LinkThread:
		t, err := threads.Get(ctx, q, n)
		if err != nil || t == nil {
			return nil, err
		}
		title := ""
		if t.Title != nil {
			title = *t.Title
		}
		return &title, nil
	case tasks.LinkProject:
		p, err := projects.Get(ctx, q, n)
		if err != nil || p == nil {
			return nil, err
		}
		return &p.Name, nil
	case tasks.LinkRecord:
		r, err := kb.Get(ctx, q, n)
		if err != nil || r == nil {
			return nil, err
		}
		return &r.Title, nil
	case tasks.LinkMessage:
		m, err := module.Communications(ctx).Message(ctx, q, n)
		if m == nil || err != nil {
			return nil, err
		}
		return &m.Subject, nil
	case tasks.LinkProposal:
		p, err := proposals.Get(ctx, q, n)
		if err != nil || p == nil {
			return nil, err
		}
		return &p.Title, nil
	}
	return nil, nil
}

func badLink(message string) error {
	return &application.Error{Code: application.Invalid, Message: message,
		Fields: []application.Field{{Name: "link", Code: "invalid", Message: message}}}
}

func checkLink(ctx context.Context, q db.Querier, l LinkInput) error {
	if !slices.Contains(tasks.SettableLinks, l.Kind) {
		return badLink("link kind must be thread, project, person, record or message")
	}
	title, err := linkTitle(ctx, q, l.Kind, l.ID)
	if err != nil {
		return err
	}
	if title == nil {
		return badLink("the linked " + l.Kind + " does not exist")
	}
	return nil
}
