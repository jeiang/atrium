package gmaillabels_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rulestest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

func labelMessages(t *testing.T, d *db.DB, uid int, labels []string, n int, addr string) int {
	t.Helper()
	text, _ := json.Marshal(labels)
	for i := range n {
		rulestest.Add(t, d, uid+i, nil, "", rulestest.Fields{
			"account": "gmail", "mailbox_id": 2, "from_addr": addr, "from_domain": "g.test", "labels": string(text)})
	}
	return uid + n
}

func scan(t *testing.T, d *db.DB) int {
	t.Helper()
	r, err := gmaillabels.Scan(context.Background(), d, 1_792_080_000)
	if err != nil {
		t.Fatal(err)
	}
	return r.Created
}

func get(v any, key string) any {
	d, _ := v.(py.Dict)
	out, _ := d.Get(key)
	return out
}

func TestLabelMergeProposalsParentSiblingAndExclusions(t *testing.T) {
	d := rulestest.DB(t)
	uid := labelMessages(t, d, 1, []string{"Top/Small"}, 3, "a@g.test")
	uid = labelMessages(t, d, uid, []string{"Big"}, 12, "big@g.test")
	uid = labelMessages(t, d, uid, []string{"Solo"}, 2, "big@g.test")
	uid = labelMessages(t, d, uid, []string{"Lonely"}, 2, "nobody@g.test")
	uid = labelMessages(t, d, uid, []string{"Fam"}, 2, "a@g.test")
	uid = labelMessages(t, d, uid, []string{"Fam/Kid"}, 2, "a@g.test")
	labelMessages(t, d, uid, []string{`\Important`, "[Gmail]/Starred"}, 2, "a@g.test")
	if n := scan(t, d); n != 3 {
		t.Fatalf("created = %d", n)
	}
	all, err := proposals.List(context.Background(), d.Read, "", gmaillabels.Kind)
	if err != nil {
		t.Fatal(err)
	}
	got := map[[2]string]*proposals.Proposal{}
	for _, p := range all {
		got[[2]string{get(p.Preview, "from").(string), get(p.Preview, "into").(string)}] = p
	}
	want := [][2]string{{"Top/Small", "Top"}, {"Solo", "Big"}, {"Fam/Kid", "Fam"}}
	if len(got) != len(want) {
		t.Fatalf("proposals = %v", got)
	}
	for _, w := range want {
		if got[w] == nil {
			t.Fatalf("missing %v in %v", w, got)
		}
	}
	if n := scan(t, d); n != 0 {
		t.Fatalf("rescan created = %d", n)
	}
	solo := got[[2]string{"Solo", "Big"}]
	if get(solo.Preview, "from_count") != int64(2) || get(solo.Preview, "into_count") != int64(12) ||
		len(get(solo.Preview, "messages").([]any)) != 2 || solo.Title != "Merge label Solo into Big" {
		t.Fatalf("solo = %v", solo.Preview)
	}
	if acct := solo.Account; acct == nil || *acct != "gmail" {
		t.Fatalf("account = %v", acct)
	}
	full, err := proposals.Get(context.Background(), d.Read, solo.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := py.JSONLoads(full.Payload)
	if !reflect.DeepEqual([]any{get(payload, "label"), get(payload, "into"), len(get(payload, "messages").([]any))}, []any{"Solo", "Big", 2}) {
		t.Fatalf("payload = %v", payload)
	}
}
