package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

type gmailTokenFake struct {
	mu     sync.Mutex
	tokens []string
}

func (g *gmailTokenFake) handler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g.mu.Lock()
	g.tokens = append(g.tokens, "access-"+string(rune('0'+len(g.tokens)+1)))
	token := g.tokens[len(g.tokens)-1]
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "token_type": "Bearer", "expires_in": 3600,
	})
}

func (g *gmailTokenFake) issued() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.tokens...)
}

func xoauth2Line(user, token string) string {
	return base64.StdEncoding.EncodeToString([]byte("user=" + user + "\x01auth=Bearer " + token + "\x01\x01"))
}

func TestGmailXOAUTH2SyncRetriesARejectedTokenWithAFreshOne(t *testing.T) {
	fake := &gmailTokenFake{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	t.Setenv("ATRIUM_GOOGLE_TOKEN_URL", srv.URL)
	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	googleHostConfig(t, dir)
	d, err := db.Connect(filepath.Join(dir, "atrium.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(context.Background(), d, filepath.Join(dir, "atrium.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) " +
		"VALUES ('oauth', 'OAuth', 'gmail', 'atrium.test@gmail.test', 1, 0, 'xoauth2', 0)"); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	script := imapscript.MustParse(t, `
S: {gmail.greeting}
C: {t} CAPABILITY
S: * CAPABILITY {gmail.caps}
S: {t} OK CAPABILITY completed
C/ ^{t} AUTHENTICATE XOAUTH2 .*$
S: {t} NO [AUTHENTICATIONFAILED] invalid credentials
! close
! accept
S: {gmail.greeting}
C: {t} CAPABILITY
S: * CAPABILITY {gmail.caps}
S: {t} OK CAPABILITY completed
C: {t} AUTHENTICATE XOAUTH2 `+xoauth2Line("atrium.test@gmail.test", "access-2")+`
S: {t} OK [CAPABILITY {gmail.authcaps}] authenticated
C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: {t} OK LIST completed
C: {t} LOGOUT
S: * BYE
S: {t} OK
`)
	server, ca := imapscript.ServeTLS(t, script)
	t.Setenv("ATRIUM_IMAP_ADDR", server.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	var out, errs bytes.Buffer
	code := f.app.Run(context.Background(), []string{"sync", "--account", "oauth"}, &out, &errs)
	stdout, stderr := out.String(), errs.String()
	if code != 0 {
		t.Fatalf("mail sync: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := fake.issued(); len(got) != 2 || got[0] != "access-1" || got[1] != "access-2" {
		t.Fatalf("issued tokens = %v, want access-1 then access-2", got)
	}
}
