package smtptest

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/mail/smtp"
)

type Stage string

const (
	StageConnect  Stage = "connect"
	StageStartTLS Stage = "starttls"
	StageLogin    Stage = "login"
	StageSend     Stage = "send"
	StageQuit     Stage = "quit"
)

type Sent struct {
	Host string
	Msg  *smtp.Message
	From string
	To   []string
}

type Server struct {
	Fail    func(stage Stage) error
	Refused smtp.Refusals
	Sent    []Sent
}

func (s *Server) fail(stage Stage) error {
	if s.Fail == nil {
		return nil
	}
	return s.Fail(stage)
}

func (s *Server) Factory(_ context.Context, host string, port int, timeout time.Duration) (smtp.Session, error) {
	if err := s.fail(StageConnect); err != nil {
		return nil, err
	}
	return &session{s: s, host: host}, nil
}

type session struct {
	s    *Server
	host string
}

func (c *session) StartTLS() error {
	return c.s.fail(StageStartTLS)
}

func (c *session) Login(user, password string) error {
	return c.s.fail(StageLogin)
}

func (c *session) SendMessage(msg *smtp.Message, from string, to []string) (smtp.Refusals, error) {
	if err := c.s.fail(StageSend); err != nil {
		return nil, err
	}
	c.s.Sent = append(c.s.Sent, Sent{Host: c.host, Msg: msg, From: from, To: to})
	return append(smtp.Refusals(nil), c.s.Refused...), nil
}

func (c *session) Quit() error {
	return c.s.fail(StageQuit)
}
