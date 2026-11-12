package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
)

const (
	KindEmbedding          = "service/embedding"
	EmbeddingID            = "service/embedding"
	embeddingModelsTimeout = 10 * time.Second
)

type EmbeddingUnconfigured struct{}

func (EmbeddingUnconfigured) Error() string { return "the embedding connection is not configured" }

type EmbeddingView struct {
	Configured bool
	URL        string
	Model      string
	State      string
	Reason     string
	CheckedAt  *time.Time
	TestFailed bool
	Data       json.RawMessage
}

func init() {
	for _, d := range Connections() {
		connections.Register(d)
	}
}

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     connections.Kind(KindEmbedding),
		Validate: validateEmbedding,
		Test:     testEmbedding,
	}}
}

func validateEmbedding(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("embedding config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "url":
			if err := httpURL(value); err != nil {
				return fmt.Errorf("embedding url %w", err)
			}
		case "model":
			if strings.TrimSpace(value) == "" {
				return errors.New("embedding model must not be blank")
			}
		default:
			return fmt.Errorf("embedding config has no %s field", key)
		}
	}
	return nil
}

func httpURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http or https URL")
	}
	return nil
}

func embeddingConfigValue(lease *connections.Lease, name string) string {
	var m map[string]string
	_ = json.Unmarshal(lease.Config(), &m)
	return strings.TrimSpace(m[name])
}

func testEmbedding(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	if embeddingConfigValue(lease, "url") == "" {
		return []connections.Result{{Capability: "embeddings", State: connections.Unconfigured, Reason: "no embedding URL configured"}}, nil
	}
	e, err := embeddingFor(ctx, lease)
	if err != nil {
		return nil, err
	}
	models := e.Models(ctx)
	reason, dim, status := e.ProbeWithStatus(ctx)
	data, err := json.Marshal(map[string]any{
		"dimension":   dim,
		"models":      models,
		"fingerprint": e.Fingerprint(),
		"hosted":      Hosted(e.Base),
		"probed_at":   float64(time.Now().UnixNano()) / 1e9,
	})
	if err != nil {
		return nil, err
	}
	result := connections.Result{Capability: "embeddings", Data: data}
	if reason != "" {
		result.State = connections.Failing
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			result.State = connections.NeedsReauth
		}
		result.Reason = reason
		return []connections.Result{result}, nil
	}
	detail := fmt.Sprintf("%d models listed, %d dimensions", len(models), dim)
	if Hosted(e.Base) {
		detail += "; hosted endpoint: mail content leaves this host"
	}
	return []connections.Result{{Capability: "embeddings", State: connections.OK, Reason: detail, Data: data}}, nil
}

func embeddingFor(ctx context.Context, lease *connections.Lease) (*Embedding, error) {
	base := embeddingConfigValue(lease, "url")
	if base == "" {
		return nil, EmbeddingUnconfigured{}
	}
	model := embeddingConfigValue(lease, "model")
	if model == "" {
		model = EmbedModel
	}
	token, err := lease.Credential("bearer")
	if err != nil {
		var missing connections.MissingCredential
		if !errors.As(err, &missing) {
			return nil, err
		}
		token = ""
	}
	client, err := lease.Client("url", HTTPTimeout(ctx))
	if err != nil {
		return nil, err
	}
	return NewEmbedding(base, model, token, client), nil
}

func embeddingDimension(raw json.RawMessage) (int, error) {
	var reply struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Data) != 1 || len(reply.Data[0].Embedding) == 0 {
		return 0, errors.New("the reply has no embedding")
	}
	return len(reply.Data[0].Embedding), nil
}

type Embedding struct {
	Base, Model, Token string
	HTTP               *http.Client
}

func NewEmbedding(base, model, token string, hc *http.Client) *Embedding {
	return &Embedding{Base: strings.TrimRight(base, "/"), Model: model, Token: token, HTTP: hc}
}

func (e *Embedding) Close() {}

func (e *Embedding) Fingerprint() string {
	return New(EndpointEmbed, e.Base, e.Model, e.Token).Fingerprint()
}

func (e *Embedding) Post(ctx context.Context, base string, payload any) (json.RawMessage, error) {
	raw, _, err := e.post(ctx, base, payload)
	return raw, err
}

func (e *Embedding) Probe(ctx context.Context) (string, int) {
	reason, dim, _ := e.ProbeWithStatus(ctx)
	return reason, dim
}

func (e *Embedding) ProbeWithStatus(ctx context.Context) (string, int, int) {
	raw, status, err := e.post(ctx, e.Base, map[string]any{"model": e.Model, "input": []string{"atrium probe"}})
	if err != nil {
		return err.Error(), 0, status
	}
	dim, err := embeddingDimension(raw)
	if err != nil {
		return err.Error(), 0, status
	}
	return "", dim, status
}

func (e *Embedding) post(ctx context.Context, base string, payload any) (json.RawMessage, int, error) {
	if e.HTTP == nil {
		return nil, 0, &httpx.ServiceUnavailable{Msg: "no embedding connection is configured"}
	}
	base = strings.TrimRight(base, "/")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	rawURL := base + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &httpx.ServiceUnavailable{Msg: fmt.Sprintf("%s: %v", rawURL, err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if e.Token != "" && base == e.Base {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, &httpx.ServiceUnavailable{Msg: fmt.Sprintf("%s: %s", rawURL, httpx.Reason(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, resp.StatusCode, ctx.Err()
		}
		return nil, resp.StatusCode, &httpx.ServiceUnavailable{Msg: fmt.Sprintf("%s: %s", rawURL, httpx.Reason(err))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		phrase := strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)+" ")
		return nil, resp.StatusCode, &httpx.ServiceUnavailable{Msg: fmt.Sprintf("%s: HTTP %d: %s", rawURL, resp.StatusCode, phrase)}
	}
	if !json.Valid(data) {
		return nil, resp.StatusCode, fmt.Errorf("%w: %s: reply is not JSON", httpx.ErrPayload, rawURL)
	}
	return data, resp.StatusCode, nil
}

func (e *Embedding) models(ctx context.Context) []string {
	if e.HTTP == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, embeddingModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.Base+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", httpx.UserAgent)
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var reply struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&reply) != nil {
		return nil
	}
	ids := make([]string, 0, len(reply.Data))
	for _, m := range reply.Data {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

func (e *Embedding) Models(ctx context.Context) []string { return e.models(ctx) }

func OpenEmbedding(ctx context.Context, q Querier) (*Embedding, error) {
	lease, err := openEmbeddingLease(ctx, q)
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return nil, EmbeddingUnconfigured{}
		}
		return nil, err
	}
	e, err := embeddingFor(ctx, lease)
	if err != nil {
		lease.Close()
		return nil, err
	}
	return e, nil
}

func EmbeddingState(ctx context.Context, q Querier) (EmbeddingView, error) {
	view, err := connections.ReadView(ctx, readOnly{q}, connections.ID(EmbeddingID))
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return EmbeddingView{}, EmbeddingUnconfigured{}
		}
		return EmbeddingView{}, err
	}
	return embeddingView(view), nil
}

func TestEmbedding(ctx context.Context, d *db.DB) (EmbeddingView, error) {
	store := connections.DefaultFor(d.Read)
	if store == nil {
		var err error
		store, err = connections.New(d, config.From(ctx).Host(), connections.DefaultRegistry())
		if err != nil {
			return EmbeddingView{}, err
		}
	}
	view, err := store.Test(ctx, connections.ID(EmbeddingID))
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return EmbeddingView{}, EmbeddingUnconfigured{}
		}
		return EmbeddingView{}, err
	}
	return embeddingView(view), nil
}

func openEmbeddingLease(ctx context.Context, q Querier) (*connections.Lease, error) {
	read := readOnly{q}
	if dq, ok := any(q).(db.Querier); ok {
		if store := connections.DefaultFor(dq); store != nil {
			return store.Open(ctx, connections.ID(EmbeddingID))
		}
	}
	return connections.OpenRead(ctx, read, config.From(ctx).Host(), connections.ID(EmbeddingID))
}

func embeddingView(view connections.View) EmbeddingView {
	out := EmbeddingView{
		Configured: embeddingConfigOf(view.Config, "url") != "",
		URL:        embeddingConfigOf(view.Config, "url"),
		Model:      embeddingConfigOf(view.Config, "model"),
		State:      string(view.State),
		Reason:     view.Reason,
		CheckedAt:  view.CheckedAt,
		TestFailed: view.TestFailed(),
	}
	for _, r := range view.Results {
		if r.Capability == "embeddings" {
			out.Data = r.Data
		}
	}
	return out
}

func embeddingConfigOf(raw json.RawMessage, name string) string {
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	return m[name]
}

func EmbeddingConfigured(ctx context.Context, q Querier) (string, error) {
	view, err := EmbeddingState(ctx, q)
	if err != nil {
		var unconfigured EmbeddingUnconfigured
		if errors.As(err, &unconfigured) {
			return "the embedding connection is not configured", nil
		}
		return "", err
	}
	if !view.Configured {
		return "the embedding connection is not configured", nil
	}
	return "", nil
}

func IsUnconfigured(err error) bool {
	var unconfigured EmbeddingUnconfigured
	return errors.As(err, &unconfigured)
}

type embeddingKey struct{}

func WithEmbedding(ctx context.Context, e *Embedding) context.Context {
	return context.WithValue(ctx, embeddingKey{}, e)
}

func EmbeddingFrom(ctx context.Context) *Embedding {
	e, _ := ctx.Value(embeddingKey{}).(*Embedding)
	return e
}

func Hosted(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()
	}
	return strings.Contains(host, ".")
}

func HTTPTimeout(ctx context.Context) time.Duration {
	return config.From(ctx).HTTPTimeout(config.OS)
}
