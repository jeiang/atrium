package module

import (
	"context"

	"github.com/jeiang/atrium/internal/communications"
)

const communicationsKey = "messages"

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
