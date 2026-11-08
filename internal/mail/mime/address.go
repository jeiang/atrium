package mime

import (
	"encoding/base64"
	"errors"
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"
)

type MsgID string

var msgIDJunkRE = regexp.MustCompile(`[<>\s]`)
var msgIDRE = regexp.MustCompile(`<[^<>\s]+>`)

func ParseMsgID(value string) MsgID {
	return MsgID(msgIDJunkRE.ReplaceAllString(value, ""))
}

func (id MsgID) Bare() string { return string(id) }

func (id MsgID) String() string {
	if id == "" {
		return ""
	}
	return "<" + string(id) + ">"
}

func ParseMsgIDs(header string) []MsgID {
	ids := msgIDRE.FindAllString(header, -1)
	out := make([]MsgID, len(ids))
	for i, id := range ids {
		out[i] = ParseMsgID(id)
	}
	return out
}

func ParseAddressList(value string) ([]netmail.Address, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	addrs, err := addressParser.ParseList(value)
	if err != nil {
		return nil, err
	}
	out := make([]netmail.Address, len(addrs))
	for i, addr := range addrs {
		out[i] = *addr
	}
	return out, nil
}

func FormatAddress(addr netmail.Address) (string, error) {
	if addr.Address == "" || !strings.Contains(addr.Address, "@") || strings.Contains(addr.Address, " ") {
		return "", errors.New("invalid address")
	}
	if !isASCII(addr.Address) {
		return "", errors.New("address must be ASCII")
	}
	if addr.Name == "" {
		return addr.Address, nil
	}
	if !isASCII(addr.Name) {
		return encodeWord([]byte(addr.Name)) + " <" + addr.Address + ">", nil
	}
	quote := ""
	if strings.ContainsAny(addr.Name, `][\()<>@,:;".`) {
		quote = `"`
	}
	name := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(addr.Name)
	return quote + name + quote + " <" + addr.Address + ">", nil
}

func encodeWord(b []byte) string {
	var q strings.Builder
	for _, c := range b {
		q.WriteString(qpHeaderByte(c))
	}
	if base64.StdEncoding.EncodedLen(len(b)) < q.Len() {
		return "=?utf-8?b?" + base64.StdEncoding.EncodeToString(b) + "?="
	}
	return "=?utf-8?q?" + q.String() + "?="
}

func qpHeaderByte(c byte) string {
	switch {
	case c == ' ':
		return "_"
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-!*+/", c) >= 0:
		return string(c)
	}
	return fmt.Sprintf("=%02X", c)
}
