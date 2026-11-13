package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/media"
)

func TestMediaSweepOnlyDeletesMediaTorrents(t *testing.T) {
	const mediaHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const plainHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	torrents := []media.Torrent{
		{Hash: mediaHash, Name: "Filed.Show", State: media.StoppedUP, Tags: "media,selected,filed"},
		{Hash: plainHash, Name: "Filed.Plain", State: media.StoppedUP, Tags: "filed"},
	}
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/torrents/info":
			out := torrents
			if r.URL.Query().Get("tag") == media.TagMedia {
				out = torrents[:1]
			}
			_ = json.NewEncoder(w).Encode(out)
		case "/api/v2/torrents/delete":
			_ = r.ParseForm()
			deleted = append(deleted, r.Form.Get("hashes"))
			_, _ = w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, srv.URL, "", "", ""))
	ran := runApp(t, "media", "sweep")
	if ran.code != cli.ExitOK {
		t.Fatalf("media sweep: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if len(deleted) != 1 || deleted[0] != mediaHash {
		t.Fatalf("deleted %v, want only %s", deleted, mediaHash)
	}
}

func TestMediaSweepJobFailsWhenQbittorrentFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	dir, _, f := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_HOST_CONFIG", mediaHost(t, dir, srv.URL, "", "", ""))
	ran := runApp(t, "media", "sweep")
	if ran.code != cli.ExitOK {
		t.Fatalf("media sweep: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	var out struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(ran.stdout), &out); err != nil || len(out.Errors) == 0 {
		t.Fatalf("media sweep output %q (%v), want reported errors", ran.stdout, err)
	}
	d := mustConnect(t, dir+"/atrium.db", keyFor(t))
	run := runJob(t, f, d, media.JobSweep)
	var status string
	if err := d.Read.QueryRow("SELECT status FROM job_runs WHERE id = ?", run).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "error" {
		t.Fatalf("media-sweep job status %q, want error", status)
	}
	var outcome string
	var jobRun sql.NullInt64
	if err := d.Read.QueryRow("SELECT outcome, job_run_id FROM operation_audit WHERE operation = 'media.sweep' ORDER BY id DESC LIMIT 1").Scan(&outcome, &jobRun); err != nil {
		t.Fatal(err)
	}
	if outcome == "ok" {
		t.Fatalf("media.sweep audit outcome %q, want a failure", outcome)
	}
	if jobRun.Int64 != run {
		t.Fatalf("media.sweep audit job run %v, want %d", jobRun, run)
	}
}
