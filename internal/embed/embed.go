package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/obs"
)

const (
	KNNMax = 4096

	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

var log = logx.Logger("atrium.embed")

type TextsFunc func(ctx context.Context, url string, texts []string) ([][]float64, error)

func QueryText(query string) string {
	return "task: search result | query: " + query
}

func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsReply struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func Texts(ctx context.Context, url string, texts []string) ([][]float64, error) {
	s := endpoint.From(ctx)
	ctx, span := obs.Start(ctx, "embed.request", trace.WithAttributes(
		obs.String(obs.EmbedModel, s.EmbedModel),
		obs.Int(obs.EmbedBatchSize, len(texts)),
		obs.Int(obs.EmbedDimensions, s.Dim),
	))
	started := time.Now()
	vectors, err := textsAt(ctx, url, texts, s)
	duration := time.Since(started)
	span.SetAttributes(obs.Bool(obs.EmbedOK, err == nil))
	if err != nil {
		obs.Fail(span, "error")
	}
	span.End()
	obs.EmbedRequests.Add(1, s.EmbedModel, strconv.FormatBool(err == nil))
	obs.EmbedDuration.Observe(duration.Seconds(), s.EmbedModel)
	return vectors, err
}

func textsAt(ctx context.Context, url string, texts []string, s endpoint.Settings) ([][]float64, error) {
	embedding := endpoint.EmbeddingFrom(ctx)
	if embedding == nil {
		return nil, &httpx.ServiceUnavailable{Msg: "no embedding connection is configured"}
	}
	raw, err := embedding.Post(ctx, url, embeddingsRequest{s.EmbedModel, texts})
	if err != nil {
		return nil, err
	}
	var reply embeddingsReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: %w", httpx.ErrPayload, err)
	}
	if len(reply.Data) != len(texts) {
		return nil, fmt.Errorf("%w: embedding response has %d vectors for %d texts", httpx.ErrPayload, len(reply.Data), len(texts))
	}
	out := make([][]float64, len(texts))
	seen := make([]bool, len(texts))
	for _, entry := range reply.Data {
		if entry.Index < 0 || entry.Index >= len(texts) || seen[entry.Index] {
			return nil, fmt.Errorf("%w: embedding response index %d is out of range or repeated", httpx.ErrPayload, entry.Index)
		}
		if len(entry.Embedding) != s.Dim {
			return nil, fmt.Errorf("%w: embedding response shape mismatch: expected %dx%d", httpx.ErrPayload, len(texts), s.Dim)
		}
		seen[entry.Index] = true
		out[entry.Index] = Normalize(entry.Embedding)
	}
	return out, nil
}

func Normalize(vector []float64) []float64 {
	var sum float64
	for _, x := range vector {
		sum += x * x
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		norm = 1
	}
	out := make([]float64, len(vector))
	for i, x := range vector {
		out[i] = x / norm
	}
	return out
}

func Serialize(vector []float64) []byte {
	f := make([]float32, len(vector))
	for i, x := range vector {
		f[i] = float32(x)
	}
	return db.SerializeFloat32(f)
}

func EmbedURL(ctx context.Context, q endpoint.Querier) (string, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return "", err
	}
	return EmbedURLFor(ctx, q, active)
}

func EmbedURLFor(ctx context.Context, q endpoint.Querier, active Index) (string, error) {
	return endpoint.EmbedURL(ctx, q, active.URL)
}

func WithModels(ctx context.Context, q endpoint.Querier) (context.Context, error) {
	active, err := Active(ctx, q)
	if err != nil {
		return ctx, err
	}
	return WithIndex(ctx, q, active)
}

func WithIndex(ctx context.Context, q endpoint.Querier, active Index) (context.Context, error) {
	return endpoint.WithModels(ctx, q, endpoint.Active{URL: active.URL, Model: active.Model, Dim: active.Dimension})
}
