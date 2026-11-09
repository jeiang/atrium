package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

const (
	port          = "993"
	dialTimeout   = 30 * time.Second
	readTimeout   = 2 * time.Minute
	writeTimeout  = 2 * time.Minute
	idleKeepalive = 5 * time.Minute
)

var errNotConnected = errors.New("IMAP client is not connected")

type conn struct {
	user, password string
	method         config.AuthMethod
	token          func(context.Context) (string, error)
	invalidate     func()
	addr           string
	tlsConfig      *tls.Config
	dial           func(context.Context) (net.Conn, error)
	dialTimeout    time.Duration
	readTimeout    time.Duration
	writeTimeout   time.Duration

	mu            sync.Mutex
	cl            *imapclient.Client
	tap           *tap
	caps          imap.CapSet
	idleKeepalive time.Duration
	bodyCap       int
	inflight      *inflight
	idleChanged   chan struct{}
}

type inflight struct {
	command, name string
	from          int
}

func New(host, user, password string) *conn {
	return newConn(host, user, password)
}

// NewXOAUTH2 authenticates with a bearer token instead of a password. The
// token function refreshes as needed; invalidate drops a cached token so an
// auth failure can retry once with a fresh one.
func NewXOAUTH2(host, user string, token func(context.Context) (string, error), invalidate func()) *conn {
	c := newConn(host, user, "")
	c.method = config.AuthXOAUTH2
	c.token = token
	c.invalidate = invalidate
	return c
}

func (c *conn) setBodyCap(n int) { c.bodyCap = n }

func newConn(host, user, password string) *conn {
	addr, serverName := config.IMAPEndpoint(host)
	tlsConfig := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if path, ok := config.OS("ATRIUM_IMAP_CA"); ok && path != "" {
		if pem, err := os.ReadFile(path); err == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				tlsConfig.RootCAs = pool
			}
		}
	}
	c := &conn{
		user:          user,
		password:      password,
		addr:          addr,
		tlsConfig:     tlsConfig,
		dialTimeout:   dialTimeout,
		readTimeout:   readTimeout,
		writeTimeout:  writeTimeout,
		idleKeepalive: idleKeepalive,
		bodyCap:       config.BodyCapBytes,
	}
	c.dial = c.dialTLS
	return c
}

func (c *conn) dialTLS(ctx context.Context) (net.Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: c.dialTimeout}, Config: c.tlsConfig}
	ctx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	return d.DialContext(ctx, "tcp", c.addr)
}

func (c *conn) await(ctx context.Context, fn func() error) error {
	return c.awaitFor(ctx, c.readTimeout, fn)
}

func (c *conn) awaitFor(ctx context.Context, timeout time.Duration, fn func() error) error {
	if c.cl == nil {
		return errNotConnected
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				c.tap.kill(fmt.Errorf("panic: %v", v))
				err := fmt.Errorf("imapclient: panic reading the server's reply: %v", v)
				if w := c.inflight; w != nil && c.tap.written(w.name, w.from) {
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
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			c.tap.kill(ctx.Err())
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
			c.tap.kill(errTimedOut)
			return <-done
		}
	}
}

func (c *conn) run(ctx context.Context, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.await(ctx, fn)
}

func (c *conn) runFor(ctx context.Context, timeout time.Duration, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.awaitFor(ctx, timeout, fn)
}

func (c *conn) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdown()
	if err := c.connect(ctx); err != nil {
		if c.method != config.AuthXOAUTH2 {
			c.shutdown()
			return err
		}
		c.invalidate()
		c.shutdown()
		if rerr := c.connect(ctx); rerr != nil {
			c.shutdown()
			return rerr
		}
	}
	return nil
}

func (c *conn) connect(ctx context.Context) error {
	nc, err := c.dial(ctx)
	if err != nil {
		return err
	}
	c.tap = newTap(nc, c.writeTimeout)
	c.idleChanged = make(chan struct{}, 1)
	c.cl = imapclient.New(c.tap, &imapclient.Options{
		IdleRestartInterval: c.idleKeepalive,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(*imapclient.UnilateralDataMailbox) { c.changed() },
			Expunge: func(uint32) { c.changed() },
			Fetch:   func(*imapclient.FetchMessageData) { c.changed() },
		},
	})
	return c.await(ctx, func() error { return c.login(ctx) })
}

func (c *conn) changed() {
	if c.idleChanged == nil {
		return
	}
	select {
	case c.idleChanged <- struct{}{}:
	default:
	}
}

func (c *conn) login(ctx context.Context) error {
	if err := c.cl.WaitGreeting(); err != nil {
		return imapErr(err)
	}
	caps := c.cl.Caps()
	if caps == nil {
		return pyError("no CAPABILITY response from server")
	}
	if !caps.Has(imap.CapIMAP4rev1) && !caps.Has(imap.Cap("IMAP4")) {
		return pyError("server not IMAP4 compliant")
	}
	args := []string{c.user}
	if c.method != config.AuthXOAUTH2 {
		args = append(args, c.password)
	}
	for _, arg := range args {
		if err := checkArg(arg); err != nil {
			return err
		}
	}
	if c.cl.State() == imap.ConnStateNotAuthenticated {
		if c.method == config.AuthXOAUTH2 {
			if err := c.authenticate(ctx); err != nil {
				return err
			}
		} else if err := c.cl.Login(c.user, c.password).Wait(); err != nil {
			return imapErr(err)
		}
	}
	c.caps = c.cl.Caps()
	if c.caps == nil {
		return pyError("no CAPABILITY response from server")
	}
	return nil
}

func (c *conn) authenticate(ctx context.Context) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	return imapErr(c.cl.Authenticate(&xoauth2{user: c.user, token: token}))
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.awaitFor(ctx, 5*time.Second, func() error { return c.cl.Logout().Wait() })
	c.shutdown()
}

func (c *conn) HasCapability(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps.Has(imap.Cap(strings.ToUpper(name)))
}

func (c *conn) ListMailboxes(ctx context.Context) ([]mail.MailboxInfo, error) {
	var out []mail.MailboxInfo
	err := c.run(ctx, func() error {
		data, err := c.cl.List("", "*", nil).Collect()
		if err != nil {
			return imapErr(err)
		}
		out = make([]mail.MailboxInfo, 0, len(data))
		for _, d := range data {
			name := d.Mailbox
			imapName := name
			if !strings.EqualFold(name, "INBOX") {
				imapName = mail.UTF7Encode(name)
			}
			flags := make([]string, len(d.Attrs))
			for i, a := range d.Attrs {
				flags[i] = string(a)
			}
			delim := ""
			if d.Delim != 0 {
				delim = string(d.Delim)
			}
			out = append(out, mail.MailboxInfo{Name: name, IMAPName: imapName, Flags: flags, Delimiter: delim})
		}
		return nil
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
		opts := &imap.StatusOptions{UIDValidity: true, UIDNext: true, NumMessages: true, HighestModSeq: c.caps.Has(imap.CapCondStore)}
		data, err := c.cl.Status(name, opts).Wait()
		if err != nil {
			return imapErr(err)
		}
		out.UIDValidity = data.UIDValidity
		out.UIDNext = uint32(data.UIDNext)
		if data.NumMessages != nil {
			out.Messages = *data.NumMessages
		}
		if opts.HighestModSeq && data.HighestModSeq > 0 {
			modseq := data.HighestModSeq
			out.HighestModSeq = &modseq
		}
		return nil
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

func (c *conn) selectBox(imapName string, readonly bool) (uint32, error) {
	name, err := mailboxArg(imapName)
	if err != nil {
		return 0, err
	}
	data, err := c.cl.Select(name, &imap.SelectOptions{ReadOnly: readonly, CondStore: c.caps.Has(imap.CapCondStore)}).Wait()
	if err != nil {
		return 0, imapErr(err)
	}
	if !readonly && data.ReadOnly {
		return 0, pyReadonly("%s is not writable", strconv.Quote(quoteName(imapName)))
	}
	if readonly {
		return 0, nil
	}
	if data.UIDValidity == 0 {
		return 0, pyAbort("SELECT did not report UIDVALIDITY")
	}
	return data.UIDValidity, nil
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
		data, err := c.cl.UIDSearch(search, nil).Wait()
		if err != nil {
			return imapErr(err)
		}
		uids := data.AllUIDs()
		out = make([]uint32, len(uids))
		for i, uid := range uids {
			out[i] = uint32(uid)
		}
		return nil
	})
	return out, err
}

func (c *conn) fetchOptions(req mail.FetchRequest) *imap.FetchOptions {
	opts := &imap.FetchOptions{UID: true, Flags: true, ChangedSince: req.ChangedSince}
	opts.Gmail.Labels = req.Labels
	switch req.Mode {
	case mail.FetchFull:
		opts.InternalDate = true
		opts.RFC822Size = true
		opts.BodyStructure = &imap.FetchItemBodyStructure{Extended: true}
		opts.Gmail.MsgID = req.Labels
		opts.Gmail.ThrID = req.Labels
		opts.BodySection = []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader, Peek: true},
			{Specifier: imap.PartSpecifierText, Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: int64(c.bodyCap)}},
		}
	case mail.FetchHeaders:
		opts.BodySection = []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierHeader, Peek: true}}
	}
	return opts
}

func (c *conn) UIDFetch(ctx context.Context, uids mail.UIDSet, req mail.FetchRequest) ([]mail.Message, error) {
	var out []mail.Message
	err := c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		data, err := c.cl.Fetch(set, c.fetchOptions(req)).Collect()
		if err != nil {
			return imapErr(err)
		}
		out = make([]mail.Message, 0, len(data))
		for _, buf := range data {
			out = append(out, fetchMessage(buf))
		}
		return nil
	})
	return out, err
}

func fetchMessage(buf *imapclient.FetchMessageBuffer) mail.Message {
	msg := mail.Message{UID: uint32(buf.UID), ModSeq: buf.ModSeq, Size: buf.RFC822Size}
	for _, f := range buf.Flags {
		msg.Flags = append(msg.Flags, string(f))
	}
	msg.Labels = append([]string(nil), buf.GmailLabels...)
	if buf.GmailMsgID != 0 {
		msg.GmMsgID = strconv.FormatUint(buf.GmailMsgID, 10)
	}
	if buf.GmailThrID != 0 {
		msg.GmThrID = strconv.FormatUint(buf.GmailThrID, 10)
	}
	if !buf.InternalDate.IsZero() {
		msg.InternalDate = buf.InternalDate.Format("02-Jan-2006 15:04:05 -0700")
	}
	for _, section := range buf.BodySection {
		switch section.Section.Specifier {
		case imap.PartSpecifierHeader:
			msg.Header = section.Bytes
		case imap.PartSpecifierText:
			msg.Text = section.Bytes
		}
	}
	msg.Attachments = attachments(buf.BodyStructure)
	return msg
}

func attachments(structure imap.BodyStructure) []mail.Attachment {
	if structure == nil {
		return nil
	}
	found := []mail.Attachment{}
	structure.Walk(func(_ []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		kind := single.MediaType()
		name := single.Filename()
		disposition := single.Disposition() != nil && strings.EqualFold(single.Disposition().Value, "attachment")
		if kind == "message/rfc822" {
			if disposition {
				found = append(found, mail.Attachment{Kind: kind, Name: name})
			}
		} else if name != "" || disposition {
			found = append(found, mail.Attachment{Kind: kind, Name: name})
		}
		return true
	})
	return found
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
		c.markWrite("COPY", "UID")
		data, err := c.cl.Copy(set, dest).Wait()
		if err != nil {
			return c.interrupted("COPY", "UID", imapErr(err))
		}
		out = &mail.CopyUID{UIDValidity: data.UIDValidity, Mapping: map[uint32]uint32{}}
		src, _ := data.SourceUIDs.Nums()
		dst, _ := data.DestUIDs.Nums()
		for i := range min(len(src), len(dst)) {
			out.Mapping[uint32(src[i])] = uint32(dst[i])
		}
		return nil
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
		c.markWrite("STORE", "UID")
		if err := c.cl.Store(set, store, nil).Close(); err != nil {
			return c.interrupted("STORE", "UID", imapErr(err))
		}
		return nil
	})
}

func (c *conn) StoreLabels(ctx context.Context, uids mail.UIDSet, add bool, labels ...string) error {
	return c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		store := &imap.StoreLabels{Op: storeOp(add), Labels: labels}
		c.markWrite("STORE", "UID")
		if err := c.cl.StoreLabels(set, store).Close(); err != nil {
			return c.interrupted("STORE", "UID", imapErr(err))
		}
		return nil
	})
}

func (c *conn) UIDExpunge(ctx context.Context, uids mail.UIDSet) error {
	return c.run(ctx, func() error {
		set, err := uidSet(uids)
		if err != nil {
			return err
		}
		c.markWrite("EXPUNGE", "UID")
		if err := c.cl.UIDExpunge(set).Close(); err != nil {
			return c.interrupted("EXPUNGE", "UID", imapErr(err))
		}
		return nil
	})
}

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
		c.markWrite("APPEND", "APPEND")
		cmd := c.cl.Append(name, int64(len(literal)), opts)
		if _, err := cmd.Write(literal); err != nil {
			_ = cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		if _, err := cmd.Wait(); err != nil {
			return c.interrupted("APPEND", "APPEND", imapErr(err))
		}
		return nil
	})
}

func (c *conn) mailboxCommand(ctx context.Context, command, imapName string, send func(name string) error) error {
	return c.run(ctx, func() error {
		name, err := mailboxArg(imapName)
		if err != nil {
			return err
		}
		c.markWrite(command, command)
		if err := send(name); err != nil {
			return c.interrupted(command, command, imapErr(err))
		}
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
	return c.run(ctx, func() error { return imapErr(c.cl.Noop().Wait()) })
}

func (c *conn) IdleChanges(ctx context.Context, duration time.Duration) (bool, error) {
	var changed bool
	err := c.runFor(ctx, duration+c.readTimeout, func() error {
		if !c.caps.Has(imap.CapIdle) {
			return pyError("Server does not support IMAP4 IDLE")
		}
		for {
			select {
			case <-c.idleChanged:
				continue
			default:
			}
			break
		}
		idle, err := c.cl.Idle()
		if err != nil {
			return imapErr(err)
		}
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-c.idleChanged:
			changed = true
		case <-timer.C:
		case <-ctx.Done():
		}
		werr := idle.Close()
		if werr == nil {
			werr = idle.Wait()
		}
		return imapErr(werr)
	})
	return changed, err
}

func (c *conn) markWrite(command, name string) {
	c.inflight = &inflight{command: command, name: name, from: c.tap.sentLen()}
}

func (c *conn) interrupted(command, name string, err error) error {
	var e *failure
	if errors.As(err, &e) && errors.Is(e, ErrAbort) && c.inflight != nil && c.tap.written(name, c.inflight.from) {
		return &mail.WriteInterrupted{Command: command, Err: err}
	}
	return err
}

func quoteName(name string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), `"`, `\"`) + `"`
}

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
