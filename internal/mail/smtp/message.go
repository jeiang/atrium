package smtp

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/mail/mime"
)

const maxLineLength = 78

// Fields are the draft columns build_message reads.
type Fields struct {
	ToAddrs   string
	CcAddrs   string
	Subject   string
	Body      string
	InReplyTo string
	Refs      string
}

// Message is the EmailMessage build_message makes: headers in insertion order and a text/plain payload already
// in its Content-Transfer-Encoding.
type Message struct {
	headers [][2]string
	payload []byte
}

// BuildMessage is smtp.build_message with make_msgid's result passed in as msgid.
func BuildMessage(sender string, d Fields, now float64, msgid string) *Message {
	m := &Message{}
	m.set("From", sender)
	m.set("To", d.ToAddrs)
	if d.CcAddrs != "" {
		m.set("Cc", d.CcAddrs)
	}
	m.set("Subject", d.Subject)
	m.set("Date", FormatDate(now))
	m.set("Message-ID", msgid)
	if d.InReplyTo != "" {
		m.set("In-Reply-To", d.InReplyTo)
		refs := d.Refs
		if refs == "" {
			refs = d.InReplyTo
		}
		m.set("References", refs)
	}
	cte, payload := encodeText([]byte(d.Body))
	m.payload = payload
	m.set("Content-Type", `text/plain; charset="utf-8"`)
	m.set("Content-Transfer-Encoding", cte)
	m.set("MIME-Version", "1.0")
	return m
}

func (m *Message) set(name, value string) { m.headers = append(m.headers, [2]string{name, value}) }

// Get is msg[name] for a header BuildMessage set; "" when absent.
func (m *Message) Get(name string) string {
	for _, h := range m.headers {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

// Bytes is the message as email.generator.BytesGenerator writes it under the default policy with the given
// line separator; utf8 is the policy's utf8 flag, which smtplib sets for an internationalized send.
func (m *Message) Bytes(linesep string, utf8 bool) ([]byte, error) {
	var b bytes.Buffer
	for _, h := range m.headers {
		folded, err := mime.FoldHeader(h[0], h[1], linesep, utf8)
		if err != nil {
			return nil, err
		}
		b.WriteString(folded)
	}
	b.WriteString(linesep)
	lines := splitNewlines(m.payload)
	for i, line := range lines {
		b.Write(line)
		if i < len(lines)-1 {
			b.WriteString(linesep)
		}
	}
	return b.Bytes(), nil
}

// splitNewlines is re.split(r"\r\n|\r|\n", b).
func splitNewlines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '\n':
			out = append(out, b[start:i])
			start = i + 1
		case '\r':
			out = append(out, b[start:i])
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	return append(out, b[start:])
}

// MakeMsgID is email.utils.make_msgid(domain=domain).
func MakeMsgID(domain string) string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	timeval := time.Now().UnixNano() / 1e7
	return fmt.Sprintf("<%d.%d.%d@%s>", timeval, os.Getpid(), binary.BigEndian.Uint64(r[:]), domain)
}

// FormatDate is email.utils.formatdate(now, localtime=False): UTC with the -0000 zone of a naive datetime.
func FormatDate(now float64) string {
	sec := math.Floor(now)
	t := time.Unix(int64(sec), 0).UTC()
	return t.Format("Mon, 02 Jan 2006 15:04:05") + " -0000"
}

// splitLines is bytes.splitlines(): splitNewlines without the empty piece after a final line break.
func splitLines(b []byte) [][]byte {
	lines := splitNewlines(b)
	if last := lines[len(lines)-1]; len(last) == 0 {
		return lines[:len(lines)-1]
	}
	return lines
}

// joinLines is b"\n".join(lines) + b"\n".
func joinLines(lines [][]byte) []byte {
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

// encodeText is contentmanager._encode_text with charset utf-8, no cte and the default policy (cte_type 8bit,
// max_line_length 78, linesep "\n").
func encodeText(body []byte) (string, []byte) {
	lines := splitLines(body)
	longest := 0
	for _, l := range lines {
		longest = max(longest, len(l))
	}
	normal := joinLines(lines)
	if longest <= maxLineLength {
		if isASCII(normal) {
			return "7bit", normal
		}
		return "8bit", normal
	}
	sniff := joinLines(lines[:min(len(lines), 10)])
	sniffQP := qpBodyEncode(sniff, maxLineLength)
	sniffB64 := base64.StdEncoding.EncodedLen(len(sniff)) + 1
	if len(sniffQP) > sniffB64 {
		return "base64", encodeBase64(normal, maxLineLength)
	}
	if len(lines) <= 10 {
		return "quoted-printable", sniffQP
	}
	return "quoted-printable", qpBodyEncode(normal, maxLineLength)
}

func encodeBase64(data []byte, maxlen int) []byte {
	per := maxlen / 4 * 3
	var b bytes.Buffer
	for i := 0; i < len(data); i += per {
		chunk := data[i:min(i+per, len(data))]
		b.WriteString(base64.StdEncoding.EncodeToString(chunk))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func qpSafe(c byte) bool {
	return c == '\t' || c == '\r' || c == '\n' || (c >= ' ' && c <= '~' && c != '=')
}

// qpBodyEncode is quoprimime.body_encode(body.decode('latin-1'), maxlen) with eol "\n".
func qpBodyEncode(body []byte, maxlen int) []byte {
	if len(body) == 0 {
		return nil
	}
	var enc []byte
	for _, c := range body {
		if qpSafe(c) {
			enc = append(enc, c)
		} else {
			enc = fmt.Appendf(enc, "=%02X", c)
		}
	}
	const softBreak = "=\n"
	maxlen1 := maxlen - 1
	var out [][]byte
	for _, line := range splitLines(enc) {
		start := 0
		laststart := len(line) - 1 - maxlen
		for start <= laststart {
			stop := start + maxlen1
			switch {
			case line[stop-2] == '=':
				out = append(out, line[start:stop-1])
				start = stop - 2
			case line[stop-1] == '=':
				out = append(out, line[start:stop])
				start = stop - 1
			default:
				out = append(out, append(append([]byte(nil), line[start:stop]...), '='))
				start = stop
			}
		}
		if n := len(line); n > 0 && (line[n-1] == ' ' || line[n-1] == '\t') {
			last := line[n-1]
			var q string
			switch room := start - laststart; {
			case room >= 3:
				q = fmt.Sprintf("=%02X", last)
			case room == 2:
				q = string(last) + softBreak
			default:
				q = softBreak + fmt.Sprintf("=%02X", last)
			}
			out = append(out, append(append([]byte(nil), line[start:n-1]...), q...))
		} else {
			out = append(out, line[start:])
		}
	}
	if last := enc[len(enc)-1]; last == '\r' || last == '\n' {
		out = append(out, nil)
	}
	return bytes.Join(out, []byte("\n"))
}
