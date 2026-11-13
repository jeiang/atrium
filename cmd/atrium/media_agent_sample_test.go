package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func sparseVideo(t *testing.T, path string, size int64) {
	t.Helper()
	mnpWrite(t, path, nil)
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

func sizedFiles(t *testing.T, dir string, size int64) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil && info.Size() == size {
				out = append(out, p)
			}
		}
		return nil
	})
	return out
}

func TestCleanupNeverDeletesRealVideosNamedLikeSamples(t *testing.T) {
	t.Parallel()
	const episode, film, other = 200 << 20, 210 << 20, 300 << 20
	dir := t.TempDir()
	_, qbURL := mnpNewQbit(t)
	agent, root := agentLibrary(t, dir, qbURL)
	pack := filepath.Join(root, "import", "Show.S02")
	mnpWrite(t, filepath.Join(pack, "Show.S02E01.Pilot.mkv"), []byte("pilot"))
	sparseVideo(t, filepath.Join(pack, "Show.S02E05.Free.Sample.mkv"), episode)
	folder := filepath.Join(root, "import", "Other.1999")
	sparseVideo(t, filepath.Join(folder, "Other.Film.1999.mkv"), other)
	sparseVideo(t, filepath.Join(folder, "Sample.People.2000.mkv"), film)
	small := filepath.Join(root, "import", "Small.2021")
	mnpWrite(t, filepath.Join(small, "Small.2021.mkv"), []byte("small bytes"))
	mnpWrite(t, filepath.Join(small, "sample.mkv"), []byte("sample"))

	seedAllFlagsIn(t, dir)
	b := mnpNewBrowser(t, startServeIn(t, dir))
	b.want(http.MethodGet, "/apps/media/downloads", nil, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {media.Import + "/Show.S02"}, "action": {"file"}, "kind": {"tv"}, "title": {"Show"}, "year": {"2020"},
		"s0": {"2"}, "e0": {"1"}, "s1": {"2"}, "e1": {"5"}}, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {media.Import + "/Other.1999"}, "action": {"file"}, "kind": {"movie"}, "title": {"Other"}, "year": {"1999"}}, http.StatusOK)
	b.want(http.MethodPost, "/apps/media/file", url.Values{"id": {media.Import + "/Small.2021"}, "action": {"file"}, "kind": {"movie"}, "title": {"Small"}, "year": {"2021"}}, http.StatusOK)

	if got := sizedFiles(t, filepath.Join(root, "tv"), episode); len(got) != 1 || !strings.Contains(filepath.Base(got[0]), "E05") {
		t.Fatalf("the episode named like a sample was not filed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(folder, "Sample.People.2000.mkv")); err != nil {
		t.Fatalf("the cleanup deleted a large film named like a sample: %v", err)
	}
	if got := sizedFiles(t, filepath.Join(root, "movies"), other); len(got) != 1 {
		t.Fatalf("the movie was not filed: %v", got)
	}
	if _, err := os.Stat(small); !os.IsNotExist(err) {
		t.Fatalf("the cleanup kept an import that holds only a small sample: %v", err)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), db.Key{})
	defer func() { _ = d.Close() }()
	if n := mediaEffectCount(t, d, media.OpImportCleanup, media.Import+"/Other.1999"); n != 0 {
		t.Fatalf("main asked to clean up a folder that holds a film: %d", n)
	}
	mediaEffectID(t, d, media.OpImportCleanup, media.Import+"/Small.2021", "done")

	item := protocol.Path{Root: protocol.RootImport, Rel: "Other.1999"}
	cleanup := protocol.PrepareRequest{SourceKind: protocol.SourceImportCleanup, Source: item, Cleanup: true}
	if status, body := agent.json(t, http.MethodPost, "/v1/filing/prepare", cleanup); status != http.StatusConflict {
		t.Fatalf("the agent prepared a cleanup that unlinks a film: %d %s", status, body)
	}
	mnpWrite(t, filepath.Join(root, "import", "Residue", "notes.txt"), []byte("notes"))
	base := prepareFiling(t, agent, protocol.PrepareRequest{SourceKind: protocol.SourceImportCleanup, Source: protocol.Path{Root: protocol.RootImport, Rel: "Residue"}, Cleanup: true}).Plan
	status, body := agent.json(t, http.MethodPost, "/v1/source", protocol.SourceRequest{Source: item})
	var src protocol.Source
	if status != http.StatusOK || json.Unmarshal(body, &src) != nil {
		t.Fatalf("source %d %s", status, body)
	}
	for _, f := range src.Files {
		if !strings.HasSuffix(f.Path.Rel, "Sample.People.2000.mkv") {
			continue
		}
		for i, lie := range []int64{f.Identity.Size, 1} {
			id := f.Identity
			id.Size = lie
			plan := base
			plan.Steps = []protocol.Step{{Index: base.Steps[0].Index, Kind: protocol.StepUnlink, Destination: f.Path, Expect: &id}}
			req := protocol.FilingRequest{Key: strings.Repeat("9", 63) + string(rune('0'+i)), Plan: plan, PlanDigest: protocol.CanonicalDigest(plan)}
			if status, body := agent.json(t, http.MethodPut, "/v1/filings/"+req.Key, req); status != http.StatusConflict || problemCode(t, body) != protocol.CodeDestinationExists {
				t.Fatalf("the agent accepted a plan that unlinks a film (size %d): %d %s", lie, status, body)
			}
		}
	}
	if info, err := os.Stat(filepath.Join(folder, "Sample.People.2000.mkv")); err != nil || info.Size() != film {
		t.Fatalf("the film named like a sample is gone: %v", err)
	}
}
