package mailtools_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
)

const vcf = `BEGIN:VCARD
VERSION:3.0
FN:Ada Lovelace
EMAIL:ada@example.test
TEL:+15550001
ORG:Analytical Engines
END:VCARD
BEGIN:VCARD
VERSION:3.0
FN:Grace Hopper
EMAIL:grace@navy.test
END:VCARD
`

func TestContactsLookupMatchesPeopleOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.vcf"), []byte(vcf), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := ctxFor(newDB(t), config.Config{ContactsDir: dir}, nil)
	out := mustCall(t, tc, "contacts_lookup", `{"query": "ada"}`)
	want := `{"results": [{"name": "Ada Lovelace", "emails": ["ada@example.test"], "phones": ["+15550001"], "org": "Analytical Engines"}]}`
	if got := py.JSONDumps(out); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	grace := list(t, get(t, mustCall(t, tc, "contacts_lookup", `{"query": "navy.test"}`), "results"))[0].(py.Dict)
	if get(t, grace, "name") != "Grace Hopper" {
		t.Errorf("grace = %s", py.JSONDumps(grace))
	}
	if got := list(t, get(t, mustCall(t, tc, "contacts_lookup", `{"query": "e", "limit": 1}`), "results")); len(got) != 1 {
		t.Errorf("limit 1 gave %d", len(got))
	}
}

func TestContactsLookupRequiresDirectory(t *testing.T) {
	tc := ctxFor(newDB(t), config.Config{}, nil)
	_, err := call(t, tc, "contacts_lookup", `{"query": "ada"}`)
	var nc tool.NotConfigured
	if !asNotConfigured(err, &nc) || nc != "ATRIUM_CONTACTS_DIR is not set" {
		t.Errorf("err = %v", err)
	}
	_, err = call(t, tc, "contacts_lookup", `{}`)
	wantToolError(t, err, "query is required")
}

func TestContactsLookupKeepsDates(t *testing.T) {
	dir := t.TempDir()
	card := "BEGIN:VCARD\nVERSION:3.0\nFN:Ada\nBDAY:--1017\nEND:VCARD\n"
	if err := os.WriteFile(filepath.Join(dir, "a.vcf"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := ctxFor(newDB(t), config.Config{ContactsDir: dir}, nil)
	out := mustCall(t, tc, "contacts_lookup", `{"query": "ada"}`)
	want := `{"results": [{"name": "Ada", "dates": [{"kind": "birthday", "label": "birthday", "month": 10, "day": 17, "year": null}]}]}`
	if got := py.JSONDumps(out); got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
