//go:build !modernc

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/imapclient"
)

type keyedApp struct {
	t      *testing.T
	hex    string
	budget string
	path   string
	vars   map[string]string
	app    *cli.App
}

func newKeyedApp(t *testing.T, hex, path string) *keyedApp {
	t.Helper()
	dir := t.TempDir()
	vars := varsFor(t).snapshot()
	vars["ATRIUM_DB"] = path
	vars["ATRIUM_CACHE_DIR"] = filepath.Join(dir, "cache")
	vars["ATRIUM_PUBLIC_HOST"] = ""
	vars["ATRIUM_CONFIG"] = ""
	delete(vars, "ATRIUM_DB_KEY")
	delete(vars, "ATRIUM_DB_KEY_FILE")
	if hex != "" {
		vars["ATRIUM_DB_KEY"] = hex
	}
	vars["CREDENTIALS_DIRECTORY"] = ""
	vars["ATRIUM_SERVE_ADDR_FILE"] = filepath.Join(dir, "serve-addr")
	return &keyedApp{t: t, hex: hex, path: path, vars: vars, app: newAppWith(config.Map(vars), imapclient.Opener, nil, quietLog)}
}

func (a *keyedApp) run(args ...string) appRun {
	var out, errs bytes.Buffer
	code := a.app.Run(context.Background(), args, &out, &errs)
	return appRun{code, out.String(), errs.String()}
}

func (a *keyedApp) key() db.Key {
	if a.hex == "" {
		return db.Key{}
	}
	k, err := db.ParseKey(a.hex)
	if err != nil {
		a.t.Fatal(err)
	}
	return k
}

func (a *keyedApp) serve() (string, func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	logs := &lockedBuffer{}
	var exit atomic.Int32
	exit.Store(-1)
	done := make(chan int, 1)
	go func() {
		code := a.app.Run(ctx, []string{"serve", "--port", "0", "--auth", "local"}, io.Discard, logs)
		exit.Store(int32(code))
		done <- code
	}()
	stop := func() error {
		cancel()
		select {
		case code := <-done:
			if code != cli.ExitOK {
				return errors.New("serve exited " + logs.String())
			}
			return nil
		case <-time.After(15 * time.Second):
			return errors.New("serve did not stop on cancel")
		}
	}
	base, err := waitForServeAddr(a.t, a.vars["ATRIUM_SERVE_ADDR_FILE"], logs, &exit, cancel)
	if err != nil {
		_ = stop()
		return "", nil
	}
	return base, stop
}

func (a *keyedApp) roundTrip() error {
	if ran := a.run("settings", "set", "monthly_budget_usd", a.budget); ran.code != cli.ExitOK {
		return errors.New("settings set: " + ran.stderr)
	}
	base, stop := a.serve()
	if stop == nil {
		return errors.New("serve did not start")
	}
	resp, err := http.Get(base + "/activity?category=settings")
	if err != nil {
		_ = stop()
		return err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		_ = stop()
		return err
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), ">"+a.budget+"<") {
		_ = stop()
		return errors.New("activity lacks the budget this app wrote: " + resp.Status)
	}
	return stop()
}

func (a *keyedApp) budgetRow(k db.Key) (string, error) {
	d, err := db.Connect(a.path, db.WithKey(k))
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Close() }()
	var v string
	err = d.Read.QueryRow("SELECT value FROM settings WHERE key = 'monthly_budget_usd'").Scan(&v)
	return v, err
}

func TestDifferentlyKeyedAppsRoundTripConcurrently(t *testing.T) {
	t.Cleanup(func() { db.SetDefaultKey(db.Key{}) })
	apps := []*keyedApp{
		newKeyedApp(t, rightKeyHex, filepath.Join(t.TempDir(), "atrium.db")),
		newKeyedApp(t, otherKeyHex, filepath.Join(t.TempDir(), "atrium.db")),
	}
	for i, a := range apps {
		a.budget = []string{"211", "322", "433"}[i]
	}
	var wg sync.WaitGroup
	for _, a := range apps {
		wg.Go(func() {
			if err := a.roundTrip(); err != nil {
				t.Errorf("app keyed %t: %v", a.hex != "", err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	for i, a := range apps {
		if got := isPlaintextSQLite(t, a.path); got != (a.hex == "") {
			t.Errorf("app %d: plaintext header %v with key configured %v", i, got, a.hex != "")
		}
		if v, err := a.budgetRow(a.key()); err != nil || v != a.budget {
			t.Errorf("app %d reopened with its own key: %q, %v; want %q", i, v, err, a.budget)
		}
		other := apps[(i+1)%len(apps)]
		if _, err := a.budgetRow(other.key()); !errors.Is(err, refusalFor(a, other)) {
			t.Errorf("app %d opened with app %d's key: %v, want %v", i, (i+1)%len(apps), err, refusalFor(a, other))
		}
		cross := newKeyedApp(t, other.hex, a.path)
		if ran := cross.run("settings", "list"); ran.code == cli.ExitOK || !strings.Contains(ran.stderr, string(refusalFor(a, other))) {
			t.Errorf("app %d's database under app %d's key: exit %d stderr %q, want %q", i, (i+1)%len(apps), ran.code, ran.stderr, refusalFor(a, other))
		}
	}
}

func refusalFor(owner, opener *keyedApp) db.KeyError {
	switch {
	case owner.hex == "":
		return db.ErrPlaintextDB
	case opener.hex == "":
		return db.ErrKeyMissing
	default:
		return db.ErrWrongKey
	}
}
