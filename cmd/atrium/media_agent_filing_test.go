package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

func (a *mediaAgent) json(t *testing.T, method, target string, v any) (int, []byte) {
	t.Helper()
	var b []byte
	var err error
	if raw, ok := v.(string); ok {
		b = []byte(raw)
	} else {
		b, err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, a.base+target, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = mediaAgentHost
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := mediaAgentClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}
func prepareFiling(t *testing.T, a *mediaAgent, req protocol.PrepareRequest) protocol.PrepareResponse {
	t.Helper()
	status, body := a.json(t, http.MethodPost, "/v1/filing/prepare", req)
	if status != 200 {
		t.Fatalf("prepare status=%d body=%s", status, body)
	}
	var p protocol.PrepareResponse
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.PlanDigest != protocol.CanonicalDigest(p.Plan) {
		t.Fatal("digest mismatch")
	}
	return p
}
func filingRequest(key string, p protocol.PrepareResponse) protocol.FilingRequest {
	return protocol.FilingRequest{Key: strings.Repeat(key, 64), Plan: p.Plan, PlanDigest: p.PlanDigest}
}
func putFiling(t *testing.T, a *mediaAgent, req protocol.FilingRequest) (protocol.FilingResult, []byte) {
	t.Helper()
	status, body := a.json(t, http.MethodPut, "/v1/filings/"+req.Key, req)
	if status != 200 {
		t.Fatalf("filing status=%d body=%s", status, body)
	}
	var result protocol.FilingResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result, body
}
func torrentPrepare(placements ...protocol.Placement) protocol.PrepareRequest {
	return protocol.PrepareRequest{SourceKind: protocol.SourceTorrent, TorrentHash: strings.Repeat("a", 40), Source: protocol.Path{Root: "downloads", Rel: "item"}, Placements: placements}
}
func place(root, src, dst string) protocol.Placement {
	return protocol.Placement{Source: protocol.Path{Root: root, Rel: src}, Destination: protocol.Path{Root: "tv", Rel: dst}}
}
func mediaTreeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(b))
		}
		out[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestMediaAgentPrepareWritesNothingAndStrictDecode(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "ep.mkv"), "episode")
	before := mediaTreeSnapshot(t, a.dir)
	p := prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "Show/Season/ep.mkv")))
	if !reflect.DeepEqual(before, mediaTreeSnapshot(t, a.dir)) {
		t.Fatal("prepare changed filesystem state")
	}
	for _, body := range []string{`{"source":{"root":"downloads","ROOT":"import","rel":"item"}}`, `{"source":{"root":"downloads","rel":"item","re\u017f":"x"}}`, `{"source":{"root":"downloads","rel":"item"},"SOURCE":{"root":"import","rel":"item"}}`, `{"source":{"root":"downloads","rel":"item"},"\u017fource":{"root":"import","rel":"item"}}`, `{"key":"a","\u212aey":"b"}`, strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70)} {
		status, b := a.json(t, http.MethodPost, "/v1/source", body)
		if status != 400 || problemCode(t, b) != "invalid_request" {
			t.Fatalf("strict decode: %d %s", status, b)
		}
	}
	req := filingRequest("9", p)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"KEY", "\u212aey"} {
		body := `{"` + alias + `":"` + req.Key + `",` + string(raw[1:])
		status, b := a.json(t, http.MethodPut, "/v1/filings/"+req.Key, body)
		if status != 400 || problemCode(t, b) != "invalid_request" {
			t.Fatalf("folded duplicate %d %s", status, b)
		}
	}
	if _, err := os.Stat(filepath.Join(a.roots["tv"], "Show")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("duplicate request mutated filesystem")
	}
	status, body := a.json(t, http.MethodPost, "/v1/source", protocol.SourceRequest{Source: protocol.Path{Root: "downloads", Rel: "item"}})
	var source protocol.Source
	if status != 200 || json.Unmarshal(body, &source) != nil || len(source.Files) != 1 {
		t.Fatalf("source: %d %s", status, body)
	}
}
func TestMediaAgentFilingIsRepeatSafe(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	for _, name := range []string{"ep.mkv", "ep.srt"} {
		writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", name), name)
	}
	p := prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "Show/Season/ep.mkv"), place("downloads", "item/ep.srt", "Show/Season/ep.srt")))
	req := filingRequest("a", p)
	var results [2][]byte
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			result, b := putFiling(t, a, req)
			if result.State != "done" {
				t.Errorf("filing=%+v", result)
			}
			results[i] = b
		})
	}
	wg.Wait()
	if !bytes.Equal(results[0], results[1]) {
		t.Fatal("concurrent replay changed result")
	}
	_, again := putFiling(t, a, req)
	if !bytes.Equal(again, results[0]) {
		t.Fatal("replay changed result")
	}
	for _, name := range []string{"ep.mkv", "ep.srt"} {
		src, err := os.Stat(filepath.Join(a.roots["downloads"], "item", name))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.Stat(filepath.Join(a.roots["tv"], "Show", "Season", name))
		if err != nil || !os.SameFile(src, dst) {
			t.Fatalf("hardlink %s: %v", name, err)
		}
	}
	before := treeOf(t, a.dir)
	req.Plan.TorrentHash = strings.Repeat("b", 40)
	req.PlanDigest = protocol.CanonicalDigest(req.Plan)
	status, b := a.json(t, http.MethodPut, "/v1/filings/"+req.Key, req)
	if status != 409 || problemCode(t, b) != "key_reused" || !slices.Equal(before, treeOf(t, a.dir)) {
		t.Fatalf("key reuse: %d %s", status, b)
	}
	req.Plan = p.Plan
	req.PlanDigest = p.PlanDigest
	status, b = a.json(t, http.MethodPost, "/v1/filings/"+req.Key+"/inspect", struct{}{})
	var inspection protocol.Inspection
	if status != 200 || json.Unmarshal(b, &inspection) != nil || len(inspection.Evidence) != 4 {
		t.Fatalf("inspection %d %s", status, b)
	}
	a.terminate(t)
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	_, again = putFiling(t, a, req)
	if !bytes.Equal(again, results[0]) {
		t.Fatal("restart replay changed result")
	}
	status, b = a.json(t, http.MethodPost, "/v1/entries", protocol.EntriesRequest{Path: protocol.Path{Root: "tv", Rel: "Show/Season"}})
	var entries protocol.Entries
	if status != 200 || json.Unmarshal(b, &entries) != nil || len(entries.Entries) != 2 {
		t.Fatalf("entries %d %s", status, b)
	}
}
func TestMediaAgentImportRenameAndUndo(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	src := filepath.Join(a.roots["import"], "item", "ep.mkv")
	writeMediaFile(t, src, "episode")
	beforeInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	p := prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: protocol.Path{Root: "import", Rel: "item"}, Placements: []protocol.Placement{place("import", "item/ep.mkv", "Show/ep.mkv")}})
	req := filingRequest("b", p)
	res, _ := putFiling(t, a, req)
	if res.State != "done" {
		t.Fatalf("rename=%+v", res)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rename retained source")
	}
	destinationInfo, err := os.Stat(filepath.Join(a.roots["tv"], "Show", "ep.mkv"))
	if err != nil || beforeInfo.Mode() != destinationInfo.Mode() || beforeInfo.Sys().(*syscall.Stat_t).Gid != destinationInfo.Sys().(*syscall.Stat_t).Gid || !os.SameFile(beforeInfo, destinationInfo) {
		t.Fatalf("rename changed inode or permissions: %v", err)
	}
	undo := protocol.FilingRequest{Key: protocol.UndoKey(req.Key), UndoOf: req.Key}
	res, body := putFiling(t, a, undo)
	if res.State != "done" {
		t.Fatalf("undo=%+v", res)
	}
	_, replay := putFiling(t, a, undo)
	if !bytes.Equal(body, replay) {
		t.Fatal("undo replay changed")
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "episode" {
		t.Fatalf("undo source %s %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(a.roots["tv"], "Show")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo retained empty directory")
	}
}
func TestMediaAgentRefusesUnsafeFilingsAndPreservesGroups(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	src := filepath.Join(a.roots["downloads"], "item", "ep.mkv")
	writeMediaFile(t, src, "episode")
	writeMediaFile(t, filepath.Join(a.roots["tv"], "existing.mkv"), "keep")
	for _, dst := range []string{"existing.mkv", "../escape.mkv", "/absolute.mkv", "show\\ep.mkv", "show//ep.mkv", strings.Repeat("x", 300)} {
		status, _ := a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/ep.mkv", dst)))
		if status == 200 {
			t.Fatalf("accepted hostile destination %q", dst)
		}
	}
	outside := filepath.Join(a.dir, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(a.roots["tv"], "symlink")); err != nil {
		t.Fatal(err)
	}
	status, _ := a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/ep.mkv", "symlink/ep.mkv")))
	if status == 200 {
		t.Fatal("accepted symlink parent")
	}
	if err := os.Symlink(src, filepath.Join(a.roots["downloads"], "item", "link.mkv")); err != nil {
		t.Fatal(err)
	}
	status, _ = a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/link.mkv", "ep.mkv")))
	if status == 200 {
		t.Fatal("accepted symlink source")
	}
	p := prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "ep.mkv")))
	req := filingRequest("c", p)
	writeMediaFile(t, filepath.Join(a.roots["tv"], "ep.mkv"), "raced destination")
	res, b := putFiling(t, a, req)
	if res.State != "failed" || res.ErrorCode != "destination_exists" {
		t.Fatalf("collision=%+v", res)
	}
	_, replay := putFiling(t, a, req)
	if !bytes.Equal(b, replay) {
		t.Fatal("failed receipt was retried")
	}
	if b, err := os.ReadFile(filepath.Join(a.roots["tv"], "ep.mkv")); err != nil || string(b) != "raced destination" {
		t.Fatal("overwrote collision")
	}
	p = prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "undo.mkv")))
	req = filingRequest("d", p)
	res, _ = putFiling(t, a, req)
	if res.State != "done" {
		t.Fatal(res)
	}
	dst := filepath.Join(a.roots["tv"], "undo.mkv")
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	writeMediaFile(t, dst, "replacement")
	res, _ = putFiling(t, a, protocol.FilingRequest{Key: protocol.UndoKey(req.Key), UndoOf: req.Key})
	if res.State != "failed" || res.ErrorCode != "identity_changed" {
		t.Fatalf("unsafe undo=%+v", res)
	}
	if b, _ := os.ReadFile(dst); string(b) != "replacement" {
		t.Fatal("undo removed replacement")
	}
	a.terminate(t)
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir, group: fmt.Sprint(os.Getgid())})
	if err := os.Chmod(src, 0o600); err != nil {
		t.Fatal(err)
	}
	status, b = a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/ep.mkv", "Wrong/ep.mkv")))
	if status != 422 || problemCode(t, b) != "permissions" {
		t.Fatalf("unreadable group source %d %s", status, b)
	}
	if err := os.Chmod(src, 0o644); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(a.dir, "setgid-probe")
	if err := os.Mkdir(probe, 0o775); err != nil {
		t.Fatal(err)
	}
	setgidRefused := errors.Is(os.Chmod(probe, os.ModeSetgid|0o775), syscall.EPERM)
	p = prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "Group/ep.mkv")))
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = putFiling(t, a, filingRequest("e", p))
	after, err := os.Stat(src)
	if err != nil || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("changed download permissions")
	}
	if setgidRefused {
		if res.State != "failed" || res.ErrorCode != "permissions" || len(res.Steps) != 0 {
			t.Fatalf("refused setgid directory=%+v", res)
		}
		if _, err := os.Lstat(filepath.Join(a.roots["tv"], "Group")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused setgid directory left behind: %v", err)
		}
	}
	t.Run("setgid group directory", func(t *testing.T) {
		if setgidRefused {
			t.Skip("this environment refuses setgid bits, so a setgid group directory cannot be created")
		}
		if res.State != "done" {
			t.Fatal(res)
		}
		info, err := os.Stat(filepath.Join(a.roots["tv"], "Group"))
		if err != nil || info.Sys().(*syscall.Stat_t).Gid != uint32(os.Getgid()) || info.Mode().Perm() != 0o775 {
			t.Fatalf("directory permissions %v %v", info, err)
		}
		if runtime.GOOS == "linux" && info.Mode()&os.ModeSetgid == 0 {
			t.Fatal("setgid missing")
		}
	})
}
func TestMediaAgentCrossDeviceFilingRefused(t *testing.T) {
	t.Parallel()
	var primary unix.Stat_t
	if err := unix.Stat(t.TempDir(), &primary); err != nil {
		t.Fatal(err)
	}
	var other string
	for _, candidate := range []string{"/dev/shm", "/Volumes", "/private/tmp"} {
		var st unix.Stat_t
		if unix.Stat(candidate, &st) == nil && st.Dev != primary.Dev {
			d, err := os.MkdirTemp(candidate, "atrium-agent-")
			if err == nil {
				other = d
				break
			}
		}
	}
	if other == "" {
		t.Skip("no writable second filesystem available")
	}
	defer func() { _ = os.RemoveAll(other) }()
	dir := t.TempDir()
	config, roots, _ := writeMediaAgentConfig(t, dir, mediaAgentOpts{})
	rewrite(t, config, `tv = "`+roots["tv"]+`"`, `tv = "`+other+`"`)
	a := startMediaAgent(t, mediaAgentOpts{dir: dir, tv: other})
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "ep.mkv"), "episode")
	status, b := a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/ep.mkv", "ep.mkv")))
	if status != 422 || problemCode(t, b) != "cross_device" {
		t.Fatalf("cross device %d %s", status, b)
	}
	if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
		t.Fatal("cross-device refusal wrote files")
	}
}
func TestMediaAgentImportCleanupAndPagination(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	for i := range 501 {
		writeMediaFile(t, filepath.Join(a.roots["import"], fmt.Sprintf("entry-%03d.txt", i)), "x")
	}
	resp, b := a.get(t, "/v1/imports", nil)
	var page protocol.ImportPage
	if resp.StatusCode != 200 || json.Unmarshal(b, &page) != nil || len(page.Entries) != 500 || page.Next == "" {
		t.Fatalf("imports %d %s", resp.StatusCode, b)
	}
	resp, b = a.get(t, "/v1/imports?cursor="+url.QueryEscape(page.Next), nil)
	if resp.StatusCode != 200 || json.Unmarshal(b, &page) != nil || len(page.Entries) != 1 || page.Next != "" {
		t.Fatalf("imports next %d %s", resp.StatusCode, b)
	}
	resp, b = a.get(t, "/v1/imports", nil)
	if resp.StatusCode != 200 || json.Unmarshal(b, &page) != nil || page.Next == "" {
		t.Fatalf("imports before restart %d %s", resp.StatusCode, b)
	}
	expired := page.Next
	a.terminate(t)
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	resp, b = a.get(t, "/v1/imports?cursor="+url.QueryEscape(expired), nil)
	if resp.StatusCode != http.StatusGone || problemCode(t, b) != "snapshot_expired" {
		t.Fatalf("restart cursor %d %s", resp.StatusCode, b)
	}
	writeMediaFile(t, filepath.Join(a.roots["import"], "item", "ep.mkv"), "video")
	req := protocol.PrepareRequest{SourceKind: protocol.SourceImportCleanup, Source: protocol.Path{Root: "import", Rel: "item"}, Cleanup: true}
	status, _ := a.json(t, http.MethodPost, "/v1/filing/prepare", req)
	if status != 409 {
		t.Fatal("cleanup accepted unfiled video")
	}
	if err := os.Remove(filepath.Join(a.roots["import"], "item", "ep.mkv")); err != nil {
		t.Fatal(err)
	}
	writeMediaFile(t, filepath.Join(a.roots["import"], "item", "sub", "readme.txt"), "residue")
	p := prepareFiling(t, a, req)
	res, _ := putFiling(t, a, filingRequest("f", p))
	if res.State != "done" {
		t.Fatal(res)
	}
	if _, err := os.Stat(filepath.Join(a.roots["import"], "item")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleanup retained item")
	}
}
