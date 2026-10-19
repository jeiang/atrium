package rebalance_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const day = 86400

func named(context.Context, string, []string, []string) (string, error) { return "Named", nil }

func scan(t *testing.T, d *db.DB, ts float64) map[string]int {
	t.Helper()
	r, err := rebalance.Scan(context.Background(), d, ts, named)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]int{"candidates": r.Candidates, "created": r.Created}
}

func detect(t *testing.T, d *db.DB) []rebalance.Candidate {
	t.Helper()
	found, err := rebalance.Detect(context.Background(), d.Read)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func pendingOf(t *testing.T, d *db.DB, kind string) []*proposals.Proposal {
	t.Helper()
	out, err := proposals.List(context.Background(), d.Read, proposals.Pending, kind)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			d, _ := v.(py.Dict)
			v, _ = d.Get(k)
		case int:
			v = v.([]any)[k]
		}
	}
	return v
}

func mixedFolder(t *testing.T, d *db.DB) {
	uid := 1
	for i := range 30 {
		rulestest.Filed(t, d, uid, "Mixed", "a@x.test", "x.test", nil, rulestest.Fields{
			"subject": fmt.Sprintf("order %d", i), "subject_tmpl": "order <n>", "date_ts": float64(i)})
		uid++
	}
	for i := range 30 {
		rulestest.Filed(t, d, uid, "Mixed", "b@y.test", "y.test", nil, rulestest.Fields{
			"subject": fmt.Sprintf("digest %d", i), "subject_tmpl": "digest <n>", "date_ts": float64(i)})
		uid++
	}
}

func TestSplitSurfacesOnlyAfterTwoRunsAndResetsWhenItDisappears(t *testing.T) {
	d := rulestest.DB(t)
	mixedFolder(t, d)
	if got := scan(t, d, 0); got["candidates"] != 1 || got["created"] != 0 {
		t.Fatalf("first = %v", got)
	}
	if got := scan(t, d, day); got["created"] != 0 {
		t.Fatalf("second within the gap = %v", got)
	}
	if got := scan(t, d, rebalance.RunGapSeconds+1); got["created"] != 1 {
		t.Fatalf("second run = %v", got)
	}
	pending := pendingOf(t, d, rebalance.Split)
	if len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	p := pending[0]
	group := get(p.Preview, "groups", 0)
	if get(p.Preview, "folder") != "Mixed" || get(group, "name") != "Mixed/Named" || get(group, "count") != int64(30) {
		t.Fatalf("preview = %v", p.Preview)
	}
	if label := get(group, "senders", 0, "label"); len(get(group, "messages").([]any)) != 30 || (label != "x.test" && label != "y.test") {
		t.Fatalf("group = %v", group)
	}
	if len(get(group, "terms").([]any)) == 0 || len(get(group, "subjects").([]any)) == 0 {
		t.Fatalf("group = %v", group)
	}
	full, err := proposals.Get(context.Background(), d.Read, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := py.JSONLoads(full.Payload)
	if len(get(payload, "groups", 0, "messages").([]any)) != 30 {
		t.Fatalf("payload = %v", payload)
	}
	if got := scan(t, d, 2*rebalance.RunGapSeconds+2); got["created"] != 0 {
		t.Fatalf("third run = %v", got)
	}
	if n, _ := proposals.PendingCount(context.Background(), d.Read); n != 1 {
		t.Fatalf("pending count = %d", n)
	}

	gone := rulestest.DB(t)
	mixedFolder(t, gone)
	scan(t, gone, 0)
	rulestest.Exec(t, gone, "DELETE FROM messages")
	scan(t, gone, 1)
	if n := rulestest.Count(t, gone, "SELECT COUNT(*) FROM rebalance_candidates"); n != 0 {
		t.Fatalf("tracked after the folder emptied = %d", n)
	}
}

func TestSmallFolderOrSmallGroupsDoNotSplit(t *testing.T) {
	d := rulestest.DB(t)
	for i := range 30 {
		rulestest.Filed(t, d, i, "Small", fmt.Sprintf("u%d@d%d.test", i, i), fmt.Sprintf("d%d.test", i), nil, rulestest.Fields{"subject_tmpl": fmt.Sprintf("t%d", i)})
	}
	for i := 30; i < 70; i++ {
		rulestest.Filed(t, d, i, "Spread", fmt.Sprintf("u%d@d%d.test", i, i), fmt.Sprintf("d%d.test", i), nil, rulestest.Fields{"subject_tmpl": fmt.Sprintf("t%d", i)})
	}
	if found := detect(t, d); len(found) != 0 {
		t.Fatalf("detect = %+v", found)
	}
}

func TestEmbeddingClustersProposeSplitForMixedSenders(t *testing.T) {
	d := rulestest.DB(t)
	for i := range 60 {
		vector := rulestest.Vector(0)
		if i >= 30 {
			vector = rulestest.Vector(1)
		}
		rulestest.Filed(t, d, i, "Topics", fmt.Sprintf("u%d@d%d.test", i, i), fmt.Sprintf("d%d.test", i), vector, rulestest.Fields{"subject_tmpl": fmt.Sprintf("t%d", i)})
	}
	found := detect(t, d)
	if len(found) != 1 || found[0].Level != "embed" || len(found[0].Groups) != 1 || len(found[0].Groups[0].IDs) != 30 {
		t.Fatalf("detect = %+v", found)
	}
}

func TestMergeProposedWhenSenderMailLivesInAnotherFolder(t *testing.T) {
	d := rulestest.DB(t)
	uid := 1
	for i := range 35 {
		rulestest.Filed(t, d, uid, "Src", fmt.Sprintf("u%d@d%d.test", i, i), fmt.Sprintf("d%d.test", i), nil, rulestest.Fields{"subject_tmpl": fmt.Sprintf("t%d", i)})
		uid++
	}
	for range 5 {
		rulestest.Filed(t, d, uid, "Src", "s@z.test", "z.test", nil, rulestest.Fields{"subject_tmpl": "notice"})
		uid++
	}
	for range 12 {
		rulestest.Filed(t, d, uid, "Dst", "s@z.test", "z.test", nil, rulestest.Fields{"subject_tmpl": "notice"})
		uid++
	}
	var merges []rebalance.Candidate
	for _, c := range detect(t, d) {
		if c.Kind == rebalance.Merge {
			merges = append(merges, c)
		}
	}
	if len(merges) != 1 || merges[0].Into != "Dst" || merges[0].Folder != "Src" {
		t.Fatalf("merges = %+v", merges)
	}
	scan(t, d, 0)
	scan(t, d, rebalance.RunGapSeconds+1)
	pending := pendingOf(t, d, rebalance.Merge)
	if len(pending) != 1 {
		t.Fatalf("pending = %v", pending)
	}
	p := pending[0].Preview
	if get(p, "into") != "Dst" || py.JSONDumps(get(p, "sources")) != `[{"folder": "Src", "count": 5}]` ||
		get(p, "into_exists") != true || get(p, "group", "count") != int64(5) {
		t.Fatalf("preview = %v", p)
	}
	if pending[0].Title != "Move 5 messages from Src to Dst" {
		t.Fatalf("title = %q", pending[0].Title)
	}
}

func TestMergeRequiresNinetyPercentShare(t *testing.T) {
	d := rulestest.DB(t)
	uid := 1
	for i := range 35 {
		rulestest.Filed(t, d, uid, "Src", fmt.Sprintf("u%d@d%d.test", i, i), fmt.Sprintf("d%d.test", i), nil, rulestest.Fields{"subject_tmpl": fmt.Sprintf("t%d", i)})
		uid++
	}
	for range 5 {
		rulestest.Filed(t, d, uid, "Src", "s@z.test", "z.test", nil, nil)
		uid++
	}
	for _, f := range []struct {
		folder string
		n      int
	}{{"Dst", 9}, {"Other", 3}} {
		for range f.n {
			rulestest.Filed(t, d, uid, f.folder, "s@z.test", "z.test", nil, nil)
			uid++
		}
	}
	for _, c := range detect(t, d) {
		if c.Kind == rebalance.Merge {
			t.Fatalf("merge = %+v", c)
		}
	}
}

func TestCleanName(t *testing.T) {
	tens := "abcdefghij"
	for in, want := range map[string]string{
		"  Bank / Card|Statements\\\x01 ":       "Bank Card Statements",
		"":                                      "",
		"a\t\n b":                               "a b",
		tens + tens + tens + tens + " tail":     tens + tens + tens + tens,
		tens + tens + tens + "abcdefghi" + " x": tens + tens + tens + "abcdefghi",
	} {
		if got := rebalance.CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFingerprintSortsGroupKeys(t *testing.T) {
	a := rebalance.Candidate{Kind: rebalance.Split, Account: "icloud", Folder: "F", Level: "addr", Groups: []rebalance.Group{{Key: "b"}, {Key: "a"}}}
	b := rebalance.Candidate{Kind: rebalance.Merge, Account: "icloud", Folder: "F", Level: "addr", Groups: []rebalance.Group{{Key: "a"}, {Key: "b"}}, Into: "G"}
	if got := a.Fingerprint(); got != "rebalance_split:icloud:F::addr:0c483189a33a70b8" {
		t.Fatalf("split fingerprint = %q", got)
	}
	if got := b.Fingerprint(); got != "rebalance_merge:icloud:F:G:addr:0c483189a33a70b8" {
		t.Fatalf("merge fingerprint = %q", got)
	}
}
