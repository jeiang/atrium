package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
)

const (
	BaseURL         = "https://api.notion.com"
	Version         = "2026-03-11"
	MinInterval     = 1.0 / 3
	pagedKeyCursor  = "start_cursor"
	pagedKeyPerPage = "page_size"
)

var retryStatuses = [...]int{429, 500, 502, 503, 504}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("notion %d %s: %s", e.Status, e.Code, e.Message)
}

func unavailable(message string) *Error {
	return &Error{Status: 0, Code: "unavailable", Message: message}
}

type Client struct {
	token   string
	BaseURL string
	HTTP    *http.Client
	API     APILimits
	last    time.Time
	paced   bool
}

func baseURL(env config.Env) string {
	if env == nil {
		env = config.OS
	}
	if u, ok := env("ATRIUM_NOTION_BASE_URL"); ok {
		if u = strings.TrimRight(u, "/"); u != "" {
			return u
		}
	}
	return BaseURL
}

func NewClient(ctx context.Context, q db.Querier, env config.Env) (*Client, error) {
	token, err := Token(ctx, q)
	if err != nil {
		return nil, err
	}
	return newCandidate(ctx, q, env, token, nil)
}

func newCandidate(ctx context.Context, q db.Querier, env config.Env, token string, hc *http.Client) (*Client, error) {
	limits, err := LoadAPI(ctx, q)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc, err = transport.For(ctx, q, "notion", limits.Timeout)
		if err != nil {
			return nil, err
		}
	}
	return &Client{token: token, BaseURL: baseURL(env), HTTP: hc, API: limits}, nil
}

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
		if wait := MinInterval - time.Since(c.last).Seconds(); wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	c.last, c.paced = time.Now(), true
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

func (c *Client) retryDelay(status int, headers http.Header, attempt int) float64 {
	delay, err := strconv.ParseFloat(strings.TrimSpace(headers.Get("Retry-After")), 64)
	if err != nil {
		delay = c.API.RetryAfterBase
		if status != http.StatusTooManyRequests {
			delay = c.API.RetryAfterBase * math.Pow(2, float64(attempt))
		}
	}
	return math.Min(math.Max(delay, 0), c.API.RetryAfterMax)
}

func refusal(status int, raw []byte) *Error {
	e := &Error{Status: status, Code: "error"}
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		if payload.Code != "" {
			e.Code = payload.Code
		}
		e.Message = payload.Message
	}
	return e
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	target := c.BaseURL + path
	var data []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = encoded
	}
	for attempt := 0; ; attempt++ {
		if err := c.pace(ctx); err != nil {
			return err
		}
		status, headers, raw, err := c.send(ctx, method, target, data)
		if err != nil {
			return err
		}
		if status < 300 {
			if out == nil || len(raw) == 0 {
				return nil
			}
			return json.Unmarshal(raw, out)
		}
		if retryable(status) && attempt < c.API.MaxRetries {
			if err := sleep(ctx, c.retryDelay(status, headers, attempt)); err != nil {
				return err
			}
			c.last = time.Now()
			continue
		}
		return refusal(status, raw)
	}
}

func paged(body any, cursor string, size int) (any, error) {
	fields := map[string]any{}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return nil, err
		}
	}
	fields[pagedKeyPerPage] = size
	if cursor != "" {
		fields[pagedKeyCursor] = cursor
	}
	return fields, nil
}

func (c *Client) Each(ctx context.Context, method, path string, body any, query url.Values, fn func(json.RawMessage) error) error {
	cursor := ""
	for {
		var reply struct {
			Results    []json.RawMessage `json:"results"`
			NextCursor string            `json:"next_cursor"`
			HasMore    bool              `json:"has_more"`
		}
		var err error
		if method == http.MethodPost {
			pagedBody, pageErr := paged(body, cursor, c.API.PageSize)
			if pageErr != nil {
				return pageErr
			}
			err = c.do(ctx, method, path, pagedBody, &reply)
		} else {
			q := url.Values{}
			for key, values := range query {
				q[key] = append(q[key], values...)
			}
			q.Set(pagedKeyPerPage, strconv.Itoa(c.API.PageSize))
			if cursor != "" {
				q.Set(pagedKeyCursor, cursor)
			}
			err = c.do(ctx, method, path+"?"+q.Encode(), nil, &reply)
		}
		if err != nil {
			return err
		}
		for _, item := range reply.Results {
			if err := fn(item); err != nil {
				return err
			}
		}
		if !reply.HasMore || reply.NextCursor == "" {
			return nil
		}
		cursor = reply.NextCursor
	}
}

func (c *Client) Search(ctx context.Context, object string, fn func(SearchResult) error) error {
	body := map[string]any{"filter": map[string]any{"property": "object", "value": object}}
	return c.Each(ctx, http.MethodPost, "/v1/search", body, nil, func(raw json.RawMessage) error {
		result, err := decode[SearchResult](raw)
		if err != nil {
			return err
		}
		return fn(result)
	})
}

func (c *Client) GetPage(ctx context.Context, pageID string) (RemotePage, error) {
	var page RemotePage
	err := c.do(ctx, http.MethodGet, "/v1/pages/"+pageID, nil, &page)
	return page, err
}

func (c *Client) PageMarkdown(ctx context.Context, pageID string) (Markdown, error) {
	var markdown Markdown
	err := c.do(ctx, http.MethodGet, "/v1/pages/"+pageID+"/markdown", nil, &markdown)
	return markdown, err
}

func (c *Client) UpdateMarkdown(ctx context.Context, pageID string, payload any) error {
	return c.do(ctx, http.MethodPatch, "/v1/pages/"+pageID+"/markdown", payload, nil)
}

func (c *Client) MovePage(ctx context.Context, pageID string, parent any) error {
	return c.do(ctx, http.MethodPost, "/v1/pages/"+pageID+"/move", map[string]any{"parent": parent}, nil)
}

func (c *Client) CreatePage(ctx context.Context, body any) (RemotePage, error) {
	var page RemotePage
	err := c.do(ctx, http.MethodPost, "/v1/pages", body, &page)
	return page, err
}

func (c *Client) SetTitle(ctx context.Context, pageID, title string) error {
	return c.do(ctx, http.MethodPatch, "/v1/pages/"+pageID, map[string]any{
		"properties": map[string]any{"title": map[string]any{"title": richText(title)}},
	}, nil)
}

func (c *Client) SetTrash(ctx context.Context, pageID string, inTrash bool) error {
	return c.do(ctx, http.MethodPatch, "/v1/pages/"+pageID, map[string]any{"in_trash": inTrash}, nil)
}

func (c *Client) AppendChildren(ctx context.Context, blockID string, children any) error {
	return c.do(ctx, http.MethodPatch, "/v1/blocks/"+blockID+"/children", map[string]any{"children": children}, nil)
}

func (c *Client) BlockChildren(ctx context.Context, blockID string, fn func(Block) error) error {
	return c.Each(ctx, http.MethodGet, "/v1/blocks/"+blockID+"/children", nil, nil, func(raw json.RawMessage) error {
		block, err := decode[Block](raw)
		if err != nil {
			return err
		}
		return fn(block)
	})
}

func (c *Client) BlocksMarkdown(ctx context.Context, blockID string, depth int) (string, error) {
	var lines []string
	number := 0
	err := c.BlockChildren(ctx, blockID, func(block Block) error {
		body := block.text()
		if block.Type == "numbered_list_item" {
			number++
		} else {
			number = 0
		}
		line := body
		switch block.Type {
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
			if block.ToDo != nil && block.ToDo.Checked {
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
		if block.HasChildren && depth < c.API.BlockDepth && block.Type != "child_page" {
			nested, err := c.BlocksMarkdown(ctx, block.ID, depth+1)
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
