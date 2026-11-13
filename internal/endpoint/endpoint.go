package endpoint

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
)

const (
	EmbedModel = "embeddinggemma"
)

type Settings struct {
	EmbedModel     string
	LLMBodyChars   int
	EmbedBodyChars int
	Batch          int
	Dim            int
	TopSenders     int
}

type settingsKey struct{}

func With(ctx context.Context, s Settings) context.Context {
	return context.WithValue(ctx, settingsKey{}, s)
}

func From(ctx context.Context) Settings {
	s, _ := ctx.Value(settingsKey{}).(Settings)
	if s.EmbedModel == "" {
		s.EmbedModel = EmbedModel
	}
	if s.LLMBodyChars <= 0 {
		s.LLMBodyChars = config.LLMBodyChars
	}
	if s.EmbedBodyChars <= 0 {
		s.EmbedBodyChars = config.EmbedBodyChars
	}
	if s.Batch <= 0 {
		s.Batch = config.EmbedBatch
	}
	if s.Dim <= 0 {
		s.Dim = config.EmbedDim
	}
	if s.TopSenders <= 0 {
		s.TopSenders = config.LLMTopSenders
	}
	return s
}
