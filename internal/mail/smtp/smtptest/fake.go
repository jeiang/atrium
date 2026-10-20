package smtptest

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/mail/smtp"
)

type Call struct {
	Call     string   `json:"call"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	Timeout  int      `json:"timeout,omitempty"`
	User     string   `json:"user,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from_addr,omitempty"`
	To       []string `json:"to_addrs,omitempty"`
}

type Sent struct {
	Host string
	Msg  *smtp.Message
	From string
	To   []string
}

type Server struct {
	Fail    func(stage string) error
	Refused smtp.Refusals
	Calls   []Call
	Sent    []Sent
}

func (s *Server) fail(stage string) error {
	if s.Fail == nil {
		return nil
	}
	return s.Fail(stage)
}

func (s *Server) Factory(_ context.Context, host string, port int, timeout time.Duration) (smtp.Session, error) {
	s.Calls = append(s.Calls, Call{Call: "connect", Host: host, Port: port, Timeout: int(timeout / time.Second)})
	if err := s.fail("connect"); err != nil {
		return nil, err
	}
	return &session{s: s, host: host}, nil
}

type session struct {
	s    *Server
	host string
}

func (c *session) StartTLS() error {
	c.s.Calls = append(c.s.Calls, Call{Call: "starttls"})
	return c.s.fail("starttls")
}

func (c *session) Login(user, password string) error {
	c.s.Calls = append(c.s.Calls, Call{Call: "login", User: user, Password: password})
	return c.s.fail("login")
}

func (c *session) SendMessage(msg *smtp.Message, from string, to []string) (smtp.Refusals, error) {
	c.s.Calls = append(c.s.Calls, Call{Call: "send_message", From: from, To: to})
	if err := c.s.fail("send"); err != nil {
		return nil, err
	}
	c.s.Sent = append(c.s.Sent, Sent{Host: c.host, Msg: msg, From: from, To: to})
	return append(smtp.Refusals(nil), c.s.Refused...), nil
}

func (c *session) Quit() error {
	c.s.Calls = append(c.s.Calls, Call{Call: "quit"})
	return c.s.fail("quit")
}
