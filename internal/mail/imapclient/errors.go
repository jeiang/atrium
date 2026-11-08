package imapclient

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/emersion/go-imap/v2"
)

var (
	ErrAbort    = errors.New("abort")
	ErrReadonly = errors.New("readonly")
	ErrProtocol = errors.New("protocol")
)

var errTimedOut = errors.New("timed out")

type failure struct {
	Err error
	Msg string
}

func (e *failure) Error() string { return e.Msg }

func (e *failure) Unwrap() error { return e.Err }

func pyError(format string, a ...any) *failure {
	return &failure{Err: ErrProtocol, Msg: fmt.Sprintf(format, a...)}
}

func pyAbort(format string, a ...any) *failure {
	return &failure{Err: ErrAbort, Msg: fmt.Sprintf(format, a...)}
}

func pyReadonly(format string, a ...any) *failure {
	return &failure{Err: ErrReadonly, Msg: fmt.Sprintf(format, a...)}
}

func imapErr(err error) error {
	if err == nil {
		return nil
	}
	var e *imap.Error
	if errors.As(err, &e) {
		switch e.Type {
		case imap.StatusResponseTypeNo, imap.StatusResponseTypeBad:
			return pyError("%s", e.Text)
		}
	}
	if errors.Is(err, errTimedOut) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return pyAbort("%s", err.Error())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return pyAbort("%s", err.Error())
	}
	return err
}
