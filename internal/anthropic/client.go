package anthropic

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	BaseURLEnv     = "ANTHROPIC_BASE_URL"
	connectTimeout = 5 * time.Second
	readTimeout    = 10 * time.Minute
)

var filesLog = logx.Logger("atrium.agent.files")

type Client struct {
	sdk sdk.Client
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
	return sdk.BetaMessageNewParams{Model: sdk.Model(p.Model), MaxTokens: p.MaxTokens, Betas: betas}
}

func (c *Client) Create(ctx context.Context, p Params) (*Message, error) {
	body, err := p.body()
	if err != nil {
		return nil, err
	}
	res, err := c.sdk.Beta.Messages.New(ctx, p.sdkParams(), option.WithRequestBody("application/json", body))
	if err != nil {
		return nil, classify(err)
	}
	return decodeMessage(res.RawJSON())
}

func (c *Client) Stream(ctx context.Context, p Params, observe func(Event) error) (*Message, error) {
	body, err := p.body()
	if err != nil {
		return nil, err
	}
	stream := c.sdk.Beta.Messages.NewStreaming(ctx, p.sdkParams(), option.WithRequestBody("application/json", body))
	defer func() { _ = stream.Close() }()
	var acc sdk.BetaMessage
	stopped := false
	for stream.Next() {
		ev := stream.Current()
		if err := acc.Accumulate(ev); err != nil {
			return nil, fmt.Errorf("anthropic: %w", err)
		}
		stopped = stopped || ev.Type == "message_stop"
		if observe == nil {
			continue
		}
		e, err := eventOf(&acc, ev)
		if err != nil {
			return nil, err
		}
		if err := observe(e); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, classify(err)
	}
	if !stopped {
		return nil, classify(io.ErrUnexpectedEOF)
	}
	return decodeMessage(acc.RawJSON())
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

func (c *Client) DeleteFile(ctx context.Context, fileID string) {
	if c == nil || fileID == "" {
		return
	}
	if _, err := c.sdk.Files.Delete(ctx, fileID, sdk.FileDeleteParams{}); err != nil {
		filesLog.Warn(fmt.Sprintf("Files API delete of %s failed: %s", fileID, classify(err)))
	}
}
