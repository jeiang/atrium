package application

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

type Message = notify.Message

func PushLabel(userAgent string) string { return notify.PushLabel(userAgent) }

type ChannelContentInput struct{ Name, Content string }
type SubscribeInput struct{ Transport, Endpoint, P256DH, Auth, Label string }
type UnsubscribeInput struct {
	Endpoint string
	DeviceID *int64
}
type ResendInput struct{ ID int64 }
type SendInput struct {
	Message notify.Message
	RunKind *tool.Kind
}
type Delivery struct {
	notify.Sent
	Outcome string
}
type TestResult struct {
	Sent    int
	Failure string
	Outcome string
}

func (s *Service) ChannelContent(ctx context.Context, in ChannelContentInput) (application.Change[Empty], error) {
	id, err := s.mutate(ctx, ChannelContentName, "channel", func(tx *sql.Tx) error { return notify.SetContent(ctx, tx, in.Name, in.Content) })
	return application.Change[Empty]{AuditID: id}, err
}
func (s *Service) Subscribe(ctx context.Context, in SubscribeInput) (application.Change[Empty], error) {
	if in.Transport == "" {
		in.Transport = notify.TransportWebPush
	}
	id, err := s.mutate(ctx, SubscribeName, "push-device", func(tx *sql.Tx) error {
		_, err := notify.RegisterDevice(ctx, tx, notify.RegisterDeviceInput{Transport: in.Transport, Endpoint: in.Endpoint, P256DH: in.P256DH, Auth: in.Auth, Label: in.Label})
		return err
	})
	return application.Change[Empty]{AuditID: id}, err
}
func (s *Service) Unsubscribe(ctx context.Context, in UnsubscribeInput) (application.Change[Empty], error) {
	id, err := s.mutate(ctx, UnsubscribeName, "push-device", func(tx *sql.Tx) error {
		if in.DeviceID != nil {
			return notify.RemoveDevice(ctx, tx, *in.DeviceID)
		}
		return notify.Unsubscribe(ctx, tx, in.Endpoint)
	})
	return application.Change[Empty]{AuditID: id}, err
}
func (s *Service) Test(ctx context.Context, _ Empty) (application.Change[TestResult], error) {
	n, failure, err := notify.PushTest(ctx, s.db)
	outcome := "delivered"
	if n == 0 {
		outcome = "not_delivered"
		if failure == "" {
			failure = notify.NoDevices
		}
		if err == nil {
			err = notify.PushError("Test not delivered: " + failure)
		}
	}
	id, err := s.finish(ctx, TestName, "push", err)
	return application.Change[TestResult]{Data: TestResult{Sent: n, Failure: failure, Outcome: outcome}, AuditID: id}, err
}
func (s *Service) Resend(ctx context.Context, in ResendInput) (application.Change[Delivery], error) {
	sent, err := notify.Resend(ctx, s.db, in.ID, s.present)
	outcome := "delivered"
	if !sent.Delivered {
		outcome = "not_delivered"
		if err == nil {
			err = notify.ChannelError("Not delivered: " + sent.Error)
		}
	}
	id, err := s.finish(ctx, ResendName, notificationTarget(in.ID), err)
	return application.Change[Delivery]{Data: Delivery{Sent: sent, Outcome: outcome}, AuditID: id}, err
}
func (s *Service) Send(ctx context.Context, in SendInput) (application.Change[Delivery], error) {
	if in.RunKind != nil {
		inv, _ := application.InvocationOf(ctx)
		var used int
		err := s.db.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE run_id = ?", inv.RunID).Scan(&used)
		if err == nil {
			key := notify.KeyPerRunCap
			if *in.RunKind == tool.Nightly {
				key = notify.KeyNightlyCap
			}
			resolved, e := settings.From(ctx).Resolve(ctx, s.db.Read, key)
			err = e
			if err == nil {
				limit, e := resolved.Int()
				err = e
				if err == nil && int64(used) >= limit {
					plural := "s"
					if limit == 1 {
						plural = ""
					}
					err = tool.Error(fmt.Sprintf("at most %d notification%s per run", limit, plural))
				}
			}
		}
		if err != nil {
			id, err := s.finish(ctx, SendName, "notification", err)
			return application.Change[Delivery]{AuditID: id}, err
		}
		in.Message.ThreadID = inv.ThreadID
		in.Message.RunID = inv.RunID
	}
	sent, err := notify.Send(ctx, s.db, in.Message)
	if err != nil {
		id, err := s.finish(ctx, SendName, "notification", err)
		return application.Change[Delivery]{AuditID: id}, err
	}
	outcome := state.OK
	label := "delivered"
	if !sent.Delivered {
		outcome = state.Failed
		label = "not_delivered"
	}
	if sent.Duplicate {
		outcome = state.Noop
		label = "duplicate"
	}
	var ids []int64
	if sent.ID > 0 {
		ids = []int64{sent.ID}
	}
	id := s.audit(ctx, SendName, notificationTarget(sent.ID), outcome, ids)
	return application.Change[Delivery]{Data: Delivery{Sent: sent, Outcome: label}, AuditID: id}, nil
}
