package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

type Service struct {
	db      *db.DB
	state   *state.Store
	present notify.Presenter
}

func New(d *db.DB) *Service { return &Service{db: d, state: state.New(d)} }

func (s *Service) WithPresenter(present notify.Presenter) *Service {
	s.present = present
	return s
}

type Notification = notify.Notification
type Channel = notify.Channel
type PushDevice = notify.PushDevice
type Empty struct{}

const (
	ConfigurationName  application.Name = "notifications.configuration"
	InboxName          application.Name = "notifications.inbox"
	ChannelContentName application.Name = "notifications.channel-content"
	SubscribeName      application.Name = "notifications.subscribe"
	UnsubscribeName    application.Name = "notifications.unsubscribe"
	TestName           application.Name = "notifications.test"
	ResendName         application.Name = "notifications.resend"
	SendName           application.Name = "notifications.send"
	PhonePolicyName    application.Name = "notifications.phone-policy"
	RevokeLinksName    application.Name = "notifications.revoke-links"
	MarkName           application.Name = "notifications.mark"
)

func Describe() []application.Definition {
	op := []application.Authority{application.Operator}
	return []application.Definition{
		application.Describe[Empty, Configuration](ConfigurationName, application.Query, op, application.NoEffect, false),
		application.Describe[InboxInput, Inbox](InboxName, application.Query, op, application.NoEffect, false),
		application.Describe[ChannelContentInput, Empty](ChannelContentName, application.Command, op, application.NoEffect, true),
		application.Describe[MarkInput, Empty](MarkName, application.Command, op, application.NoEffect, true),
		application.Describe[SubscribeInput, Subscribed](SubscribeName, application.Command, op, application.NoEffect, true),
		application.Describe[UnsubscribeInput, Empty](UnsubscribeName, application.Command, op, application.NoEffect, true),
		application.Describe[Empty, TestResult](TestName, application.Command, op, application.External, true),
		application.Describe[ResendInput, Delivery](ResendName, application.Command, op, application.External, true),
		application.Describe[SendInput, Delivery](SendName, application.Command, []application.Authority{application.Agent, application.System}, application.External, true),
		application.Describe[PhonePolicyInput, Empty](PhonePolicyName, application.Command, op, application.NoEffect, true),
		application.Describe[Empty, Empty](RevokeLinksName, application.Command, op, application.NoEffect, true),
	}
}

var log = logx.Logger("atrium.notifications.application")

func (s *Service) audit(ctx context.Context, name application.Name, target string, outcome state.Outcome, ids []int64) int64 {
	id, err := s.state.Audit(context.WithoutCancel(ctx), nil, auditEntry(ctx, name, target, outcome, ids))

	if err != nil {
		log.Error("operation audit failed", "operation", string(name), logx.Err, err)
	}
	return id
}
func (s *Service) finish(ctx context.Context, name application.Name, target string, err error) (int64, error) {
	outcome := state.OK
	if err != nil {
		outcome = state.Failed
		var channel notify.ChannelError
		var push notify.PushError
		var pinned *settings.PinnedError
		var disabled *module.OwnerDisabled
		var te tool.Error
		var unsupported notify.UnsupportedTransport
		var invalid notify.DeviceInvalid
		if errors.As(err, &invalid) {
			err = &application.Error{Code: application.Invalid, Message: err.Error(), Fields: []application.Field{{Name: invalid.Field, Code: "invalid", Message: err.Error()}}, Cause: err}
		}
		if errors.As(err, &unsupported) {
			err = &application.Error{Code: application.Invalid, Message: err.Error(), Fields: []application.Field{{Name: "transport", Code: "unsupported_transport", Message: err.Error()}}, Cause: err}
		}
		if errors.As(err, &channel) || errors.As(err, &push) || errors.As(err, &pinned) || errors.As(err, &disabled) || errors.As(err, &te) {
			outcome = state.Refused
			err = &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
		}
		var e *application.Error
		if errors.As(err, &e) {
			switch e.Code {
			case application.Invalid:
				outcome = state.Invalid
			case application.NotFound:
				outcome = state.NotFound
			case application.Refused:
				outcome = state.Refused
			}
		}
	}
	return s.audit(ctx, name, target, outcome, nil), err
}
func notificationTarget(id int64) string { return "notification:" + strconv.FormatInt(id, 10) }

func auditEntry(ctx context.Context, name application.Name, target string, outcome state.Outcome, ids []int64) state.Entry {
	details := "{}"
	if len(ids) > 0 {
		raw, _ := json.Marshal(map[string][]int64{"notification_ids": ids})
		details = string(raw)
	}
	return state.Entry{Operation: string(name), Target: target, Outcome: outcome, DetailsJSON: details}
}
func (s *Service) mutate(ctx context.Context, name application.Name, target string, fn func(*sql.Tx) error) (int64, error) {
	var id int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		var err error
		id, err = s.state.Audit(ctx, tx, auditEntry(ctx, name, target, state.OK, nil))
		return err
	})
	if err != nil {
		return s.finish(ctx, name, target, err)
	}
	return id, nil
}
