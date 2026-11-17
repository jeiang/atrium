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

type MessageScan struct {
	Name string
	Run  Refresher
}

type namedScan struct {
	unit string
	scan MessageScan
}

func compileMessageScans(decls []Declaration) ([]namedScan, error) {
	owners := claims{}
	var out []namedScan
	for _, d := range decls {
		for _, s := range d.MessageScans {
			if err := owners.take("message scan", s.Name, d.Name); err != nil {
				return nil, err
			}
			out = append(out, namedScan{unit: d.Name, scan: s})
		}
	}
	return out, nil
}

func MessageScans(ctx context.Context) []MessageScan {
	s := SetFrom(ctx)
	if s == nil {
		return nil
	}
	snap := s.Current()
	var out []MessageScan
	for _, n := range s.Catalog().regs.messageScans {
		if snap != nil && snap.Enabled(n.unit) {
			out = append(out, n.scan)
		}
	}
	return out
}

func RunMessageScan(ctx context.Context, name string, d *db.DB, now float64, r Refresh) (any, bool, error) {
	for _, s := range MessageScans(ctx) {
		if s.Name == name {
			out, err := s.Run(ctx, d, now, r)
			return out, true, err
		}
	}
	return nil, false, nil
}
