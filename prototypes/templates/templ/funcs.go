package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"prototype/templates/shared"
)

// ---- tiny helpers templ needs because { expr } only takes strings ----

func itoa(n int) string { return strconv.Itoa(n) }

func f1(f float64) string { return fmt.Sprintf("%.1f", f) }
func f3(f float64) string { return fmt.Sprintf("%.3f", f) }
func f4(f float64) string { return fmt.Sprintf("%.4f", f) }
func f0(f float64) string { return fmt.Sprintf("%.0f", f) }

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func cond[T any](b bool, yes, no T) T {
	if b {
		return yes
	}
	return no
}

func plural(n int, one, many string) string { return cond(n == 1, one, many) }

func ts(epoch float64, style string) string { return shared.TS(epoch, style) }

func trunc(s string, n int) string { return shared.Truncate(s, n) }

func enc(s string) string { return shared.URLEncode(s) }

// Txt wraps an escaped plain string as a component (for MenuItem labels, which are trusted HTML slots).
func Txt(s string) templ.Component { return templ.Raw(html.EscapeString(s)) }

// md is the ONE place markdown output is marked trusted. shared.Markdown sanitises with bluemonday.
func md(s string) templ.Component { return templ.Raw(shared.Markdown(s)) }

// iconSVG is trusted: it comes from the compile-time icons map, never from user data.
func iconPaths(name string) templ.Component { return templ.Raw(icons[name]) }

// ---- label tables (Jinja dict literals) ----

type kv struct{ K, V string }

// Python dicts keep insertion order and `.items()` iterates in it; Go maps do not, so ordered tables are slices.
var kindLabels = []kv{
	{"rebalance_split", "Split folder"},
	{"rebalance_merge", "Merge folders"},
	{"deletion_rule", "Deletion rule"},
	{"gmail_label_merge", "Merge labels"},
	{"inbox_backlog", "Inbox backlog"},
	{"notion_link", "Notion link"},
	{"notion_edit", "Notion edit"},
	{"calendar_event", "Calendar event"},
	{"reminder", "Reminder"},
	{"notion_wiki_plan", "Wiki plan"},
}

var kindIcons = map[string]string{
	"rebalance_split":   "split",
	"rebalance_merge":   "merge",
	"deletion_rule":     "trash-2",
	"gmail_label_merge": "tag",
	"inbox_backlog":     "inbox",
	"notion_link":       "link",
	"notion_edit":       "pencil",
	"calendar_event":    "calendar",
	"reminder":          "scroll-text",
	"notion_wiki_plan":  "book-open",
}

var statusLabels = map[string]string{
	"pending":    "Pending",
	"approved":   "Applying",
	"applied":    "Applied",
	"failed":     "Failed",
	"dismissed":  "Dismissed",
	"suppressed": "Dismissed forever",
}

var fieldLabels = map[string]string{
	"from_addr": "From address", "from_domain": "From domain", "from_name": "From name",
	"reply_to": "Reply-To", "rpath_domain": "Return-Path domain", "list_id": "List-Id",
	"subject": "Subject", "subject_tmpl": "Subject template", "msgid_domain": "Message-ID domain",
	"precedence": "Precedence", "category": "Category", "to": "To", "dkim_d": "DKIM domain",
	"esp": "Sending service", "att_type": "Attachment type", "att_name": "Attachment name",
	"age_days": "Age in days",
}

var opLabels = map[string]string{
	"eq": "is", "in": "is one of", "suffix": "is or ends in domain", "re": "matches regex",
	"present": "is present", "absent": "is absent", "gte": "is at least",
}

var decisionLabels = map[string]string{"convert": "Convert", "merge": "Merge", "drop": "Drop", "keep": "Keep"}

var deletionReasons = map[string]string{
	"operator_deleted": "You have deleted mail like this before.",
	"otp":              "One-time codes are useless once they expire.",
}

// get mirrors `DICT.get(key, default)`.
func get(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func kindLabel(k string) string {
	for _, e := range kindLabels {
		if e.K == k {
			return e.V
		}
	}
	return k
}

func kindIcon(k string) string { return get(kindIcons, k, "list-checks") }

// sortedCounts mirrors `dict.items()`; Python keeps insertion order, Go maps need an explicit order.
// The fake data is alphabetical already; a real port needs an ordered type (or a slice) in the data model.
func sortedCounts(m map[string]int) []struct {
	K string
	N int
} {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]struct {
		K string
		N int
	}, 0, len(keys))
	for _, k := range keys {
		out = append(out, struct {
			K string
			N int
		}{k, m[k]})
	}
	return out
}

// ---- macros that returned plain text ----

func modeNote(mode string, paused bool) string {
	switch {
	case mode == "live" && paused:
		return "Paused: routing is recorded but no mail is changed. Resume to apply it."
	case mode == "live":
		return "Live mode: atrium files read mail, flags unread mail from your contacts and trashes mail your deletion rules match."
	default:
		return "Shadow mode: nothing is changed automatically. Proposals you approve still run."
	}
}

func noun(account string) string { return cond(account == "gmail", "label", "folder") }

func place(loc string) string {
	switch loc {
	case "INBOX":
		return "Inbox"
	}
	return strings.ReplaceAll(loc, "|", " · ")
}

func sender(m Message) string { return or(m.FromName, or(m.FromAddr, "Unknown sender")) }

func pagePath(p []PathCrumb) string {
	parts := make([]string, len(p))
	for i, c := range p {
		parts[i] = c.Title
	}
	return strings.Join(parts, " / ")
}

// conditionValue mirrors `value | join(", ") if value is iterable and value is not string else value`.
func conditionValue(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, x != ""
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = fmt.Sprint(e)
		}
		return strings.Join(parts, ", "), true
	}
	return fmt.Sprint(v), true
}

// ---- thread / proposal view logic that Jinja did with `{% set %}` ----

func (v View) Busy() bool { return v.Active != nil && v.Active.State == "running" }

func (v View) LastRunID() int {
	if len(v.Runs) == 0 {
		return 0
	}
	return v.Runs[len(v.Runs)-1].ID
}

func (v View) AfterSeq() int {
	if len(v.Items) == 0 {
		return 0
	}
	return v.Items[len(v.Items)-1].Seq
}

func filterGroups(gs []Group) []Group {
	var out []Group
	for _, g := range gs {
		if len(g.Messages) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// picks mirrors the PICKS dict + `| selectattr("messages") | list` in _proposal.html.
func (p Proposal) Picks() []Group {
	v := p.Preview
	var gs []Group
	switch p.Kind {
	case "rebalance_split", "inbox_backlog":
		gs = v.Groups
	case "rebalance_merge":
		if v.Group != nil {
			gs = []Group{*v.Group}
		}
	case "deletion_rule":
		gs = []Group{{Messages: v.Messages, Count: v.Backlog}}
	case "gmail_label_merge":
		gs = []Group{{Messages: v.Messages, Count: v.FromCount}}
	}
	return filterGroups(gs)
}

func countDiff(d []DiffLine, op string) int {
	n := 0
	for _, l := range d {
		if l.Op == op {
			n++
		}
	}
	return n
}

func pickedTotal(gs []Group) int {
	n := 0
	for _, g := range gs {
		n += len(g.Messages)
	}
	return n
}

func btnVariant(b bool, yes, no string) string { return cond(b, yes, no) }

const assetVersion = "proto"

type ctxKey struct{}

// csrf reads the request-scoped token from the context (the Jinja global csrf_token()).
func csrf(ctx context.Context) string {
	s, _ := ctx.Value(ctxKey{}).(string)
	return s
}

func hasPrefix(s, p string) bool { return strings.HasPrefix(s, p) }

func fileNames(fs []File) string {
	n := make([]string, len(fs))
	for i, f := range fs {
		n[i] = f.Name
	}
	return strings.Join(n, ", ")
}

func projName(p *Project) string {
	if p == nil {
		return "Project"
	}
	return p.Name
}

func roundInt(f float64) int { return shared.Round(f) }

func fileSize(n int64) string { return shared.FileSizeFormat(n) }

func sourceIcon(kind string) string {
	switch kind {
	case "mail":
		return "mail"
	case "notion":
		return "book-user"
	}
	return "globe"
}

// PC is the shared context the macros inside _proposal.html close over in Jinja (p, v, picks, picker, pending).
type PC struct {
	P       Proposal
	V       Preview
	Pending bool
	Picks   []Group
	Picker  bool
}

func newPC(p Proposal) PC {
	picks := p.Picks()
	pending := p.Status == "pending"
	return PC{P: p, V: p.Preview, Pending: pending, Picks: picks, Picker: pending && len(picks) > 0}
}

func proposalCount(n int, status, kind string) string {
	s := itoa(n) + " "
	if status != "all" {
		s += strings.ToLower(get(statusLabels, status, "")) + " "
	}
	s += plural(n, "proposal", "proposals")
	if kind != "" {
		s += " · " + kindLabel(kind)
	}
	return s
}

func countSuffix(counts map[string]int, v string) string {
	if n := counts[v]; n != 0 {
		return " (" + itoa(n) + ")"
	}
	return ""
}

func resultSummary(p Proposal) string {
	if p.Result != nil && p.Result.Summary != "" {
		return p.Result.Summary
	}
	return "Applied"
}

// hxHeaders formats like Jinja's tojson (", " / ": " separators) so the output diffs cleanly; templ.JSONString is compact.
func hxHeaders(csrf string) string {
	k, _ := json.Marshal("X-CSRF-Token")
	v, _ := json.Marshal(csrf)
	return "{" + string(k) + ": " + string(v) + "}"
}
