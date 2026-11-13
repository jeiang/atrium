package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func (c *Client) call(ctx context.Context, client *http.Client, method, target string, in, out any) error {
	body := bytes.NewReader(nil)
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+target, body)
	if err != nil {
		return ErrUnavailable
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client != nil {
		copied := *c
		copied.http = client
		c = &copied
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var p protocol.Problem
		if protocol.Decode(resp.Body, protocol.MaxBody, &p) != nil || p.Code == "" {
			p = protocol.Problem{Code: protocol.CodeUnavailable, Mutation: protocol.MutationUnknown}
		}
		if p.Mutation == "" {
			p.Mutation = protocol.MutationNone
		}
		return &Error{Status: resp.StatusCode, Code: p.Code, Mutation: p.Mutation}
	}
	if err := protocol.Decode(resp.Body, protocol.MaxBody, out); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) Source(ctx context.Context, p protocol.Path) (protocol.Source, error) {
	var out protocol.Source
	return out, c.call(ctx, nil, http.MethodPost, "/v1/source", protocol.SourceRequest{Source: p}, &out)
}

func (c *Client) Entries(ctx context.Context, p protocol.Path) (protocol.Entries, error) {
	var out protocol.Entries
	return out, c.call(ctx, nil, http.MethodPost, "/v1/entries", protocol.EntriesRequest{Path: p}, &out)
}

func (c *Client) Imports(ctx context.Context) ([]protocol.Path, error) {
	all := []protocol.Path{}
	cursor := ""
	for range 1000 {
		target := "/v1/imports"
		if cursor != "" {
			target += "?" + url.Values{"cursor": {cursor}}.Encode()
		}
		var page protocol.ImportPage
		if err := c.call(ctx, nil, http.MethodGet, target, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Entries...)
		if page.Next == "" {
			return all, nil
		}
		cursor = page.Next
	}
	return nil, ErrUnavailable
}

func (c *Client) Prepare(ctx context.Context, in protocol.PrepareRequest) (protocol.PrepareResponse, error) {
	var out protocol.PrepareResponse
	return out, c.call(ctx, nil, http.MethodPost, "/v1/filing/prepare", in, &out)
}

func (c *Client) File(ctx context.Context, in protocol.FilingRequest) (protocol.FilingResult, error) {
	var out protocol.FilingResult
	client, err := c.lease.StreamClient("url", 30*time.Minute)
	if err != nil {
		return out, ErrUnavailable
	}
	err = c.call(ctx, client, http.MethodPut, "/v1/filings/"+url.PathEscape(in.Key), in, &out)
	if err == nil && out.Key != in.Key {
		return out, ErrUnavailable
	}
	return out, err
}

func (c *Client) Filing(ctx context.Context, key string, after int, wait time.Duration) (protocol.FilingResult, error) {
	var out protocol.FilingResult
	client, err := c.lease.StreamClient("url", wait+10*time.Second)
	if err != nil {
		return out, ErrUnavailable
	}
	q := url.Values{"after": {strconv.Itoa(after)}, "wait_seconds": {strconv.Itoa(int(wait / time.Second))}}
	return out, c.call(ctx, client, http.MethodGet, "/v1/filings/"+url.PathEscape(key)+"?"+q.Encode(), nil, &out)
}

func (c *Client) Inspect(ctx context.Context, key string) (protocol.Inspection, error) {
	var out protocol.Inspection
	return out, c.call(ctx, nil, http.MethodPost, "/v1/filings/"+url.PathEscape(key)+"/inspect", nil, &out)
}
