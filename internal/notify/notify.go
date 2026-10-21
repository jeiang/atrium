package notify

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const notConfigured = "ntfy not configured (" + KeyURL + " and " + KeyTopic + ")"

var (
	control    = rx.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`, 0)
	threadLink = rx.MustCompile(`^/chat/\d+$`, 0)
)

type Notification struct {
	ID         int64      `json:"id"`
	TS         float64    `json:"ts"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	ThreadID   *int64     `json:"thread_id"`
	RunID      *int64     `json:"run_id"`
	ThreadLink *string    `json:"thread_link"`
	Delivered  store.Flag `json:"delivered"`
	Error      *string    `json:"error"`
}

type Message struct {
	Title, Body string
	ThreadID    int64
	RunID       int64
	ThreadLink  *string
	Now         func() float64
	Topic       string
	Click       string
}

type Sent struct {
	ID        int64
	Delivered bool
	Error     string
}

func Plain(text string, limit int) string {
	text = py.Strip(text)
	cleaned, err := control.Sub("", text, 0)
	if err != nil {
		cleaned = text
	}
	return tool.Clip(cleaned, limit)
}

func LinkFor(raw *string, threadID int64) (string, error) {
	if raw == nil {
		if threadID != 0 {
			return fmt.Sprintf("/chat/%d", threadID), nil
		}
		return "", nil
	}
	link := py.Strip(*raw)
	if isDigits(link) {
		link = "/chat/" + link
	}
	if m, _ := threadLink.Match(link); m == nil {
		return "", tool.Error("thread_link must be a thread id or /chat/<id>")
	}
	return link, nil
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return !unicode.Is(unicode.Nd, r) }) < 0
}

func Deliver(ctx context.Context, q db.Querier, env config.Env, title, body, topic, click string) (bool, string, error) {
	base, err := text(ctx, q, KeyURL)
	if err != nil {
		return false, "", err
	}
	if topic == "" {
		if topic, err = text(ctx, q, KeyTopic); err != nil {
			return false, "", err
		}
	}
	if base == "" || topic == "" {
		return false, notConfigured, nil
	}
	timeout, err := timeoutOf(ctx, q)
	if err != nil {
		return false, "", err
	}
	token := config.Ntfy(env).Token
	payload := py.Dict{{Key: "topic", Value: topic}, {Key: "title", Value: title}, {Key: "message", Value: body}}
	if click != "" {
		payload = append(payload, py.Pair{Key: "click", Value: click})
	}
	target, err := url.Parse(base)
	if err != nil {
		return false, "", &tool.Raised{Class: "InvalidURL"}
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return false, "ntfy unreachable: UnsupportedProtocol", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(py.JSONDumps(payload, py.JSONOpts{NoEnsureASCII: true, Compact: true})))
	if err != nil {
		return false, "", &tool.Raised{Class: "InvalidURL"}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := newClient(timeout)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		return false, "ntfy unreachable: " + errorClass(err), nil
	}
	if resp.StatusCode >= 400 {
		return false, fmt.Sprintf("ntfy %d", resp.StatusCode), nil
	}
	return true, "", nil
}

func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func errorClass(err error) string {
	var op *net.OpError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "RemoteProtocolError"
	case strings.Contains(err.Error(), "TLS handshake timeout"):
		return "ConnectTimeout"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "ConnectTimeout"
		}
		return "ConnectError"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "ConnectError"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "ReadTimeout"
	}
	return "ReadError"
}

func Send(ctx context.Context, q db.Querier, env config.Env, m Message) (Sent, error) {
	titleChars, err := whole(ctx, q, KeyTitleChars)
	if err != nil {
		return Sent{}, err
	}
	bodyChars, err := whole(ctx, q, KeyBodyChars)
	if err != nil {
		return Sent{}, err
	}
	title, body := Plain(m.Title, titleChars), Plain(m.Body, bodyChars)
	if title == "" {
		return Sent{}, tool.Error("title is required")
	}
	link, err := LinkFor(m.ThreadLink, m.ThreadID)
	if err != nil {
		return Sent{}, err
	}
	message := body
	var linkValue any
	if link != "" {
		message = body + "\n\nThread: " + link
		linkValue = link
	}
	now := m.Now
	if now == nil {
		now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	res, err := q.ExecContext(ctx, "INSERT INTO notifications (ts, title, body, thread_id, run_id, thread_link) VALUES (?, ?, ?, ?, ?, ?)",
		now(), title, body, nullID(m.ThreadID), nullID(m.RunID), linkValue)
	if err != nil {
		return Sent{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Sent{}, err
	}
	delivered, failure, err := Deliver(ctx, q, env, title, message, m.Topic, m.Click)
	if err != nil {
		return Sent{}, err
	}
	var failureValue any
	if failure != "" {
		failureValue = failure
	}
	if _, err := q.ExecContext(ctx, "UPDATE notifications SET delivered = ?, error = ? WHERE id = ?", delivered, failureValue, id); err != nil {
		return Sent{}, err
	}
	return Sent{ID: id, Delivered: delivered, Error: failure}, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func Recent(ctx context.Context, q db.Querier, limit int) ([]Notification, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, ts, title, body, thread_id, run_id, thread_link, delivered, error FROM notifications ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Notification, error) {
		var n Notification
		var threadID, runID sql.NullInt64
		var link, failure sql.NullString
		var delivered int64
		if err := r.Scan(&n.ID, &n.TS, &n.Title, &n.Body, &threadID, &runID, &link, &delivered, &failure); err != nil {
			return n, err
		}
		n.Delivered = store.Flag(delivered != 0)
		if threadID.Valid {
			n.ThreadID = &threadID.Int64
		}
		if runID.Valid {
			n.RunID = &runID.Int64
		}
		if link.Valid {
			n.ThreadLink = &link.String
		}
		if failure.Valid {
			n.Error = &failure.String
		}
		return n, nil
	})
}
