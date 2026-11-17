package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/apps/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/davmirror"
)

func connectionSnapshot(t *testing.T, dir string) string {
	t.Helper()
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	var revision, audits int64
	var state, reason, checked, results string
	if err := d.Read.QueryRow("SELECT revision, state, COALESCE(reason, ''), COALESCE(CAST(checked_at AS TEXT), ''), COALESCE(results_json, ''), (SELECT COUNT(*) FROM operation_audit) "+
		"FROM connections WHERE id = 'icloud/icloud'").Scan(&revision, &state, &reason, &checked, &results, &audits); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d|%s|%s|%s|%s|%d", revision, state, reason, checked, results, audits)
}

func traceSecretFree(t *testing.T, capability, out string) {
	t.Helper()
	for _, leak := range []string{davPassword, "operator", "icloud.invalid", "1234567890", "/42/", "WEBAUTH",
		base64.StdEncoding.EncodeToString([]byte("operator@icloud.invalid:" + davPassword))} {
		if strings.Contains(out, leak) {
			t.Errorf("the %s trace shows %q:\n%s", capability, leak, out)
		}
	}
}

func traceContains(t *testing.T, capability, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("the %s trace lacks %q:\n%s", capability, want, out)
		}
	}
}

func traceMatch(t *testing.T, capability, out, pattern string) []string {
	t.Helper()
	found := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if found == nil {
		t.Errorf("the %s trace does not match %q:\n%s", capability, pattern, out)
	}
	return found
}

func TestConnectionTraceShowsEachDAVExchangeWithoutSecretsOrSaving(t *testing.T) {
	dir, dav := partitionedICloud(t)
	if got := runApp(t, "connections", "list"); got.code != cli.ExitOK {
		t.Fatalf("connections list: %d %q", got.code, got.stderr)
	}
	before := connectionSnapshot(t, dir)

	card := runApp(t, "connections", "test", "icloud/icloud", "--trace", "carddav")
	if card.code != cli.ExitOK {
		t.Fatalf("carddav trace: %d %q %q", card.code, card.stdout, card.stderr)
	}
	traceSecretFree(t, "carddav", card.stdout)
	traceContains(t, "carddav", card.stdout,
		"capability: carddav\n",
		"#1 root step\n> PROPFIND https://"+frontHost+":"+dav.port+"/\n",
		"> header names: Authorization, Content-Type, Depth\n> authorization attached: yes\n> depth: 0\n> content-type: application/xml; charset=utf-8\n",
		"> user-agent: not set, Go sends its default\n",
		"<C:addressbook-home-set/>",
		"< HTTP/1.1 207 Multi-Status\n< time: ",
		"< Set-Cookie: <value withheld>\n",
		"< X-Apple-User-Partition: 7\n",
		"< HTTP/1.1 401 Unauthorized\n",
		"< Www-Authenticate: X-MobileMe-AuthToken realm=\"Newcastle\", Basic realm=\"Newcastle\"\n",
		"result: carddav ok 1 collection\n",
	)
	traceMatch(t, "carddav", card.stdout, `user: <user:[a-p]{6}>, from the mail account login; the same as the mail login: yes\n`)
	traceMatch(t, "carddav", card.stdout, `< time: [0-9]+ ms\n`)
	traceMatch(t, "carddav", card.stdout, `account <n:[a-p]{6}> for <user:[a-p]{6}>`)
	principal := traceMatch(t, "carddav", card.stdout, `<D:href>/<n:([a-p]{6})>/principal/</D:href>`)
	home := traceMatch(t, "carddav", card.stdout, `#2 home set step\n> PROPFIND https://`+regexp.QuoteMeta(frontHost+":"+dav.port)+`/<n:([a-p]{6})>/carddavhome/\n`)
	partitioned := traceMatch(t, "carddav", card.stdout, `#3 home set step\n> PROPFIND https://`+regexp.QuoteMeta(partitionHost+":"+dav.port)+`/<n:([a-p]{6})>/carddavhome/\n> header names: Authorization, Content-Type, Depth\n> authorization attached: yes\n> depth: 1\n`)
	if principal != nil && home != nil && partitioned != nil && (principal[1] != home[1] || home[1] != partitioned[1]) {
		t.Errorf("one account id masked to different hashes: %s, %s, %s", principal[1], home[1], partitioned[1])
	}
	if strings.Contains(card.stdout, "#4 ") {
		t.Errorf("the carddav trace made more requests than discovery:\n%s", card.stdout)
	}

	dav.set(func(p *partitionedDAV) { p.partitionRefuses = true })
	refused := runApp(t, "connections", "test", "icloud/icloud", "--trace", "carddav", "--repeat", "2")
	if refused.code == cli.ExitOK {
		t.Errorf("a refused carddav trace exited 0: %q", refused.stdout)
	}
	traceSecretFree(t, "refused carddav", refused.stdout)
	traceContains(t, "refused carddav", refused.stdout,
		"result: carddav failing iCloud accepted the app password but refused the home set request on the partition host",
		" repeat 1 of 2 of the home set step\n> PROPFIND https://"+partitionHost+":"+dav.port+"/<n:",
		" repeat 2 of 2 of the home set step\n",
		"\nrepeat: the home set request failed 2 of 2 times\n",
	)
	if n := strings.Count(refused.stdout, "home set step\n> PROPFIND https://"+partitionHost); n != 5 {
		t.Errorf("the refused trace sent %d partition home set requests, want the first, 2 retries and 2 repeats:\n%s", n, refused.stdout)
	}
	if got := runApp(t, "connections", "test", "icloud/icloud", "--trace", "carddav", "--repeat", "6"); got.code == cli.ExitOK {
		t.Errorf("--repeat 6 was accepted: %q", got.stdout)
	}

	if after := connectionSnapshot(t, dir); after != before {
		t.Errorf("tracing changed the database: %q, then %q", before, after)
	}
	if got := runApp(t, "connections", "test", "icloud/icloud", "--trace", "carddav", "--json"); got.code == cli.ExitOK {
		t.Errorf("--trace with --json was accepted: %q", got.stdout)
	}
}

func TestConnectionTraceRunsForCalDAVAndCardDAVSideBySide(t *testing.T) {
	requireCipher(t)
	setEnv(t, "ATRIUM_DB_KEY", rightKeyHex)
	dir, srv := davFixture(t)
	sharedICloudHost(t, dir, davPassword)
	t.Setenv(davmirror.CalDAVEnv, srv.URL+"/")
	t.Setenv(davmirror.CardDAVEnv, srv.URL+"/")
	srv.SetObject(caldavtest.BookPath+"alice.vcf", aliceCard)

	for capability, home := range map[string]string{"caldav": "<C:calendar-home-set/>", "carddav": "<C:addressbook-home-set/>"} {
		got := runApp(t, "connections", "test", "icloud/icloud", "--trace", capability)
		if got.code != cli.ExitOK {
			t.Fatalf("%s trace: %d %q %q", capability, got.code, got.stdout, got.stderr)
		}
		traceSecretFree(t, capability, got.stdout)
		traceContains(t, capability, got.stdout, "capability: "+capability+"\n", "#1 root step\n", "> authorization attached: yes\n", home, "result: "+capability+" ok ")
	}
}
