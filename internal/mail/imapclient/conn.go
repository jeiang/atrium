// Package imapclient is mail.Conn over go-imap v2: go-imap drives the session and encodes the commands, and
// every response is read again as Python's imaplib reads it, so the results are what imap.py returned.
package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/py"
)

const (
	port          = "993"
	bodyCapBytes  = 65536
	dialTimeout   = 30 * time.Second
	readTimeout   = 2 * time.Minute
	writeTimeout  = 2 * time.Minute
	idleKeepalive = 5 * time.Minute
)

var log = logx.Logger("atrium.imap")

var allowedVersions = []string{"IMAP4REV1", "IMAP4"}

var (
	anyState  = []string{"NONAUTH", "AUTH", "SELECTED", "LOGOUT"}
	authState = []string{"AUTH", "SELECTED"}
	states    = map[string][]string{
		"APPEND": authState, "CAPABILITY": anyState, "COPY": {"SELECTED"}, "CREATE": authState,
		"DELETE": authState, "EXAMINE": authState, "EXPUNGE": {"SELECTED"}, "FETCH": {"SELECTED"},
		"IDLE": authState, "LIST": authState, "LOGIN": {"NONAUTH"}, "LOGOUT": anyState, "NOOP": anyState,
		"SEARCH": {"SELECTED"}, "SELECT": authState, "STATUS": authState, "STORE": {"SELECTED"},
		"SUBSCRIBE": authState, "UID": {"SELECTED"},
	}
	idleChanges = []string{"EXISTS", "EXPUNGE", "FETCH", "RECENT"}
)

// New returns the IMAPS client for host, write-gated by env.
func New(host, user, password string, env config.Env) mail.Client {
	return mail.Gated(newConn(host, user, password), env)
}

func newConn(host, user, password string) *conn {
	c := &conn{
		user:          user,
		password:      password,
		addr:          net.JoinHostPort(host, port),
		tlsConfig:     &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		dialTimeout:   dialTimeout,
		readTimeout:   readTimeout,
		writeTimeout:  writeTimeout,
		idleKeepalive: idleKeepalive,
	}
	c.dial = c.dialTLS
	return c
}

// dialTLS is IMAP4_SSL with ssl.create_default_context(): certificate and host name verified.
func (c *conn) dialTLS(ctx context.Context) (net.Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: c.dialTimeout}, Config: c.tlsConfig}
	ctx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	return d.DialContext(ctx, "tcp", c.addr)
}

type conn struct {
	user, password string
	addr           string
	tlsConfig      *tls.Config
	dial           func(context.Context) (net.Conn, error)
	dialTimeout    time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration

	mu            sync.Mutex
	cl            *imapclient.Client
	tap           *tap
	ur            store
	caps          map[string]bool
	state         string
	readonly      bool
	idling        bool
	idleQ         []idleResponse
	lastCode      string
	sentPos       int
	idleKeepalive time.Duration
	inflight      *inflight
}

type inflight struct {
	command, name string
	from          int
}

var errNotConnected = errors.New("IMAP client is not connected")

// await runs fn while a watchdog closes the connection when ctx ends or no byte arrives for readTimeout, or for
// the longer window an IDLE wait sets.
func (c *conn) await(ctx context.Context, fn func() error) error {
	if c.cl == nil {
		return errNotConnected
	}
	t := c.tap
	t.touch(0)
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				t.kill(fmt.Errorf("panic: %v", v))
				err := fmt.Errorf("imapclient: panic reading the server's reply: %v", v)
				if w := c.inflight; w != nil && t.written(w.name, w.from) {
					err = &mail.WriteInterrupted{Command: w.command, Err: err}
				}
				c.inflight = nil
				done <- err
			}
		}()
		err := fn()
		c.inflight = nil
		done <- err
	}()
	timer := time.NewTimer(c.readTimeout)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			t.kill(ctx.Err())
			err := <-done
			if err == nil {
				return nil
			}
			var interrupted *mail.WriteInterrupted
			if errors.As(err, &interrupted) {
				return &mail.WriteInterrupted{Command: interrupted.Command, Err: ctx.Err()}
			}
			return ctx.Err()
		case <-timer.C:
			idle, watching, window := t.idleFor(c.readTimeout)
			if watching && idle >= window {
				t.kill(errTimedOut)
				return <-done
			}
			if !watching {
				idle = 0
			}
			timer.Reset(window - idle)
		}
	}
}

// read returns the next response, waiting for go-imap to receive it; failure is imaplib's socket error text.
func (c *conn) read() (response, error) {
	for {
		r, ok, failure, err := c.tap.next()
		if err != nil || ok {
			return r, err
		}
		if failure != nil {
			return r, socketError(failure)
		}
		select {
		case <-c.tap.arrived:
		case <-c.cl.Closed():
			if r, ok, _, err := c.tap.next(); err != nil || ok {
				return r, err
			}
			return r, socketError(errors.New("EOF"))
		}
	}
}

func socketError(failure error) *Error { return pyAbort("socket error: %s", failure.Error()) }

// apply stores a response as imaplib's _get_response does.
func (c *conn) apply(r response) {
	if r.kind == continuation {
		return
	}
	if r.kind == untagged {
		for _, f := range r.frags {
			c.appendUntagged(r.typ, f)
		}
	}
	if r.coded {
		c.appendUntagged(r.code, r.data)
	}
}

func (c *conn) appendUntagged(typ string, d datum) {
	if d.none {
		d = datum{text: []byte{}}
	}
	if !c.idling {
		c.ur.add(typ, d)
		return
	}
	if n := len(c.idleQ); n == 0 || !c.idleQ[n-1].data[len(c.idleQ[n-1].data)-1].tuple {
		c.idleQ = append(c.idleQ, idleResponse{typ: typ, data: []datum{d}})
		return
	}
	last := &c.idleQ[len(c.idleQ)-1]
	last.data = append(last.data, d)
}

// begin is imaplib's _command checks before a command is sent.
func (c *conn) begin(name string) error {
	if !slices.Contains(states[name], c.state) {
		return pyError("command %s illegal in state %s, only allowed in states %s", name, c.state, strings.Join(states[name], ", "))
	}
	for _, typ := range []string{"OK", "NO", "BAD"} {
		delete(c.ur, typ)
	}
	if _, ok := c.ur["READ-ONLY"]; ok && !c.readonly {
		return pyReadonly("mailbox status changed to READ-ONLY")
	}
	return nil
}

// beginUID is IMAP4.uid's state check, then _command's.
func (c *conn) beginUID(command string) error {
	if !slices.Contains(states[command], c.state) {
		return pyError("command %s illegal in state %s, only allowed in states %s", command, c.state, strings.Join(states[command], ", "))
	}
	return c.begin("UID")
}

func (c *conn) checkBye() error {
	if bye := c.ur["BYE"]; len(bye) > 0 && len(bye[len(bye)-1].text) > 0 {
		return pyAbort("%s", decodeReplace(bye[len(bye)-1].text, true))
	}
	return nil
}

func decodeReplace(b []byte, ascii bool) string {
	var s strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if ascii && b[0] >= 0x80 {
			r, n = utf8.RuneError, 1
		}
		s.WriteRune(r)
		b = b[n:]
	}
	return s.String()
}

// complete is imaplib's _command_complete: the tagged response to the command just sent.
func wrapCommand(name string, err error) error {
	var e *Error
	if errors.As(err, &e) {
		return &Error{Kind: e.Kind, Msg: fmt.Sprintf("command: %s => %s", name, e.Msg)}
	}
	return err
}

// markWrite names the write command in flight, so a panic while its reply is read is reported as an unknown
// outcome; await clears it when the command returns.
func (c *conn) markWrite(command, name string) { c.inflight = &inflight{command, name, c.sentPos} }

// ignoreAfterReply drops go-imap's own error once the server's tagged OK has been parsed: the write ran, and an
// error would make a caller retry it.
func (c *conn) ignoreAfterReply(command string, werr error) {
	if err := goErr(werr); err != nil {
		log.Warn("go-imap failed after the server accepted "+command, logx.Err, err.Error())
	}
}

// interrupted marks a failed completion of a write as outcome-unknown when the connection broke after go-imap
// wrote the command named name: its bytes may have been executed. A reply of NO or BAD is not a transport failure
// and a command never written is provably not applied; both stay as they are.
func (c *conn) interrupted(command, name string, err error) error {
	var e *Error
	if errors.As(err, &e) && e.Kind == "abort" && c.tap.written(name, c.sentPos) {
		return &mail.WriteInterrupted{Command: command, Err: err}
	}
	return err
}

// awaitSent waits until go-imap has written a command named name and returns its index in the tap's sent list.
func (c *conn) awaitSent(name string) (int, error) {
	for {
		if i := c.tap.target(name, c.sentPos); i >= 0 {
			return i, nil
		}
		if failure := c.tap.failure(); failure != nil {
			return -1, socketError(failure)
		}
		select {
		case <-c.tap.arrived:
		case <-c.cl.Closed():
			if i := c.tap.target(name, c.sentPos); i >= 0 {
				return i, nil
			}
			return -1, socketError(errors.New("EOF"))
		}
	}
}

// complete is imaplib's _command_complete: the tagged response to the command named name that was just sent.
// Tagged replies to commands go-imap sent by itself before it (IDLE re-issues) are skipped; a tag go-imap never
// sent is imaplib's unexpected tagged response.
func (c *conn) complete(name string) (string, []datum, error) {
	if err := c.checkBye(); err != nil {
		return "", nil, err
	}
	target, err := c.awaitSent(name)
	if err != nil {
		return "", nil, wrapCommand(name, err)
	}
	for {
		if err := c.checkBye(); err != nil {
			return "", nil, pyAbort("command: %s => %s", name, err.Error())
		}
		r, err := c.read()
		if err != nil {
			return "", nil, wrapCommand(name, err)
		}
		if r.kind != tagged {
			c.apply(r)
			continue
		}
		i := c.tap.sentIndex(r.tag, c.sentPos)
		if i < 0 {
			return "", nil, pyAbort("command: %s => unexpected tagged response: %s", name, py.ReprBytes(r.line))
		}
		if i != target {
			continue
		}
		c.sentPos = i + 1
		c.apply(r)
		data := []datum{{text: r.dat}}
		c.lastCode = ""
		if r.coded {
			c.lastCode = r.code
		}
		if err := c.checkBye(); err != nil {
			return "", nil, err
		}
		if r.typ == "BAD" {
			return "", nil, pyError("%s command error: %s %s", name, r.typ, listRepr(data))
		}
		return r.typ, data, nil
	}
}

func (c *conn) untaggedResponse(typ string, dat []datum, name string) []datum {
	if typ == "NO" {
		return dat
	}
	data, ok := c.ur.pop(name)
	if !ok {
		return []datum{{none: true}}
	}
	return data
}

func okError(typ string, data []datum) error {
	if typ != "OK" {
		return pyError("IMAP command failed: %s %s", typ, listRepr(data))
	}
	return nil
}

func quoteName(name string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), `"`, `\"`) + `"`
}

// checkArg is the bytes(arg, "ascii") and control character checks imaplib applies to a command argument.
func checkArg(arg string) error {
	for i, r := range arg {
		if r > 0x7f {
			return fmt.Errorf("'ascii' codec can't encode character %s in position %d: ordinal not in range(128)", fmt.Sprintf("'\\u%04x'", r), i)
		}
	}
	if strings.ContainsAny(arg, "\x00\r\n") {
		return errors.New("NUL, CR and LF not allowed in commands")
	}
	return nil
}

// mailboxArg is the name go-imap needs to send imapName, which go-imap encodes to modified UTF-7 itself.
func mailboxArg(imapName string) (string, error) {
	if err := checkArg(imapName); err != nil {
		return "", err
	}
	name, err := mail.UTF7Decode(imapName)
	if err != nil {
		return "", err
	}
	if mail.UTF7Encode(name) != imapName {
		return "", fmt.Errorf("mailbox name %s is not canonical modified UTF-7", quoteName(imapName))
	}
	return name, nil
}

func uidSet(uids mail.UIDSet) (imap.UIDSet, error) {
	if uids.All() {
		return imap.UIDSet{{Start: 1, Stop: 0}}, nil
	}
	if len(uids.List()) == 0 {
		return nil, errors.New("empty UID set")
	}
	var set imap.UIDSet
	for _, u := range uids.List() {
		set.AddNum(imap.UID(u))
	}
	return set, nil
}

// goErr keeps go-imap's error only when it is not a server reply, which complete already reported.
func goErr(err error) error {
	var e *imap.Error
	if err == nil || errors.As(err, &e) {
		return nil
	}
	return err
}

func (c *conn) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdown()
	nc, err := c.dial(ctx)
	if err != nil {
		return err
	}
	c.tap = newTap(nc, c.writeTimeout)
	c.cl = imapclient.New(c.tap, &imapclient.Options{IdleRestartInterval: c.idleKeepalive})
	c.ur, c.caps, c.state, c.readonly, c.idling, c.idleQ, c.sentPos = store{}, nil, "LOGOUT", false, false, nil, 0
	err = c.await(ctx, c.open)
	if err != nil {
		c.shutdown()
	}
	return err
}

// open is IMAP4._connect, IMAP4.login and ImapClient.connect's capability read.
func (c *conn) open() error {
	welcome, err := c.read()
	if err != nil {
		return err
	}
	c.apply(welcome)
	switch {
	case c.ur["PREAUTH"] != nil:
		c.state = "AUTH"
	case c.ur["OK"] != nil:
		c.state = "NONAUTH"
	default:
		return pyError("invalid greeting: %s", decodeReplace(welcome.line, true))
	}
	if err := c.refreshCaps(welcome.code != "CAPABILITY"); err != nil {
		return err
	}
	if !slices.ContainsFunc(allowedVersions, func(v string) bool { return c.caps[v] }) {
		return pyError("server not IMAP4 compliant")
	}
	for _, arg := range []string{c.user, c.password} {
		if err := checkArg(arg); err != nil {
			return err
		}
	}
	if err := c.begin("LOGIN"); err != nil {
		return err
	}
	werr := c.cl.Login(c.user, c.password).Wait()
	typ, dat, err := c.complete("LOGIN")
	if err != nil {
		return err
	}
	if typ != "OK" {
		return pyError("%s", decodeReplace(dat[len(dat)-1].text, false))
	}
	if err := goErr(werr); err != nil {
		return err
	}
	c.state = "AUTH"
	return c.refreshCaps(c.lastCode != "CAPABILITY")
}

// refreshCaps is imaplib's _refresh_capabilities. goSent says go-imap sent CAPABILITY on its own, which it
// does when the greeting or LOGIN reply carries no capability list.
func (c *conn) refreshCaps(goSent bool) error {
	data, had := c.ur.pop("CAPABILITY")
	if !had {
		if err := c.begin("CAPABILITY"); err != nil {
			return err
		}
		if !goSent {
			if _, err := c.cl.Capability().Wait(); goErr(err) != nil {
				return err
			}
		}
		typ, dat, err := c.complete("CAPABILITY")
		if err != nil {
			return err
		}
		data = c.untaggedResponse(typ, dat, "CAPABILITY")
		if len(data) == 1 && data[0].none {
			return pyError("no CAPABILITY response from server")
		}
	} else if goSent {
		if _, _, err := c.complete("CAPABILITY"); err != nil {
			return err
		}
		c.ur.pop("CAPABILITY")
	}
	last := data[len(data)-1]
	if last.tuple {
		return errors.New("decoding to str: need a bytes-like object, tuple found")
	}
	for i, b := range last.text {
		if b >= 0x80 {
			return fmt.Errorf("'ascii' codec can't decode byte 0x%02x in position %d: ordinal not in range(128)", b, i)
		}
	}
	c.caps = map[string]bool{}
	for _, name := range split(strings.ToUpper(string(last.text))) {
		c.caps[name] = true
	}
	return nil
}

func (c *conn) shutdown() {
	if c.cl == nil {
		return
	}
	_ = c.cl.Close()
	c.cl, c.tap = nil, nil
}

func (c *conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cl == nil {
		return
	}
	_ = c.await(context.Background(), func() error {
		c.state = "LOGOUT"
		return c.cl.Logout().Wait()
	})
	c.shutdown()
}

func (c *conn) HasCapability(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps[strings.ToUpper(name)]
}

// run runs one command under the lock and the watchdog.
func (c *conn) run(ctx context.Context, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.await(ctx, fn)
}

func (c *conn) ListMailboxes(ctx context.Context) ([]mail.MailboxInfo, error) {
	var out []mail.MailboxInfo
	err := c.run(ctx, func() error {
		if err := c.begin("LIST"); err != nil {
			return err
		}
		werr := c.cl.List("", "*", nil).Close()
		typ, dat, err := c.complete("LIST")
		if err != nil {
			return err
		}
		data := c.untaggedResponse(typ, dat, "LIST")
		if err := okError(typ, data); err != nil {
			return err
		}
		if err := goErr(werr); err != nil {
			return err
		}
		out, err = parseList(data)
		return err
	})
	return out, err
}

func (c *conn) Status(ctx context.Context, imapName string) (mail.Status, error) {
	var out mail.Status
	err := c.run(ctx, func() error {
		name, err := mailboxArg(imapName)
		if err != nil {
			return err
		}
		if err := c.begin("STATUS"); err != nil {
			return err
		}
		opts := &imap.StatusOptions{UIDValidity: true, UIDNext: true, NumMessages: true, HighestModSeq: c.caps["CONDSTORE"]}
		_, werr := c.cl.Status(name, opts).Wait()
		typ, dat, err := c.complete("STATUS")
		if err != nil {
			return err
		}
		data := c.untaggedResponse(typ, dat, "STATUS")
		if err := okError(typ, data); err != nil {
			return err
		}
		if err := goErr(werr); err != nil {
			return err
		}
		out, err = parseStatus(data)
		return err
	})
	return out, err
}

func (c *conn) Select(ctx context.Context, imapName string) error {
	return c.run(ctx, func() error { _, err := c.selectBox(imapName, true); return err })
}

func (c *conn) SelectRW(ctx context.Context, imapName string) (uint32, error) {
	var validity uint32
	err := c.run(ctx, func() (err error) {
		validity, err = c.selectBox(imapName, false)
		return err
	})
	return validity, err
}

// selectBox is IMAP4.select as ImapClient.select and select_rw call it.
func (c *conn) selectBox(imapName string, readonly bool) (uint32, error) {
	name, err := mailboxArg(imapName)
	if err != nil {
		return 0, err
	}
	c.ur, c.readonly = store{}, readonly
	command := "SELECT"
	if readonly {
		command = "EXAMINE"
	}
	if err := c.begin(command); err != nil {
		return 0, err
	}
	data, werr := c.cl.Select(name, &imap.SelectOptions{ReadOnly: readonly}).Wait()
	typ, dat, err := c.complete(command)
	if err != nil {
		return 0, err
	}
	if typ != "OK" {
		c.state = "AUTH"
		return 0, okError(typ, dat)
	}
	c.state = "SELECTED"
	if _, ok := c.ur["READ-ONLY"]; ok && !readonly {
		return 0, pyReadonly("%s is not writable", py.Repr(quoteName(imapName)))
	}
	if err := goErr(werr); err != nil || readonly {
		return 0, err
	}
	validity := selectedValidity(data, c.ur)
	if validity == 0 {
		return 0, pyAbort("SELECT did not report UIDVALIDITY")
	}
	return validity, nil
}

func selectedValidity(data *imap.SelectData, ur store) uint32 {
	if data != nil && data.UIDValidity != 0 {
		return data.UIDValidity
	}
	for _, d := range ur["UIDVALIDITY"] {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(d.text)), 10, 32); err == nil {
			return uint32(n)
		}
	}
	return 0
}

func (c *conn) UIDSearch(ctx context.Context, criteria mail.SearchCriteria) ([]uint32, error) {
	var out []uint32
	err := c.run(ctx, func() error {
		search := &imap.SearchCriteria{Since: criteria.Since}
		if criteria.UID != nil {
			set, err := uidSet(*criteria.UID)
			if err != nil {
				return err
			}
			search.UID = []imap.UIDSet{set}
		}
		if criteria.HeaderName != "" {
			search.Header = []imap.SearchCriteriaHeaderField{{Key: criteria.HeaderName, Value: criteria.HeaderValue}}
		}
		if criteria.GmailRaw != "" {
			search.Gmail.Raw = []string{criteria.GmailRaw}
		}
		if err := c.beginUID("SEARCH"); err != nil {
			return err
		}
		_, werr := c.cl.UIDSearch(search, nil).Wait()
		typ, dat, err := c.complete("UID")
		if err != nil {
			return err
		}
		data := c.untaggedResponse(typ, dat, "SEARCH")
		if err := okError(typ, data); err != nil {
			return err
		}
		if err := goErr(werr); err != nil {
			return err
		}
		out, err = parseSearch(data)
		return err
	})
	return out, err
}

// fetchOptions is imap.fetch_items(gmail, with_body) with CHANGEDSINCE, or mailops.VERIFY_ITEMS(_GMAIL).
func fetchOptions(items mail.FetchItems) *imap.FetchOptions {
	opts := &imap.FetchOptions{UID: true, Flags: true, ChangedSince: items.ChangedSince}
	opts.Gmail.Labels = items.Labels
	switch {
	case items.Header || items.Body:
		opts.InternalDate = true
		opts.RFC822Size = true
		opts.BodyStructure = &imap.FetchItemBodyStructure{Extended: true}
		opts.Gmail.MsgID = items.Labels
		opts.Gmail.ThrID = items.Labels
		opts.BodySection = []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader, Peek: true},
			{Specifier: imap.PartSpecifierText, Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: bodyCapBytes}},
		}
	case items.HeaderOnly:
		opts.BodySection = []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierHeader, Peek: true}}
	}
	return opts
}

func (c *conn) UIDFetch(ctx context.Context, uids mail.UIDSet, items mail.FetchItems) ([]mail.Message, error) {
	var out []mail.Message
	err := c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		if err := c.beginUID("FETCH"); err != nil {
			return err
		}
		werr := c.cl.Fetch(set, fetchOptions(items)).Close()
		typ, dat, err := c.complete("UID")
		if err != nil {
			return err
		}
		data := c.untaggedResponse(typ, dat, "FETCH")
		if err := okError(typ, data); err != nil {
			return err
		}
		if err := goErr(werr); err != nil {
			return err
		}
		out, err = parseFetch(data)
		return err
	})
	return out, err
}

// mutate is ImapClient.mutate after its write gate: the UID command's data, then every COPYUID code it got.
func (c *conn) mutate(command string, send func() error) ([]datum, error) {
	c.markWrite(command, "UID")
	c.ur.pop("COPYUID")
	if err := c.beginUID(command); err != nil {
		return nil, err
	}
	werr := send()
	typ, dat, err := c.complete("UID")
	if err != nil {
		return nil, c.interrupted(command, "UID", err)
	}
	data := c.untaggedResponse(typ, dat, "FETCH")
	if err := okError(typ, data); err != nil {
		return nil, err
	}
	copied, _ := c.ur.pop("COPYUID")
	for _, d := range copied {
		data = append(data, datum{text: append([]byte("COPYUID "), d.text...)})
	}
	c.ignoreAfterReply(command, werr)
	return data, nil
}

func (c *conn) Copy(ctx context.Context, uids mail.UIDSet, destIMAPName string) (*mail.CopyUID, error) {
	var out *mail.CopyUID
	err := c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		dest, err := mailboxArg(destIMAPName)
		if err != nil {
			return err
		}
		data, err := c.mutate("COPY", func() error {
			_, err := c.cl.Copy(set, dest).Wait()
			return err
		})
		if err != nil {
			return err
		}
		out, err = parseCopyUID(data)
		return err
	})
	return out, err
}

func storeOp(add bool) imap.StoreFlagsOp {
	if add {
		return imap.StoreFlagsAdd
	}
	return imap.StoreFlagsDel
}

func (c *conn) StoreFlags(ctx context.Context, uids mail.UIDSet, add bool, flags ...string) error {
	return c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		store := &imap.StoreFlags{Op: storeOp(add), Silent: true}
		for _, f := range flags {
			store.Flags = append(store.Flags, imap.Flag(f))
		}
		_, err = c.mutate("STORE", func() error { return c.cl.Store(set, store, nil).Close() })
		return err
	})
}

func (c *conn) StoreLabels(ctx context.Context, uids mail.UIDSet, add bool, labels ...string) error {
	return c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		store := &imap.StoreLabels{Op: storeOp(add), Labels: labels}
		_, err = c.mutate("STORE", func() error { return c.cl.StoreLabels(set, store).Close() })
		return err
	})
}

func (c *conn) UIDExpunge(ctx context.Context, uids mail.UIDSet) error {
	return c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		_, err = c.mutate("EXPUNGE", func() error { return c.cl.UIDExpunge(set).Close() })
		return err
	})
}

// mapCRLF is imaplib's MapCRLF.sub(CRLF, message): every CR, LF and CRLF becomes CRLF.
func mapCRLF(raw []byte) []byte {
	out := make([]byte, 0, len(raw)+bytes.Count(raw, []byte("\n")))
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			out = append(out, '\r', '\n')
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, raw[i])
		}
	}
	return out
}

func (c *conn) Append(ctx context.Context, imapName string, raw []byte, flags ...string) error {
	return c.run(ctx, func() error {
		name, err := mailboxArg(imapName)
		if err != nil {
			return err
		}
		if len(flags) == 0 {
			flags = []string{`\Seen`}
		}
		opts := &imap.AppendOptions{Time: time.Now()}
		for _, f := range flags {
			opts.Flags = append(opts.Flags, imap.Flag(f))
		}
		literal := mapCRLF(raw)
		if err := c.begin("APPEND"); err != nil {
			return err
		}
		c.markWrite("APPEND", "APPEND")
		werr := func() error {
			cmd := c.cl.Append(name, int64(len(literal)), opts)
			if _, err := cmd.Write(literal); err != nil {
				_ = cmd.Close()
				return err
			}
			if err := cmd.Close(); err != nil {
				return err
			}
			_, err := cmd.Wait()
			return err
		}()
		typ, dat, err := c.complete("APPEND")
		if err != nil {
			return c.interrupted("APPEND", "APPEND", err)
		}
		if err := okError(typ, dat); err != nil {
			return err
		}
		c.ignoreAfterReply("APPEND", werr)
		return nil
	})
}

// mailboxCommand is ImapClient.mutate_mailbox after its write gate.
func (c *conn) mailboxCommand(ctx context.Context, command, imapName string, send func(name string) error) error {
	return c.run(ctx, func() error {
		name, err := mailboxArg(imapName)
		if err != nil {
			return err
		}
		if err := c.begin(command); err != nil {
			return err
		}
		c.markWrite(command, command)
		werr := send(name)
		typ, dat, err := c.complete(command)
		if err != nil {
			return c.interrupted(command, command, err)
		}
		if err := okError(typ, dat); err != nil {
			return err
		}
		c.ignoreAfterReply(command, werr)
		return nil
	})
}

func (c *conn) CreateMailbox(ctx context.Context, imapName string) error {
	return c.mailboxCommand(ctx, "CREATE", imapName, func(name string) error { return c.cl.Create(name, nil).Wait() })
}

func (c *conn) SubscribeMailbox(ctx context.Context, imapName string) error {
	return c.mailboxCommand(ctx, "SUBSCRIBE", imapName, func(name string) error { return c.cl.Subscribe(name).Wait() })
}

func (c *conn) DeleteMailbox(ctx context.Context, imapName string) error {
	return c.mailboxCommand(ctx, "DELETE", imapName, func(name string) error { return c.cl.Delete(name).Wait() })
}

func (c *conn) Noop(ctx context.Context) error {
	return c.run(ctx, func() error {
		if err := c.begin("NOOP"); err != nil {
			return err
		}
		werr := c.cl.Noop().Wait()
		if _, _, err := c.complete("NOOP"); err != nil {
			return err
		}
		return goErr(werr)
	})
}

// IdleChanges is ImapClient.idle_changes: IDLE until the server reports EXISTS, EXPUNGE, FETCH or RECENT
// (true) or duration passes (false). go-imap ends and re-issues the IDLE every idleKeepalive; the tagged reply of
// each is the server's proof of life, and a server silent for idleKeepalive plus readTimeout loses the
// connection.
func (c *conn) IdleChanges(ctx context.Context, duration time.Duration) (bool, error) {
	var changed bool
	err := c.run(ctx, func() error {
		if !c.caps["IDLE"] {
			return pyError("Server does not support IMAP4 IDLE")
		}
		c.idling = true
		if err := c.begin("IDLE"); err != nil {
			c.idling = false
			return err
		}
		idle, werr := c.cl.Idle()
		if err := c.awaitIdleStart(); err != nil {
			c.idling, c.idleQ = false, nil
			if werr == nil {
				_ = idle.Close()
			}
			return err
		}
		if werr != nil {
			c.idling = false
			return werr
		}
		var err error
		changed, err = c.idleWait(time.Now().Add(duration))
		return c.idleExit(idle, err)
	})
	return changed, err
}

// awaitIdleStart is Idler.__enter__'s wait for the continuation request.
func (c *conn) awaitIdleStart() error {
	for {
		r, err := c.read()
		if err != nil {
			return err
		}
		c.apply(r)
		switch r.kind {
		case continuation:
			return nil
		case tagged:
			if r.typ == "NO" {
				return pyError("idle denied: %s", listRepr([]datum{{text: r.dat}}))
			}
			return pyAbort("unexpected status response: %s", py.ReprBytes(r.line))
		}
	}
}

// idleWait is the iteration over the Idler until a change or the deadline.
func (c *conn) idleWait(deadline time.Time) (bool, error) {
	c.tap.touch(c.idleKeepalive + c.readTimeout)
	defer c.tap.touch(0)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		for len(c.idleQ) > 0 {
			resp := c.idleQ[0]
			c.idleQ = c.idleQ[1:]
			if slices.Contains(idleChanges, resp.typ) {
				return true, nil
			}
		}
		r, ok, failure, err := c.tap.next()
		if err != nil {
			return false, err
		}
		if ok {
			if r.kind == untagged {
				c.apply(r)
			}
			continue
		}
		if failure != nil {
			return false, socketError(failure)
		}
		select {
		case <-c.tap.arrived:
		case <-c.cl.Closed():
			if r, ok, _, err := c.tap.next(); err != nil {
				return false, err
			} else if ok {
				if r.kind == untagged {
					c.apply(r)
				}
				continue
			}
			return false, socketError(errors.New("EOF"))
		case <-timer.C:
			return false, nil
		}
	}
}

// idleExit is Idler.__exit__: leftover responses go to the untagged responses, then DONE.
func (c *conn) idleExit(idle *imapclient.IdleCommand, failure error) error {
	c.idling = false
	for _, resp := range c.idleQ {
		for _, d := range resp.data {
			c.ur.add(resp.typ, d)
		}
	}
	c.idleQ = nil
	werr := idle.Close()
	if werr == nil {
		werr = idle.Wait()
	}
	if _, _, err := c.complete("IDLE"); err != nil {
		return err
	}
	if failure != nil {
		return failure
	}
	return goErr(werr)
}
