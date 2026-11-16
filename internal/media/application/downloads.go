package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const QueueName application.Name = "media.queue"

type QueueInput struct{}

type Waiting struct {
	Item media.Item
	View media.View
}

type Queue struct {
	Queue      []Waiting
	QueueError string
	Kinds      []string
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
	out := Queue{Queue: []Waiting{}, Kinds: media.Kinds}
	if out.QueueError, err = s.queue(ctx, set, &out); err != nil {
		return application.Read[Queue]{}, err
	}
	rev, err := s.revision(ctx, string(QueueName), in, out)
	if err != nil {
		return application.Read[Queue]{}, err
	}
	return application.Read[Queue]{Revision: rev, Data: out}, nil
}

func (s *Service) queue(ctx context.Context, set media.Settings, out *Queue) (string, error) {
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
	shows := media.Shows(ctx, agent)
	for _, it := range found {
		out.Queue = append(out.Queue, Waiting{Item: it, View: set.Planned(set.Defaults(it, shows))})
	}
	return errText, nil
}
