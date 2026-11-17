package main

import (
	"bytes"
	"context"
	"errors"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/apps/mail/imapclient"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
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
	if err := budgetServedG1(base, a.budget); err != nil {
		_ = stop()
		return err
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
	t.Parallel()
	apps := []*keyedApp{
		newKeyedApp(t, rightKeyHex, filepath.Join(t.TempDir(), "atrium.db")),
		newKeyedApp(t, otherKeyHex, filepath.Join(t.TempDir(), "atrium.db")),
		newKeyedApp(t, "", filepath.Join(t.TempDir(), "atrium.db")),
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

func webReadG1(base, path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Atrium-Client", "web")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New(path + ": " + resp.Status + " " + string(body))
	}
	return json.Unmarshal(body, out)
}

func budgetServedG1(base, budget string) error {
	var activity struct {
		Data struct {
			Items []struct {
				Operation string `json:"operation"`
				Target    string `json:"target"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := webReadG1(base, "/web/v1/activity?category=settings", &activity); err != nil {
		return err
	}
	logged := false
	for _, item := range activity.Data.Items {
		logged = logged || (item.Operation == "settings.set" && item.Target == "monthly_budget_usd")
	}
	if !logged {
		return errors.New("activity lacks the budget this app wrote")
	}
	cursor := ""
	for {
		var read struct {
			Data struct {
				Items []struct {
					Key   string  `json:"key"`
					Value *string `json:"value"`
				} `json:"items"`
				NextCursor *string `json:"next_cursor"`
			} `json:"data"`
		}
		if err := webReadG1(base, "/web/v1/settings?limit=100&cursor="+url.QueryEscape(cursor), &read); err != nil {
			return err
		}
		for _, row := range read.Data.Items {
			if row.Key == "monthly_budget_usd" {
				if row.Value == nil || *row.Value != budget {
					return errors.New("settings serve a budget this app did not write")
				}
				return nil
			}
		}
		if read.Data.NextCursor == nil || *read.Data.NextCursor == "" {
			return errors.New("settings lack the budget this app wrote")
		}
		cursor = *read.Data.NextCursor
	}
}
