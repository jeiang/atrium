package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	"commands":                           "application",
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
	"event_candidates":                   "calendar",
	"events":                             "mail",
	"export_pages":                       "kb",
	"flag_decisions":                     "mail",
	"inference_classification_deferrals": "inference",
	"job_resource_leases":                "scheduler",
	"job_run_threads":                    "scheduler",
	"job_runs":                           "scheduler",
	"jobs":                               "scheduler",
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
	"operation_audit":                    "application",
	"ops_findings":                       "serverops",
	"ops_samples":                        "serverops",
	"package_events":                     "mail",
	"packages":                           "mail",
	"pass_run_alerts":                    "notify",
	"pass_runs":                          "passrun",
	"projects":                           "assistant",
	"proposals":                          "proposals",
	"push_subscriptions":                 "notify",
	"read_revisions":                     "application",
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

var invalidates = map[string]string{
	"accounts":                           "mail",
	"action_links":                       "notify",
	"agent_events":                       "calendar",
	"agent_profile":                      "knowledge",
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
	"decisions":                          "mail",
	"drafts":                             "mail",
	"effect_import_refs":                 "effects",
	"effect_resolutions":                 "effects",
	"effect_resources":                   "effects",
	"effects":                            "effects",
	"event_candidates":                   "calendar",
	"events":                             "mail",
	"export_pages":                       "knowledge",
	"flag_decisions":                     "mail",
	"inference_classification_deferrals": "assistant",
	"job_run_threads":                    "jobs",
	"job_runs":                           "jobs",
	"jobs":                               "jobs",
	"kb_records":                         "knowledge",
	"live_baseline":                      "mail",
	"live_state":                         "mail",
	"loop_overrides":                     "mail",
	"mail_accounts":                      "mail",
	"mail_scans":                         "mail",
	"mailboxes":                          "mail",
	"media_channels":                     "media",
	"media_seen":                         "media",
	"media_state":                        "media",
	"memory_jobs":                        "assistant",
	"memory_nodes":                       "assistant",
	"memory_state":                       "assistant",
	"memory_views":                       "assistant",
	"messages":                           "mail",
	"nightly_observations":               "assistant",
	"notifications":                      "notify",
	"notion_pages":                       "notion",
	"notion_sources":                     "notion",
	"notion_state":                       "notion",
	"ntfy_channels":                      "notify",
	"oauth_states":                       "connections",
	"open_loops":                         "mail",
	"ops_findings":                       "ops",
	"ops_samples":                        "ops",
	"package_events":                     "mail",
	"packages":                           "mail",
	"pass_run_alerts":                    "notify",
	"pass_runs":                          "ops",
	"projects":                           "assistant",
	"proposals":                          "review",
	"push_subscriptions":                 "notify",
	"rebalance_candidates":               "mail",
	"rules":                              "mail",
	"runs":                               "assistant",
	"secrets":                            "settings",
	"settings":                           "settings",
	"settings_audit":                     "settings",
	"snoozes":                            "mail",
	"subagents":                          "assistant",
	"tasks":                              "tasks",
	"thread_files":                       "assistant",
	"thread_messages":                    "assistant",
	"thread_plans":                       "assistant",
	"thread_queue":                       "assistant",
	"threads":                            "assistant",
	"tool_calls":                         "assistant",
	"tool_results":                       "assistant",
	"usage":                              "assistant",
	"watch_dismissed":                    "mail",
	"wiki_pages":                         "notion",
}

var revisionExempt = map[string]string{
	"db_key_history":      "database key lineage, read by no application operation",
	"effect_claims":       "runtime bookkeeping",
	"embed_index":         "derived index rows",
	"embed_kb_":           "vector index",
	"embed_message_":      "vector index",
	"embed_notion_":       "vector index",
	"job_resource_leases": "runtime bookkeeping",
	"kb_fts":              "full-text index",
	"memory_fts":          "full-text index",
	"message_fts":         "full-text index",
	"notion_fts":          "full-text index",
	"operation_audit":     "the operation audit itself",
	"commands":            "API command bookkeeping; commands.status derives its revision from the row",
	"read_revisions":      "the revision counters themselves",
	"run_events":          "run state changes bump through runs",
	"schema_migrations":   "runtime bookkeeping",
	"skills":              "read by no application operation",
	"thread_fts":          "full-text index",
}

var revisionColumnExempt = map[string][]string{
	"effects":     {"lease_at"},
	"job_runs":    {"heartbeat_at", "owner"},
	"jobs":        {"lease_owner", "lease_until"},
	"memory_jobs": {"lease_until"},
	"runs":        {"owner", "lease_until"},
}

var revisionWhen = map[string]string{}

var (
	readTrigger    = regexp.MustCompile(`CREATE\s+TRIGGER\s+IF\s+NOT\s+EXISTS\s+([A-Za-z0-9_]+)_read_(insert|update|delete)\s+AFTER\s+(INSERT|UPDATE|DELETE)(?:\s+OF\s+([A-Za-z0-9_,\s]+?))?\s+ON\s+([A-Za-z0-9_]+)(?:\s+WHEN\s+([^;]*?))?\s+BEGIN\s+INSERT\s+INTO\s+read_revisions\(scope,value\)\s+VALUES\('([a-z]+)',1\)`)
	anyReadTrigger = regexp.MustCompile(`CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_]+_read_(?:insert|update|delete))\b`)
	tableBody      = regexp.MustCompile(`CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

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
	var schema strings.Builder
	for _, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		schema.Write(body)
		for _, m := range createTable.FindAllStringSubmatch(string(body), -1) {
			names[normalize(m[1])] = true
		}
	}
	if err := checkRevisions(names, schema.String()); err != nil {
		return err
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

func checkRevisions(names map[string]bool, schema string) error {
	var problems []string
	for name := range names {
		_, covered := invalidates[name]
		_, exempt := revisionExempt[name]
		if covered == exempt {
			problems = append(problems, name+" needs exactly one of a revision scope or a revision exemption")
		}
	}
	for name := range invalidates {
		if !names[name] {
			problems = append(problems, name+" has a revision scope but no table")
		}
	}
	for name := range revisionExempt {
		if !names[name] {
			problems = append(problems, name+" has a revision exemption but no table")
		}
	}
	columns := tableColumns(schema)
	for name, excluded := range revisionColumnExempt {
		if _, ok := columns[name]; !ok {
			problems = append(problems, name+" has a revision column exemption but no table")
			continue
		}
		for _, column := range excluded {
			if !slices.Contains(columns[name], column) {
				problems = append(problems, fmt.Sprintf("%s has a revision column exemption for %s, which it does not have", name, column))
			}
		}
	}
	found := map[string]bool{}
	shaped := map[string]bool{}
	for _, m := range readTrigger.FindAllStringSubmatch(schema, -1) {
		table, op, event, of, on, when, scope := m[1], m[2], m[3], m[4], m[5], m[6], m[7]
		trigger := table + "_read_" + op
		shaped[trigger] = true
		if table != on || strings.ToUpper(op) != event || invalidates[table] != scope || found[table+"/"+op] {
			problems = append(problems, fmt.Sprintf("trigger %s on %s writes scope %q; want one per operation on its own table with scope %q", trigger, on, scope, invalidates[table]))
			continue
		}
		found[table+"/"+op] = true
		if when != "" && strings.Join(strings.Fields(when), " ") != revisionWhen[trigger] {
			problems = append(problems, fmt.Sprintf("trigger %s has WHEN %s, which is not allowlisted", trigger, strings.Join(strings.Fields(when), " ")))
		}
		if of != "" && event != "UPDATE" {
			problems = append(problems, fmt.Sprintf("trigger %s lists columns on %s", trigger, event))
		}
		if event != "UPDATE" {
			continue
		}
		excluded, partial := revisionColumnExempt[table]
		switch {
		case of == "" && partial:
			problems = append(problems, fmt.Sprintf("trigger %s must list every %s column except %s", trigger, table, strings.Join(excluded, ", ")))
		case of != "" && !partial:
			problems = append(problems, fmt.Sprintf("trigger %s lists columns but %s has no revision column exemption", trigger, table))
		case of != "":
			var want []string
			for _, column := range columns[table] {
				if !slices.Contains(excluded, column) {
					want = append(want, column)
				}
			}
			var got []string
			for _, column := range strings.Split(of, ",") {
				got = append(got, strings.TrimSpace(column))
			}
			sort.Strings(want)
			sort.Strings(got)
			if !slices.Equal(got, want) {
				problems = append(problems, fmt.Sprintf("trigger %s lists %s; want %s", trigger, strings.Join(got, ", "), strings.Join(want, ", ")))
			}
		}
	}
	for _, m := range anyReadTrigger.FindAllStringSubmatch(schema, -1) {
		if !shaped[m[1]] {
			problems = append(problems, "trigger "+m[1]+" does not have the revision trigger shape")
		}
	}
	for trigger := range revisionWhen {
		if !shaped[trigger] {
			problems = append(problems, trigger+" has an allowlisted WHEN but no trigger")
		}
	}
	for name := range invalidates {
		for _, op := range []string{"insert", "update", "delete"} {
			if !found[name+"/"+op] {
				problems = append(problems, "missing revision trigger "+name+"_read_"+op)
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("revision triggers: %s", strings.Join(problems, "; "))
	}
	return nil
}

func tableColumns(schema string) map[string][]string {
	out := map[string][]string{}
	for _, loc := range tableBody.FindAllStringSubmatchIndex(schema, -1) {
		name := schema[loc[2]:loc[3]]
		depth, start := 1, loc[1]
		var items []string
		for i := start; i < len(schema) && depth > 0; i++ {
			switch schema[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					items = append(items, schema[start:i])
				}
			case ',':
				if depth == 1 {
					items = append(items, schema[start:i])
					start = i + 1
				}
			}
		}
		for _, item := range items {
			fields := strings.Fields(item)
			if len(fields) == 0 {
				continue
			}
			keyword, _, _ := strings.Cut(fields[0], "(")
			switch strings.ToUpper(keyword) {
			case "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "CONSTRAINT":
				continue
			}
			out[name] = append(out[name], strings.Trim(fields[0], `"`+"`"))
		}
	}
	return out
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
