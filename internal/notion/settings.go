package notion

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	WikiRootKey            = "wiki_root"
	SensitivityPropertyKey = "sensitivity_property"
	PrivateValueKey        = "notion_private_value"
	WikiLogTitleKey        = "wiki_log_title"
	MaxAliasesKey          = "notion_max_aliases"
	MaxSourcesKey          = "notion_max_sources"
	LabelCharsKey          = "notion_label_chars"
	LinkPageCharsKey       = "notion_link_page_chars"
	EditPageCharsKey       = "notion_edit_page_chars"
	LinkMaxTokensKey       = "notion_link_max_tokens"
	EditMaxTokensKey       = "notion_edit_max_tokens"
	BranchLimitKey         = "notion_branch_limit"
	RelatedLimitKey        = "notion_related_limit"
	SnippetCharsKey        = "notion_snippet_chars"
	HeadSourceCharsKey     = "notion_head_source_chars"
	RetryAfterBaseKey      = "notion_retry_after_default_seconds"
	RetryAfterMaxKey       = "notion_retry_after_max_seconds"
	MaxRetriesKey          = "notion_max_retries"
	TimeoutKey             = "notion_timeout_seconds"
	PageSizeKey            = "notion_page_size"
	BlockDepthKey          = "notion_block_depth"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	text := func(key, def string) settings.Def {
		return settings.Def{
			Key: key, Module: "notion", Type: settings.String, Default: def,
			Check: nonBlank, Class: settings.UI, Scopes: instance,
		}
	}
	number := func(key string, typ settings.Type, def string, check func(string) error) settings.Def {
		return settings.Def{
			Key: key, Module: "notion", Type: typ, Default: def,
			Check: check, Class: settings.Advanced, Scopes: instance,
		}
	}
	settings.MustRegister(
		settings.Def{
			Key: WikiRootKey, Module: "notion", Type: settings.String,
			Check: pageID, Class: settings.UI, Scopes: instance,
		},
		text(SensitivityPropertyKey, "sensitivity"),
		text(PrivateValueKey, "private"),
		text(WikiLogTitleKey, "Wiki Log"),
		number(MaxAliasesKey, settings.Int, "20", settings.Between(1, 100)),
		number(MaxSourcesKey, settings.Int, "12", settings.Between(1, 50)),
		number(LabelCharsKey, settings.Int, "48", settings.Between(8, 200)),
		number(LinkPageCharsKey, settings.Int, "2500", settings.Between(100, 50000)),
		number(EditPageCharsKey, settings.Int, "12000", settings.Between(100, 100000)),
		number(LinkMaxTokensKey, settings.Int, "160", settings.Between(16, 4096)),
		number(EditMaxTokensKey, settings.Int, "1500", settings.Between(64, 16384)),
		number(BranchLimitKey, settings.Int, "50", settings.Between(1, 500)),
		number(RelatedLimitKey, settings.Int, "10", settings.Between(1, 50)),
		number(SnippetCharsKey, settings.Int, "220", settings.Between(20, 2000)),
		number(HeadSourceCharsKey, settings.Int, "1000", settings.Between(100, 10000)),
		number(RetryAfterBaseKey, settings.Float, "1", settings.Between(0, 300)),
		number(RetryAfterMaxKey, settings.Float, "60", settings.Between(1, 3600)),
		number(MaxRetriesKey, settings.Int, "5", settings.Between(0, 20)),
		number(TimeoutKey, settings.Int, "60", settings.Between(1, 600)),
		number(PageSizeKey, settings.Int, "100", settings.Between(1, 100)),
		number(BlockDepthKey, settings.Int, "5", settings.Between(0, 20)),
	)
}

func nonBlank(value string) error {
	if py.Strip(value) == "" {
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
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	r.err = err
	return found.Value
}

func (r *resolver) int(key string) int {
	if r.err != nil {
		return 0
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	if err != nil {
		r.err = err
		return 0
	}
	n, err := found.Int()
	r.err = err
	return int(n)
}

func (r *resolver) float(key string) float64 {
	if r.err != nil {
		return 0
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	if err != nil {
		r.err = err
		return 0
	}
	f, err := found.Float()
	r.err = err
	return f
}

type APILimits struct {
	RetryAfterBase float64
	RetryAfterMax  float64
	MaxRetries     int
	Timeout        time.Duration
	PageSize       int
	BlockDepth     int
}

func defaultAPI() APILimits {
	def := func(key string) string {
		d, _ := settings.Default.Lookup(key)
		return d.Default
	}
	asInt := func(key string) int {
		n, _ := py.ParseInt(def(key))
		return int(n)
	}
	asFloat := func(key string) float64 {
		f, _ := py.ParseFloat(def(key))
		return f
	}
	return APILimits{
		RetryAfterBase: asFloat(RetryAfterBaseKey),
		RetryAfterMax:  asFloat(RetryAfterMaxKey),
		MaxRetries:     asInt(MaxRetriesKey),
		Timeout:        time.Duration(asInt(TimeoutKey)) * time.Second,
		PageSize:       asInt(PageSizeKey),
		BlockDepth:     asInt(BlockDepthKey),
	}
}

func LoadAPI(ctx context.Context, q db.Querier) (APILimits, error) {
	r := resolver{ctx: ctx, q: q}
	out := APILimits{
		RetryAfterBase: r.float(RetryAfterBaseKey),
		RetryAfterMax:  r.float(RetryAfterMaxKey),
		MaxRetries:     r.int(MaxRetriesKey),
		Timeout:        time.Duration(r.int(TimeoutKey)) * time.Second,
		PageSize:       r.int(PageSizeKey),
		BlockDepth:     r.int(BlockDepthKey),
	}
	return out, r.err
}

type SearchLimits struct {
	Branch     int
	Related    int
	Snippet    int
	HeadSource int
}

func LoadSearch(ctx context.Context, q db.Querier) (SearchLimits, error) {
	r := resolver{ctx: ctx, q: q}
	out := SearchLimits{
		Branch:     r.int(BranchLimitKey),
		Related:    r.int(RelatedLimitKey),
		Snippet:    r.int(SnippetCharsKey),
		HeadSource: r.int(HeadSourceCharsKey),
	}
	return out, r.err
}

type ProposalLimits struct {
	LinkPageChars int
	EditPageChars int
	LinkMaxTokens int
	EditMaxTokens int
}

func LoadProposals(ctx context.Context, q db.Querier) (ProposalLimits, error) {
	r := resolver{ctx: ctx, q: q}
	out := ProposalLimits{
		LinkPageChars: r.int(LinkPageCharsKey),
		EditPageChars: r.int(EditPageCharsKey),
		LinkMaxTokens: r.int(LinkMaxTokensKey),
		EditMaxTokens: r.int(EditMaxTokensKey),
	}
	return out, r.err
}

type Sensitivity struct {
	Property string
	Private  string
}

func LoadSensitivity(ctx context.Context, q db.Querier) (Sensitivity, error) {
	r := resolver{ctx: ctx, q: q}
	out := Sensitivity{
		Property: py.Lower(py.Strip(r.value(SensitivityPropertyKey))),
		Private:  py.Lower(py.Strip(r.value(PrivateValueKey))),
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
		MaxAliases: r.int(MaxAliasesKey),
		MaxSources: r.int(MaxSourcesKey),
		LabelChars: r.int(LabelCharsKey),
	}
	return out, r.err
}

func (w Wiki) RootID() (string, error) {
	if w.Root == "" {
		return "", WikiError("ATRIUM_NOTION_WIKI_ROOT is not set")
	}
	return w.Root, nil
}
