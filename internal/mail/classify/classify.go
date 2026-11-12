package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.mail.classify")

type Service struct {
	c *classifier.Service
}

func New(c *classifier.Service) *Service { return &Service{c: c} }

func MessageItem(account string, messageID int64) classifier.Item {
	return classifier.Item{Key: account + ":" + strconv.FormatInt(messageID, 10)}
}

type Flags struct {
	PersonAsking         bool `json:"person_asking"`
	DeadlineOrPaymentDue bool `json:"deadline_or_payment_due"`
	SecurityEvent        bool `json:"security_event"`
	Flag                 bool `json:"flag"`
}

func (s *Service) JudgeFlag(ctx context.Context, item classifier.Item, m Message, body string) (Flags, error) {
	var flags Flags
	err := s.judge(ctx, classifier.Flag, item, flagSystem, messagePrompt(m, body), flagSchema, &flags)
	return flags, err
}

func (s *Service) JudgeEvent(ctx context.Context, item classifier.Item, m Message, body string) (bool, error) {
	var answer struct {
		HasEvent bool `json:"has_event"`
	}
	err := s.judge(ctx, classifier.Event, item, eventSystem, messagePrompt(m, body), eventSchema, &answer)
	return answer.HasEvent, err
}

func (s *Service) judge(ctx context.Context, caller classifier.Caller, item classifier.Item, system, user string, schema json.RawMessage, v any) error {
	raw, err := s.c.Classify(ctx, classifier.Request{Caller: caller, Item: item, System: system, User: user, Schema: schema})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	return nil
}

type Deferral struct {
	first *classifier.Deferred
}

func (d *Deferral) Stopped() bool { return d.first != nil }

func (d *Deferral) Err() error {
	if d.first == nil {
		return nil
	}
	return d.first
}

func (d *Deferral) Note(err error) bool {
	var deferred *classifier.Deferred
	if !errors.As(err, &deferred) {
		return false
	}
	if d.first == nil {
		d.first = deferred
		log.Warn("paid classification deferred for the rest of this pass; free stages continue",
			"caller", string(deferred.Caller), "item", deferred.ItemKey, "reason", deferred.Reason)
	}
	return true
}

func Unanswered(err error) (final, ok bool) {
	var unanswered *classifier.Unanswered
	if !errors.As(err, &unanswered) {
		return false, false
	}
	if unanswered.Reason != classifier.ReasonExhausted {
		log.Warn("paid classification gave no usable answer for this message; the pass continues",
			"caller", string(unanswered.Caller), "item", unanswered.ItemKey, "reason", unanswered.Reason, "final", unanswered.Final,
			logx.Err, unanswered.Error())
	}
	return unanswered.Final, true
}
