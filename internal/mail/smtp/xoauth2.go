package smtp

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
)

// XOAUTH2Session is the optional SMTP auth path for a bearer token.
type XOAUTH2Session interface {
	LoginXOAUTH2(ctx context.Context, user string, token func(context.Context) (string, error), invalidate func()) error
}

// SendXOAUTH2 submits msg with an XOAUTH2 bearer token instead of a password.
func SendXOAUTH2(ctx context.Context, account, user string, msg *Message, recipients []string,
	token func(context.Context) (string, error), invalidate func()) (Refusals, error) {
	if len(recipients) == 0 {
		return nil, &Failure{Kind: "NoRecipients", Msg: "no recipients"}
	}
	return send(ctx, defaultFactory(), account, user, "", msg, recipients, token, invalidate)
}

func (s *session) LoginXOAUTH2(ctx context.Context, user string, token func(context.Context) (string, error), invalidate func()) error {
	if err := s.ehloOrHelo(); err != nil {
		return err
	}
	if !s.hasExtn("auth") {
		return &Failure{Kind: notSupported, Msg: "SMTP AUTH extension not supported by server."}
	}
	if !slices.Contains(strings.Fields(s.features["auth"]), "XOAUTH2") {
		return &Failure{Kind: notSupported, Msg: "XOAUTH2 not advertised by the server."}
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		value, err := token(ctx)
		if err != nil {
			return err
		}
		response := "user=" + user + "\x01auth=Bearer " + value + "\x01\x01"
		code, reply, err := s.docmd("AUTH", "XOAUTH2 "+base64.StdEncoding.EncodeToString([]byte(response)))
		if err != nil {
			return err
		}
		if code == 235 || code == 503 {
			return nil
		}
		if code == 334 {
			if err := s.putcmd("", ""); err != nil {
				return err
			}
			if code, reply, err = s.getreply(); err != nil {
				return err
			}
			if code == 235 {
				return nil
			}
		}
		last = &ResponseError{Kind: "SMTPAuthenticationError", Code: code, Msg: reply}
		if attempt == 0 && invalidate != nil {
			invalidate()
		}
	}
	return last
}
