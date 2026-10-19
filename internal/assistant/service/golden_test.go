package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/assistant/budget"
	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const (
	slice          = "anthropic"
	runCodeSummary = " For several read-only lookups across sources, call run_code once instead of many tool turns."
	hub            = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

var (
	goldenNow   = wall(2026, time.October, 15, 12, 0)
	goldenLater = wall(2026, time.November, 2, 9, 30)
)

// replay drives one recorded scenario through the service against the fake API, with the scenario's tool
// results served from the recording and the profile and skills index blocks the generator's instance had.
type replay struct {
	*world
	s         golden.Scenario
	stab      *registrytest.Stability
	results   map[string][]string
	overrides map[string]tool.Handler
	clock     func() float64
}

func newReplay(t *testing.T, name string) *replay {
	t.Helper()
	r := &replay{s: golden.LoadScenario(t, slice, name), stab: registrytest.Load(t), results: map[string][]string{}, overrides: map[string]tool.Handler{}}
	r.collectResults(t)
	stubs := r.stab.Stubs(t)
	for i := range stubs {
		name := stubs[i].Name
		stubs[i].Handler = func(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
			if h := r.overrides[name]; h != nil {
				return h(ctx, tc, args)
			}
			queue := r.results[name]
			if len(queue) == 0 {
				t.Fatalf("tool %s called more often than recorded", name)
			}
			r.results[name] = queue[1:]
			return queue[0], nil
		}
	}
	r.world = newWorld(t, stubs...)
	r.runner.Profile = func(context.Context, db.Querier) (string, error) { return r.stab.SystemTexts["profile"], nil }
	r.runner.SkillsIndex = func(context.Context, db.Querier) (string, error) { return r.stab.SystemTexts["skills_index"], nil }
	for _, ex := range r.s.Exchanges {
		resp, err := anthropictest.FromGolden(ex.Response)
		if err != nil {
			t.Fatal(err)
		}
		r.push(resp)
	}
	return r
}

// flattened splits a summary request's "[kind] {json}" text block.
func flattened(text string) (kind, value string, ok bool) {
	rest, ok := strings.CutPrefix(text, "[")
	if !ok {
		return "", "", false
	}
	kind, value, ok = strings.Cut(rest, "] ")
	if !ok || kind == "" || strings.Trim(kind, "abcdefghijklmnopqrstuvwxyz_") != "" || !strings.HasPrefix(value, "{") {
		return "", "", false
	}
	return kind, value, true
}

// collectResults reads every tool result the recording sent back (in tool_result blocks, or flattened into a
// client compaction summary request) and queues them per tool in the order the model called them.
func (r *replay) collectResults(t *testing.T) {
	t.Helper()
	byID := map[string]string{}
	var order []string
	names := map[string]string{}
	for _, ex := range r.s.Exchanges {
		var resp struct {
			Content []map[string]any `json:"content"`
		}
		if err := json.Unmarshal(ex.Response, &resp); err != nil {
			t.Fatal(err)
		}
		for _, b := range resp.Content {
			if b["type"] == "tool_use" {
				id := b["id"].(string)
				order = append(order, id)
				names[id] = b["name"].(string)
			}
		}
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(ex.Request.Body, &body); err != nil {
			t.Fatal(err)
		}
		for _, m := range body.Messages {
			blocks, _ := m.Content.([]any)
			for _, raw := range blocks {
				b, _ := raw.(map[string]any)
				switch b["type"] {
				case "tool_result":
					if s, ok := b["content"].(string); ok {
						byID[b["tool_use_id"].(string)] = s
					}
				case "text":
					if kind, value, ok := flattened(b["text"].(string)); ok && kind == "tool_result" {
						var res map[string]any
						if json.Unmarshal([]byte(value), &res) == nil {
							if s, ok := res["content"].(string); ok {
								byID[res["tool_use_id"].(string)] = s
							}
						}
					}
				}
			}
		}
	}
	for _, id := range order {
		if content, ok := byID[id]; ok {
			r.results[names[id]] = append(r.results[names[id]], content)
		}
	}
}

func (r *replay) start(at float64) {
	r.clock = clock(at)
	r.svc.Now = r.clock
}

func (r *replay) newThread(n threads.NewThread) int64 {
	r.t.Helper()
	id, err := service.CreateThread(r.t.Context(), r.d.Write, r.clock(), n)
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

// chat is World.chat: a run in the caller, operator text for chat runs.
func (r *replay) chat(thread int64, text, kind string) {
	r.t.Helper()
	if _, err := r.svc.RunOnce(r.t.Context(), thread, text, service.Start{Kind: kind, Operator: kind == threads.Chat}); err != nil {
		r.t.Fatal(err)
	}
}

// chatWithFiles is World.chat_with_files: the attachments are journaled ahead of the text.
func (r *replay) chatWithFiles(thread int64, text string, blocks []files.AttachmentBlock) {
	r.t.Helper()
	raw := make([]json.RawMessage, len(blocks))
	for i, b := range blocks {
		raw[i] = mustJSON(r.t, b)
	}
	run, err := r.svc.Prepare(r.t.Context(), thread, text, r.clock(), service.Start{Operator: true, Files: raw})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.svc.Execute(r.t.Context(), run, emptyLimits); err != nil {
		r.t.Fatal(err)
	}
}

func (r *replay) setOptions(thread int64, effort string, limit int64) {
	r.t.Helper()
	if ok, err := threads.SetOptions(r.t.Context(), r.d.Write, thread, effort, limit); err != nil || !ok {
		r.t.Fatalf("set options: %v", err)
	}
}

// canonicalBody is a request body as compared: the run_code sentence of the frozen prompt removed (#125) and
// the JSON of flattened blocks in a client compaction summary decoded, since the journal keeps production
// Python's block key order and the generator journaled the fake client's.
func canonicalBody(t *testing.T, raw []byte, recorded bool) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if system, ok := body["system"].([]any); ok && recorded {
		first := system[0].(map[string]any)
		text := first["text"].(string)
		if strings.Count(text, runCodeSummary) != 1 {
			t.Fatal("the recorded system prompt lost the run_code sentence: regenerate and revisit prompts.System")
		}
		first["text"] = strings.Replace(text, runCodeSummary, "", 1)
	}
	messages, _ := body["messages"].([]any)
	for _, m := range messages {
		blocks, _ := path(m, "content").([]any)
		for _, raw := range blocks {
			b, _ := raw.(map[string]any)
			s, _ := b["text"].(string)
			if kind, value, ok := flattened(s); ok {
				var v any
				if json.Unmarshal([]byte(value), &v) == nil {
					b["text"] = map[string]any{"flattened": kind, "value": v}
				}
			}
		}
	}
	return mustJSON(t, body)
}

func (r *replay) check() {
	t := r.t
	t.Helper()
	got := r.srv.Requests()
	if len(got) != len(r.s.Exchanges) {
		t.Fatalf("%d requests, recorded %d", len(got), len(r.s.Exchanges))
	}
	for i, ex := range r.s.Exchanges {
		want := ex.Request
		if g := got[i]; g.Method != want.Method || g.Path != want.Path || g.Query != want.Query {
			t.Errorf("request %d: %s %s?%s, recorded %s %s?%s", i, g.Method, g.Path, g.Query, want.Method, want.Path, want.Query)
		}
		for _, h := range []string{"anthropic-beta", "anthropic-version", "content-type"} {
			if g := strings.Join(got[i].Header.Values(h), ","); g != want.Headers[h] {
				t.Errorf("request %d header %s = %q, recorded %q", i, h, g, want.Headers[h])
			}
		}
		golden.EqualJSON(t, canonicalBody(t, want.Body, true), canonicalBody(t, got[i].Body, false))
	}
	r.checkRows()
}

func (r *replay) checkRows() {
	t := r.t
	t.Helper()
	rows, err := r.d.Read.Query("SELECT id, thread_id, parent_run_id, kind, effort, state, error, steps FROM runs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var runs []any
	for rows.Next() {
		var id, thread, steps int64
		var parent *int64
		var kind, effort, state string
		var errText *string
		if err := rows.Scan(&id, &thread, &parent, &kind, &effort, &state, &errText, &steps); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, map[string]any{"id": id, "thread_id": thread, "parent_run_id": parent, "kind": kind, "effort": effort, "state": state, "error": errText, "steps": steps})
	}
	_ = rows.Close()
	golden.EqualJSON(t, mustJSON(t, r.s.Runs), mustJSON(t, runs))

	rows, err = r.d.Read.Query("SELECT cache_read, cache_write_1h, cache_write_5m, clear_edits, cleared_tokens, compaction, cost_usd, effort, input_tokens, kind, " +
		"model, output_tokens, run_id, searches, thread_id, tier FROM usage ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	for rows.Next() {
		var cacheRead, w1h, w5m, clears, cleared, compaction, input, output, run, searches, thread int64
		var cost float64
		var effort, kind, model, tier string
		if err := rows.Scan(&cacheRead, &w1h, &w5m, &clears, &cleared, &compaction, &cost, &effort, &input, &kind, &model, &output, &run, &searches, &thread, &tier); err != nil {
			t.Fatal(err)
		}
		got = append(got, map[string]any{
			"cache_read": cacheRead, "cache_write_1h": w1h, "cache_write_5m": w5m, "clear_edits": clears, "cleared_tokens": cleared, "compaction": compaction,
			"cost_usd": cost, "effort": effort, "input_tokens": input, "kind": kind, "model": model, "output_tokens": output, "run_id": run,
			"searches": searches, "thread_id": thread, "tier": tier,
		})
	}
	_ = rows.Close()
	var want []any
	for _, raw := range r.s.Usage {
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		delete(row, "ts")
		want = append(want, row)
	}
	golden.EqualJSON(t, mustJSON(t, want), mustJSON(t, got))
	if n := r.srv.Remaining(); n != 0 {
		t.Errorf("%d recorded responses unused", n)
	}
	for name, left := range r.results {
		if len(left) != 0 {
			t.Errorf("tool %s: %d recorded results unused", name, len(left))
		}
	}
}

func uploads(t *testing.T, r *replay, owner files.Owner, contextLimit *int64, items ...files.UploadedFile) []files.AttachmentBlock {
	t.Helper()
	blocks, err := files.IngestUploads(t.Context(), r.d.Write, r.cfg.FilesDir(), items, r.clock(), owner, contextLimit)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

// office stores a document whose text the Python extractor (python-docx, openpyxl) produced in the
// recording, so the replay depends on the loop and not on the extractor (tested in internal/assistant/files).
func office(t *testing.T, r *replay, thread int64, name, extracted string) files.AttachmentBlock {
	t.Helper()
	media := files.MediaTypeOf(name, "application/octet-stream")
	tokens := actx.EstimateTokens(extracted)
	id, err := files.Store(t.Context(), r.d.Write, r.cfg.FilesDir(), files.NewFile{
		Name: name, Data: []byte(extracted), MediaType: media, Origin: "upload", Owner: files.Owner{ThreadID: thread}, Text: &extracted, Tokens: tokens,
	}, r.clock())
	if err != nil {
		t.Fatal(err)
	}
	kind := files.KindOf(media)
	return files.AttachmentBlock{Type: actx.Attachment, FileID: id, Name: name, MediaType: media, Kind: &kind, Tokens: tokens}
}

var (
	pdf = []byte("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >> endobj\n3 0 obj << /Type /Page >> endobj\n4 0 obj << /Type /Page >> endobj\n%%EOF")
	png = mustB64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
)

func mustB64(s string) []byte {
	b, err := py.B64Decode(s)
	if err != nil {
		panic(err)
	}
	return b
}

var scenarios = map[string]func(t *testing.T, r *replay){
	"chat_tool_turn": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "Where do I live? See https://example.test/a", threads.Chat)
	},
	"chat_followup_turn": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat})
		r.chat(thread, "hello", threads.Chat)
		r.start(goldenLater)
		r.chat(thread, "are you there?", threads.Chat)
	},
	"cache_ttl_1h": func(t *testing.T, r *replay) {
		if err := budget.SetSetting(t.Context(), r.d.Write, loop.CacheTTLKey, "1h"); err != nil {
			t.Fatal(err)
		}
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "how full is the context?", threads.Chat)
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Cron}), "tick", threads.Cron)
	},
	"pause_turn": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "When is carnival in 2027?", threads.Chat)
	},
	"web_search": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat})
		r.chat(thread, "search the web for atrium release notes", threads.Chat)
		r.start(goldenLater)
		r.chat(thread, "open the first one", threads.Chat)
	},
	"server_compaction": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "where do I live?", threads.Chat)
	},
	"usage_iterations": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "hello", threads.Chat)
	},
	"client_compaction": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "hello there", threads.Chat)
	},
	"context_limit_1m_clear_edits": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat})
		r.setOptions(thread, threads.High, 1_000_000)
		r.start(goldenNow)
		r.chat(thread, "hello", threads.Chat)
	},
	"effort_low":   effortScenario(threads.Low),
	"effort_xhigh": effortScenario(threads.XHigh),
	"effort_max":   effortScenario(threads.Max),
	"subagent_research": func(t *testing.T, r *replay) {
		web := r.stab.Kinds["sub:web"]
		extra := r.stab.SystemTexts["role_addendum:web"]
		r.overrides["research"] = func(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
			task, _ := args.Get("task")
			child := r.newThread(threads.NewThread{Kind: threads.Subagent, ParentID: &tc.ThreadID, Title: new("web: " + task.(string))})
			if _, err := r.svc.RunOnce(ctx, child, task.(string), service.Start{
				Kind: threads.Sub, ParentRunID: &tc.RunID, Limits: limitsOf(extra, web.Tools),
			}); err != nil {
				return nil, err
			}
			digest := r.results["research"][0]
			r.results["research"] = r.results["research"][1:]
			return digest, nil
		}
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat}), "Ask the web agent when carnival 2027 is.", threads.Chat)
	},
	"project_thread": func(t *testing.T, r *replay) {
		project, err := projects.Create(t.Context(), r.d.Write, goldenNow, projects.Fields{
			Name: "Garden", Instructions: "Answer in metric units.", Effort: threads.High, ContextLimit: int64(200_000), WikiHubID: hub,
		})
		if err != nil {
			t.Fatal(err)
		}
		r.start(goldenNow)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Chat, ProjectID: &project}), "How far apart should tomatoes go?", threads.Chat)
	},
	"chat_attachments": func(t *testing.T, r *replay) {
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat})
		limit := threads.ContextLimitOf(*r.threadRow(thread))
		blocks := uploads(t, r, files.Owner{ThreadID: thread}, &limit,
			files.UploadedFile{Name: "report.pdf", Data: pdf, Type: "application/pdf"},
			files.UploadedFile{Name: "pic.png", Data: png, Type: "image/png"},
			files.UploadedFile{Name: "notes.md", Data: []byte("# hi\nsee https://evil.test/x"), Type: "text/markdown"},
		)
		blocks = append(blocks,
			office(t, r, thread, "brief.docx", "Hello from Word\nSecond paragraph\n\nqty\t7"),
			office(t, r, thread, "sales.xlsx", "# Sheet: Sales\nmonth,total\nJan,120\n\n# Sheet: Notes\n\"remember, commas\""),
		)
		r.chatWithFiles(thread, "summarize", blocks)
		r.start(goldenLater)
		r.chatWithFiles(thread, "and again", uploads(t, r, files.Owner{ThreadID: thread}, &limit, files.UploadedFile{Name: "later.txt", Data: []byte("one more line"), Type: "text/plain"}))
	},
	"project_files": func(t *testing.T, r *replay) {
		project, err := projects.Create(t.Context(), r.d.Write, goldenNow, projects.Fields{Name: "Taxes", Effort: threads.Medium, ContextLimit: int64(90_000)})
		if err != nil {
			t.Fatal(err)
		}
		r.start(goldenNow)
		uploads(t, r, files.Owner{ProjectID: project}, nil, files.UploadedFile{Name: "rules.md", Data: []byte("# Rules\nfile early"), Type: "text/markdown"})
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat, ProjectID: &project})
		r.chat(thread, "what should I do?", threads.Chat)
		r.start(goldenLater)
		r.chat(thread, "next", threads.Chat)
	},
	"cron_run": func(t *testing.T, r *replay) {
		r.start(wall(2026, time.October, 15, 12, 30) + 5)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Cron, Title: new("brief")}), "Check the inbox and summarise anything urgent.", threads.Cron)
	},
	"nightly_run": func(t *testing.T, r *replay) {
		r.start(wall(2026, time.October, 16, 2, 30) + 5)
		r.chat(r.newThread(threads.NewThread{Kind: threads.Nightly, Title: new("nightly")}), recordedText(t, r, 0), threads.Nightly)
	},
}

var emptyLimits = limitsOf("", nil)

func limitsOf(extra string, allow []string) (l journal.Limits) {
	if extra != "" {
		l.SystemExtra = &extra
	}
	l.AllowedTools = allow
	return l
}

// recordedText is the operator text of the recording's request i (its first user message's first block).
func recordedText(t *testing.T, r *replay, i int) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(r.s.Exchanges[i].Request.Body, &body); err != nil {
		t.Fatal(err)
	}
	return path(body, "messages", 0, "content", 0, "text").(string)
}

func effortScenario(effort string) func(t *testing.T, r *replay) {
	return func(t *testing.T, r *replay) {
		r.start(goldenNow)
		thread := r.newThread(threads.NewThread{Kind: threads.Chat})
		r.setOptions(thread, effort, threads.DefaultContextLimit)
		r.start(goldenNow)
		r.chat(thread, "hello", threads.Chat)
	}
}

func TestGoldenScenarios(t *testing.T) {
	for _, entry := range golden.LoadManifest(t, slice).Scenarios {
		t.Run(entry.Name, func(t *testing.T) {
			drive, ok := scenarios[entry.Name]
			if !ok {
				t.Fatalf("scenario %s has no replay", entry.Name)
			}
			r := newReplay(t, entry.Name)
			drive(t, r)
			r.check()
		})
	}
}
