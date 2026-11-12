package notion

import (
	"context"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	WikiRootKey            = "wiki_root"
	SensitivityPropertyKey = "sensitivity_property"
	PrivateValueKey        = "notion_private_value"
	WikiLogTitleKey        = "wiki_log_title"
)

func Settings() []settings.Def {
	text := func(key, def string) settings.Def {
		return settings.Value(settings.UI, settings.Def{
			Key: key, Module: "notion", Type: settings.String, Default: def,
			Check: nonBlank})
	}
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: WikiRootKey, Module: "notion", Type: settings.String,
			Check: pageID}),
		text(SensitivityPropertyKey, "sensitivity"),
		text(PrivateValueKey, "private"),
		text(WikiLogTitleKey, "Wiki Log"),
	}
}

func init() { settings.MustRegister(Settings()...) }

func nonBlank(value string) error {
	if strings.TrimSpace(value) == "" {
		return settings.Invalid("must not be blank")
	}
	return nil
}

func pageID(value string) error {
	id := notionid.Normalize(value)
	if len(id) != 36 {
		return settings.Invalid("must be a Notion page id: 32 hexadecimal digits, dashes optional")
	}
	for i, c := range id {
		dash := i == 8 || i == 13 || i == 18 || i == 23
		hex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
		if dash == hex {
			return settings.Invalid("must be a Notion page id: 32 hexadecimal digits, dashes optional")
		}
	}
	return nil
}

type resolver struct {
	ctx context.Context
	q   db.Querier
	err error
}

func (r *resolver) value(key string) string {
	if r.err != nil {
		return ""
	}
	found, err := settings.From(r.ctx).Resolve(r.ctx, r.q, key)
	r.err = err
	return found.Value
}

type APILimits struct {
	RetryAfterBase float64
	RetryAfterMax  float64
	MaxRetries     int
	Timeout        time.Duration
	PageSize       int
	BlockDepth     int
}

type SearchLimits struct {
	Branch     int
	Related    int
	Snippet    int
	HeadSource int
}

type Sensitivity struct {
	Property string
	Private  string
}

func LoadSensitivity(ctx context.Context, q db.Querier) (Sensitivity, error) {
	r := resolver{ctx: ctx, q: q}
	out := Sensitivity{
		Property: strings.ToLower(strings.TrimSpace(r.value(SensitivityPropertyKey))),
		Private:  strings.ToLower(strings.TrimSpace(r.value(PrivateValueKey))),
	}
	return out, r.err
}

type Wiki struct {
	Root       string
	LogTitle   string
	MaxAliases int
	MaxSources int
	LabelChars int
}

func LoadWiki(ctx context.Context, q db.Querier) (Wiki, error) {
	r := resolver{ctx: ctx, q: q}
	out := Wiki{
		Root:       notionid.Normalize(r.value(WikiRootKey)),
		LogTitle:   r.value(WikiLogTitleKey),
		MaxAliases: TuningNotionMaxAliases,
		MaxSources: TuningNotionMaxSources,
		LabelChars: TuningNotionLabelChars,
	}
	return out, r.err
}

func (w Wiki) RootID() (string, error) {
	if w.Root == "" {
		return "", WikiError("ATRIUM_NOTION_WIKI_ROOT is not set")
	}
	return w.Root, nil
}
