package safety

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
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

func (WriteRefused) PyClass() string { return "WriteRefused" }

func (WriteRefused) PyModule() string { return "atrium.safety" }

func ApprovedWrites(ctx context.Context, proposalID string) context.Context {
	return context.WithValue(ctx, approvedKey{}, proposalID)
}

func OperatorSend(ctx context.Context, sentMailbox string) context.Context {
	return context.WithValue(ctx, operatorSendKey{}, sentMailbox)
}

func AutomaticWrites(ctx context.Context, pausePath string) context.Context {
	return context.WithValue(ctx, automaticKey{}, pausePath)
}

func ApprovedProposal(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(approvedKey{}).(string)
	return id, ok
}

func IsPaused(pausePath string) bool {
	_, err := os.Stat(pausePath)
	return err == nil
}

func SetPaused(pausePath string, paused bool) error {
	if !paused {
		if err := os.Remove(pausePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(pausePath), 0o777); err != nil {
		return err
	}
	now := time.Now()
	if os.Chtimes(pausePath, now, now) == nil {
		return nil
	}
	f, err := os.OpenFile(pausePath, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	return f.Close()
}

type ModeFunc func(ctx context.Context) (string, error)

func WriteGate(ctx context.Context, current ModeFunc, action, target string) error {
	if pausePath, ok := ctx.Value(automaticKey{}).(string); ok && IsPaused(pausePath) {
		return WriteRefused(fmt.Sprintf("refusing automatic mailbox write %s: atrium is paused", py.Repr(action)))
	}
	if _, ok := ApprovedProposal(ctx); ok {
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
		return WriteRefused(fmt.Sprintf("refusing mailbox write %s: mail_mode (ATRIUM_MAIL_MAIL_MODE) is not %s", py.Repr(action), Live))
	}
	return nil
}
