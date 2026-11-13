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

	"github.com/jeiang/atrium/internal/settings"
)

const (
	KeyLLMBodyChars  = "llm_body_chars"
	KeyLLMTopSenders = "llm_top_senders"

	EndpointEmbed = "embed"
)

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

func (e Endpoint) Fingerprint() string {
	sum := sha256.Sum256([]byte(e.URL + "\x00" + e.Model + "\x00" + e.key))
	return hex.EncodeToString(sum[:8])
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

type Active struct {
	URL, Model string
	Dim        int
}

func WithModels(ctx context.Context, q Querier, active Active) (context.Context, error) {
	s := Settings{
		EmbedModel:     EmbedModel,
		LLMBodyChars:   TuningLlmBodyChars,
		EmbedBodyChars: TuningMailEmbedBodyChars,
		Batch:          TuningMailEmbedBatch,
		TopSenders:     TuningLlmTopSenders,
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
	return With(ctx, s), nil
}
