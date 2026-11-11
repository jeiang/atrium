package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jeiang/atrium/internal/config"
)

const (
	BaseURLEnv     = "ANTHROPIC_BASE_URL"
	connectTimeout = 5 * time.Second
	readTimeout    = 10 * time.Minute
)

type Client struct {
	sdk sdk.Client
}

type Outcome struct {
	Message            *Message
	Usage              Usage
	Started            bool
	Complete           bool
	DefinitelyUnsent   bool
	DefinitelyRejected bool
	Err                error
}

type Option func(*settings)

type settings struct {
	httpClient *http.Client
}

func HTTPClient(c *http.Client) Option {
	return func(s *settings) { s.httpClient = c }
}

func New(key string, env config.Env, opts ...Option) (*Client, error) {
	if key == "" {
		return nil, config.NotConfigured("anthropic_api_key is not set")
	}
	s := settings{httpClient: defaultHTTPClient()}
	for _, o := range opts {
		o(&s)
	}
	reqOpts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(key),
		option.WithHTTPClient(s.httpClient),
		option.WithMiddleware(spendCapNoRetry),
	}
	if base, ok := env(BaseURLEnv); ok && base != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(base))
	}
	return &Client{sdk: sdk.NewClient(reqOpts...)}, nil
}

func spendCapNoRetry(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if resp != nil && resp.StatusCode == http.StatusTooManyRequests && resp.Header.Get("Retry-After") == "" {
		resp.Header.Set("X-Should-Retry", "false")
		resp.Header.Set(spendCapHeader, "1")
	}
	return resp, err
}

func defaultHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext
	t.ResponseHeaderTimeout = readTimeout
	return &http.Client{Transport: t}
}

func (p Params) sdkParams() sdk.BetaMessageNewParams {
	betas := make([]sdk.AnthropicBeta, len(p.Betas))
	for i, b := range p.Betas {
		betas[i] = sdk.AnthropicBeta(b)
	}
	return sdk.BetaMessageNewParams{Betas: betas}
}

func (c *Client) Create(ctx context.Context, p Params) Outcome {
	body, err := p.body()
	if err != nil {
		return Outcome{DefinitelyUnsent: true, Err: err}
	}
	res, err := c.sdk.Beta.Messages.New(ctx, p.sdkParams(), option.WithRequestBody("application/json", body), option.WithMaxRetries(0))
	if err != nil {
		classified := classify(err)
		return Outcome{DefinitelyRejected: rejected(classified), Err: classified}
	}
	raw := res.RawJSON()
	msg, err := decodeMessage(raw)
	if err == nil {
		return Outcome{Message: msg, Usage: msg.Usage, Started: true, Complete: true}
	}
	known := usageOfRaw(raw)
	partial := &Message{ID: res.ID, Model: string(res.Model), Usage: known}
	return Outcome{Message: partial, Usage: known, Started: true, Complete: true, Err: err}
}

func (c *Client) Stream(ctx context.Context, p Params, observe func(Event) error) Outcome {
	body, err := p.body()
	if err != nil {
		return Outcome{DefinitelyUnsent: true, Err: err}
	}
	stream := c.sdk.Beta.Messages.NewStreaming(ctx, p.sdkParams(), option.WithRequestBody("application/json", body), option.WithMaxRetries(0))
	defer func() { _ = stream.Close() }()
	var acc sdk.BetaMessage
	var known Usage
	started := false
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		started = started || ev.Type == "message_start"
		stopped = stopped || ev.Type == "message_stop"
		if err := acc.Accumulate(ev); err != nil {
			return failedStream(&acc, known, started, stopped, fmt.Errorf("anthropic: %w", err))
		}
		if ev.Type == "message_start" || ev.Type == "message_delta" {
			known = usageOf(&acc)
		}
		if observe == nil {
			continue
		}
		if e := eventOf(&acc, ev); e != nil {
			if err := observe(e); err != nil {
				return failedStream(&acc, known, started, stopped, err)
			}
		}
	}
	err = stream.Err()
	if stopped {
		err = nil
	}
	if err == nil && !stopped {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return failedStream(&acc, known, started, stopped, classify(err))
	}
	msg, err := decodeMessage(acc.RawJSON())
	if err != nil {
		return failedStream(&acc, known, started, stopped, err)
	}
	return Outcome{Message: msg, Usage: msg.Usage, Started: true, Complete: true}
}

func failedStream(acc *sdk.BetaMessage, known Usage, started, stopped bool, err error) Outcome {
	return Outcome{
		Message:            knownMessage(acc, known),
		Usage:              known,
		Started:            started,
		Complete:           stopped,
		DefinitelyRejected: !started && rejected(err),
		Err:                err,
	}
}

func knownMessage(acc *sdk.BetaMessage, known Usage) *Message {
	if msg, err := decodeMessage(acc.RawJSON()); err == nil {
		return msg
	}
	if acc.ID == "" {
		return nil
	}
	return &Message{ID: acc.ID, Model: string(acc.Model), Usage: known}
}

func usageOf(acc *sdk.BetaMessage) Usage { return usageOfRaw(acc.RawJSON()) }

func rejected(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.StatusCode >= 400 && api.StatusCode < 500
}

func (c *Client) ModelIDs(ctx context.Context) ([]string, error) {
	pager := c.sdk.Models.ListAutoPaging(ctx, sdk.ModelListParams{})
	var ids []string
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
	}
	if err := pager.Err(); err != nil {
		return nil, classify(err)
	}
	return ids, nil
}
