package features

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/pyhtml"
	"github.com/jeiang/atrium/internal/rx"
)

var log = logx.Logger("atrium.features")

const month = `(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?`

var (
	currencyRE = rx.MustCompile(
		`(?:[$€£]\s?\d[\d,]*\.\d{2})|(?:\b(?:usd|ttd|tt\$|cad)\s?\d[\d,]*(?:\.\d{2})?)|(?:\b\d[\d,]*\.\d{2}\s?(?:usd|ttd|cad)\b)`,
		rx.IgnoreCase,
	)
	otpRE         = rx.MustCompile(`(?<![\d.,$#-])\d{6}(?![\d.,-])`, 0)
	vcodeRE       = rx.MustCompile(`verification code|security code|one[- ]time|passcode|your code|login code`, 0)
	replyPrefixRE = rx.MustCompile(`^\s*(re|fwd?|fw|aw)\s*:\s*`, rx.IgnoreCase)
	amountRE      = rx.MustCompile(`(?:[$€£]|usd|ttd|tt\$|us\$|cad)\s?\d[\d,]*(?:\.\d+)?|\d[\d,]*(?:\.\d+)?\s?(?:usd|ttd|cad)`, 0)
	monthDayRE    = rx.MustCompile(`\b`+month+`\s+\d{1,2}(?:st|nd|rd|th)?(?:,?\s+\d{2,4})?`, 0)
	dayMonthRE    = rx.MustCompile(`\b\d{1,2}(?:st|nd|rd|th)?\s+`+month+`(?:,?\s+\d{2,4})?`, 0)
	numericDateRE = rx.MustCompile(`\b\d{1,4}[/-]\d{1,2}[/-]\d{1,4}\b`, 0)
	numberWordRE  = rx.MustCompile(`\b\w*\d\w*\b`, 0)
	hashNumberRE  = rx.MustCompile(`#\s*<n>`, 0)
	spaceRE       = rx.MustCompile(`\s+`, 0)
	scriptStyleRE = rx.MustCompile(`(?is)<(script|style).*?</\1>`, 0)
	tagRE         = rx.MustCompile(`<[^>]+>`, 0)
	digitsRE      = rx.MustCompile(`\d+`, 0)
	msgidStripRE  = rx.MustCompile(`[<>\s]`, 0)
	msgidRE       = rx.MustCompile(`<[^<>\s]+>`, 0)
	dkimDomainRE  = rx.MustCompile(`\bd=([^;\s]+)`, 0)
)

var espExact = map[string]string{
	"x-amazon-ses":    "ses",
	"x-mg-id":         "mailgun",
	"x-sendgrid-eid":  "sendgrid",
	"x-mc-user":       "mandrill",
	"x-pm-message-id": "postmark",
	"x-shopid":        "shopify",
	"x-campaign":      "mailchimp",
	"x-campaignid":    "mailchimp",
	"x-mailchimp-id":  "mailchimp",
	"x-mcid":          "mailchimp",
	"x-github-reason": "github",
	"x-github-sender": "github",
}

var espPrefix = [...]struct{ prefix, esp string }{
	{"x-ses", "ses"},
	{"x-mailgun", "mailgun"},
	{"x-sg", "sendgrid"},
	{"x-mandrill", "mandrill"},
	{"x-postmark", "postmark"},
	{"x-sparkpost", "sparkpost"},
	{"x-msys", "sparkpost"},
	{"x-shopify", "shopify"},
}

type Attachment struct {
	Type, Name string
}

type Features struct {
	MsgID       string   `json:"msgid"`
	MsgIDDomain string   `json:"msgid_domain"`
	InReplyTo   string   `json:"in_reply_to"`
	Refs        string   `json:"refs"`
	DateTS      float64  `json:"date_ts"`
	FromAddr    string   `json:"from_addr"`
	FromDomain  string   `json:"from_domain"`
	FromName    string   `json:"from_name"`
	ToAddrs     []string `json:"to_addrs"`
	ReplyTo     string   `json:"reply_to"`
	RPathDomain string   `json:"rpath_domain"`
	ListID      string   `json:"list_id"`
	Precedence  string   `json:"precedence"`
	Subject     string   `json:"subject"`
	SubjectTmpl string   `json:"subject_tmpl"`
	DKIMD       []string `json:"dkim_d"`
	ESP         []string `json:"esp"`
	AttTypes    []string `json:"att_types"`
	AttNames    []string `json:"att_names"`
	HasMoney    bool     `json:"has_money"`
	KStatement  bool     `json:"k_statement"`
	KReceipt    bool     `json:"k_receipt"`
	KOrder      bool     `json:"k_order"`
	KVCode      bool     `json:"k_vcode"`
	KOTP6       bool     `json:"k_otp6"`
	BodyLen     int      `json:"body_len"`
	Body        string   `json:"body"`
}

func sub(re *rx.Regexp, repl, s string) string {
	out, _ := re.Sub(repl, s, 0)
	return out
}

func found(re *rx.Regexp, s string) bool {
	m, _ := re.Search(s)
	return m != nil
}

func prefix(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func SubjectTemplate(subject string) string {
	s := mime.Strip(subject)
	for {
		n := sub(replyPrefixRE, "", s)
		if n == s {
			break
		}
		s = n
	}
	s = mime.Lower(s)
	s = sub(amountRE, "<amt>", s)
	s = sub(monthDayRE, "<date>", s)
	s = sub(dayMonthRE, "<date>", s)
	s = sub(numericDateRE, "<date>", s)
	s = sub(numberWordRE, "<n>", s)
	s = sub(hashNumberRE, "<n>", s)
	return mime.Strip(sub(spaceRE, " ", s))
}

func DomainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.Trim(mime.Lower(addr[i+1:]), "<>")
}

func StripMarkup(text string) string {
	text = sub(scriptStyleRE, " ", text)
	text = sub(tagRE, " ", text)
	return mime.Strip(sub(spaceRE, " ", pyhtml.Unescape(text)))
}

func FilenamePattern(name string) string { return sub(digitsRE, "#", mime.Lower(name)) }

func NormalizeMsgID(value string) string { return sub(msgidStripRE, "", value) }

func MsgIDList(value string) []string {
	ids, _ := msgidRE.FindAll(value)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = NormalizeMsgID(id)
	}
	return out
}

func ContentDigest(header, text []byte) string {
	h := sha256.New()
	h.Write(header)
	h.Write([]byte{0})
	h.Write(text)
	return hex.EncodeToString(h.Sum(nil))
}

// EmailTS: ok is false where Python raises.
func EmailTS(value string) (float64, bool) { return mime.ParseDate(value) }

// Header is "" when absent or when Python raises.
func Header(msg *mime.Message, name string) string {
	v, ok, err := msg.Get(name)
	if !ok || err != nil {
		return ""
	}
	return v
}

func addresses(msg *mime.Message, names ...string) ([]string, error) {
	var out []string
	for _, name := range names {
		values, err := msg.GetAll(name)
		if err != nil {
			values = nil
		}
		addrs, err := mime.GetAddresses(values)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if a.Address != "" {
				out = append(out, mime.Lower(a.Address))
			}
		}
	}
	return out, nil
}

func espOf(msg *mime.Message) []string {
	set := map[string]bool{}
	for _, key := range msg.Keys() {
		k := mime.Lower(key)
		if esp, ok := espExact[k]; ok {
			set[esp] = true
			continue
		}
		matched := false
		for _, p := range espPrefix {
			if strings.HasPrefix(k, p.prefix) {
				set[p.esp] = true
				matched = true
				break
			}
		}
		if !matched && strings.Contains(prefix(mime.Lower(Header(msg, key)), 200), "amazonses") {
			set["ses"] = true
		}
	}
	return sortedSet(set)
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func bodyText(msg *mime.Message) string {
	converters := [...]struct {
		kind    string
		convert func(string) string
	}{
		{"plain", func(t string) string { return mime.Strip(sub(spaceRE, " ", t)) }},
		{"html", StripMarkup},
	}
	for _, c := range converters {
		part, err := msg.Body(c.kind)
		if err != nil || part == nil {
			continue
		}
		content, err := part.Content()
		if err != nil {
			continue
		}
		if text := c.convert(content); text != "" {
			return text
		}
	}
	return ""
}

func mimeAttachments(msg *mime.Message) []Attachment {
	parts, _ := msg.Attachments()
	out := make([]Attachment, 0, len(parts))
	for _, p := range parts {
		name, _, err := p.Filename()
		if err != nil {
			break
		}
		out = append(out, Attachment{Type: p.ContentType(), Name: name})
	}
	return out
}

// Extract: err is set where Python's extract raises (a Content-Type value its header parser cannot handle).
func Extract(header, text []byte, attachments []Attachment, fallbackTS float64) (f Features, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("features: recovered from a panic in extract", logx.Err, fmt.Sprint(r))
			f, err = Features{}, fmt.Errorf("features: internal error: %v", r)
		}
	}()
	msg, err := mime.Parse(append(slices.Clip(header), text...))
	if err != nil {
		return Features{}, err
	}
	from, err := mime.ParseAddr(Header(msg, "From"))
	if err != nil {
		return Features{}, err
	}
	addr := mime.Lower(from.Address)
	dateTS, ok := EmailTS(Header(msg, "Date"))
	if !ok {
		dateTS = fallbackTS
	}
	subject := Header(msg, "Subject")
	body := bodyText(msg)
	head := prefix(body, 4000)
	low := mime.Lower(head)
	replyTo, err := addresses(msg, "Reply-To")
	if err != nil {
		return Features{}, err
	}
	rpath, err := addresses(msg, "Return-Path")
	if err != nil {
		return Features{}, err
	}
	dkim := map[string]bool{}
	values, _ := msg.GetAll("DKIM-Signature")
	for _, v := range values {
		if m, _ := dkimDomainRE.Search(v); m != nil {
			dkim[mime.Lower(m.Group(1))] = true
		}
	}
	if attachments == nil {
		attachments = mimeAttachments(msg)
	}
	types, names := map[string]bool{}, map[string]bool{}
	for _, a := range attachments {
		types[a.Type] = true
		if a.Name != "" {
			names[FilenamePattern(a.Name)] = true
		}
	}
	attNames := sortedSet(names)
	if len(attNames) > 3 {
		attNames = attNames[:3]
	}
	to := map[string]bool{}
	tos, err := addresses(msg, "To", "Delivered-To", "X-Original-To", "Cc")
	if err != nil {
		return Features{}, err
	}
	for _, a := range tos {
		to[a] = true
	}
	msgid := NormalizeMsgID(Header(msg, "Message-ID"))
	f = Features{
		MsgID:       msgid,
		MsgIDDomain: DomainOf(msgid),
		InReplyTo:   strings.Join(MsgIDList(Header(msg, "In-Reply-To")), " "),
		Refs:        strings.Join(MsgIDList(Header(msg, "References")), " "),
		DateTS:      dateTS,
		FromAddr:    addr,
		FromDomain:  DomainOf(addr),
		FromName:    from.Name,
		ToAddrs:     sortedSet(to),
		ListID:      mime.Lower(mime.Strip(Header(msg, "List-Id"))),
		Precedence:  mime.Lower(mime.Strip(Header(msg, "Precedence"))),
		Subject:     subject,
		SubjectTmpl: SubjectTemplate(subject),
		DKIMD:       sortedSet(dkim),
		ESP:         espOf(msg),
		AttTypes:    sortedSet(types),
		AttNames:    attNames,
		HasMoney:    found(currencyRE, head),
		KStatement:  strings.Contains(low, "statement"),
		KReceipt:    strings.Contains(low, "receipt"),
		KOrder:      strings.Contains(low, "order"),
		KVCode:      found(vcodeRE, low),
		KOTP6:       found(otpRE, prefix(body, 600)),
		BodyLen:     len([]rune(body)),
		Body:        prefix(body, config.FTSBodyChars),
	}
	if len(replyTo) > 0 {
		f.ReplyTo = replyTo[0]
	}
	if len(rpath) > 0 {
		f.RPathDomain = DomainOf(rpath[0])
	}
	return f, nil
}
