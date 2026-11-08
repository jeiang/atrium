package features

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const month = `(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?`

var (
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
	KOrder      bool     `json:"k_order"`
	KVCode      bool     `json:"k_vcode"`
	KOTP6       bool     `json:"k_otp6"`
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
	s := py.Strip(subject)
	for {
		n := sub(replyPrefixRE, "", s)
		if n == s {
			break
		}
		s = n
	}
	s = strings.ToLower(s)
	s = sub(amountRE, "<amt>", s)
	s = sub(monthDayRE, "<date>", s)
	s = sub(dayMonthRE, "<date>", s)
	s = sub(numericDateRE, "<date>", s)
	s = sub(numberWordRE, "<n>", s)
	s = sub(hashNumberRE, "<n>", s)
	return py.Strip(sub(spaceRE, " ", s))
}

func DomainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.Trim(strings.ToLower(addr[i+1:]), "<>")
}

func StripMarkup(text string) string {
	text = sub(scriptStyleRE, " ", text)
	text = sub(tagRE, " ", text)
	return py.Strip(sub(spaceRE, " ", html.UnescapeString(text)))
}

func FilenamePattern(name string) string { return sub(digitsRE, "#", strings.ToLower(name)) }

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

func EmailTS(value string) (float64, bool) { return mime.ParseDate(value) }

func addresses(msg *mime.Message, names ...string) []string {
	var out []string
	for _, name := range names {
		for _, a := range msg.Addresses(name) {
			if a.Address != "" {
				out = append(out, strings.ToLower(a.Address))
			}
		}
	}
	return out
}

func espOf(msg *mime.Message) []string {
	set := map[string]bool{}
	for _, key := range msg.Keys() {
		k := strings.ToLower(key)
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
		if !matched && strings.Contains(prefix(strings.ToLower(msg.Header(key)), 200), "amazonses") {
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
		{"plain", func(t string) string { return py.Strip(sub(spaceRE, " ", t)) }},
		{"html", StripMarkup},
	}
	for _, c := range converters {
		part := msg.Body(c.kind)
		if part == nil {
			continue
		}
		if text := c.convert(part.Content()); text != "" {
			return text
		}
	}
	return ""
}

func mimeAttachments(msg *mime.Message) []Attachment {
	parts := msg.Attachments()
	out := make([]Attachment, 0, len(parts))
	for _, p := range parts {
		out = append(out, Attachment{Type: p.ContentType(), Name: p.Filename()})
	}
	return out
}

func Extract(header, text []byte, attachments []Attachment, fallbackTS float64, ftsBodyChars int) Features {
	msg := mime.Parse(append(slices.Clip(header), text...))
	fromName, fromAddr := "", ""
	if addrs := msg.Addresses("From"); len(addrs) > 0 {
		fromName, fromAddr = addrs[0].Name, addrs[0].Address
	}
	addr := strings.ToLower(fromAddr)
	dateTS, ok := EmailTS(msg.Header("Date"))
	if !ok {
		dateTS = fallbackTS
	}
	subject := msg.Header("Subject")
	body := bodyText(msg)
	head := prefix(body, 4000)
	low := strings.ToLower(head)
	replyTo := addresses(msg, "Reply-To")
	rpath := addresses(msg, "Return-Path")
	dkim := map[string]bool{}
	for _, v := range msg.Headers("DKIM-Signature") {
		if m, _ := dkimDomainRE.Search(v); m != nil {
			dkim[strings.ToLower(m.Group(1))] = true
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
	for _, a := range addresses(msg, "To", "Delivered-To", "X-Original-To", "Cc") {
		to[a] = true
	}
	msgid := NormalizeMsgID(msg.Header("Message-ID"))
	f := Features{
		MsgID:       msgid,
		MsgIDDomain: DomainOf(msgid),
		InReplyTo:   strings.Join(MsgIDList(msg.Header("In-Reply-To")), " "),
		Refs:        strings.Join(MsgIDList(msg.Header("References")), " "),
		DateTS:      dateTS,
		FromAddr:    addr,
		FromDomain:  DomainOf(addr),
		FromName:    fromName,
		ToAddrs:     sortedSet(to),
		ListID:      strings.ToLower(py.Strip(msg.Header("List-Id"))),
		Precedence:  strings.ToLower(py.Strip(msg.Header("Precedence"))),
		Subject:     subject,
		SubjectTmpl: SubjectTemplate(subject),
		DKIMD:       sortedSet(dkim),
		ESP:         espOf(msg),
		AttTypes:    sortedSet(types),
		AttNames:    attNames,
		KOrder:      strings.Contains(low, "order"),
		KVCode:      found(vcodeRE, low),
		KOTP6:       found(otpRE, prefix(body, 600)),
		Body:        prefix(body, ftsBodyChars),
	}
	if len(replyTo) > 0 {
		f.ReplyTo = replyTo[0]
	}
	if len(rpath) > 0 {
		f.RPathDomain = DomainOf(rpath[0])
	}
	return f
}
