package watches_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type exchange struct {
	Feature string          `json:"feature"`
	Request json.RawMessage `json:"request"`
	Reply   json.RawMessage `json:"reply"`
}

// TestPackageRequestsMatchTheGoldenBodies rescans the golden mail against a llama.cpp stand-in that accepts only
// the request bodies Python sent and expects every recorded package request to be sent. The clock is one day
// after t0, inside the 60-day window of all of the mail.
func TestPackageRequestsMatchTheGoldenBodies(t *testing.T) {
	var all []exchange
	if err := json.Unmarshal(golden.ReadFile(t, "pipeline", "llm_exchanges.json"), &all); err != nil {
		t.Fatal(err)
	}
	want := map[string]exchange{}
	for _, e := range all {
		if e.Feature == "package" {
			key, err := golden.Canonical(e.Request)
			if err != nil {
				t.Fatal(err)
			}
			want[string(key)] = e
		}
	}
	if len(want) == 0 {
		t.Fatal("no package exchanges recorded")
	}
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	wantPackages := packageSummaries(t, d)
	exec(t, d, "DELETE FROM watch_scans")
	exec(t, d, "DELETE FROM packages")
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
			t.Errorf("request body is not one Python sent:\n%s", body)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		sent[string(key)] = true
		reply, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(e.Reply)}}}})
		_, _ = w.Write(reply)
	}))
	defer srv.Close()
	o := watches.Options{Clock: func() float64 { return now }, Env: env()}
	if _, err := watches.ScanPackages(ctx, d, config.Config{ChatURL: srv.URL}, 1000, 1792166400, o); err != nil {
		t.Fatal(err)
	}
	if got := packageSummaries(t, d); !slices.Equal(got, wantPackages) || len(got) == 0 {
		t.Errorf("packages = %q\nwant %q", got, wantPackages)
	}
	for key, e := range want {
		if !sent[key] {
			t.Errorf("recorded request never sent: %s", e.Request)
		}
	}
}

func packageSummaries(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Read.Query("SELECT carrier || '|' || tracking_number || '|' || merchant || '|' || merchant_key || '|' || order_ref || '|' || " +
		"status || '|' || status_text FROM packages ORDER BY tracking_number, order_ref")
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var s string
		return s, r.Scan(&s)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
