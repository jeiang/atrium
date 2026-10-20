package scans_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

const (
	scan1 = 1_792_096_200.0
	scan2 = 1_793_392_200.0
)

type stored struct {
	kind, account, title, payload, preview string
	created                                float64
}

const scanned = "kind IN ('rebalance_split', 'rebalance_merge', 'deletion_rule', 'gmail_label_merge')"

func storedProposals(t *testing.T, d *db.DB) map[string]stored {
	t.Helper()
	rows, err := d.Read.Query("SELECT fingerprint, kind, account, title, payload, preview, created_ts FROM proposals WHERE " + scanned)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]stored{}
	_, err = db.Collect(rows, func(r *sql.Rows) (bool, error) {
		var fp string
		var s stored
		err := r.Scan(&fp, &s.kind, &s.account, &s.title, &s.payload, &s.preview, &s.created)
		out[fp] = s
		return true, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScannersRebuildTheGoldenProposals(t *testing.T) {
	d, _ := dbtest.Copy(t, filepath.Join(golden.Dir(), "atrium.db"))
	want := storedProposals(t, d)
	if len(want) != 5 {
		t.Fatalf("golden scanned proposals = %d", len(want))
	}
	if _, err := d.Write.Exec("DELETE FROM proposals WHERE " + scanned); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := deletions.Scan(ctx, d, scan1); err != nil {
		t.Fatal(err)
	}
	if _, err := gmaillabels.Scan(ctx, d, scan1); err != nil {
		t.Fatal(err)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "{\"name\": \"Shop B\"}"}}]}`))
	}))
	defer model.Close()
	if _, err := rebalance.Scan(ctx, d, scan2, rebalance.SuggestName(model.URL)); err != nil {
		t.Fatal(err)
	}
	got := storedProposals(t, d)
	if len(got) != len(want) {
		t.Fatalf("rebuilt %d proposals, want %d", len(got), len(want))
	}
	for fp, w := range want {
		g, ok := got[fp]
		if !ok {
			t.Errorf("missing %s", fp)
			continue
		}
		if g.kind != w.kind || g.account != w.account || g.title != w.title || g.created != w.created {
			t.Errorf("%s = %+v, want %+v", fp, g, w)
		}
		golden.EqualJSON(t, []byte(w.payload), []byte(g.payload))
		golden.EqualJSON(t, []byte(w.preview), []byte(g.preview))
		if g.payload != w.payload || g.preview != w.preview {
			t.Errorf("%s: stored JSON text differs from json.dumps", fp)
		}
	}
}
