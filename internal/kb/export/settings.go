package export

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/notionid"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	NotionEnabledKey = "export_notion_enabled"
	NotionParentKey  = "export_notion_parent"
	NotionKindsKey   = "export_notion_kinds"
	NotionPrivateKey = "export_notion_private_to"

	ObsidianDirKey     = "export_obsidian_dir"
	ObsidianKindsKey   = "export_obsidian_kinds"
	ObsidianPrivateKey = "export_obsidian_private_to"

	KindsNone = "none"
)

var kindNames = []string{"pii", "messages", "personal"}

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
		Name: "export", Title: "Knowledge export", Module: "export", Template: "export_settings",
		Fields: []integrations.Field{
			{Key: NotionEnabledKey, Label: "Notion export", Bool: true},
			{Key: NotionParentKey, Label: "Notion destination"},
			{Key: NotionKindsKey, Label: "Notion kinds", Multi: true, Choices: kindNames},
			{Key: NotionPrivateKey, Label: "Notion private records"},
			{Key: ObsidianDirKey, Label: "Obsidian folder"},
			{Key: ObsidianKindsKey, Label: "Obsidian kinds", Multi: true, Choices: kindNames},
			{Key: ObsidianPrivateKey, Label: "Obsidian private records"},
		},
	})
}

func pageID(value string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	id := notionid.Normalize(text)
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
