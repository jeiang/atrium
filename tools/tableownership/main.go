package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var owners = map[string]string{
	"accounts":                           "mail",
	"action_links":                       "actionlink",
	"agent_events":                       "calendar",
	"agent_profile":                      "kb",
	"attention_snoozes":                  "attention",
	"auth_bearers":                       "auth",
	"auth_logins":                        "auth",
	"auth_sessions":                      "auth",
	"commitments":                        "mail",
	"connection_capabilities":            "connections",
	"connection_credentials":             "connections",
	"connections":                        "connections",
	"context_ledger":                     "assistant",
	"context_projections":                "assistant",
	"corrections":                        "mail",
	"db_key_history":                     "db",
	"decisions":                          "mail",
	"drafts":                             "mail",
	"effect_claims":                      "ledger",
	"effect_import_refs":                 "ledger",
	"effect_resolutions":                 "ledger",
	"effect_resources":                   "ledger",
	"effects":                            "ledger",
	"embed_index":                        "embed",
	"embed_kb_":                          "embed",
	"embed_message_":                     "embed",
	"embed_notion_":                      "embed",
	"endpoint_probes":                    "mail",
	"event_candidates":                   "calendar",
	"events":                             "mail",
	"export_pages":                       "kb",
	"flag_decisions":                     "mail",
	"inference_classification_deferrals": "inference",
	"job_runs":                           "assistant",
	"jobs":                               "assistant",
	"kb_fts":                             "kb",
	"kb_records":                         "kb",
	"live_baseline":                      "mail",
	"live_state":                         "mail",
	"loop_overrides":                     "mail",
	"mail_accounts":                      "config",
	"mail_scans":                         "mail",
	"mailboxes":                          "mail",
	"media_channels":                     "media",
	"media_seen":                         "media",
	"media_state":                        "media",
	"memory_fts":                         "assistant",
	"memory_jobs":                        "assistant",
	"memory_nodes":                       "assistant",
	"memory_state":                       "assistant",
	"memory_views":                       "assistant",
	"message_fts":                        "mail",
	"messages":                           "mail",
	"nightly_observations":               "kb",
	"notifications":                      "notify",
	"notion_fts":                         "notion",
	"notion_pages":                       "notion",
	"notion_sources":                     "notion",
	"notion_state":                       "notion",
	"ntfy_channels":                      "notify",
	"oauth_states":                       "connections",
	"open_loops":                         "mail",
	"ops_findings":                       "serverops",
	"ops_samples":                        "serverops",
	"package_events":                     "mail",
	"packages":                           "mail",
	"pass_run_alerts":                    "notify",
	"pass_runs":                          "passrun",
	"projects":                           "assistant",
	"proposals":                          "proposals",
	"push_subscriptions":                 "notify",
	"rebalance_candidates":               "mail",
	"rules":                              "mail",
	"run_events":                         "assistant",
	"runs":                               "assistant",
	"schema_migrations":                  "db",
	"secrets":                            "settings",
	"settings":                           "settings",
	"settings_audit":                     "settings",
	"skills":                             "assistant",
	"snoozes":                            "mail",
	"subagents":                          "assistant",
	"tasks":                              "tasks",
	"thread_files":                       "assistant",
	"thread_fts":                         "assistant",
	"thread_messages":                    "assistant",
	"thread_plans":                       "assistant",
	"thread_queue":                       "assistant",
	"threads":                            "assistant",
	"tool_calls":                         "assistant",
	"tool_results":                       "assistant",
	"usage":                              "inference",
	"watch_dismissed":                    "mail",
	"wiki_pages":                         "notion",
}

var vectorPrefixes = []string{"embed_message_", "embed_notion_", "embed_kb_"}

var (
	createTable = regexp.MustCompile(`CREATE\s+(?:VIRTUAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)
	sqlRef      = regexp.MustCompile(`\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM|FROM|JOIN)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	vectorCall  = regexp.MustCompile(`\.(?:MessageTable|NotionTable|KBTable)\(\)`)
)

type violation struct {
	path string
	line int
	text string
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if err := checkSchema(root); err != nil {
		fail(err)
	}
	found, err := scan(root)
	if err != nil {
		fail(err)
	}
	if len(found) == 0 {
		return
	}
	for _, v := range found {
		fmt.Fprintf(os.Stderr, "%s:%d: %s\n", v.path, v.line, strings.TrimSpace(v.text))
	}
	fmt.Fprintf(os.Stderr, "tableownership: %d cross-module SQL sites\n", len(found))
	os.Exit(1)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tableownership:", err)
	os.Exit(1)
}

func checkSchema(root string) error {
	names := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(root, "internal", "db", "schema", "*.sql"))
	if err != nil {
		return err
	}
	files = append(files, filepath.Join(root, "internal", "db", "migrate.go"))
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range createTable.FindAllStringSubmatch(string(body), -1) {
			names[normalize(m[1])] = true
		}
	}
	var missing []string
	for name := range names {
		if _, ok := owners[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("tables created in the schema without an owner: %s", strings.Join(missing, ", "))
	}
	return nil
}

func normalize(table string) string {
	for _, prefix := range vectorPrefixes {
		if strings.HasPrefix(table, prefix) {
			return prefix
		}
	}
	return table
}

func scan(root string) ([]violation, error) {
	var found []violation
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "testdata", "vendor", ".worktrees":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/db/") || strings.HasPrefix(rel, "tools/") {
				return nil
			}
			module := moduleOf(rel)
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(body), "\n") {
				found = append(found, inspect(rel, module, i+1, line)...)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].path != found[j].path {
			return found[i].path < found[j].path
		}
		return found[i].line < found[j].line
	})
	return found, nil
}

func moduleOf(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 1 && parts[0] == "internal" {
		return parts[1]
	}
	if parts[0] == "cmd" {
		return "cmd"
	}
	return ""
}

func inspect(path, module string, line int, text string) []violation {
	var out []violation
	seen := map[string]bool{}
	for _, m := range sqlRef.FindAllStringSubmatch(text, -1) {
		table := normalize(m[1])
		owner, ok := owners[table]
		if !ok || owner == module || seen[table] {
			continue
		}
		seen[table] = true
		out = append(out, violation{path: path, line: line, text: text})
	}
	if module != "embed" {
		for _, prefix := range vectorPrefixes {
			if strings.Contains(text, prefix) && !seen[prefix] {
				seen[prefix] = true
				out = append(out, violation{path: path, line: line, text: text})
			}
		}
		if vectorCall.MatchString(text) {
			out = append(out, violation{path: path, line: line, text: text})
		}
	}
	return out
}
