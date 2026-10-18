package imapclient

// Gmail IMAP extensions, see
// https://developers.google.com/workspace/gmail/imap/imap-extensions
//
// This file is part of atrium's local patch and is not in upstream go-imap.

import (
	"fmt"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/internal/imapwire"
	"github.com/emersion/go-imap/v2/internal/utf7"
)

// FetchItemDataGmailMsgID holds data returned by FETCH X-GM-MSGID.
type FetchItemDataGmailMsgID struct {
	MsgID uint64
}

func (FetchItemDataGmailMsgID) fetchItemData() {}

// FetchItemDataGmailThrID holds data returned by FETCH X-GM-THRID.
type FetchItemDataGmailThrID struct {
	ThrID uint64
}

func (FetchItemDataGmailThrID) fetchItemData() {}

// FetchItemDataGmailLabels holds data returned by FETCH X-GM-LABELS.
//
// Labels are decoded from modified UTF-7. System labels keep their leading
// backslash. An empty label list yields a non-nil empty slice.
type FetchItemDataGmailLabels struct {
	Labels []string
}

func (FetchItemDataGmailLabels) fetchItemData() {}

// StoreLabels sends a STORE X-GM-LABELS command.
//
// Unless StoreLabels.Silent is set, the server will return the updated values.
//
// This requires X-GM-EXT-1.
func (c *Client) StoreLabels(numSet imap.NumSet, store *imap.StoreLabels) *FetchCommand {
	cmd := &FetchCommand{
		numSet: numSet,
		msgs:   make(chan *FetchMessageData, 128),
	}
	enc := c.beginCommand(uidCmdName("STORE", imapwire.NumSetKind(numSet)), cmd)
	enc.SP().NumSet(numSet).SP()
	switch store.Op {
	case imap.StoreFlagsSet:
		// nothing to do
	case imap.StoreFlagsAdd:
		enc.Special('+')
	case imap.StoreFlagsDel:
		enc.Special('-')
	default:
		panic(fmt.Errorf("imapclient: unknown store labels op: %v", store.Op))
	}
	enc.Atom("X-GM-LABELS")
	if store.Silent {
		enc.Atom(".SILENT")
	}
	enc.SP().List(len(store.Labels), func(i int) {
		writeGmailLabel(enc.Encoder, store.Labels[i])
	})
	enc.end()
	return cmd
}

func writeFetchItemsGmail(listEnc *imapwire.ListEncoder, options *imap.FetchGmailOptions) {
	if options.MsgID {
		listEnc.Item().Atom("X-GM-MSGID")
	}
	if options.ThrID {
		listEnc.Item().Atom("X-GM-THRID")
	}
	if options.Labels {
		listEnc.Item().Atom("X-GM-LABELS")
	}
}

func writeSearchKeyGmail(encodeItem func() *imapwire.Encoder, criteria *imap.SearchCriteriaGmail) {
	for _, raw := range criteria.Raw {
		encodeItem().Atom("X-GM-RAW").SP().String(raw)
	}
	if criteria.MsgID != 0 {
		encodeItem().Atom("X-GM-MSGID").SP().ModSeq(criteria.MsgID)
	}
	if criteria.ThrID != 0 {
		encodeItem().Atom("X-GM-THRID").SP().ModSeq(criteria.ThrID)
	}
	for _, label := range criteria.Labels {
		writeGmailLabel(encodeItem().Atom("X-GM-LABELS").SP(), label)
	}
}

// searchCriteriaGmailIsASCII reports whether no X-GM-RAW query needs
// CHARSET UTF-8. Labels are sent as modified UTF-7, which is ASCII.
func searchCriteriaGmailIsASCII(criteria *imap.SearchCriteriaGmail) bool {
	for _, raw := range criteria.Raw {
		if !isASCII(raw) {
			return false
		}
	}
	return true
}

// writeGmailLabel sends a system label (`\Inbox`) as an atom and any other
// label as a modified UTF-7 string.
func writeGmailLabel(enc *imapwire.Encoder, label string) {
	if isGmailSystemLabel(label) {
		enc.Atom(label)
	} else if enc.QuotedUTF8 {
		enc.String(utf7.Escape(label))
	} else {
		enc.String(utf7.Encode(label))
	}
}

func isGmailSystemLabel(label string) bool {
	if len(label) < 2 || label[0] != '\\' {
		return false
	}
	for i := 1; i < len(label); i++ {
		if !imapwire.IsAtomChar(label[i]) {
			return false
		}
	}
	return true
}

func readFetchItemGmail(dec *imapwire.Decoder, attName string) (FetchItemData, error) {
	if !dec.ExpectSP() {
		return nil, dec.Err()
	}
	switch attName {
	case "X-GM-MSGID", "X-GM-THRID":
		var id uint64
		if !dec.Expect(dec.ModSeq(&id), "64-bit Gmail ID") {
			return nil, dec.Err()
		}
		if attName == "X-GM-MSGID" {
			return FetchItemDataGmailMsgID{MsgID: id}, nil
		}
		return FetchItemDataGmailThrID{ThrID: id}, nil
	case "X-GM-LABELS":
		labels := []string{}
		err := dec.ExpectList(func() error {
			label, err := readGmailLabel(dec)
			if err != nil {
				return err
			}
			labels = append(labels, label)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("in X-GM-LABELS: %v", err)
		}
		return FetchItemDataGmailLabels{Labels: labels}, nil
	default:
		panic(fmt.Errorf("imapclient: unknown Gmail msg-att %q", attName))
	}
}

// readGmailLabel reads an unquoted system label (`\Inbox`) or an astring
// holding a modified UTF-7 label (`"\\Inbox"` decodes to `\Inbox` as well).
func readGmailLabel(dec *imapwire.Decoder) (string, error) {
	var s string
	if dec.Special('\\') {
		if !dec.Expect(dec.Atom(&s), "system label") {
			return "", dec.Err()
		}
		return `\` + s, nil
	}
	if !dec.ExpectAString(&s) {
		return "", dec.Err()
	}
	label, err := utf7.Decode(s)
	if err != nil {
		return "", fmt.Errorf("label %q: %v", s, err)
	}
	return label, nil
}

// skipFetchItem discards the value of a msg-att the client does not know,
// so that one unknown item does not abort the connection.
func skipFetchItem(dec *imapwire.Decoder) error {
	if dec.Special('[') {
		dec.DiscardUntilByte(']')
		if !dec.ExpectSpecial(']') {
			return dec.Err()
		}
	}
	if dec.Special('<') {
		dec.DiscardUntilByte('>')
		if !dec.ExpectSpecial('>') {
			return dec.Err()
		}
	}
	if !dec.ExpectSP() {
		return dec.Err()
	}
	return skipFetchValue(dec)
}

func skipFetchValue(dec *imapwire.Decoder) error {
	var s string
	if dec.Special('\\') {
		dec.Func(&s, func(ch byte) bool { return ch == '*' || imapwire.IsAtomChar(ch) })
		return nil
	}
	if dec.String(&s) || dec.Atom(&s) {
		return nil
	}
	isList, err := dec.List(func() error { return skipFetchValue(dec) })
	if err != nil {
		return err
	} else if !dec.Expect(isList, "msg-att value") {
		return dec.Err()
	}
	return nil
}
