package webfetch_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/webfetch"
	"github.com/jeiang/atrium/internal/py"
)

type corpusError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type lookupRecord struct {
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	Answers []string `json:"answers"`
}

type connectionRecord struct {
	Address string            `json:"address"`
	Host    string            `json:"host"`
	Method  string            `json:"method"`
	Port    int               `json:"port"`
	Scheme  string            `json:"scheme"`
	Target  string            `json:"target"`
	Headers map[string]string `json:"headers"`
}

type corpusRecord struct {
	Fn          string             `json:"fn"`
	Case        string             `json:"case"`
	Input       []json.RawMessage  `json:"input"`
	Output      json.RawMessage    `json:"output"`
	Error       *corpusError       `json:"error"`
	Lookups     []lookupRecord     `json:"lookups"`
	Connections []connectionRecord `json:"connections"`
	AllowedURLs []string           `json:"allowed_urls"`
}

type scriptedResponse struct {
	Scheme  string      `json:"scheme"`
	Host    string      `json:"host"`
	Port    int         `json:"port"`
	Target  string      `json:"target"`
	Status  int         `json:"status"`
	Headers [][2]string `json:"headers"`
	Body    struct {
		B64       string `json:"b64"`
		RepeatB64 string `json:"repeat_b64"`
		Count     int    `json:"count"`
	} `json:"body"`
	Chunk int `json:"chunk"`
}

type manifest struct {
	DNS          map[string][]string   `json:"dns"`
	DNSSequences map[string][][]string `json:"dns_sequences"`
	Responses    []scriptedResponse    `json:"responses"`
}

func corpusPath(name string) string {
	return filepath.Join("..", "..", "..", "testdata", "corpora", name)
}

func loadCorpus(t *testing.T) ([]corpusRecord, manifest) {
	t.Helper()
	var m manifest
	raw, err := os.ReadFile(corpusPath("webfetch_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(corpusPath("webfetch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var records []corpusRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r corpusRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return records, m
}

func (r scriptedResponse) wire() []byte {
	body := []byte(nil)
	switch {
	case r.Body.RepeatB64 != "":
		unit, _ := base64.StdEncoding.DecodeString(r.Body.RepeatB64)
		body = bytes.Repeat(unit, r.Body.Count)
	default:
		body, _ = base64.StdEncoding.DecodeString(r.Body.B64)
	}
	headers := append([][2]string(nil), r.Headers...)
	if r.Chunk > 0 {
		headers = append(headers, [2]string{"Transfer-Encoding", "chunked"})
	} else if !hasHeader(headers, "content-length") {
		headers = append(headers, [2]string{"Content-Length", strconv.Itoa(len(body))})
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "HTTP/1.1 %d X\r\n", r.Status)
	for _, h := range headers {
		fmt.Fprintf(&out, "%s: %s\r\n", h[0], h[1])
	}
	out.WriteString("\r\n")
	if r.Chunk > 0 {
		for i := 0; i < len(body); i += r.Chunk {
			chunk := body[i:min(i+r.Chunk, len(body))]
			fmt.Fprintf(&out, "%x\r\n", len(chunk))
			out.Write(chunk)
			out.WriteString("\r\n")
		}
		out.WriteString("0\r\n\r\n")
	} else {
		out.Write(body)
	}
	return out.Bytes()
}

func hasHeader(headers [][2]string, name string) bool {
	for _, h := range headers {
		if strings.EqualFold(h[0], name) {
			return true
		}
	}
	return false
}

// scriptConn answers one request with the scripted reply for its target, as the generator's fake connection does.
type scriptConn struct {
	dest      webfetch.Dest
	responses map[string]scriptedResponse
	record    func(connectionRecord)
	request   bytes.Buffer
	reply     io.Reader
	fail      error
}

func (c *scriptConn) Write(p []byte) (int, error) {
	c.request.Write(p)
	head, _, found := strings.Cut(c.request.String(), "\r\n\r\n")
	if !found || c.reply != nil || c.fail != nil {
		return len(p), nil
	}
	lines := strings.Split(head, "\r\n")
	method, rest, _ := strings.Cut(lines[0], " ")
	target, _, _ := strings.Cut(rest, " ")
	headers := map[string]string{}
	for _, l := range lines[1:] {
		name, value, _ := strings.Cut(l, ": ")
		if strings.ToLower(name) != "host" {
			headers[name] = value
		}
	}
	c.record(connectionRecord{Address: c.dest.Address.String(), Host: c.dest.Host, Method: method, Port: c.dest.Port, Scheme: c.dest.Scheme, Target: target, Headers: headers})
	key := fmt.Sprintf("%s %s %d %s", c.dest.Scheme, c.dest.Host, c.dest.Port, target)
	if r, ok := c.responses[key]; ok {
		c.reply = bytes.NewReader(r.wire())
	} else {
		c.fail = errors.New("connection refused")
	}
	return len(p), nil
}

func (c *scriptConn) Read(p []byte) (int, error) {
	if c.fail != nil {
		return 0, c.fail
	}
	return c.reply.Read(p)
}

func (c *scriptConn) Close() error                     { return nil }
func (c *scriptConn) LocalAddr() net.Addr              { return nil }
func (c *scriptConn) RemoteAddr() net.Addr             { return nil }
func (c *scriptConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptConn) SetWriteDeadline(time.Time) error { return nil }

// scenario is one fetch of the corpus: the fake DNS, the scripted connections and what both recorded.
type scenario struct {
	m           manifest
	responses   map[string]scriptedResponse
	lookups     []lookupRecord
	connections []connectionRecord
}

func newScenario(m manifest) *scenario {
	s := &scenario{m: m, responses: map[string]scriptedResponse{}}
	for _, r := range m.Responses {
		s.responses[fmt.Sprintf("%s %s %d %s", r.Scheme, r.Host, r.Port, r.Target)] = r
	}
	return s
}

func inetAton(host string) (netip.Addr, bool) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, false
	}
	var vals []uint64
	for _, p := range parts {
		v, err := strconv.ParseUint(p, 0, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		vals = append(vals, v)
	}
	var n uint64
	for i, v := range vals[:len(vals)-1] {
		if v > 255 {
			return netip.Addr{}, false
		}
		n |= v << (8 * (3 - i))
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(5-len(vals))) {
		return netip.Addr{}, false
	}
	n |= last
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true
}

func (s *scenario) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	name := strings.TrimRight(host, ".")
	var texts []string
	var found bool
	if answers, ok := s.m.DNSSequences[name]; ok {
		n := 0
		for _, l := range s.lookups {
			if l.Host == host {
				n++
			}
		}
		texts, found = answers[min(n, len(answers)-1)], true
	} else if answers, ok := s.m.DNS[name]; ok {
		texts, found = answers, true
	}
	if !found {
		if ip, err := netip.ParseAddr(host); err == nil {
			texts, found = []string{ip.String()}, true
		} else if ip, ok := inetAton(host); ok {
			texts, found = []string{ip.String()}, true
		}
	}
	if !found {
		s.lookups = append(s.lookups, lookupRecord{Host: host})
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	s.lookups = append(s.lookups, lookupRecord{Host: host, Answers: append([]string{}, texts...)})
	addrs := make([]netip.Addr, len(texts))
	for i, t := range texts {
		addrs[i] = netip.MustParseAddr(t)
	}
	return addrs, nil
}

func (s *scenario) fetcher() *webfetch.Fetcher {
	return &webfetch.Fetcher{
		Lookup: s.lookup,
		Connect: func(_ context.Context, d webfetch.Dest) (net.Conn, error) {
			return &scriptConn{dest: d, responses: s.responses, record: func(r connectionRecord) { s.connections = append(s.connections, r) }}, nil
		},
	}
}

func checkTraces(t *testing.T, label string, s *scenario, r corpusRecord) {
	t.Helper()
	var wantLookups []lookupRecord
	for _, l := range r.Lookups {
		l.Port = 0
		if l.Answers == nil && false {
			l.Answers = []string{}
		}
		wantLookups = append(wantLookups, l)
	}
	var gotLookups []lookupRecord
	for _, l := range s.lookups {
		gotLookups = append(gotLookups, normalizeLookup(l))
	}
	for i := range wantLookups {
		wantLookups[i] = normalizeLookup(wantLookups[i])
	}
	if !reflect.DeepEqual(gotLookups, wantLookups) {
		t.Errorf("%s: lookups %v, want %v", label, gotLookups, wantLookups)
	}
	gotConns := s.connections
	if len(gotConns) != len(r.Connections) {
		t.Errorf("%s: %d connections, want %d", label, len(gotConns), len(r.Connections))
		return
	}
	for i, c := range r.Connections {
		g := gotConns[i]
		if g.Address != c.Address && canonical(g.Address) != canonical(c.Address) || g.Host != c.Host || g.Method != c.Method || g.Port != c.Port ||
			g.Scheme != c.Scheme || g.Target != c.Target || !reflect.DeepEqual(g.Headers, c.Headers) {
			t.Errorf("%s: connection %d is %+v, want %+v", label, i, g, c)
		}
	}
}

func canonical(addr string) string {
	if ip, err := netip.ParseAddr(addr); err == nil {
		return ip.WithZone("").String()
	}
	return addr
}

func normalizeLookup(l lookupRecord) lookupRecord {
	out := lookupRecord{Host: l.Host}
	if l.Answers != nil {
		out.Answers = []string{}
		for _, a := range l.Answers {
			out.Answers = append(out.Answers, canonical(a))
		}
	}
	return out
}

func errorType(err error) (string, string) {
	var c py.Classed
	if errors.As(err, &c) {
		return c.PyClass(), err.Error()
	}
	return fmt.Sprintf("%T", err), err.Error()
}

func TestDefaultAllowedCorpus(t *testing.T) {
	records, _ := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "default_allowed" {
			continue
		}
		var text string
		var want bool
		_ = json.Unmarshal(r.Input[0], &text)
		_ = json.Unmarshal(r.Output, &want)
		ip, err := netip.ParseAddr(text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if got := webfetch.DefaultAllowed(ip); got != want {
			t.Errorf("DefaultAllowed(%s) = %v, want %v", text, got, want)
		}
		n++
	}
	if n != 67 {
		t.Errorf("%d default_allowed records", n)
	}
}

func TestNormalizeURLCorpus(t *testing.T) {
	records, _ := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "normalize_url" {
			continue
		}
		var in *string
		_ = json.Unmarshal(r.Input[0], &in)
		text := ""
		if in != nil {
			text = *in
		}
		got, err := webfetch.NormalizeURL(text)
		if r.Error != nil {
			var te tool.Error
			if !errors.As(err, &te) || err.Error() != r.Error.Message {
				t.Errorf("NormalizeURL(%q) = %q, %v, want error %q", text, got, err, r.Error.Message)
			}
		} else {
			var want string
			_ = json.Unmarshal(r.Output, &want)
			if err != nil || got != want {
				t.Errorf("NormalizeURL(%q) = %q, %v, want %q", text, got, err, want)
			}
		}
		n++
	}
	if n != 25 {
		t.Errorf("%d normalize_url records", n)
	}
}

func TestFetchPageCorpus(t *testing.T) {
	records, m := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "fetch_page" {
			continue
		}
		var url string
		_ = json.Unmarshal(r.Input[0], &url)
		s := newScenario(m)
		page, err := s.fetcher().FetchPage(t.Context(), url)
		label := "fetch_page " + url
		if r.Error != nil {
			class, message := errorType(err)
			if err == nil || class != r.Error.Type || !sameMessage(r, message) {
				t.Errorf("%s: error %v (%s), want %s: %s", label, err, class, r.Error.Type, r.Error.Message)
			}
		} else {
			var want struct {
				BodyB64 *string `json:"body_b64"`
				BodyLen int     `json:"body_len"`
				Charset string  `json:"charset"`
				URL     string  `json:"url"`
			}
			_ = json.Unmarshal(r.Output, &want)
			switch {
			case err != nil:
				t.Errorf("%s: %v", label, err)
			case len(page.Body) != want.BodyLen || page.Charset != want.Charset || page.URL != want.URL:
				t.Errorf("%s: got %d bytes, %q, %q; want %d bytes, %q, %q", label, len(page.Body), page.Charset, page.URL, want.BodyLen, want.Charset, want.URL)
			case want.BodyB64 != nil && base64.StdEncoding.EncodeToString(page.Body) != *want.BodyB64:
				t.Errorf("%s: body %q", label, page.Body)
			}
		}
		checkTraces(t, label, s, r)
		n++
	}
	if n != 91 {
		t.Errorf("%d fetch_page records", n)
	}
}

// sameMessage compares an error text, except where the corpus allows the system's wording: the resolver's text
// after "dns: " and the ValueError of an out-of-range or non-numeric port.
func sameMessage(r corpusRecord, got string) bool {
	want := r.Error.Message
	if strings.HasPrefix(want, "dns: ") && strings.HasPrefix(got, "dns: ") && want != "dns: no address" {
		return true
	}
	return got == want
}

func TestPlainTextCorpus(t *testing.T) {
	records, _ := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "plain_text" {
			continue
		}
		var markup string
		var want [2]string
		_ = json.Unmarshal(r.Input[0], &markup)
		_ = json.Unmarshal(r.Output, &want)
		title, text := webfetch.PlainText(markup)
		if title != want[0] || text != want[1] {
			t.Errorf("PlainText(%s) = %q, %q; want %q, %q", r.Case, title, text, want[0], want[1])
		}
		n++
	}
	if n != 15 {
		t.Errorf("%d plain_text records", n)
	}
}

// extractDivergent lists the corpus pages whose readable text differs from trafilatura 2.2.0's
// (testdata/corpora/README.md, "Web fetch divergences").
var extractDivergent = map[string]string{
	"article":         "go-trafilatura collapses the whitespace inside a pre element",
	"minimal":         "trafilatura's rescue keeps the nav text of a page with under 250 characters of content",
	"nested_skip":     "go-trafilatura keeps the content of a template element and starts a new line at each dropped nested nav",
	"unbalanced_skip": "go-trafilatura's fallback extractors keep the text of an unclosed header",
	"uppercase_tags":  "trafilatura's rescue keeps the nav text of a page with under 250 characters of content",
	"malformed":       "go-trafilatura drops the text of the div with a quoted > in an attribute",
	"whitespace":      "go-trafilatura drops the heading text of a page with under 250 characters of content",
	"plain_text":      "go-trafilatura joins the lines of bare text into one paragraph",
}

func TestExtractTextCorpus(t *testing.T) {
	records, _ := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "extract_text" {
			continue
		}
		var in [2]string
		var want [2]string
		_ = json.Unmarshal(r.Input[0], &in[0])
		_ = json.Unmarshal(r.Input[1], &in[1])
		_ = json.Unmarshal(r.Output, &want)
		title, text := webfetch.ExtractText(in[0], in[1])
		same := title == want[0] && text == want[1]
		reason, divergent := extractDivergent[r.Case]
		switch {
		case divergent && same:
			t.Errorf("ExtractText(%s) now matches trafilatura; remove it from the divergence list (%s)", r.Case, reason)
		case !divergent && !same:
			t.Errorf("ExtractText(%s) = %q, %q; want %q, %q", r.Case, title, text, want[0], want[1])
		}
		n++
	}
	if n != 15 {
		t.Errorf("%d extract_text records", n)
	}
}

func TestWebFetchToolCorpus(t *testing.T) {
	records, m := loadCorpus(t)
	n := 0
	for _, r := range records {
		if r.Fn != "web_fetch" {
			continue
		}
		args, err := py.JSONLoads(r.Input[0])
		if err != nil {
			t.Fatal(err)
		}
		allowed := map[string]struct{}{}
		for _, u := range r.AllowedURLs {
			allowed[u] = struct{}{}
		}
		s := newScenario(m)
		out, err := webfetch.Tool(s.fetcher()).Handler(t.Context(), &tool.Context{AllowedURLs: allowed}, args.(py.Dict))
		label := fmt.Sprintf("web_fetch %s", r.Input[0])
		if r.Error != nil {
			var te tool.Error
			if !errors.As(err, &te) || err.Error() != r.Error.Message {
				t.Errorf("%s: error %v, want %q", label, err, r.Error.Message)
			}
		} else if err != nil {
			t.Errorf("%s: %v", label, err)
		} else {
			var want map[string]any
			_ = json.Unmarshal(r.Output, &want)
			got := out.(py.Dict)
			for _, key := range []string{"untrusted", "url"} {
				v, _ := got.Get(key)
				if !reflect.DeepEqual(v, want[key]) {
					t.Errorf("%s: %s = %v, want %v", label, key, v, want[key])
				}
			}
			text, _ := got.Get("text")
			if len([]rune(text.(string))) > len([]rune(want["text"].(string))) && len([]rune(want["text"].(string))) <= 40 {
				t.Errorf("%s: text %q is longer than the cap", label, text)
			}
		}
		checkTraces(t, label, s, r)
		n++
	}
	if n != 27 {
		t.Errorf("%d web_fetch records", n)
	}
}
