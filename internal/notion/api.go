// Package notion is the Notion mirror: the API client of notion_api.py, the page text conversion of
// notion_markdown.py, the sync of notion_sync.py, the hybrid search of notion_search.py and the `notion sync` and
// `notion search` commands.
package notion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/py"
)

const (
	BaseURL         = "https://api.notion.com"
	Version         = "2026-03-11"
	MinInterval     = 1.0 / 3
	RetryAfterBase  = 1.0
	RetryAfterMax   = 60.0
	MaxRetries      = 5
	Timeout         = 60 * time.Second
	PageSize        = 100
	BlockDepth      = 5
	pagedKeyCursor  = "start_cursor"
	pagedKeyPerPage = "page_size"
)

var retryStatuses = [...]int{429, 500, 502, 503, 504}

// Error is NotionError: a refusal from Notion, or with Status 0 and Code "unavailable" (NotionUnavailable) a
// request that never got an answer.
type Error struct {
	Status  int
	Code    string
	Message string
	class   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("notion %d %s: %s", e.Status, e.Code, e.Message)
}

func (e *Error) PyClass() string {
	if e.class != "" {
		return e.class
	}
	return "NotionError"
}

func (e *Error) PyModule() string { return "atrium.notion_api" }

func unavailable(message string) *Error {
	return &Error{Status: 0, Code: "unavailable", Message: message, class: "NotionUnavailable"}
}

// Client is NotionClient. Sleep and Clock are the seams of the pacing and the retry waits; the zero values are
// the real ones.
type Client struct {
	token   string
	BaseURL string
	HTTP    *http.Client
	Sleep   func(ctx context.Context, seconds float64) error
	Clock   func() float64
	last    float64
	paced   bool
}

// NewClient is NotionClient(token).
func NewClient(token string) *Client {
	return &Client{
		token:   token,
		BaseURL: BaseURL,
		HTTP:    &http.Client{Timeout: Timeout},
		Sleep:   sleep,
		Clock:   monotonic,
	}
}

func (c *Client) String() string { return "NotionClient()" }

func (c *Client) GoString() string { return "NotionClient()" }

var epoch = time.Now()

func monotonic() float64 { return time.Since(epoch).Seconds() }

func sleep(ctx context.Context, seconds float64) error {
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) pace(ctx context.Context) error {
	if c.paced {
		if wait := MinInterval - (c.Clock() - c.last); wait > 0 {
			if err := c.Sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	c.last, c.paced = c.Clock(), true
	return nil
}

func (c *Client) send(ctx context.Context, method, target string, body []byte) (int, http.Header, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, nil, unavailable(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", Version)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, unavailable(err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, unavailable(err.Error())
	}
	return resp.StatusCode, resp.Header, raw, nil
}

func retryable(status int) bool {
	for _, s := range retryStatuses {
		if s == status {
			return true
		}
	}
	return false
}

func retryDelay(status int, headers http.Header, attempt int) float64 {
	delay, err := py.ParseFloat(headers.Get("Retry-After"))
	if err != nil {
		delay = RetryAfterBase
		if status != http.StatusTooManyRequests {
			delay = RetryAfterBase * math.Pow(2, float64(attempt))
		}
	}
	return math.Min(math.Max(delay, 0), RetryAfterMax)
}

func refusal(status int, raw []byte) *Error {
	e := &Error{Status: status, Code: "error"}
	if payload, ok := mustDict(raw); ok {
		if v, ok := payload.Get("code"); ok {
			e.Code = pyStr(v)
		}
		if v, ok := payload.Get("message"); ok {
			e.Message = pyStr(v)
		}
	}
	return e
}

func mustDict(raw []byte) (py.Dict, bool) {
	v, err := py.JSONLoads(raw)
	if err != nil {
		return nil, false
	}
	d, ok := v.(py.Dict)
	return d, ok
}

func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return py.ReprValue(v)
}

// Query is a query string in the order urlencode writes it.
type Query []py.Pair

func (q Query) encode() string {
	var parts []string
	for _, p := range q {
		if p.Value == nil {
			continue
		}
		parts = append(parts, url.QueryEscape(p.Key)+"="+url.QueryEscape(pyStr(p.Value)))
	}
	return strings.Join(parts, "&")
}

// Request is request: one call with pacing and the retries of rate limits and server errors; the reply is the
// decoded JSON object.
func (c *Client) Request(ctx context.Context, method, path string, body any, query Query) (py.Dict, error) {
	target := c.BaseURL + path
	if enc := query.encode(); enc != "" {
		target += "?" + enc
	}
	var data []byte
	if body != nil {
		data = []byte(py.JSONDumps(body))
	}
	for attempt := 0; ; attempt++ {
		if err := c.pace(ctx); err != nil {
			return nil, err
		}
		status, headers, raw, err := c.send(ctx, method, target, data)
		if err != nil {
			return nil, err
		}
		if status < 300 {
			if len(raw) == 0 {
				return py.Dict{}, nil
			}
			v, err := py.JSONLoads(raw)
			if err != nil {
				return nil, err
			}
			d, _ := v.(py.Dict)
			return d, nil
		}
		if retryable(status) && attempt < MaxRetries {
			if err := c.Sleep(ctx, retryDelay(status, headers, attempt)); err != nil {
				return nil, err
			}
			c.last = c.Clock()
			continue
		}
		return nil, refusal(status, raw)
	}
}

// Each is paginate: fn gets every result of every page, and the next page is requested only after fn has seen the
// last result of the one before.
func (c *Client) Each(ctx context.Context, method, path string, body py.Dict, query Query, fn func(py.Dict) error) error {
	cursor := ""
	for {
		var reply py.Dict
		var err error
		if method == http.MethodPost {
			paged := append(append(py.Dict{}, body...), py.Pair{Key: pagedKeyPerPage, Value: PageSize})
			if cursor != "" {
				paged = append(paged, py.Pair{Key: pagedKeyCursor, Value: cursor})
			}
			reply, err = c.Request(ctx, method, path, paged, nil)
		} else {
			paged := append(append(Query{}, query...), py.Pair{Key: pagedKeyPerPage, Value: PageSize})
			if cursor != "" {
				paged = append(paged, py.Pair{Key: pagedKeyCursor, Value: cursor})
			}
			reply, err = c.Request(ctx, method, path, nil, paged)
		}
		if err != nil {
			return err
		}
		for _, item := range list(reply, "results") {
			if err := fn(asDict(item)); err != nil {
				return err
			}
		}
		next, _ := reply.Get("next_cursor")
		cursor, _ = next.(string)
		if !py.Truthy(field(reply, "has_more")) || cursor == "" {
			return nil
		}
	}
}

// Search is search: every page, or every data source, of the workspace, trashed ones when inTrash.
func (c *Client) Search(ctx context.Context, object string, inTrash bool, fn func(py.Dict) error) error {
	filter := py.Dict{{Key: "property", Value: "object"}, {Key: "value", Value: object}}
	if inTrash {
		filter = append(filter, py.Pair{Key: "in_trash", Value: true})
	}
	return c.Each(ctx, http.MethodPost, "/v1/search", py.Dict{{Key: "filter", Value: filter}}, nil, fn)
}

func (c *Client) GetPage(ctx context.Context, pageID string) (py.Dict, error) {
	return c.Request(ctx, http.MethodGet, "/v1/pages/"+pageID, nil, nil)
}

func (c *Client) PageMarkdown(ctx context.Context, pageID string) (py.Dict, error) {
	return c.Request(ctx, http.MethodGet, "/v1/pages/"+pageID+"/markdown", nil, nil)
}

func (c *Client) UpdateMarkdown(ctx context.Context, pageID string, payload any) (py.Dict, error) {
	return c.Request(ctx, http.MethodPatch, "/v1/pages/"+pageID+"/markdown", payload, nil)
}

func (c *Client) PatchProperties(ctx context.Context, pageID string, properties any) (py.Dict, error) {
	return c.Request(ctx, http.MethodPatch, "/v1/pages/"+pageID, py.Dict{{Key: "properties", Value: properties}}, nil)
}

func (c *Client) MovePage(ctx context.Context, pageID string, parent any) (py.Dict, error) {
	return c.Request(ctx, http.MethodPost, "/v1/pages/"+pageID+"/move", py.Dict{{Key: "parent", Value: parent}}, nil)
}

func (c *Client) SetDatabaseTrash(ctx context.Context, databaseID string, inTrash bool) (py.Dict, error) {
	return c.Request(ctx, http.MethodPatch, "/v1/databases/"+databaseID, py.Dict{{Key: "in_trash", Value: inTrash}}, nil)
}

func (c *Client) CreatePage(ctx context.Context, body any) (py.Dict, error) {
	return c.Request(ctx, http.MethodPost, "/v1/pages", body, nil)
}

func (c *Client) SetTrash(ctx context.Context, pageID string, inTrash bool) (py.Dict, error) {
	return c.Request(ctx, http.MethodPatch, "/v1/pages/"+pageID, py.Dict{{Key: "in_trash", Value: inTrash}}, nil)
}

func (c *Client) AppendChildren(ctx context.Context, blockID string, children any) (py.Dict, error) {
	return c.Request(ctx, http.MethodPatch, "/v1/blocks/"+blockID+"/children", py.Dict{{Key: "children", Value: children}}, nil)
}

func (c *Client) BlockChildren(ctx context.Context, blockID string, fn func(py.Dict) error) error {
	return c.Each(ctx, http.MethodGet, "/v1/blocks/"+blockID+"/children", nil, nil, fn)
}

func (c *Client) PropertyItems(ctx context.Context, pageID, propertyID string, fn func(py.Dict) error) error {
	return c.Each(ctx, http.MethodGet, "/v1/pages/"+pageID+"/properties/"+quote(propertyID), nil, nil, fn)
}

func quote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~':
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)+0x100, 16)[1:]))
		}
	}
	return b.String()
}

func asDict(v any) py.Dict {
	d, _ := v.(py.Dict)
	return d
}

func field(d py.Dict, key string) any {
	v, _ := d.Get(key)
	return v
}

func text(d py.Dict, key string) string {
	s, _ := field(d, key).(string)
	return s
}

func list(d py.Dict, key string) []any {
	l, _ := field(d, key).([]any)
	return l
}

func richText(items []any) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(text(asDict(item), "plain_text"))
	}
	return b.String()
}

// BlocksMarkdown is blocks_markdown: the text of a block's children, nested ones indented, to BlockDepth levels.
func (c *Client) BlocksMarkdown(ctx context.Context, blockID string, depth int) (string, error) {
	var lines []string
	number := 0
	err := c.BlockChildren(ctx, blockID, func(block py.Dict) error {
		kind, _ := field(block, "type").(string)
		data := asDict(field(block, kind))
		body := richText(list(data, "rich_text"))
		if kind == "numbered_list_item" {
			number++
		} else {
			number = 0
		}
		line := body
		switch kind {
		case "heading_1":
			line = "# " + body
		case "heading_2":
			line = "## " + body
		case "heading_3":
			line = "### " + body
		case "bulleted_list_item":
			line = "- " + body
		case "numbered_list_item":
			line = strconv.Itoa(number) + ". " + body
		case "to_do":
			mark := " "
			if py.Truthy(field(data, "checked")) {
				mark = "x"
			}
			line = "- [" + mark + "] " + body
		case "quote":
			line = "> " + body
		case "code":
			line = "```\n" + body + "\n```"
		}
		if line != "" {
			lines = append(lines, strings.Repeat("  ", depth)+line)
		}
		if py.Truthy(field(block, "has_children")) && depth < BlockDepth && kind != "child_page" {
			nested, err := c.BlocksMarkdown(ctx, text(block, "id"), depth+1)
			if err != nil {
				return err
			}
			lines = append(lines, nested)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	kept := lines[:0]
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n"), nil
}
