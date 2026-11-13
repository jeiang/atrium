package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/notify"
)

type InboxInput struct {
	Limit  int
	Before int64
}
type Inbox struct {
	Items []Notification
	More  bool
}

func (s *Service) Inbox(ctx context.Context, in InboxInput) (application.Read[Inbox], error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Limit < 1 {
		return application.Read[Inbox]{}, &application.Error{Code: application.Invalid, Message: "limit must be between 1 and 100."}
	}
	counters, err := s.state.Read(ctx, nil, []string{state.Notify})
	if err != nil {
		return application.Read[Inbox]{}, err
	}
	found, err := notify.RecentBefore(ctx, s.db.Read, in.Before, in.Limit+1)
	if err != nil {
		return application.Read[Inbox]{}, err
	}
	data := Inbox{Items: found, More: len(found) > in.Limit}
	if data.More {
		data.Items = found[:in.Limit]
	}
	if data.Items == nil {
		data.Items = []Notification{}
	}
	return application.Read[Inbox]{Data: data, Revision: application.Revision(state.Revision(string(InboxName), counters, in, "operator"))}, nil
}
func (s *Service) Failing(ctx context.Context) ([]string, error) {
	devices, err := notify.Devices(ctx, s.db.Read)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range devices {
		if d.Failures >= notify.FailingAfter {
			out = append(out, d.Label)
		}
	}
	return out, nil
}
