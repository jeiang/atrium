package run_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/mailcmd"
	"github.com/jeiang/atrium/internal/mail/mailtest"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/testutil/golden"
	"github.com/jeiang/atrium/internal/testutil/llmstub"
)

type scenarioStep struct {
	Op      string   `json:"op"`
	At      string   `json:"at"`
	Mode    string   `json:"mode"`
	Account string   `json:"account"`
	Key     string   `json:"key"`
	Keys    []string `json:"keys"`
	Src     string   `json:"src"`
	Dest    string   `json:"dest"`
	Box     string   `json:"box"`
	Flags   []string `json:"flags"`
	Labels  []string `json:"labels"`
	Folder  string   `json:"folder"`
	Kind    string   `json:"kind"`
	Forever bool     `json:"forever"`
}

type pipelineScenario struct {
	Clocks      map[string]float64 `json:"clocks"`
	Steps       []scenarioStep     `json:"steps"`
	Rules       json.RawMessage    `json:"rules"`
	Environment map[string]string  `json:"environment"`
	CBTTHTML    string             `json:"cbtt_html"`
	Money       []struct {
		Bank           string   `json:"bank"`
		Name           string   `json:"name"`
		Kind           string   `json:"kind"`
		Currency       string   `json:"currency"`
		Last4          string   `json:"last4"`
		SenderPatterns []string `json:"sender_patterns"`
		Forex          bool     `json:"forex"`
	} `json:"money_accounts"`
}

type seedMessage struct {
	Key    string   `json:"key"`
	Flags  []string `json:"flags"`
	Labels []string `json:"labels"`
	Header string   `json:"header"`
	Text   string   `json:"text"`
}

type seedAccount struct {
	Mailboxes  map[string][]seedMessage `json:"mailboxes"`
	SpecialUse map[string][]string      `json:"special_use"`
	Validity   uint32                   `json:"uidvalidity"`
}

type arrival struct {
	seedMessage
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
}

type seeds struct {
	Accounts    map[string]seedAccount `json:"accounts"`
	Categories  map[string][]string    `json:"gmail_categories"`
	Arrivals    []arrival              `json:"arrivals"`
	ContactsVCF string                 `json:"contacts_vcf"`
}

func recordedEvents(t *testing.T) func(feature string, body []byte) bool {
	t.Helper()
	var recorded []struct {
		Feature string          `json:"feature"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_exchanges.json"), &recorded); err != nil {
		t.Fatal(err)
	}
	wantEvents := map[string]bool{}
	sentEvents := map[string]bool{}
	for _, e := range recorded {
		if e.Feature == "event" {
			key, err := golden.Canonical(e.Request)
			if err != nil {
				t.Fatal(err)
			}
			wantEvents[string(key)] = true
		}
	}
	t.Cleanup(func() {
		for key := range wantEvents {
			if !sentEvents[key] {
				t.Errorf("recorded event request never sent: %s", key)
			}
		}
	})
	return func(feature string, body []byte) bool {
		if feature != "event" {
			return true
		}
		key, err := golden.Canonical(body)
		if err != nil || !wantEvents[string(key)] {
			t.Errorf("event request body is not one Python sent:\n%s", body)
			return false
		}
		sentEvents[string(key)] = true
		return true
	}
}

func loadPipeline(t *testing.T, file string, into any) {
	t.Helper()
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", file), into); err != nil {
		t.Fatal(err)
	}
}

type placed struct {
	box string
	uid uint32
}

type replayAccount struct {
	fake *mailtest.FakeClient
	uids map[string]placed
}

type replay struct {
	t        *testing.T
	ctx      context.Context
	d        *db.DB
	clock    float64
	accounts map[string]*replayAccount
	app      *cli.App
	stdin    *bytes.Buffer
	cbtt     string
	ledger   []money.NewAccount
}

func (r *replay) now() float64 { return r.clock }

func (r *replay) open(_ context.Context, account string) (mail.Client, error) {
	return r.accounts[account].fake, nil
}

func (r *replay) cli(args ...string) string {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	if code := r.app.Run(r.ctx, args, &stdout, &stderr); code != 0 {
		r.t.Fatalf("%v exited %d: %s", args, code, stderr.String())
	}
	return stdout.String()
}

func (r *replay) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.d.Write.ExecContext(r.ctx, query, args...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *replay) int64(query string, args ...any) int64 {
	r.t.Helper()
	var v int64
	if err := r.d.Read.QueryRowContext(r.ctx, query, args...).Scan(&v); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
	return v
}

func (r *replay) messageID(key string) int64 {
	return r.int64("SELECT id FROM messages WHERE msgid = ? ORDER BY gone_at IS NULL DESC, id DESC", key+"@golden.example")
}

func (r *replay) proposalID(kind, account string) int64 {
	return r.int64("SELECT id FROM proposals WHERE kind = ? AND account IS ? AND status = 'pending' ORDER BY id", kind, nullString(account))
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *replay) runSync(mode string) {
	r.t.Helper()
	r.t.Setenv("ATRIUM_MODE", mode)
	cfg, err := config.Load(config.OS)
	if err != nil {
		r.t.Fatal(err)
	}
	accounts, err := run.SelectAccounts(cfg, "")
	if err != nil {
		r.t.Fatal(err)
	}
	pass := run.New(r.d, cfg, r.open, r.now)
	pass.Money.Fetch = func(context.Context, string) (string, error) { return r.cbtt, nil }
	if err := pass.Locked(r.ctx, r.ctx, accounts); err != nil {
		r.t.Fatalf("run_sync (%s): %v", mode, err)
	}
}

func (r *replay) step(s scenarioStep, clocks map[string]float64, rules json.RawMessage, arrivals []arrival) {
	r.t.Helper()
	switch s.Op {
	case "clock":
		r.clock = clocks[s.At]
	case "rules_import":
		r.stdin.Reset()
		r.stdin.Write(rules)
		r.cli("rules", "import", "-")
	case "money_accounts":
		for _, a := range r.ledger {
			if _, err := money.AddAccount(r.ctx, r.d.Write, a, r.clock); err != nil {
				r.t.Fatal(err)
			}
		}
	case "run_sync":
		r.runSync(s.Mode)
	case "server_move":
		a := r.accounts[s.Account]
		p := a.uids[s.Key]
		m := a.fake.Boxes[p.box].Messages[p.uid]
		a.uids[s.Key] = placed{s.Dest, a.fake.Boxes[s.Dest].Add(m.Header, m.Text, m.Flags, m.Labels)}
		a.fake.Boxes[p.box].Remove(p.uid)
	case "server_delete":
		a := r.accounts[s.Account]
		keys := s.Keys
		if keys == nil {
			keys = []string{s.Key}
		}
		for _, key := range keys {
			p := a.uids[key]
			a.fake.Boxes[p.box].Remove(p.uid)
			delete(a.uids, key)
		}
	case "server_flags":
		a := r.accounts[s.Account]
		p := a.uids[s.Key]
		a.fake.Boxes[p.box].SetFlags(p.uid, s.Flags, nil)
	case "server_labels":
		a := r.accounts[s.Account]
		p := a.uids[s.Key]
		a.fake.Boxes[p.box].SetFlags(p.uid, nil, s.Labels)
	case "correction":
		if _, err := corrections.Record(r.ctx, r.d, r.messageID(s.Key), s.Folder, corrections.Source, r.clock); err != nil {
			r.t.Fatal(err)
		}
	case "backlog":
		r.cli("live", "backlog")
	case "arrive":
		for _, m := range arrivals {
			a := r.accounts[m.Account]
			var labels []string
			if m.Account == config.Gmail {
				labels = []string{`\Inbox`}
			}
			a.uids[m.Key] = placed{m.Mailbox, a.fake.Boxes[m.Mailbox].Add([]byte(m.Header), []byte(m.Text), m.Flags, labels)}
		}
	case "live_undo":
		id := r.int64("SELECT a.id FROM auto_actions a JOIN messages m ON m.id = a.message_id WHERE m.msgid = ? AND a.kind = ? ORDER BY a.id",
			s.Key+"@golden.example", s.Kind)
		r.cli("live", "undo", fmt.Sprint(id))
	case "approve":
		r.cli("proposals", "approve", fmt.Sprint(r.proposalID(s.Kind, s.Account)))
	case "dismiss":
		args := []string{"proposals", "dismiss", fmt.Sprint(r.proposalID(s.Kind, s.Account))}
		if s.Forever {
			args = append(args, "--forever")
		}
		r.cli(args...)
	case "scan_all":
		r.cli("proposals", "scan-all")
	default:
		r.t.Fatalf("unknown step %q", s.Op)
	}
}

func seedReplay(t *testing.T, s seeds, order map[string][]string) map[string]*replayAccount {
	t.Helper()
	out := map[string]*replayAccount{}
	for name, spec := range s.Accounts {
		acc := &replayAccount{fake: mailtest.NewFakeClient(config.OS), uids: map[string]placed{}}
		acc.fake.Gmail = name == config.Gmail
		names := slices.Clone(order[name])
		for box := range spec.Mailboxes {
			if !slices.Contains(names, box) {
				names = append(names, box)
			}
		}
		for _, box := range names {
			fb := acc.fake.AddBox(box, mailtest.NewBox(spec.Validity), spec.SpecialUse[box]...)
			for _, m := range spec.Mailboxes[box] {
				acc.uids[m.Key] = placed{box, fb.Add([]byte(m.Header), []byte(m.Text), m.Flags, m.Labels)}
			}
		}
		if name == config.Gmail {
			for category, keys := range s.Categories {
				for _, k := range keys {
					acc.fake.CategoryUIDs[category] = append(acc.fake.CategoryUIDs[category], acc.uids[k].uid)
				}
			}
		}
		out[name] = acc
	}
	return out
}

type row map[string]any

func scanAll(t *testing.T, rows *sql.Rows) []row {
	t.Helper()
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []row
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		r := row{}
		for i, c := range cols {
			r[c] = normalize(values[i])
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// normalize makes a SQLite value and its JSON rendering comparable: every number is a float64, text is a string.
func normalize(v any) any {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case []byte:
		return string(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return x.String()
		}
		return f
	case bool:
		if x {
			return 1.0
		}
		return 0.0
	}
	return v
}

func goldenRows(t *testing.T, table string) []row {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(golden.ReadFile(t, "pipeline", filepath.Join("results", table+".json"))))
	dec.UseNumber()
	var rows []row
	if err := dec.Decode(&rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for k, v := range r {
			r[k] = normalize(v)
		}
	}
	return rows
}

func (r *replay) tableRows(table, order string) []row {
	r.t.Helper()
	rows, err := r.d.Read.QueryContext(r.ctx, "SELECT * FROM "+table+" ORDER BY "+order)
	if err != nil {
		r.t.Fatal(err)
	}
	return scanAll(r.t, rows)
}

func describe(r row) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func compareTable(t *testing.T, table string, want, got []row) {
	t.Helper()
	if len(want) == 0 {
		t.Errorf("%s: no golden rows", table)
	}
	if len(want) != len(got) {
		t.Errorf("%s: %d rows, want %d", table, len(got), len(want))
	}
	for i := range min(len(want), len(got)) {
		w, g := want[i], got[i]
		var diffs []string
		for k, wv := range w {
			if gv, ok := g[k]; !ok || gv != wv {
				diffs = append(diffs, fmt.Sprintf("%s=%v (want %v)", k, gv, wv))
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				diffs = append(diffs, k+" is not a golden column")
			}
		}
		if len(diffs) > 0 {
			t.Errorf("%s row %d: %s\n  got  %s\n  want %s", table, i, strings.Join(diffs, "; "), describe(g), describe(w))
		}
	}
}

// pipelineTables are the generator's PIPELINE_TABLES with their order.
var pipelineTables = []struct{ name, order string }{
	{"decisions", "id"},
	{"flag_decisions", "message_id"},
	{"events", "id"},
	{"transactions", "id"},
	{"transaction_sources", "id"},
	{"packages", "id"},
	{"event_candidates", "message_id"},
	{"proposals", "id"},
	{"proposal_actions", "id"},
	{"auto_actions", "id"},
	{"corrections", "message_id"},
	{"commitments", "id"},
	{"open_loops", "thread_key"},
}

// TestGoldenPipelineDifferential replays every step of the Python generator's scenario (tools/golden/make_db.py)
// through the Go mail core: the locked sync-and-process pass at each recorded clock, the operator's corrections,
// and the rules, live and proposals commands through the CLI, against the same seeded mailboxes and the scripted
// local model. Every core table must then equal the Python rows column for column, timestamps included. Proposal
// ids are matched by fingerprint and translated wherever a row refers to one.
func TestGoldenPipelineDifferential(t *testing.T) {
	local := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = local })

	var sc pipelineScenario
	loadPipeline(t, "scenario.json", &sc)
	var seed seeds
	loadPipeline(t, "mailboxes.json", &seed)
	srv := llmstub.Server(t, recordedEvents(t))

	dir := t.TempDir()
	contacts := filepath.Join(dir, "contacts")
	if err := os.MkdirAll(contacts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contacts, "people.vcf"), []byte(seed.ContactsVCF), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range sc.Environment {
		t.Setenv(k, v)
	}
	t.Setenv("ATRIUM_DB", filepath.Join(dir, "atrium.db"))
	t.Setenv("ATRIUM_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("ATRIUM_CONTACTS_DIR", contacts)
	t.Setenv("ATRIUM_CHAT_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_EMBED_URL", srv.URL+"/v1")
	t.Setenv("ATRIUM_MODE", config.Shadow)
	cfg, err := config.Load(config.OS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		t.Fatal(err)
	}

	gd, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	boxRows, err := gd.Read.QueryContext(ctx, "SELECT account, imap_name FROM mailboxes ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	order := map[string][]string{}
	for _, b := range scanAll(t, boxRows) {
		order[b["account"].(string)] = append(order[b["account"].(string)], b["imap_name"].(string))
	}

	r := &replay{t: t, ctx: ctx, d: d, stdin: &bytes.Buffer{}, cbtt: sc.CBTTHTML}
	for _, a := range sc.Money {
		r.ledger = append(r.ledger, money.NewAccount{Bank: a.Bank, Name: a.Name, Kind: a.Kind, Currency: a.Currency, Last4: a.Last4, SenderPatterns: a.SenderPatterns, Forex: a.Forex})
	}
	r.accounts = seedReplay(t, seed, order)
	clients := func(config.Config, config.Env) func(context.Context, string) (mail.Client, error) { return r.open }
	opts := mailcmd.Options{Clients: clients, Now: r.now}
	r.app = cli.New("atrium", nil)
	r.app.Stdin = r.stdin
	r.app.Add(opts.RulesCommand(), opts.LiveCommand(), opts.ProposalsCommand())
	for i, s := range sc.Steps {
		t.Logf("step %d: %s %s%s", i, s.Op, s.At, s.Mode)
		r.step(s, sc.Clocks, sc.Rules, seed.Arrivals)
	}

	goldenProposals := goldenRows(t, "proposals")
	byFingerprint := map[string]float64{}
	for _, p := range r.tableRows("proposals", "id") {
		byFingerprint[p["fingerprint"].(string)] = p["id"].(float64)
	}
	proposalIDs := map[float64]float64{}
	var wantProposals []row
	for _, p := range goldenProposals {
		id, ok := byFingerprint[p["fingerprint"].(string)]
		if !ok {
			t.Errorf("proposals: no replayed proposal with fingerprint %v (%v)", p["fingerprint"], p["title"])
			continue
		}
		proposalIDs[p["id"].(float64)] = id
		p["id"] = id
		wantProposals = append(wantProposals, p)
	}
	translate := func(rows []row, column string) []row {
		for _, x := range rows {
			if v, ok := x[column].(float64); ok {
				x[column] = proposalIDs[v]
			}
		}
		return rows
	}
	for _, tb := range pipelineTables {
		var want []row
		switch tb.name {
		case "proposals":
			want = wantProposals
		case "proposal_actions", "commitments":
			want = translate(goldenRows(t, tb.name), "proposal_id")
		default:
			want = goldenRows(t, tb.name)
		}
		compareTable(t, tb.name, want, r.tableRows(tb.name, tb.order))
	}
}
