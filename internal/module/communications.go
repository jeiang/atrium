package module

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/communications"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const (
	communicationsKey = "messages"
	refreshKey        = "messages refresh"
)

type Refresh struct {
	Env        config.Env
	Classifier *classifier.Service
	Paid       communications.Deferral
	Clock      func() float64
}

type Refresher func(ctx context.Context, d *db.DB, now float64, r Refresh) (any, error)

func compileRefresh(decls []Declaration) (Refresher, error) {
	owners := claims{}
	var out Refresher
	for _, d := range decls {
		if d.CommunicationsRefresh == nil {
			continue
		}
		if err := owners.take("communications refresh", refreshKey, d.Name); err != nil {
			return nil, err
		}
		out = d.CommunicationsRefresh
	}
	return out, nil
}

func RefreshCommunications(ctx context.Context, d *db.DB, now float64, r Refresh) (any, error) {
	s := SetFrom(ctx)
	if s == nil || s.Catalog().regs.refresh == nil {
		return nil, nil
	}
	return s.Catalog().regs.refresh(ctx, d, now, r)
}

func compileCommunications(decls []Declaration) (communications.Feed, error) {
	owners := claims{}
	var out communications.Feed
	for _, d := range decls {
		if d.Communications == nil {
			continue
		}
		if err := owners.take("communications provider", communicationsKey, d.Name); err != nil {
			return communications.Feed{}, err
		}
		out = communications.Feed{Unit: d.Name, Provider: d.Communications}
	}
	return out, nil
}

func (c *Catalog) Communications() communications.Feed { return c.regs.communications }

func Communications(ctx context.Context) communications.Feed {
	s := SetFrom(ctx)
	if s == nil {
		return communications.Feed{}
	}
	snap := s.Current()
	if snap == nil {
		return communications.Feed{}
	}
	feed := s.Catalog().Communications()
	if !snap.Enabled(feed.Unit) {
		return communications.Feed{}
	}
	return feed
}
