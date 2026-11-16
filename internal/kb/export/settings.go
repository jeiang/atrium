package export

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settingspage"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ObsidianDirKey     = "export_obsidian_dir"
	ObsidianKindsKey   = "export_obsidian_kinds"
	ObsidianPrivateKey = "export_obsidian_private_to"

	ObsidianSection = "export_obsidian"

	KindsNone = "none"
)

var kindNames = []string{"pii", "messages", "personal"}

var kindLabels = map[string]string{
	"pii":      "Contact details",
	"messages": "Mail and messages",
	"personal": "Personal",
}

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: ObsidianDirKey, Module: "export", Type: settings.String, Check: dirPath},
	}
}

func Settings() []settings.Def {
	return []settings.Def{
		settings.Value(settings.UI, settings.Def{
			Key: ObsidianKindsKey, Module: "export", Type: settings.String, Default: strings.Join(kindNames, ","), Check: kinds}),
		settings.Value(settings.UI, settings.Def{
			Key: ObsidianPrivateKey, Module: "export", Type: settings.String, Check: dirPath}),
	}
}

func Sections() []settingspage.Section {
	return []settingspage.Section{
		{
			Name: ObsidianSection, Title: "Markdown export", Module: "export", Template: "export_obsidian_settings",
			Fields: []settingspage.Field{
				{Key: ObsidianKindsKey, Label: "Include", Multi: true, Choices: kindNames, ChoiceLabels: kindLabels},
				{Key: ObsidianPrivateKey, Label: "Include private records"},
			},
			Validate: validateObsidian,
			Data:     obsidianData,
		},
	}
}

type obsidianStatus struct {
	Last   float64
	Folder string
}

func obsidianData(ctx context.Context, q db.Querier) (any, error) {
	var last float64
	if err := q.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated_at), 0) FROM export_pages WHERE target = ?", TargetObsidian).Scan(&last); err != nil {
		return nil, err
	}
	return obsidianStatus{Last: last, Folder: strings.TrimSpace(config.From(ctx).HostValue(ObsidianDirKey))}, nil
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
	if _, hosted := settings.From(ctx).HostDefOf(key); hosted {
		return strings.TrimSpace(config.From(ctx).HostValue(key)), true, nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(found.Value), found.Pinned(), nil
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
