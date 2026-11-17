package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/library/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
)

const QueueName application.Name = "media.queue"

type QueueInput struct {
	TorrentsOnly bool
}

type Waiting struct {
	Item library.Item
	View library.View
}

type Queue struct {
	Torrents      []contract.Torrent
	TorrentsError *application.Error
	Queue         []Waiting
	QueueError    string
	Kinds         []string
}

func DescribeQueue() application.Definition {
	return application.Describe[QueueInput, Queue](QueueName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) Queue(ctx context.Context, in QueueInput) (application.Read[Queue], error) {
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[Queue]{}, err
	}
	out := Queue{Torrents: []contract.Torrent{}, Queue: []Waiting{}, Kinds: library.Kinds}
	if out.Torrents, err = set.Intake.List(ctx, contract.Filter{}); err != nil {
		var appErr *application.Error
		if appErr, err = known(classify(err)); err != nil {
			return application.Read[Queue]{}, err
		}
		out.Torrents, out.TorrentsError = []contract.Torrent{}, appErr
	}
	if !in.TorrentsOnly {
		if out.QueueError, err = s.queue(ctx, set, &out); err != nil {
			return application.Read[Queue]{}, err
		}
	}
	rev, err := s.revision(ctx, string(QueueName), in, out)
	if err != nil {
		return application.Read[Queue]{}, err
	}
	return application.Read[Queue]{Revision: rev, Data: out}, nil
}

func (s *Service) queue(ctx context.Context, set library.Settings, out *Queue) (string, error) {
	if err := set.Intake.Ready(ctx); err != nil {
		return err.Error(), nil
	}
	agent, err := set.Agent(protocol.CapabilityFile)
	if err != nil {
		return err.Error(), nil
	}
	found, errText, err := set.Items(ctx, set.Intake, agent, set.DownloadPrefix)
	if err != nil {
		return "", err
	}
	shows := library.Shows(ctx, agent)
	for _, it := range found {
		out.Queue = append(out.Queue, Waiting{Item: it, View: set.Planned(set.Defaults(it, shows))})
	}
	return errText, nil
}

func known(err error) (*application.Error, error) {
	appErr, ok := err.(*application.Error)
	if !ok {
		return nil, err
	}
	return appErr, nil
}
