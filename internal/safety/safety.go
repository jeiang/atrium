package safety

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/pause"
)

const (
	Live         = config.Live
	Shadow       = config.Shadow
	AppendAction = "IMAP APPEND"
)

type (
	approvedKey     struct{}
	operatorSendKey struct{}
	automaticKey    struct{}
)

type WriteRefused string

func (e WriteRefused) Error() string { return string(e) }

func ApprovedWrites(ctx context.Context, proposalID string) context.Context {
	return context.WithValue(ctx, approvedKey{}, proposalID)
}

func OperatorSend(ctx context.Context, sentMailbox string) context.Context {
	return context.WithValue(ctx, operatorSendKey{}, sentMailbox)
}

// OperatorSendTarget returns the Sent mailbox an operator send may APPEND its copy to.
func OperatorSendTarget(ctx context.Context) (string, bool) {
	sent, ok := ctx.Value(operatorSendKey{}).(string)
	return sent, ok
}

func AutomaticWrites(ctx context.Context, pausePath string) context.Context {
	return context.WithValue(ctx, automaticKey{}, pausePath)
}

func approvedProposal(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(approvedKey{}).(string)
	return id, ok
}

func ApprovalIsConsent(ctx context.Context) bool {
	_, ok := approvedProposal(ctx)
	return ok
}

type ModeFunc func(ctx context.Context) (string, error)

func WriteGate(ctx context.Context, current ModeFunc, action, target string) error {
	if pausePath, ok := ctx.Value(automaticKey{}).(string); ok && pause.Paused(pausePath) {
		return WriteRefused(fmt.Sprintf("refusing automatic mailbox write %s: atrium is paused", strconv.Quote(action)))
	}
	if ApprovalIsConsent(ctx) {
		return nil
	}
	if sent, ok := ctx.Value(operatorSendKey{}).(string); ok && action == AppendAction && target == sent {
		return nil
	}
	mode := ""
	if current != nil {
		var err error
		if mode, err = current(ctx); err != nil {
			return err
		}
	}
	if mode != Live {
		return WriteRefused(fmt.Sprintf("refusing mailbox write %s: mail_mode (ATRIUM_MAIL_MAIL_MODE) is not %s", strconv.Quote(action), Live))
	}
	return nil
}
