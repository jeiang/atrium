package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	_ "github.com/jeiang/atrium/internal/builtin"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
)

var derivedHost = map[string]string{
	"calendar.calendar_dir":      "services.atrium.stateDir",
	"contacts.contacts_dir":      "services.atrium.stateDir",
	"core.otel_traces_endpoint":  "services.atrium.observability.tracesEndpoint",
	"db.key":                     "services.atrium.database.keyFile",
	"export.export_obsidian_dir": "services.atrium.knowledgeExport.obsidianDirectory",
	"mail.mail_accounts_file":    "services.atrium.mailAccounts",
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: settingsgen OUTPUT_DIR")
		os.Exit(2)
	}
	if err := generate(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "settingsgen:", err)
		os.Exit(1)
	}
}

func generate(dir string) error {
	defs := settings.New().Defs()
	entries := settings.Manifest()
	byKey := make(map[string]settings.Def, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	for _, d := range defs {
		entry, ok := settings.ImportEntryFor(d.Key)
		if !ok {
			return fmt.Errorf("registered setting %s is not in the import manifest", d.Key)
		}
		if entry.Module != d.Module {
			return fmt.Errorf("registered setting %s is in module %s but the manifest says %s", d.Key, d.Module, entry.Module)
		}
	}
	schema, err := schemaJSON(entries, byKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range map[string]string{
		"atrium.schema.json": schema,
		"environment.md":     environmentDoc(entries, byKey),
		"reference.md":       referenceDoc(defs, entries, byKey),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func schemaType(t settings.Type) any {
	switch t {
	case settings.Int:
		return []string{"integer", "string"}
	case settings.Float:
		return []string{"number", "string"}
	case settings.Bool:
		return []string{"boolean", "string"}
	}
	return "string"
}

func schemaDefault(t settings.Type, value string) any {
	switch t {
	case settings.Int:
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return n
		}
	case settings.Float:
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	case settings.Bool:
		return value == "true"
	}
	return value
}

func schemaProperty(entry settings.ImportEntry) map[string]any {
	prop := map[string]any{"type": schemaType(entry.Type)}
	if len(entry.Choices) > 0 {
		prop["enum"] = entry.Choices
	}
	if entry.Default != "" {
		prop["default"] = schemaDefault(entry.Type, entry.Default)
	}
	return prop
}

func schemaJSON(entries []settings.ImportEntry, byKey map[string]settings.Def) (string, error) {
	modules := map[string]any{}
	classes := map[string]string{}
	destinations := map[string]string{}
	derived := map[string]string{}
	var cutover, secrets []string
	for _, entry := range entries {
		dotted := entry.Module + "." + entry.Key
		classes[dotted] = string(entry.Class)
		if entry.Runtime {
			cutover = append(cutover, dotted)
		}
		if def, ok := byKey[entry.Key]; ok && def.IsSecret() {
			secrets = append(secrets, dotted)
		}
		if target, ok := derivedHost[dotted]; ok {
			derived[dotted] = target
			destinations[dotted] = target
		}
		if entry.Class != settings.ImportHost || entry.Runtime {
			destinations[dotted] = settings.CutoverDestination(entry)
		}
		if entry.Class != settings.ImportHost || entry.Runtime {
			continue
		}
		if _, ok := derivedHost[dotted]; ok {
			continue
		}
		def, registered := byKey[entry.Key]
		secret := registered && def.IsSecret()
		module, _ := modules[entry.Module].(map[string]any)
		if module == nil {
			module = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}
			modules[entry.Module] = module
		}
		props := module["properties"].(map[string]any)
		if !secret {
			props[entry.Key] = schemaProperty(entry)
		}
		props[entry.Key+"_file"] = map[string]any{"type": "string", "description": "path of a file holding the value"}
	}
	modules["connections"] = map[string]any{
		"type":                 "object",
		"additionalProperties": connectionSchema(),
	}
	slices.Sort(cutover)
	slices.Sort(secrets)
	root := map[string]any{
		"$schema":               "https://json-schema.org/draft/2020-12/schema",
		"title":                 "atrium host configuration",
		"type":                  "object",
		"additionalProperties":  false,
		"properties":            modules,
		"x-atrium-classes":      classes,
		"x-atrium-cutover":      cutover,
		"x-atrium-destinations": destinations,
		"x-atrium-derived":      derived,
		"x-atrium-secret":       secrets,
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func connectionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind":             map[string]any{"type": "string"},
			"label":            map[string]any{"type": "string"},
			"config":           map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"transport":        map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"capabilities":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"credential_files": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		},
		"required": []string{"kind"},
	}
}

func envName(module, key string) string {
	return "ATRIUM_" + strings.ToUpper(module) + "_" + strings.ToUpper(key)
}

func environmentDoc(entries []settings.ImportEntry, byKey map[string]settings.Def) string {
	var b strings.Builder
	b.WriteString("# Host environment variables\n\n")
	b.WriteString("Generated by `just settingsgen`. Do not edit.\n\n")
	b.WriteString("The file named by `" + config.HostConfigEnv + "` is TOML with one table per module and an optional `[connections.<id>]` table. An environment variable overrides that file. Host secret inputs accept only a `_FILE` variable naming a file whose trimmed contents are the value. Operator settings are stored in the database and are not set from the environment; a cut-over operator or tuning key is refused with its destination.\n\n")
	b.WriteString("| Variable | Host key |\n| --- | --- |\n")
	for _, entry := range entries {
		if entry.Class != settings.ImportHost || entry.Runtime {
			continue
		}
		dotted := entry.Module + "." + entry.Key
		if _, ok := derivedHost[dotted]; ok {
			continue
		}
		name := envName(entry.Module, entry.Key)
		if def, ok := byKey[entry.Key]; ok && def.IsSecret() {
			name += "_FILE"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` |\n", name, dotted)
	}
	b.WriteString("\n## Bootstrap\n\n| Variable | Meaning |\n| --- | --- |\n")
	for _, row := range [][2]string{
		{"ATRIUM_HOST_CONFIG", "path of the host configuration file"},
		{"ATRIUM_CONFIG", "path of the legacy pin file; cut-over keys are refused"},
		{"ATRIUM_INITIAL_TZ", "time zone stored as the operator `tz` value when a fresh database has none"},
		{"ATRIUM_DB", "path of the database"},
		{"ATRIUM_CACHE_DIR", "path of the message cache"},
		{"ATRIUM_DB_KEY", "database key as 64 hex digits"},
		{"ATRIUM_DB_KEY_FILE", "path of a file holding the database key"},
		{"ATRIUM_PUBLIC_HOST", "the one Host header the public listener accepts"},
	} {
		fmt.Fprintf(&b, "| `%s` | %s |\n", row[0], row[1])
	}
	return b.String()
}

func referenceDoc(defs []settings.Def, entries []settings.ImportEntry, byKey map[string]settings.Def) string {
	var b strings.Builder
	b.WriteString("# Configuration reference\n\n")
	b.WriteString("Generated by `just settingsgen`. Do not edit.\n\n")
	b.WriteString("## Host configuration\n\n")
	b.WriteString("The file named by `" + config.HostConfigEnv + "` is TOML with one table per module and an optional `[connections.<id>]` table. Unknown keys are refused. A host value is read-only in the application: `atrium settings set` and the Settings pages refuse it. Precedence: environment variable, host file, default.\n\n")
	module := ""
	for _, entry := range entries {
		if entry.Class != settings.ImportHost || entry.Runtime {
			continue
		}
		dotted := entry.Module + "." + entry.Key
		if _, ok := derivedHost[dotted]; ok {
			continue
		}
		if entry.Module != module {
			module = entry.Module
			fmt.Fprintf(&b, "\n### %s\n\n| Key | Type | Default | Environment variable |\n| --- | --- | --- | --- |\n", module)
		}
		typ := string(entry.Type)
		if len(entry.Choices) > 0 {
			typ += " (" + strings.Join(entry.Choices, ", ") + ")"
		}
		def := "none"
		if entry.Default != "" {
			def = "`" + entry.Default + "`"
		}
		name := envName(entry.Module, entry.Key)
		if d, ok := byKey[entry.Key]; ok && d.IsSecret() {
			name += "_FILE"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` |\n", entry.Key, typ, def, name)
	}
	b.WriteString("\n## Operator settings\n\n")
	b.WriteString("Operator settings are stored in the database and edited in the application. They are not host configuration and are not set from the environment. A cut-over key is refused with its destination.\n\n")
	module = ""
	for _, d := range defs {
		entry, _ := settings.ImportEntryFor(d.Key)
		if entry.Class != settings.ImportOperator {
			continue
		}
		if d.Module != module {
			module = d.Module
			fmt.Fprintf(&b, "\n### %s\n\n| Key | Type | Default | Class |\n| --- | --- | --- | --- |\n", module)
		}
		typ := string(d.Type)
		if len(d.Choices) > 0 {
			typ += " (" + strings.Join(d.Choices, ", ") + ")"
		}
		def := "none"
		if d.Default != "" {
			def = "`" + d.Default + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", d.Key, typ, def, d.Class())
	}
	return b.String()
}
