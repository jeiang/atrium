package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/inference"
)

func ledgerRow(t *testing.T, dir string, query string, args ...any) (state string, partial, reservation, accounted int64, input, output int64, requestID, errorText string) {
	t.Helper()
	d := asstDB(t, dir)
	var reqID, errText *string
	if err := d.Read.QueryRow(query, args...).Scan(&state, &partial, &reservation, &accounted, &input, &output, &reqID, &errText); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if reqID != nil {
		requestID = *reqID
	}
	if errText != nil {
		errorText = *errText
	}
	return
}

func TestInferenceConcurrentAdmissionSendsOneRequest(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)
	now := float64(time.Now().UnixMicro()) / 1e6
	var reservationN int64
	if err := d.Read.QueryRow("SELECT reservation_nusd FROM usage ORDER BY id DESC LIMIT 1").Scan(&reservationN); err != nil {
		t.Fatal(err)
	}
	reservation := float64(reservationN) / inference.NanoUSD
	var committedN int64
	if err := d.Read.QueryRow("SELECT COALESCE(SUM(accounted_nusd), 0) FROM usage WHERE month = ? AND budget_class <> 'subscription'",
		asstMonth(t, now)).Scan(&committedN); err != nil {
		t.Fatal(err)
	}
	ceiling := 150 * (1 - inference.ClassificationReserveShare)
	spend := ceiling - 1.5*reservation - float64(committedN)/inference.NanoUSD
	if spend <= 0 {
		t.Fatalf("fixture spent too much to leave room for one reservation: %v", spend)
	}
	asstSpend(t, d, inference.SourceLoop, spend)

	baseline := len(api.Requests())
	api.Push(asstReply("alpha"))
	release := make(chan struct{})
	arrived := make(chan struct{})
	var once sync.Once
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		api.seen = append(api.seen, r)
		api.mu.Unlock()
		once.Do(func() {
			close(arrived)
			<-release
		})
	}

	first := make(chan appRun, 1)
	second := make(chan appRun, 1)
	go func() { first <- asstRunApp(asstApp(f), context.Background(), "agent", "run", "--text", "alpha") }()
	<-arrived
	go func() { second <- asstRunApp(asstApp(f), context.Background(), "agent", "run", "--text", "beta") }()
	refused := <-second
	close(release)
	ran := <-first

	if ran.code != cli.ExitOK {
		t.Fatalf("admitted run: %d %s", ran.code, ran.stderr)
	}
	if !strings.Contains(refused.stdout, `"state": "failed"`) || !strings.Contains(refused.stdout, "monthly budget") {
		t.Fatalf("second run: %d %q %q, want a monthly budget refusal", refused.code, refused.stdout, refused.stderr)
	}
	if got := len(api.Requests()) - baseline; got != 1 {
		t.Errorf("%d provider requests, want exactly 1", got)
	}
}

func TestInferenceInterruptedStreamSettlesPartial(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	api.Push(anthropictest.Response{StartUsage: anthropictest.Usage{Input: 500, Output: 20}.JSON(), InterruptAfterStart: true})
	ran, _ := asstRun(t, "--text", "go")
	if ran.State != "failed" {
		t.Fatalf("interrupted run: %+v %q", ran, ran.err())
	}
	state, partial, _, accounted, input, output, requestID, _ := ledgerRow(t, dir,
		"SELECT state, partial, reservation_nusd, accounted_nusd, input_tokens, output_tokens, request_id, error FROM usage WHERE run_id = ?", ran.ID)
	if state != "settled" || partial != 1 || input != 500 || output != 20 || accounted <= 0 || requestID == "" {
		t.Errorf("partial row: state %q partial %d input %d output %d accounted %d request %q", state, partial, input, output, accounted, requestID)
	}
}

func TestInferenceUnknownOutcomeStaysReserved(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	api.Disconnect.Store(true)
	ran, _ := asstRun(t, "--text", "go")
	if ran.State != "failed" {
		t.Fatalf("disconnected run: %+v %q", ran, ran.err())
	}
	if got := len(api.Requests()); got != 1 {
		t.Fatalf("%d provider requests, want one unretried attempt", got)
	}
	state, _, reservation, accounted, _, _, _, _ := ledgerRow(t, dir,
		"SELECT state, partial, reservation_nusd, accounted_nusd, input_tokens, output_tokens, request_id, error FROM usage WHERE run_id = ?", ran.ID)
	if state != "unknown" || reservation <= 0 || accounted != 0 {
		t.Errorf("unknown row: state %q reservation %d accounted %d", state, reservation, accounted)
	}

	api.Disconnect.Store(false)
	api.Push(asstReply("later"))
	if again, _ := asstRun(t, "--text", "later"); again.State != "done" {
		t.Fatalf("run after an unknown outcome: %+v %q", again, again.err())
	}
	var unknown int
	if err := asstDB(t, dir).Read.QueryRow("SELECT COUNT(*) FROM usage WHERE state = 'unknown'").Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 1 {
		t.Errorf("%d unknown rows after a later run, want the original reservation kept", unknown)
	}
}

func TestInferenceRejectedCallReleasesItsReservation(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	api.Push(anthropictest.Failure(400, "invalid_request_error", "bad request"))
	ran, _ := asstRun(t, "--text", "go")
	if ran.State != "failed" || !strings.Contains(ran.err(), "bad request") {
		t.Fatalf("rejected run: %+v %q", ran, ran.err())
	}
	state, _, reservation, accounted, _, _, _, errorText := ledgerRow(t, dir,
		"SELECT state, partial, reservation_nusd, accounted_nusd, input_tokens, output_tokens, request_id, error FROM usage WHERE run_id = ?", ran.ID)
	if state != "settled" || reservation <= 0 || accounted != 0 || errorText == "" {
		t.Errorf("rejected row: state %q reservation %d accounted %d error %q", state, reservation, accounted, errorText)
	}
}

func TestInferenceMemoryCallsAreAdmittedAndBilled(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	long := strings.Repeat("evidence ", 120)
	for i := range 4 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text(long)))
		if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "message "+strings.Repeat("x", i+1)); ran.code != cli.ExitOK {
			t.Fatalf("chat run %d: %d %s", i, ran.code, ran.stderr)
		}
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory view: %d %s", ran.code, ran.stderr)
	}
	for range 20 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory work: %d %s", ran.code, ran.stderr)
	}
	state, _, reservation, accounted, _, _, _, _ := ledgerRow(t, dir,
		"SELECT state, partial, reservation_nusd, accounted_nusd, input_tokens, output_tokens, request_id, error FROM usage WHERE kind = 'memory' ORDER BY id DESC LIMIT 1")
	if state != "settled" || reservation <= 0 || accounted <= 0 {
		t.Errorf("memory row: state %q reservation %d accounted %d", state, reservation, accounted)
	}
	var class string
	if err := asstDB(t, dir).Read.QueryRow("SELECT budget_class FROM usage WHERE kind = 'memory' ORDER BY id DESC LIMIT 1").Scan(&class); err != nil {
		t.Fatal(err)
	}
	if class != string(inference.Unattended) {
		t.Errorf("memory budget class %q, want unattended", class)
	}

	if ran := runApp(t, "agent", "run", "--text", "more"); ran.code != cli.ExitOK {
		t.Fatalf("second chat run: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("second memory view: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "settings", "set", "monthly_budget_usd", "0.000001"); ran.code != cli.ExitOK {
		t.Fatalf("settings set: %d %s", ran.code, ran.stderr)
	}
	before := len(api.Requests())
	if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory work under an exhausted budget: %d %s", ran.code, ran.stderr)
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("memory sent %d requests under an exhausted budget", got)
	}
	var state2, errText string
	var nextAt float64
	if err := asstDB(t, dir).Read.QueryRow("SELECT state, COALESCE(error, ''), next_at FROM memory_jobs WHERE thread_id = 1 ORDER BY id DESC LIMIT 1").Scan(&state2, &errText, &nextAt); err != nil {
		t.Fatal(err)
	}
	if state2 != "queued" || !strings.Contains(errText, "monthly budget") || nextAt <= float64(time.Now().UnixMicro())/1e6 {
		t.Errorf("job state %q next_at %v error %q, want a monthly budget refusal left queued for next month", state2, nextAt, errText)
	}
}

func TestInferenceMonthSpendSurvivesATimezoneChange(t *testing.T) {
	now := float64(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir, api, f := moduleFixtureAt(t, flagsExcept("", ""), func() float64 { return now })
	asstWarm(t)
	d := asstDB(t, dir)
	asstSpendAt(t, d, inference.SourceLoop, 149.99, now)

	f.unset("ATRIUM_CORE_TZ")
	asstSet(t, "tz", "Europe/London")

	sent := len(api.Requests())
	ran := runApp(t, "agent", "run", "--text", "hello")
	if ran.code != cli.ExitError || !strings.Contains(ran.stdout+ran.stderr, "monthly budget") {
		t.Fatalf("run after the timezone change: exit %d stdout %q stderr %q, want a monthly budget refusal", ran.code, ran.stdout, ran.stderr)
	}
	if got := len(api.Requests()) - sent; got != 0 {
		t.Errorf("%d provider requests after the refusal", got)
	}
}

func TestInferenceInterruptedStreamChargesTheOutputItReceived(t *testing.T) {
	dir, api, f := moduleFixture(t, flagsExcept("", ""))
	asstWarm(t)
	d := asstDB(t, dir)

	text := strings.Repeat("generated output ", 400)
	api.Push(anthropictest.Response{
		StopReason: "end_turn",
		Usage:      anthropictest.Usage{Input: 700, Output: 900}.JSON(),
		StartUsage: anthropictest.Usage{Input: 700}.JSON(),
		Content:    append([]json.RawMessage{anthropictest.Text(text)}, anthropictest.WebSearch("s1", "atrium")...),
		Stall:      true,
	})
	base := asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE kind = 'tool_start'")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan appRun, 1)
	go func() { done <- runAppContext(t, f, ctx, "agent", "run", "--text", "go") }()
	deadline := time.Now().Add(30 * time.Second)
	for asstCount(t, d, "SELECT COUNT(*) FROM run_events WHERE kind = 'tool_start'") <= base {
		if time.Now().After(deadline) {
			t.Fatal("the stalled stream never delivered text")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the canceled run did not return")
	}

	state, partial, _, accounted, _, output, _, _ := ledgerRow(t, dir,
		"SELECT state, partial, reservation_nusd, accounted_nusd, input_tokens, output_tokens, request_id, error FROM usage ORDER BY id DESC LIMIT 1")
	if state != "settled" || partial != 1 {
		t.Fatalf("interrupted row: state %q partial %d", state, partial)
	}
	if output < 1600 || output > 2000 {
		t.Errorf("output tokens %d, want the received text estimated at about 1700", output)
	}
	if accounted <= 0 {
		t.Errorf("accounted %d, want the received output charged", accounted)
	}
}

func TestInferenceLargePDFIsEstimatedFromItsStoredTokens(t *testing.T) {
	_, api, d, w := asstServe(t)

	pdf := make([]byte, 0, 6<<20)
	pdf = append(pdf, "%PDF-1.7\n"...)
	for range 5 {
		pdf = append(pdf, "1 0 obj << /Type /Page >> endobj\n"...)
	}
	for len(pdf) < 6<<20 {
		pdf = append(pdf, '%')
	}

	before := len(api.Requests())
	api.Push(asstReply("read"))
	thread := w.startFiles(map[string]string{"text": "read this", "model": "claude-opus-5-5"},
		[]asstFile{{"big.pdf", "application/pdf", pdf}})
	if state := asstSettleThread(t, d, thread); state != "done" {
		t.Fatalf("large PDF run %s", state)
	}
	if got := len(api.Requests()) - before; got != 1 {
		t.Fatalf("%d provider requests, want 1", got)
	}
	var prompt, reservation int64
	if err := d.Read.QueryRow("SELECT estimated_prompt_tokens, reservation_nusd FROM usage WHERE thread_id = ? ORDER BY id DESC LIMIT 1", thread).
		Scan(&prompt, &reservation); err != nil {
		t.Fatal(err)
	}
	if prompt <= 0 || prompt > 100_000 {
		t.Errorf("estimated prompt %d, want the stored attachment estimate rather than the base64 size", prompt)
	}
	if reservation <= 0 || reservation >= int64(10*inference.NanoUSD) {
		t.Errorf("reservation %d, want a positive value below the $10 Opus run cap", reservation)
	}
}

func TestInferenceMemoryMonthlyRefusalResumesNextMonth(t *testing.T) {
	now := float64(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix())
	dir, api, _ := moduleFixtureAt(t, flagsExcept("", ""), func() float64 { return now })
	if ran := runApp(t, "agent", "run", "--text", "warm"); ran.code != cli.ExitOK {
		t.Fatalf("warm run: %d %s", ran.code, ran.stderr)
	}
	long := strings.Repeat("evidence ", 120)
	for i := range 4 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text(long)))
		if ran := runApp(t, "agent", "run", "--thread", "1", "--text", "message "+strings.Repeat("x", i+1)); ran.code != cli.ExitOK {
			t.Fatalf("chat run %d: %d %s", i, ran.code, ran.stderr)
		}
	}
	if ran := runApp(t, "memory", "view", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory view: %d %s", ran.code, ran.stderr)
	}
	if ran := runApp(t, "settings", "set", "monthly_budget_usd", "0.000001"); ran.code != cli.ExitOK {
		t.Fatalf("settings set: %d %s", ran.code, ran.stderr)
	}
	before := len(api.Requests())
	if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
		t.Fatalf("memory work under an exhausted budget: %d %s", ran.code, ran.stderr)
	}
	if got := len(api.Requests()) - before; got != 0 {
		t.Fatalf("memory sent %d requests under an exhausted budget", got)
	}
	d := asstDB(t, dir)
	queued, failed := 0, 0
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_jobs WHERE thread_id = 1 AND state = 'queued'").Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_jobs WHERE thread_id = 1 AND state = 'failed'").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if queued == 0 || failed != 0 {
		t.Fatalf("%d queued and %d failed jobs after a monthly refusal, want all queued", queued, failed)
	}
	if ran := runApp(t, "settings", "set", "monthly_budget_usd", "150"); ran.code != cli.ExitOK {
		t.Fatalf("settings restore: %d %s", ran.code, ran.stderr)
	}
	now = float64(time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC).Unix())
	for range 20 {
		api.Push(anthropictest.Reply("end_turn", anthropictest.Text("summary")))
	}
	for range 8 {
		if ran := runApp(t, "memory", "work", "1"); ran.code != cli.ExitOK {
			t.Fatalf("memory work next month: %d %s", ran.code, ran.stderr)
		}
		var pending int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_jobs WHERE thread_id = 1 AND state <> 'done'").Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
	}
	if got := len(api.Requests()) - before; got == 0 {
		t.Fatalf("memory sent no requests next month")
	}
	var pending int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM memory_jobs WHERE thread_id = 1 AND state <> 'done'").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("%d memory jobs not done after the next month", pending)
	}
}
