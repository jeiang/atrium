package agentclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

type Error struct {
	Status   int
	Code     string
	Mutation string
}

func (e *Error) Error() string {
	return fmt.Sprintf("media agent: status %d (%s, mutation %s)", e.Status, e.Code, e.Mutation)
}

var ErrUnavailable = errors.New("the media agent is unavailable")

type Client struct {
	lease   *connections.Lease
	base    string
	bearer  string
	http    *http.Client
	Observe func(context.Context, connections.State, string)
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return errors.New("the media agent needs an origin URL without credentials, path, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("the media agent requires HTTPS except on loopback")
}

func New(lease *connections.Lease) (*Client, error) {
	if lease == nil {
		return nil, errors.New("the media agent connection is not configured")
	}
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, err
	}
	if err := ValidateURL(base); err != nil {
		return nil, err
	}
	bearer, err := lease.Credential("bearer")
	if err != nil {
		return nil, err
	}
	client, err := lease.StreamClient("url", 10*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{lease: lease, base: strings.TrimRight(base, "/"), bearer: bearer, http: client}, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.http.Do(req)
	if err != nil {
		if c.Observe != nil {
			c.Observe(req.Context(), connections.Failing, "unavailable")
		}
		return nil, ErrUnavailable
	}
	if c.Observe != nil && (resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode >= 500) {
		state := connections.Failing
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			state = connections.NeedsReauth
		}
		c.Observe(req.Context(), state, "agent refused the request")
	}
	return resp, nil
}

func (c *Client) Health(ctx context.Context) (protocol.Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return protocol.Health{}, ErrUnavailable
	}
	resp, err := c.do(req)
	if err != nil {
		return protocol.Health{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return protocol.Health{}, &Error{Status: resp.StatusCode, Code: "unavailable", Mutation: "none"}
	}
	var out protocol.Health
	if err := protocol.Decode(resp.Body, 1<<20, &out); err != nil {
		return out, ErrUnavailable
	}
	return out, nil
}
