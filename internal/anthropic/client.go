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
	BaseURLEnv = "ANTHROPIC_BASE_URL"
	// The Python SDK's httpx defaults: 5 s to connect, 600 s for everything else.
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

// New is make_client: the key comes from ANTHROPIC_API_KEY (config.NotConfigured when unset) and the base
// URL from ANTHROPIC_BASE_URL, as the Python SDK read it; nothing else in the environment is consulted.
func New(env config.Env, opts ...Option) (*Client, error) {
	key, err := config.AnthropicKey(env)
	if err != nil {
		return nil, err
	}
	s := settings{httpClient: defaultHTTPClient()}
	for _, o := range opts {
		o(&s)
	}
	reqOpts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(key),
		option.WithHTTPClient(s.httpClient),
	}
	if base, ok := env(BaseURLEnv); ok && base != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(base))
	}
	return &Client{sdk: sdk.NewClient(reqOpts...)}, nil
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

// Create is client.beta.messages.create: one non-streaming request.
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

// Stream is client.beta.messages.stream through get_final_message. observe sees every stream event in order
// (Kind empty for those that carry no progress); its error aborts the call and is returned as is.
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

// DeleteFile is files.remote_delete: best effort, a failure is logged as a warning.
func (c *Client) DeleteFile(ctx context.Context, fileID string) {
	if c == nil || fileID == "" {
		return
	}
	if _, err := c.sdk.Files.Delete(ctx, fileID, sdk.FileDeleteParams{}); err != nil {
		filesLog.Warn(fmt.Sprintf("Files API delete of %s failed: %s", fileID, classify(err)))
	}
}
