package mailtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	netmail "net/mail"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/mail"
)

func RawWith(subject, sender, msgid, body string) (header, text []byte) {
	h := fmt.Sprintf("From: %s\r\nTo: me@x.test\r\nSubject: %s\r\nMessage-ID: <%s@x.test>\r\n"+
		"Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n", sender, subject, msgid)
	return []byte(h), []byte(body)
}

type FakeMessage struct {
	Header []byte
	Text   []byte
	Flags  []string
	Labels []string
	ModSeq uint64
	Date   time.Time
}

type FakeBox struct {
	Validity uint32
	ModSeq   uint64
	Messages map[uint32]*FakeMessage
	NextUID  uint32
}

func NewBox(validity uint32) *FakeBox {
	return &FakeBox{Validity: validity, ModSeq: 1, Messages: map[uint32]*FakeMessage{}, NextUID: 1}
}

func (b *FakeBox) Add(header, text []byte, flags, labels []string) uint32 {
	uid := b.NextUID
	b.NextUID++
	b.ModSeq++
	b.Messages[uid] = &FakeMessage{Header: header, Text: text, Flags: slices.Clone(flags), Labels: cloneOrNil(labels), ModSeq: b.ModSeq, Date: headerDate(header)}
	return uid
}

func (b *FakeBox) SetFlags(uid uint32, flags, labels []string) {
	b.ModSeq++
	m := b.Messages[uid]
	if flags != nil {
		m.Flags = slices.Clone(flags)
	}
	if labels != nil {
		m.Labels = slices.Clone(labels)
	}
	m.ModSeq = b.ModSeq
}

func (b *FakeBox) Remove(uid uint32) {
	b.ModSeq++
	delete(b.Messages, uid)
}

func (b *FakeBox) UIDs() []uint32 {
	uids := make([]uint32, 0, len(b.Messages))
	for uid := range b.Messages {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	return uids
}

type FakeFetch struct {
	UIDs  mail.UIDSet
	Items mail.FetchRequest
}

type FakeMailbox struct {
	Box        *FakeBox
	Name       string
	Flags      []string
	RWValidity *uint32
}

type FakeWrite struct {
	Verb    string
	Mailbox string
	UIDs    []uint32
	Args    []string
}

type FakeAppend struct {
	Mailbox string
	Raw     []byte
	Flags   []string
}

type FakeClient struct {
	fakeConn

	Boxes        map[string]*FakeMailbox
	Order        []string
	Gmail        bool
	CategoryUIDs map[string][]uint32
	FailWrite    func(FakeWrite) error
	StatusHook   func(imapName string, messages uint32) uint32
	Idle         bool

	idle      sync.Mutex
	idlers    int
	signals   chan error
	Selected  string
	RW        string
	Connected bool
	Closed    bool
	Noops     int
	Searches  []mail.SearchCriteria
	Fetches   []FakeFetch
	Writes    []FakeWrite
	Created   []string
	Deleted   []string
	Appended  []FakeAppend
}

func NewFakeClient() *FakeClient {
	f := &FakeClient{
		Boxes:        map[string]*FakeMailbox{},
		CategoryUIDs: map[string][]uint32{},
	}
	f.fakeConn = fakeConn{f}
	return f
}

func (f *FakeClient) Open(_ context.Context, opts mail.OpenOptions) (mail.Client, error) {
	return mail.Gated(f, opts.Q), nil
}

func (f *FakeClient) AddBox(imapName string, box *FakeBox, flags ...string) *FakeBox {
	if _, ok := f.Boxes[imapName]; !ok {
		f.Order = append(f.Order, imapName)
	}
	f.Boxes[imapName] = &FakeMailbox{Box: box, Name: imapName, Flags: flags}
	return box
}

func (f *FakeClient) name(imapName string) string {
	if m, ok := f.Boxes[imapName]; ok && m.Name != "" {
		return m.Name
	}
	return imapName
}

var caps = map[string]bool{"IDLE": true, "CONDSTORE": true, "UIDPLUS": true}

func (f *FakeClient) labelCount(label string) uint32 {
	var n uint32
	for _, box := range f.Boxes {
		for _, m := range box.Box.Messages {
			if m.Labels != nil && slices.Contains(m.Labels, label) {
				n++
			}
		}
	}
	return n
}

func cloneOrNil(s []string) []string {
	if s == nil {
		return nil
	}
	return slices.Clone(s)
}

type fakeConn struct{ f *FakeClient }

func (c fakeConn) box(imapName string) (*FakeBox, error) {
	m, ok := c.f.Boxes[imapName]
	if !ok {
		return nil, fmt.Errorf("fake IMAP: no mailbox %q", imapName)
	}
	return m.Box, nil
}

func (c fakeConn) Connect(context.Context) error {
	c.f.Connected = true
	return nil
}

func (c fakeConn) Close() { c.f.Closed = true }

func (c fakeConn) HasCapability(name string) bool { return caps[strings.ToUpper(name)] }

func (c fakeConn) ListMailboxes(context.Context) ([]mail.MailboxInfo, error) {
	out := make([]mail.MailboxInfo, 0, len(c.f.Order))
	for _, n := range c.f.Order {
		out = append(out, mail.MailboxInfo{Name: c.f.name(n), IMAPName: n, Flags: slices.Clone(c.f.Boxes[n].Flags), Delimiter: "/"})
	}
	return out, nil
}

func (c fakeConn) Status(_ context.Context, imapName string) (mail.Status, error) {
	m, ok := c.f.Boxes[imapName]
	if !ok && c.f.Gmail {
		one := uint64(1)
		count := c.f.labelCount(c.f.name(imapName))
		if c.f.StatusHook != nil {
			count = c.f.StatusHook(imapName, count)
		}
		return mail.Status{UIDValidity: 1, UIDNext: 1, HighestModSeq: &one, Messages: count}, nil
	}
	if !ok {
		return mail.Status{}, fmt.Errorf("fake IMAP: no mailbox %q", imapName)
	}
	modseq := m.Box.ModSeq
	count := uint32(len(m.Box.Messages))
	if c.f.StatusHook != nil {
		count = c.f.StatusHook(imapName, count)
	}
	return mail.Status{UIDValidity: m.Box.Validity, UIDNext: m.Box.NextUID, HighestModSeq: &modseq, Messages: count}, nil
}

func (c fakeConn) Select(_ context.Context, imapName string) error {
	c.f.Selected = imapName
	return nil
}

func headerDate(header []byte) time.Time {
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(strings.ToLower(line), "date:") {
			continue
		}
		value := strings.TrimSpace(line[len("date:"):])
		if t, err := netmail.ParseDate(value); err == nil {
			return t
		}
	}
	return time.Time{}
}

func messageDate(m *FakeMessage) time.Time {
	if !m.Date.IsZero() {
		return m.Date
	}
	return headerDate(m.Header)
}

func (c fakeConn) UIDSearch(_ context.Context, criteria mail.SearchCriteria) ([]uint32, error) {
	c.f.Searches = append(c.f.Searches, criteria)
	if criteria.GmailRaw != "" {
		_, category, _ := strings.Cut(criteria.GmailRaw, ":")
		uids := slices.Clone(c.f.CategoryUIDs[category])
		slices.Sort(uids)
		return uids, nil
	}
	box, err := c.box(c.f.Selected)
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, uid := range box.UIDs() {
		switch {
		case criteria.UID != nil && !criteria.UID.All() && !slices.Contains(criteria.UID.List(), uid):
			continue
		case criteria.HeaderName != "" && !strings.Contains(string(box.Messages[uid].Header), criteria.HeaderValue):
			continue
		case !criteria.Since.IsZero() && messageDate(box.Messages[uid]).Before(criteria.Since):
			continue
		}
		out = append(out, uid)
	}
	return out, nil
}

func (c fakeConn) UIDFetch(_ context.Context, uids mail.UIDSet, req mail.FetchRequest) ([]mail.Message, error) {
	box, err := c.box(c.f.Selected)
	if err != nil {
		return nil, err
	}
	c.f.Fetches = append(c.f.Fetches, FakeFetch{uids, req})
	full := req.Mode == mail.FetchFull
	var out []mail.Message
	for _, uid := range box.UIDs() {
		if !uids.All() && !slices.Contains(uids.List(), uid) {
			continue
		}
		m := box.Messages[uid]
		if req.ChangedSince != 0 && m.ModSeq <= req.ChangedSince {
			continue
		}
		msg := mail.Message{UID: uid, Flags: slices.Clone(m.Flags), Labels: cloneOrNil(m.Labels), ModSeq: m.ModSeq}
		if m.Labels != nil {
			msg.GmMsgID = fmt.Sprintf("g%d", uid)
			msg.GmThrID = fmt.Sprintf("t%d", uid)
		}
		switch {
		case full:
			msg.Size = int64(len(m.Header) + len(m.Text))
			msg.InternalDate = "06-Oct-2026 10:00:00 +0000"
			msg.Attachments = []mail.Attachment{}
			msg.Header = m.Header
			msg.Text = m.Text
		case req.Mode == mail.FetchHeaders:
			msg.Header = m.Header
		}
		out = append(out, msg)
	}
	return out, nil
}

func (c fakeConn) IdleChanges(ctx context.Context, wait time.Duration) (bool, error) {
	if c.f.Idle {
		return true, nil
	}
	signals := c.f.idleSignals()
	c.f.idle.Lock()
	c.f.idlers++
	c.f.idle.Unlock()
	defer func() {
		c.f.idle.Lock()
		c.f.idlers--
		c.f.idle.Unlock()
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false, nil
	case <-t.C:
		return false, nil
	case err := <-signals:
		return err == nil, err
	}
}

func (f *FakeClient) idleSignals() chan error {
	f.idle.Lock()
	defer f.idle.Unlock()
	if f.signals == nil {
		f.signals = make(chan error)
	}
	return f.signals
}

func (f *FakeClient) Idling() int {
	f.idle.Lock()
	defer f.idle.Unlock()
	return f.idlers
}

func (f *FakeClient) Wake(ctx context.Context) error { return f.signal(ctx, nil) }

func (f *FakeClient) DropIdle(ctx context.Context) error { return f.signal(ctx, io.ErrUnexpectedEOF) }

func (f *FakeClient) signal(ctx context.Context, err error) error {
	select {
	case f.idleSignals() <- err:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c fakeConn) Noop(context.Context) error {
	c.f.Noops++
	return nil
}

func (c fakeConn) SelectRW(_ context.Context, imapName string) (uint32, error) {
	c.f.Selected = imapName
	c.f.RW = imapName
	if m, ok := c.f.Boxes[imapName]; ok {
		if m.RWValidity != nil {
			return *m.RWValidity, nil
		}
		return m.Box.Validity, nil
	}
	return 1, nil
}

func (c fakeConn) record(w FakeWrite) error {
	if c.f.FailWrite != nil {
		if err := c.f.FailWrite(w); err != nil {
			return err
		}
	}
	c.f.Writes = append(c.f.Writes, w)
	return nil
}

func (c fakeConn) messages(uids mail.UIDSet) (*FakeBox, []uint32, error) {
	box, err := c.box(c.f.Selected)
	if err != nil {
		return nil, nil, err
	}
	if uids.All() {
		return box, box.UIDs(), nil
	}
	for _, uid := range uids.List() {
		if _, ok := box.Messages[uid]; !ok {
			return nil, nil, fmt.Errorf("fake IMAP: no UID %d in %q", uid, c.f.Selected)
		}
	}
	return box, uids.List(), nil
}

func (c fakeConn) Copy(_ context.Context, uids mail.UIDSet, destIMAPName string) (*mail.CopyUID, error) {
	box, list, err := c.messages(uids)
	if err != nil {
		return nil, err
	}
	dest, err := c.box(destIMAPName)
	if err != nil {
		return nil, err
	}
	if err := c.record(FakeWrite{Verb: "COPY", Mailbox: c.f.Selected, UIDs: list, Args: []string{destIMAPName}}); err != nil {
		return nil, err
	}
	mapping := make(map[uint32]uint32, len(list))
	for _, uid := range list {
		m := box.Messages[uid]
		mapping[uid] = dest.Add(m.Header, m.Text, m.Flags, m.Labels)
	}
	return &mail.CopyUID{UIDValidity: dest.Validity, Mapping: mapping}, nil
}

func (c fakeConn) store(uids mail.UIDSet, add, labels bool, names []string) error {
	box, list, err := c.messages(uids)
	if err != nil {
		return err
	}
	op := "+"
	if !add {
		op = "-"
	}
	if labels {
		op += "X-GM-LABELS"
	} else {
		op += "FLAGS.SILENT"
	}
	if err := c.record(FakeWrite{Verb: "STORE", Mailbox: c.f.Selected, UIDs: list, Args: append([]string{op}, names...)}); err != nil {
		return err
	}
	for _, uid := range list {
		m := box.Messages[uid]
		target := slices.Clone(m.Flags)
		if labels {
			target = slices.Clone(m.Labels)
		}
		for _, n := range names {
			i := slices.Index(target, n)
			if add && i < 0 {
				target = append(target, n)
			}
			if !add && i >= 0 {
				target = slices.Delete(target, i, i+1)
			}
		}
		if target == nil {
			target = []string{}
		}
		if labels {
			box.SetFlags(uid, nil, target)
		} else {
			box.SetFlags(uid, target, nil)
		}
	}
	return nil
}

func (c fakeConn) StoreFlags(_ context.Context, uids mail.UIDSet, add bool, flags ...string) error {
	return c.store(uids, add, false, flags)
}

func (c fakeConn) StoreLabels(_ context.Context, uids mail.UIDSet, add bool, labels ...string) error {
	return c.store(uids, add, true, labels)
}

func (c fakeConn) UIDExpunge(_ context.Context, uids mail.UIDSet) error {
	box, list, err := c.messages(uids)
	if err != nil {
		return err
	}
	if err := c.record(FakeWrite{Verb: "EXPUNGE", Mailbox: c.f.Selected, UIDs: list}); err != nil {
		return err
	}
	for _, uid := range list {
		if slices.Contains(box.Messages[uid].Flags, `\Deleted`) {
			box.Remove(uid)
		}
	}
	return nil
}

func (c fakeConn) Append(_ context.Context, imapName string, raw []byte, flags ...string) error {
	m, ok := c.f.Boxes[imapName]
	if !ok {
		return fmt.Errorf("fake IMAP: no mailbox %q", imapName)
	}
	if err := c.record(FakeWrite{Verb: "APPEND", Mailbox: imapName, Args: slices.Clone(flags)}); err != nil {
		return err
	}
	header, text := splitRaw(raw)
	m.Box.Add(header, text, flags, nil)
	c.f.Appended = append(c.f.Appended, FakeAppend{Mailbox: imapName, Raw: slices.Clone(raw), Flags: slices.Clone(flags)})
	return nil
}

func splitRaw(raw []byte) (header, text []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i+4], raw[i+4:]
	}
	return raw, nil
}

func (c fakeConn) CreateMailbox(_ context.Context, imapName string) error {
	if err := c.record(FakeWrite{Verb: "CREATE", Mailbox: imapName}); err != nil {
		return err
	}
	c.f.AddBox(imapName, NewBox(7))
	c.f.Created = append(c.f.Created, imapName)
	return nil
}

func (c fakeConn) SubscribeMailbox(_ context.Context, imapName string) error {
	return c.record(FakeWrite{Verb: "SUBSCRIBE", Mailbox: imapName})
}

func (c fakeConn) DeleteMailbox(_ context.Context, imapName string) error {
	if err := c.record(FakeWrite{Verb: "DELETE", Mailbox: imapName}); err != nil {
		return err
	}
	c.f.Deleted = append(c.f.Deleted, imapName)
	delete(c.f.Boxes, imapName)
	c.f.Order = slices.DeleteFunc(c.f.Order, func(n string) bool { return n == imapName })
	return nil
}
