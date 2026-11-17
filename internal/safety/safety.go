package safety

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
)

const (
	Live   = config.Live
	Shadow = config.Shadow
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

func AutomaticWritesPause(ctx context.Context) (string, bool) {
	pausePath, ok := ctx.Value(automaticKey{}).(string)
	return pausePath, ok
}

func approvedProposal(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(approvedKey{}).(string)
	return id, ok
}

func ApprovalIsConsent(ctx context.Context) bool {
	_, ok := approvedProposal(ctx)
	return ok
}
