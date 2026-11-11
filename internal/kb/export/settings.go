package export

import (
	"context"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
)

const (
	NotionEnabledKey = "export_notion_enabled"
	NotionParentKey  = "export_notion_parent"
	NotionKindsKey   = "export_notion_kinds"
	NotionPrivateKey = "export_notion_private_to"

	ObsidianDirKey     = "export_obsidian_dir"
	ObsidianKindsKey   = "export_obsidian_kinds"
	ObsidianPrivateKey = "export_obsidian_private_to"

	NotionSection   = "export_notion"
	ObsidianSection = "export_obsidian"

	KindsNone = "none"
)

var kindNames = []string{"pii", "messages", "personal"}

var kindLabels = map[string]string{
	"pii":      "Contact details",
	"messages": "Mail and messages",
	"personal": "Personal",
}

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{
			Key: NotionEnabledKey, Module: "export", Type: settings.Bool, Default: "false"}),
		settings.Value(settings.UI, settings.Def{
			Key: NotionParentKey, Module: "export", Type: settings.String, Check: pageID}),
		settings.Value(settings.UI, settings.Def{
			Key: NotionKindsKey, Module: "export", Type: settings.String, Default: strings.Join(kindNames, ","), Check: kinds}),
		settings.Value(settings.UI, settings.Def{
			Key: NotionPrivateKey, Module: "export", Type: settings.String, Check: pageID}),
		settings.Value(settings.UI, settings.Def{
			Key: ObsidianDirKey, Module: "export", Type: settings.String, Check: dirPath}),
		settings.Value(settings.UI, settings.Def{
			Key: ObsidianKindsKey, Module: "export", Type: settings.String, Default: strings.Join(kindNames, ","), Check: kinds}),
		settings.Value(settings.UI, settings.Def{
			Key: ObsidianPrivateKey, Module: "export", Type: settings.String, Check: dirPath}),
	)
	integrations.RegisterSection(integrations.Section{
		Name: NotionSection, Title: "Export knowledge to Notion", Module: "export", Template: "export_notion_settings",
		Fields: []integrations.Field{
			{Key: NotionEnabledKey, Label: "Enabled", Bool: true},
			{Key: NotionParentKey, Label: "Parent page"},
			{Key: NotionKindsKey, Label: "Include", Multi: true, Choices: kindNames, ChoiceLabels: kindLabels},
			{Key: NotionPrivateKey, Label: "Include private records"},
		},
		Validate: validateNotion,
		Data:     notionData,
	})
	integrations.RegisterSection(integrations.Section{
		Name: ObsidianSection, Title: "Obsidian export", Module: "export", Template: "export_obsidian_settings",
		Fields: []integrations.Field{
			{Key: ObsidianDirKey, Label: "Folder"},
			{Key: ObsidianKindsKey, Label: "Include", Multi: true, Choices: kindNames, ChoiceLabels: kindLabels},
			{Key: ObsidianPrivateKey, Label: "Include private records"},
		},
		Validate: validateObsidian,
		Data:     obsidianData,
	})
}

type notionStatus struct {
	Last    float64
	Blocked int64
	PageURL string
}

type obsidianStatus struct {
	Last float64
}

func notionData(ctx context.Context, q db.Querier) (any, error) {
	var last float64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated_at), 0) FROM export_pages WHERE target = ?", TargetNotion).Scan(&last); err != nil {
		return nil, err
	}
	blocked, err := writes.UnknownTarget(ctx, q, targetPrefix)
	if err != nil {
		return nil, err
	}
	found, err := settings.From(ctx).Resolve(ctx, q, NotionParentKey)
	if err != nil {
		return nil, err
	}
	id, _ := pageInput(found.Value)
	return notionStatus{Last: last, Blocked: blocked, PageURL: notionURL(id)}, nil
}

func obsidianData(ctx context.Context, q db.Querier) (any, error) {
	var last float64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated_at), 0) FROM export_pages WHERE target = ?", TargetObsidian).Scan(&last); err != nil {
		return nil, err
	}
	return obsidianStatus{Last: last}, nil
}

func notionURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.notion.so/" + strings.ReplaceAll(id, "-", "")
}

func validateNotion(ctx context.Context, q db.Querier, values map[string]string) error {
	parent, _, err := effective(ctx, q, values, NotionParentKey)
	if err != nil {
		return err
	}
	parent, err = pageInput(parent)
	if err != nil {
		return err
	}
	if _, ok := values[NotionParentKey]; ok {
		values[NotionParentKey] = parent
	}
	enabled, _, err := effective(ctx, q, values, NotionEnabledKey)
	if err != nil {
		return err
	}
	if enabled == "true" && parent == "" {
		return settings.FieldInvalid{Key: NotionParentKey, Err: settings.Invalid("set a parent page before turning the export on")}
	}
	private, pinned, err := effective(ctx, q, values, NotionPrivateKey)
	if err != nil {
		return err
	}
	private, err = pageInput(private)
	if err != nil {
		return err
	}
	if _, ok := values[NotionPrivateKey]; ok {
		if parent != "" && private == parent {
			values[NotionPrivateKey] = parent
		} else {
			values[NotionPrivateKey] = ""
		}
	} else if !pinned && private != "" && private != parent {
		values[NotionPrivateKey] = ""
	}
	return nil
}

func validateObsidian(ctx context.Context, q db.Querier, values map[string]string) error {
	dir, _, err := effective(ctx, q, values, ObsidianDirKey)
	if err != nil {
		return err
	}
	dir = cleanDir(dir)
	if _, ok := values[ObsidianDirKey]; ok {
		values[ObsidianDirKey] = dir
	}
	private, pinned, err := effective(ctx, q, values, ObsidianPrivateKey)
	if err != nil {
		return err
	}
	private = cleanDir(private)
	if _, ok := values[ObsidianPrivateKey]; ok {
		if dir != "" && private == dir {
			values[ObsidianPrivateKey] = dir
		} else {
			values[ObsidianPrivateKey] = ""
		}
	} else if !pinned && private != "" && private != dir {
		values[ObsidianPrivateKey] = ""
	}
	return nil
}

func effective(ctx context.Context, q db.Querier, values map[string]string, key string) (string, bool, error) {
	if value, ok := values[key]; ok {
		return value, false, nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(found.Value), found.Pinned(), nil
}

func pageInput(value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", nil
	}
	if id := notionid.Normalize(text); validPageID(id) {
		return id, nil
	}
	if u, err := url.Parse(text); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if ids := notionid.IDsInText(u.Path); len(ids) > 0 {
			return ids[len(ids)-1], nil
		}
	}
	return "", settings.Invalid("must be a Notion page URL or id")
}

func validPageID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		dash := i == 8 || i == 13 || i == 18 || i == 23
		hex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
		if dash == hex {
			return false
		}
	}
	return true
}

func pageID(value string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	if !validPageID(notionid.Normalize(text)) {
		return settings.Invalid("must be a Notion page id: 32 hexadecimal digits, dashes optional")
	}
	return nil
}

func cleanDir(value string) string {
	text := strings.TrimSpace(value)
	if text == "" {
		return ""
	}
	return filepath.Clean(text)
}

func dirPath(value string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	if !filepath.IsAbs(text) {
		return settings.Invalid("must be an absolute path")
	}
	return nil
}

func kinds(value string) error {
	text := strings.TrimSpace(value)
	if text == "" || text == KindsNone {
		return nil
	}
	seen := make(map[string]bool, len(kindNames))
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if !slices.Contains(kindNames, part) {
			return settings.Invalid("must be a comma list of pii, messages, personal, or none")
		}
		if seen[part] {
			return settings.Invalid("must not repeat a kind")
		}
		seen[part] = true
	}
	return nil
}

func mask(value string) tool.Marks {
	var out tool.Marks
	for _, part := range strings.Split(value, ",") {
		switch strings.TrimSpace(part) {
		case "pii":
			out |= tool.PII
		case "messages":
			out |= tool.Messages
		case "personal":
			out |= tool.Personal
		}
	}
	return out
}
