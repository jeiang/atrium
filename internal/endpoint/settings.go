package endpoint

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyChatURL       = "mail_chat_url"
	KeyChatModel     = "mail_chat_model"
	KeyChatMaxTokens = "mail_chat_max_tokens"
	KeyChatKey       = "mail_chat_key"
	KeyLLMBodyChars  = "llm_body_chars"
	KeyLLMTopSenders = "llm_top_senders"

	EndpointChat  = "chat"
	EndpointEmbed = "embed"
)

func SettingsDefs() []settings.Def {
	return []settings.Def{
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: KeyChatKey, Module: "mail", Type: settings.String}),
		settings.Deploy(settings.Def{Key: transport.CAKey(Integration(EndpointChat)), Module: "mail", Type: settings.String}),
		settings.Deploy(settings.Def{Key: transport.ProxyKey(Integration(EndpointChat)), Module: "mail", Type: settings.String}),
	}
}

func init() { settings.MustRegister(SettingsDefs()...) }

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type readOnly struct{ Querier }

func (readOnly) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("endpoint settings: read-only querier")
}

type Reader struct {
	ctx context.Context
	q   readOnly
	err error
}

func Read(ctx context.Context, q Querier) *Reader { return &Reader{ctx: ctx, q: readOnly{q}} }

func (r *Reader) Err() error { return r.err }

func (r *Reader) resolve(key string) settings.Resolved {
	if r.err != nil {
		return settings.Resolved{}
	}
	found, err := settings.From(r.ctx).Resolve(r.ctx, r.q, key)
	r.err = err
	return found
}

func (r *Reader) Text(key string) string { return r.resolve(key).Value }

func (r *Reader) Int(key string) int {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	n, err := found.Int()
	r.err = err
	return int(n)
}

func (r *Reader) Float(key string) float64 {
	found := r.resolve(key)
	if r.err != nil {
		return 0
	}
	f, err := found.Float()
	r.err = err
	return f
}

func (r *Reader) Ints(key string) []int {
	found := r.resolve(key)
	if r.err != nil {
		return nil
	}
	out, err := SplitInts(found.Value)
	r.err = err
	return out
}

func (r *Reader) List(key string) []string { return SplitList(r.resolve(key).Value) }

func SplitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func SplitInts(value string) ([]int, error) {
	parts := SplitList(value)
	if len(parts) == 0 {
		return nil, settings.Invalid("must list at least one whole number")
	}
	out := make([]int, len(parts))
	for i, part := range parts {
		n, ok := parseInt(part)
		if !ok || n < 1 || n > math.MaxInt32 {
			return nil, settings.Invalid("must be comma-separated whole numbers of at least 1, got " + strconv.Quote(part))
		}
		out[i] = int(n)
	}
	return out, nil
}

func parseInt(value string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return n, err == nil
}

type Endpoint struct {
	Kind, URL, Model string
	key              string
}

func New(kind, url, model, key string) Endpoint {
	return Endpoint{Kind: kind, URL: url, Model: model, key: key}
}

func Endpoints(ctx context.Context, q Querier) ([]Endpoint, error) {
	r := Read(ctx, q)
	chat := r.Text(KeyChatModel)
	if r.Err() != nil {
		return nil, r.Err()
	}
	store := secrets.NewIn(settings.From(ctx), nil, readOnly{q}, nil)
	chatURL, err := urlOf(ctx, q, KeyChatURL)
	if err != nil {
		return nil, err
	}
	chatKey, err := store.Value(ctx, KeyChatKey)
	if err != nil {
		return nil, err
	}
	embed := New(EndpointEmbed, "", EmbedModel, "")
	if e, err := OpenEmbedding(ctx, q); err == nil {
		embed.URL, embed.Model, embed.key = e.Base, e.Model, e.Token
		e.Close()
	} else if !IsUnconfigured(err) {
		return nil, err
	}
	return []Endpoint{New(EndpointChat, chatURL, chat, chatKey), embed}, nil
}

func (e Endpoint) Fingerprint() string {
	sum := sha256.Sum256([]byte(e.URL + "\x00" + e.Model + "\x00" + e.key))
	return hex.EncodeToString(sum[:8])
}

func Of(endpoints []Endpoint, kind string) (Endpoint, bool) {
	for _, e := range endpoints {
		if e.Kind == kind {
			return e, true
		}
	}
	return Endpoint{}, false
}

func Integration(kind string) string { return "mail_" + kind }

func WithEndpoints(ctx context.Context, q Querier, endpoints []Endpoint) (context.Context, error) {
	out := ctx
	for _, e := range endpoints {
		if e.Kind != EndpointChat {
			continue
		}
		client, err := transport.Configured(ctx, readOnly{q}, Integration(e.Kind), config.From(ctx).HTTPTimeout(config.OS))
		if err != nil {
			return ctx, err
		}
		out = httpx.WithEndpoint(out, e.URL, e.key, client)
	}
	return out, nil
}

func ChatURL(ctx context.Context, q Querier) (string, error) {
	return urlOf(ctx, q, KeyChatURL)
}

func EmbedURL(ctx context.Context, q Querier, activeURL string) (string, error) {
	if activeURL != "" {
		return activeURL, nil
	}
	e, err := OpenEmbedding(ctx, q)
	if err != nil {
		if IsUnconfigured(err) {
			return "", nil
		}
		return "", err
	}
	defer e.Close()
	return e.Base, nil
}

func urlOf(ctx context.Context, q Querier, key string) (string, error) {
	found, err := settings.From(ctx).Resolve(ctx, readOnly{q}, key)
	return strings.TrimRight(found.Value, "/"), err
}

type Active struct {
	URL, Model string
	Dim        int
}

func WithModels(ctx context.Context, q Querier, active Active) (context.Context, error) {
	r := Read(ctx, q)
	s := Settings{
		ChatModel:      r.Text(KeyChatModel),
		EmbedModel:     EmbedModel,
		LLMBodyChars:   TuningLlmBodyChars,
		EmbedBodyChars: TuningMailEmbedBodyChars,
		Batch:          TuningMailEmbedBatch,
		MaxTokens:      TuningMailChatMaxTokens,
		TopSenders:     TuningLlmTopSenders,
	}
	if r.Err() != nil {
		return ctx, r.Err()
	}
	s.Dim = active.Dim
	if e, err := OpenEmbedding(ctx, q); err == nil {
		s.EmbedModel = e.Model
		ctx = WithEmbedding(ctx, e)
	} else if !IsUnconfigured(err) {
		return ctx, err
	}
	if active.URL != "" {
		s.EmbedModel = active.Model
	}
	chatURL, err := urlOf(ctx, q, KeyChatURL)
	if err != nil {
		return ctx, err
	}
	chatKey, err := secrets.NewIn(settings.From(ctx), nil, readOnly{q}, nil).Value(ctx, KeyChatKey)
	if err != nil {
		return ctx, err
	}
	return WithEndpoints(With(ctx, s), q, []Endpoint{New(EndpointChat, chatURL, s.ChatModel, chatKey)})
}
