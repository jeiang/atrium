package smtp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

// Session is the part of smtplib.SMTP smtp.send drives, after the connect the Factory made.
type Session interface {
	StartTLS() error
	Login(user, password string) error
	SendMessage(msg *Message, from string, to []string) (Refusals, error)
	// Quit is the context manager's exit: QUIT, an error for a reply other than 221 unless the server is
	// already gone, and the connection closed in every case.
	Quit() error
}

// Factory is SMTP_FACTORY: it connects and reads the greeting.
type Factory func(ctx context.Context, host string, port int, timeout time.Duration) (Session, error)

// Dialer makes real sessions. Zero values use the system resolver, the system roots with the SMTP host as the
// verified name, and the host's FQDN for EHLO.
type Dialer struct {
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	TLSConfig   *tls.Config
	LocalName   string
}

const (
	maxLine      = 8192
	maxChallenge = 5
)

// Dial is the Factory over TCP.
func (d Dialer) Dial(ctx context.Context, host string, port int, timeout time.Duration) (Session, error) {
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, osError(err)
	}
	s := &session{conn: conn, r: bufio.NewReader(conn), host: host, timeout: timeout, tlsConfig: d.TLSConfig}
	code, msg, err := s.getreply()
	if err != nil {
		return nil, err
	}
	if code != 220 {
		s.close()
		return nil, &ResponseError{Kind: "SMTPConnectError", Code: code, Msg: msg}
	}
	s.localName = d.LocalName
	if s.localName == "" {
		s.localName = localHostname(ctx)
	}
	return s, nil
}

// localHostname is the EHLO name smtplib picks: socket.getfqdn(), or a domain literal of the host's address
// when that has no dot.
func localHostname(ctx context.Context) string {
	name, _ := os.Hostname()
	fqdn := name
	if addrs, err := net.DefaultResolver.LookupHost(ctx, name); err == nil && len(addrs) > 0 {
		if names, err := net.DefaultResolver.LookupAddr(ctx, addrs[0]); err == nil {
			for _, n := range names {
				if n = strings.TrimSuffix(n, "."); strings.Contains(n, ".") {
					fqdn = n
					break
				}
			}
		}
	}
	if strings.Contains(fqdn, ".") {
		return fqdn
	}
	addr := "127.0.0.1"
	if ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", name); err == nil && len(ips) > 0 {
		addr = ips[0].String()
	}
	return "[" + addr + "]"
}

type session struct {
	conn      net.Conn
	r         *bufio.Reader
	host      string
	timeout   time.Duration
	tlsConfig *tls.Config
	localName string

	heloDone   bool
	ehloDone   bool
	doesESMTP  bool
	features   map[string]string
	utf8Cmds   bool
	challenges int
}

func (s *session) close() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

func (s *session) deadline() {
	if s.timeout > 0 {
		_ = s.conn.SetDeadline(time.Now().Add(s.timeout))
	}
}

func (s *session) send(b []byte) error {
	if s.conn == nil {
		return &Failure{Kind: disconnected, Msg: "please run connect() first"}
	}
	s.deadline()
	if _, err := s.conn.Write(b); err != nil {
		s.close()
		return &Failure{Kind: disconnected, Msg: "Server not connected"}
	}
	return nil
}

func (s *session) putcmd(cmd, args string) error {
	line := cmd
	if args != "" {
		line = cmd + " " + args
	}
	if strings.ContainsAny(line, "\r\n") {
		line = strings.ReplaceAll(strings.ReplaceAll(line, "\n", `\n`), "\r", `\r`)
		return &Failure{Kind: "ValueError", Msg: "command and arguments contain prohibited newline characters: " + line}
	}
	if !s.utf8Cmds {
		if err := py.ASCIIEncodeError(line); err != nil {
			return err
		}
	}
	return s.send([]byte(line + "\r\n"))
}

func (s *session) readline() ([]byte, error) {
	var line []byte
	for len(line) <= maxLine {
		b, err := s.r.ReadByte()
		if errors.Is(err, io.EOF) {
			return line, nil
		}
		if err != nil {
			return line, err
		}
		line = append(line, b)
		if b == '\n' {
			break
		}
	}
	return line, nil
}

func (s *session) getreply() (int, string, error) {
	if s.conn == nil {
		return 0, "", &Failure{Kind: disconnected, Msg: "please run connect() first"}
	}
	var resp []string
	code := -1
	for {
		s.deadline()
		line, err := s.readline()
		if err != nil {
			s.close()
			return 0, "", &Failure{Kind: disconnected, Msg: "Connection unexpectedly closed: " + osError(err).Error()}
		}
		if len(line) == 0 {
			s.close()
			return 0, "", &Failure{Kind: disconnected, Msg: "Connection unexpectedly closed"}
		}
		if len(line) > maxLine {
			s.close()
			return 0, "", &ResponseError{Kind: "SMTPResponseException", Code: 500, Msg: "Line too long.", text: true}
		}
		var rest []byte
		if len(line) > 4 {
			rest = line[4:]
		}
		resp = append(resp, string(bytes.Trim(rest, " \t\r\n")))
		n, err := strconv.Atoi(strings.TrimSpace(string(line[:min(3, len(line))])))
		if err != nil {
			code = -1
			break
		}
		code = n
		if len(line) < 4 || line[3] != '-' {
			break
		}
	}
	return code, strings.Join(resp, "\n"), nil
}

func (s *session) docmd(cmd, args string) (int, string, error) {
	if err := s.putcmd(cmd, args); err != nil {
		return 0, "", err
	}
	return s.getreply()
}

func (s *session) helo() (int, string, error) {
	if err := s.putcmd("helo", s.localName); err != nil {
		return 0, "", err
	}
	code, msg, err := s.getreply()
	if err == nil {
		s.heloDone = true
	}
	return code, msg, err
}

func (s *session) ehlo() (int, string, error) {
	s.features = map[string]string{}
	if err := s.putcmd("ehlo", s.localName); err != nil {
		return 0, "", err
	}
	code, msg, err := s.getreply()
	if err != nil {
		return 0, "", err
	}
	if code == -1 && msg == "" {
		s.close()
		return 0, "", &Failure{Kind: disconnected, Msg: "Server not connected"}
	}
	s.ehloDone = true
	if code != 250 {
		return code, msg, nil
	}
	s.doesESMTP = true
	for _, each := range strings.Split(msg, "\n")[1:] {
		if len(each) >= 5 && strings.EqualFold(each[:5], "auth=") {
			s.features["auth"] += " " + each[5:]
			continue
		}
		i := 0
		for i < len(each) && (isAlnum(each[i]) || i > 0 && each[i] == '-') {
			i++
		}
		if i == 0 {
			continue
		}
		feature := strings.ToLower(each[:i])
		params := trimLatin1Space(each[i:])
		if feature == "auth" {
			s.features[feature] += " " + params
		} else {
			s.features[feature] = params
		}
	}
	return code, msg, nil
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isLatin1Space(c byte) bool {
	return c == ' ' || c >= '\t' && c <= '\r' || c >= 0x1c && c <= 0x1f || c == 0x85 || c == 0xa0
}

func trimLatin1Space(s string) string {
	start, end := 0, len(s)
	for start < end && isLatin1Space(s[start]) {
		start++
	}
	for end > start && isLatin1Space(s[end-1]) {
		end--
	}
	return s[start:end]
}

func (s *session) hasExtn(name string) bool {
	_, ok := s.features[strings.ToLower(name)]
	return ok
}

func (s *session) ehloOrHelo() error {
	if s.heloDone || s.ehloDone {
		return nil
	}
	code, _, err := s.ehlo()
	if err != nil {
		return err
	}
	if code >= 200 && code <= 299 {
		return nil
	}
	code, msg, err := s.helo()
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return &ResponseError{Kind: "SMTPHeloError", Code: code, Msg: msg}
	}
	return nil
}

func (s *session) StartTLS() error {
	if err := s.ehloOrHelo(); err != nil {
		return err
	}
	if !s.hasExtn("starttls") {
		return &Failure{Kind: notSupported, Msg: "STARTTLS extension not supported by server."}
	}
	code, reply, err := s.docmd("STARTTLS", "")
	if err != nil {
		return err
	}
	if code != 220 {
		return &ResponseError{Kind: "SMTPResponseException", Code: code, Msg: reply}
	}
	cfg := &tls.Config{}
	if s.tlsConfig != nil {
		cfg = s.tlsConfig.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = s.host
	}
	conn := tls.Client(s.conn, cfg)
	s.deadline()
	if err := conn.Handshake(); err != nil {
		s.close()
		return osError(err)
	}
	s.conn = conn
	s.r = bufio.NewReader(conn)
	s.heloDone, s.ehloDone, s.doesESMTP = false, false, false
	s.features = map[string]string{}
	return nil
}

func (s *session) Login(user, password string) error {
	if err := s.ehloOrHelo(); err != nil {
		return err
	}
	if !s.hasExtn("auth") {
		return &Failure{Kind: notSupported, Msg: "SMTP AUTH extension not supported by server."}
	}
	advertised := strings.Fields(s.features["auth"])
	var authlist []string
	for _, mech := range [...]string{"CRAM-MD5", "PLAIN", "LOGIN"} {
		if slices.Contains(advertised, mech) {
			authlist = append(authlist, mech)
		}
	}
	if len(authlist) == 0 {
		return &Failure{Kind: "SMTPException", Msg: "No suitable authentication method found."}
	}
	var last error
	for _, mech := range authlist {
		err := s.auth(mech, user, password)
		var rerr *ResponseError
		if errors.As(err, &rerr) && rerr.Kind == "SMTPAuthenticationError" {
			last = err
			continue
		}
		return err
	}
	return last
}

func (s *session) authResponse(mech, user, password string, challenge []byte, initial bool) (string, bool) {
	switch mech {
	case "CRAM-MD5":
		if initial {
			return "", false
		}
		h := hmac.New(md5.New, []byte(password))
		h.Write(challenge)
		return user + " " + hex.EncodeToString(h.Sum(nil)), true
	case "PLAIN":
		return "\x00" + user + "\x00" + password, true
	}
	if initial || s.challenges < 2 {
		return user, true
	}
	return password, true
}

func (s *session) auth(mech, user, password string) error {
	var code int
	var resp string
	var err error
	if initial, ok := s.authResponse(mech, user, password, nil, true); ok {
		code, resp, err = s.docmd("AUTH", mech+" "+base64.StdEncoding.EncodeToString([]byte(initial)))
		s.challenges = 1
	} else {
		code, resp, err = s.docmd("AUTH", mech)
		s.challenges = 0
	}
	for err == nil && code == 334 {
		s.challenges++
		challenge, derr := decodeChallenge(resp)
		if derr != nil {
			return derr
		}
		answer, _ := s.authResponse(mech, user, password, challenge, false)
		code, resp, err = s.docmd(base64.StdEncoding.EncodeToString([]byte(answer)), "")
		if err == nil && s.challenges > maxChallenge {
			return &Failure{Kind: "SMTPException", Msg: fmt.Sprintf("Server AUTH mechanism infinite loop. Last response: (%d, %s)", code, py.ReprBytes([]byte(resp)))}
		}
	}
	if err != nil {
		return err
	}
	if code == 235 || code == 503 {
		return nil
	}
	return &ResponseError{Kind: "SMTPAuthenticationError", Code: code, Msg: resp}
}

// decodeChallenge is base64.decodebytes: characters outside the alphabet are skipped, padding is required.
func decodeChallenge(s string) ([]byte, error) {
	var clean []byte
	pads := 0
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/':
			clean = append(clean, c)
			pads = 0
		case c == '=':
			pads++
		}
	}
	switch n := len(clean) % 4; {
	case n == 1:
		return nil, &Failure{Kind: "Error", Msg: fmt.Sprintf("Invalid base64-encoded string: number of data characters (%d) cannot be 1 more than a multiple of 4", len(clean))}
	case n > 0 && pads < 4-n:
		return nil, &Failure{Kind: "Error", Msg: "Incorrect padding"}
	}
	return base64.RawStdEncoding.DecodeString(string(clean))
}

func quoteaddr(addr string) (string, error) {
	parsed, err := mime.ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if parsed != (mime.Addr{}) {
		return "<" + parsed.Address + ">", nil
	}
	if strings.HasPrefix(strings.TrimSpace(addr), "<") {
		return addr, nil
	}
	return "<" + addr + ">", nil
}

func (s *session) rset() {
	s.utf8Cmds = false
	_, _, _ = s.docmd("rset", "")
}

func (s *session) mail(from string, options []string) (int, string, error) {
	optionlist := ""
	if len(options) > 0 && s.doesESMTP {
		for _, o := range options {
			if strings.EqualFold(o, "smtputf8") {
				if !s.hasExtn("smtputf8") {
					return 0, "", &Failure{Kind: notSupported, Msg: "SMTPUTF8 not supported by server"}
				}
				s.utf8Cmds = true
				break
			}
		}
		optionlist = " " + strings.Join(options, " ")
	}
	quoted, err := quoteaddr(from)
	if err != nil {
		return 0, "", err
	}
	return s.docmd("mail", "from:"+quoted+optionlist)
}

func quotePeriods(b []byte) []byte {
	var out []byte
	for i, c := range b {
		if c == '.' && (i == 0 || b[i-1] == '\n') {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	return out
}

func (s *session) data(msg []byte) (int, string, error) {
	code, repl, err := s.docmd("data", "")
	if err != nil {
		return 0, "", err
	}
	if code != 354 {
		return 0, "", &ResponseError{Kind: "SMTPDataError", Code: code, Msg: repl}
	}
	q := quotePeriods(msg)
	if !bytes.HasSuffix(q, []byte("\r\n")) {
		q = append(q, "\r\n"...)
	}
	q = append(q, ".\r\n"...)
	if err := s.send(q); err != nil {
		return 0, "", err
	}
	return s.getreply()
}

func (s *session) abort(code int) {
	if code == 421 {
		s.close()
	} else {
		s.rset()
	}
}

func (s *session) SendMessage(msg *Message, from string, to []string) (Refusals, error) {
	if err := s.ehloOrHelo(); err != nil {
		return nil, err
	}
	international := py.ASCIIEncodeError(from+strings.Join(to, "")) != nil
	var mailOptions []string
	if international {
		if !s.hasExtn("smtputf8") {
			return nil, &Failure{Kind: notSupported, Msg: "One or more source or delivery addresses require internationalized email support, but the server does not advertise the required SMTPUTF8 capability"}
		}
		mailOptions = []string{"SMTPUTF8", "BODY=8BITMIME"}
	}
	flat, err := msg.Bytes("\r\n", international)
	if err != nil {
		return nil, err
	}
	var opts []string
	if s.doesESMTP {
		if s.hasExtn("size") {
			opts = append(opts, fmt.Sprintf("size=%d", len(flat)))
		}
		opts = append(opts, mailOptions...)
	}
	code, resp, err := s.mail(from, opts)
	if err != nil {
		return nil, err
	}
	if code != 250 {
		s.abort(code)
		return nil, &SenderRefused{Code: code, Msg: resp, Sender: from}
	}
	var refused Refusals
	for _, each := range to {
		quoted, err := quoteaddr(each)
		if err != nil {
			return nil, err
		}
		code, resp, err := s.docmd("rcpt", "to:"+quoted)
		if err != nil {
			return nil, err
		}
		if code != 250 && code != 251 {
			refused = refused.set(Refusal{Addr: each, Code: code, Msg: resp})
		}
		if code == 421 {
			s.close()
			return nil, &RecipientsRefused{Refused: refused}
		}
	}
	if len(refused) == len(to) {
		s.rset()
		return nil, &RecipientsRefused{Refused: refused}
	}
	code, resp, err = s.data(flat)
	if err != nil {
		return nil, err
	}
	if code != 250 {
		s.abort(code)
		return nil, &ResponseError{Kind: "SMTPDataError", Code: code, Msg: resp}
	}
	return refused, nil
}

func (s *session) Quit() error {
	defer s.close()
	code, msg, err := s.docmd("QUIT", "")
	var f *Failure
	if errors.As(err, &f) && f.Kind == disconnected {
		return nil
	}
	if err != nil {
		return err
	}
	if code != 221 {
		return &ResponseError{Kind: "SMTPResponseException", Code: code, Msg: msg}
	}
	return nil
}
