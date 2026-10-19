package openloops_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type exchange struct {
	Feature string          `json:"feature"`
	Request json.RawMessage `json:"request"`
	Reply   json.RawMessage `json:"reply"`
}

func goldenExchanges(t *testing.T, feature string) map[string]exchange {
	t.Helper()
	var all []exchange
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_exchanges.json"), &all); err != nil {
		t.Fatal(err)
	}
	out := map[string]exchange{}
	for _, e := range all {
		if e.Feature != feature {
			continue
		}
		key, err := golden.Canonical(e.Request)
		if err != nil {
			t.Fatal(err)
		}
		out[string(key)] = e
	}
	if len(out) == 0 {
		t.Fatalf("no %s exchanges recorded", feature)
	}
	return out
}

// TestCommitmentRequestsMatchTheGoldenBodies rescans the golden mail against a llama.cpp stand-in that accepts
// only the request bodies Python sent, and expects every recorded commitment request to be sent. The clock is
// one day after t0: the 14-day window then holds exactly the mail Python scanned (nothing before 2026-10-02) and every message counts as present, as it was when Python scanned.
func TestCommitmentRequestsMatchTheGoldenBodies(t *testing.T) {
	want := goldenExchanges(t, "commitments")
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	exec(t, d, "DELETE FROM commitment_scans")
	exec(t, d, "UPDATE messages SET gone_at = NULL")
	sent := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key, err := golden.Canonical(body)
		if err != nil {
			t.Error(err)
		}
		e, ok := want[string(key)]
		if !ok {
			t.Errorf("request body is not one Python sent; user prompt:\n%s", requestUser(t, body))
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		sent[string(key)] = true
		reply, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(e.Reply)}}}})
		_, _ = w.Write(reply)
	}))
	defer srv.Close()
	cfg := config.Config{ChatURL: srv.URL}
	if _, err := openloops.ScanCommitments(ctx, d, cfg, env(), 1792166400, nil, 1000); err != nil {
		t.Fatal(err)
	}
	for key, e := range want {
		if !sent[key] {
			var req struct{ Messages []struct{ Content string } }
			_ = json.Unmarshal(e.Request, &req)
			t.Errorf("recorded request never sent; user prompt:\n%s", req.Messages[1].Content)
		}
	}
}

type loopRow struct {
	ID                                        int64
	Key, State, Counterpart, Subject, Account string
	Last                                      int64
	Since                                     float64
}

func loopRows(t *testing.T, d *db.DB) map[string]loopRow {
	t.Helper()
	rows, err := d.Read.Query("SELECT thread_key, state, counterpart, subject, account, last_message_id, since FROM open_loops")
	if err != nil {
		t.Fatal(err)
	}
	list, err := db.Collect(rows, func(r *sql.Rows) (loopRow, error) {
		var l loopRow
		return l, r.Scan(&l.Key, &l.State, &l.Counterpart, &l.Subject, &l.Account, &l.Last, &l.Since)
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]loopRow{}
	for _, l := range list {
		out[l.Key] = l
	}
	return out
}

// loopClock is the clock of the open-loop refresh that built the golden open_loops (sync4 in scenario.json).
const loopClock = 1792094400.0

// TestComputeRebuildsTheGoldenLoops recomputes open_loops from the golden mail as it stood at the refresh: mail
// moved or deleted later is present again and mail that arrived later is absent. Row ids follow Python's history
// of passes, so loops are compared by thread key.
func TestComputeRebuildsTheGoldenLoops(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	want := loopRows(t, d)
	exec(t, d, "UPDATE messages SET gone_at = NULL WHERE gone_at > ?", loopClock)
	exec(t, d, "UPDATE messages SET gone_at = 1 WHERE first_seen > ?", loopClock)
	exec(t, d, "DELETE FROM open_loops")
	compute(t, d, loopClock)
	got := loopRows(t, d)
	if len(got) != len(want) {
		t.Errorf("loops = %d, golden %d", len(got), len(want))
	}
	for key, w := range want {
		if g := got[key]; g != w {
			t.Errorf("%s = %+v, golden %+v", key, g, w)
		}
	}
}
