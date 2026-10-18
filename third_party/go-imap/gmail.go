package imap

// Gmail IMAP extensions, see
// https://developers.google.com/workspace/gmail/imap/imap-extensions
//
// This file is part of atrium's local patch and is not in upstream go-imap.

// CapGmailExt1 is advertised by servers supporting the Gmail IMAP extensions.
const CapGmailExt1 Cap = "X-GM-EXT-1"

// FetchGmailOptions contains the Gmail FETCH data items to request.
//
// These require X-GM-EXT-1.
type FetchGmailOptions struct {
	MsgID  bool // X-GM-MSGID
	ThrID  bool // X-GM-THRID
	Labels bool // X-GM-LABELS
}

// StoreLabels alters the Gmail labels of messages (STORE X-GM-LABELS).
//
// Labels are decoded strings: system labels keep their leading backslash
// (e.g. `\Inbox`), others are encoded to modified UTF-7 on the wire.
//
// This requires X-GM-EXT-1.
type StoreLabels struct {
	Op     StoreFlagsOp
	Silent bool
	Labels []string
}

// SearchCriteriaGmail contains Gmail search keys.
//
// These require X-GM-EXT-1.
type SearchCriteriaGmail struct {
	Raw    []string // X-GM-RAW, Gmail web search syntax
	MsgID  uint64   // X-GM-MSGID, zero means unset
	ThrID  uint64   // X-GM-THRID, zero means unset
	Labels []string // X-GM-LABELS
}

func (criteria *SearchCriteriaGmail) and(other *SearchCriteriaGmail) {
	criteria.Raw = append(criteria.Raw, other.Raw...)
	if criteria.MsgID == 0 {
		criteria.MsgID = other.MsgID
	}
	if criteria.ThrID == 0 {
		criteria.ThrID = other.ThrID
	}
	criteria.Labels = append(criteria.Labels, other.Labels...)
}
