package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func TestMediaAgentCrashNeverResumes(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	var placements []protocol.Placement
	for i := range 300 {
		name := fmt.Sprintf("sub-%03d.srt", i)
		writeMediaFile(t, filepath.Join(a.roots["import"], "item", name), name)
		placements = append(placements, place("import", "item/"+name, name))
	}
	p := prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: protocol.Path{Root: "import", Rel: "item"}, Placements: placements})
	req := filingRequest("1", p)
	writeMediaFile(t, filepath.Join(a.roots["import"], "queued", "x.srt"), "queued")
	queued := filingRequest("3", prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: protocol.Path{Root: "import", Rel: "queued"}, Placements: []protocol.Placement{place("import", "queued/x.srt", "queued.srt")}}))
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPut, a.base+"/v1/filings/"+req.Key, bytes.NewReader(payload))
	request.Host = mediaAgentHost
	request.Header.Set("Authorization", "Bearer "+a.token)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		resp, err := mediaAgentClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	observed := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, b := a.get(t, "/v1/filings/"+req.Key+"?after=0&wait_seconds=1", nil)
		if resp.StatusCode == 404 {
			continue
		}
		var result protocol.FilingResult
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		if result.State == "running" && len(result.Steps) > 0 && len(result.Steps) < len(p.Plan.Steps) {
			observed = true
			break
		}
		if result.State != "running" {
			t.Fatalf("did not observe mid-filing state: %+v", result)
		}
	}
	if !observed {
		t.Fatal("never observed a confirmed in-progress receipt")
	}
	queuedPayload, _ := json.Marshal(queued)
	queuedRequest, _ := http.NewRequest(http.MethodPut, a.base+"/v1/filings/"+queued.Key, bytes.NewReader(queuedPayload))
	queuedRequest.Host = mediaAgentHost
	queuedRequest.Header.Set("Authorization", "Bearer "+a.token)
	queuedFinished := make(chan struct{})
	go func() {
		defer close(queuedFinished)
		if resp, err := mediaAgentClient.Do(queuedRequest); err == nil {
			_ = resp.Body.Close()
		}
	}()
	waitFor(t, func() bool {
		resp, _ := a.get(t, "/v1/filings/"+queued.Key, nil)
		return resp.StatusCode == 200
	})
	if err := a.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	filings := filepath.Join(a.dir, "state", "filings")
	if _, err := os.Stat(filepath.Join(filings, req.Key, "result.json")); !os.IsNotExist(err) {
		t.Fatalf("the filing finished before the kill: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(filings, queued.Key)); err != nil || len(entries) != 1 {
		t.Fatalf("the queued filing started before the kill: %v %v", entries, err)
	}
	if err := a.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-a.done
	<-finished
	<-queuedFinished
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	if res, body := putFiling(t, a, queued); res.State != "failed" || len(res.Steps) != 0 {
		t.Fatalf("queued filing recovered as %s", body)
	}
	if _, err := os.Stat(filepath.Join(a.roots["import"], "queued", "x.srt")); err != nil {
		t.Fatalf("the queued filing ran: %v", err)
	}
	result, body := putFiling(t, a, req)
	if result.State != "partial" || result.ErrorCode != protocol.CodeUnavailable {
		t.Fatalf("recovery=%s", body)
	}
	destinationCount := 0
	for i := range 300 {
		name := fmt.Sprintf("sub-%03d.srt", i)
		src, srcErr := os.ReadFile(filepath.Join(a.roots["import"], "item", name))
		dst, dstErr := os.ReadFile(filepath.Join(a.roots["tv"], name))
		if srcErr == nil && dstErr == nil || srcErr != nil && dstErr != nil {
			t.Fatalf("file duplicated or lost %s: src=%v dst=%v", name, srcErr, dstErr)
		}
		if srcErr == nil && string(src) != name || dstErr == nil && string(dst) != name {
			t.Fatalf("file content changed %s", name)
		}
		if dstErr == nil {
			destinationCount++
		}
	}
	if destinationCount != len(result.Steps) {
		t.Fatalf("confirmed=%d actual destinations=%d", len(result.Steps), destinationCount)
	}
	before := treeOf(t, a.dir)
	_, replay := putFiling(t, a, req)
	if !bytes.Equal(body, replay) || strings.Join(before, "\n") != strings.Join(treeOf(t, a.dir), "\n") {
		t.Fatal("recovery replay resumed writes")
	}
}
func TestMediaAgentSuspendedFilingBeyondShutdownBound(t *testing.T) {
	t.Parallel()
	t.Run("continued then terminated", func(t *testing.T) { t.Parallel(); suspendedFiling(t, false) })
	t.Run("terminated while stopped", func(t *testing.T) { t.Parallel(); suspendedFiling(t, true) })
}
func suspendedFiling(t *testing.T, terminateWhileStopped bool) {
	a := startMediaAgent(t, mediaAgentOpts{})
	var placements []protocol.Placement
	for i := range 300 {
		name := fmt.Sprintf("sub-%03d.srt", i)
		writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", name), name)
		placements = append(placements, place("downloads", "item/"+name, name))
	}
	p := prepareFiling(t, a, torrentPrepare(placements...))
	req := filingRequest("2", p)
	payload, _ := json.Marshal(req)
	request, _ := http.NewRequest(http.MethodPut, a.base+"/v1/filings/"+req.Key, bytes.NewReader(payload))
	request.Host = mediaAgentHost
	request.Header.Set("Authorization", "Bearer "+a.token)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		resp, err := mediaAgentClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	observed := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, b := a.get(t, "/v1/filings/"+req.Key+"?after=0&wait_seconds=1", nil)
		if resp.StatusCode == 404 {
			continue
		}
		var result protocol.FilingResult
		if json.Unmarshal(b, &result) != nil {
			t.Fatal("invalid progress")
		}
		if result.State == "running" && len(result.Steps) > 0 && len(result.Steps) < len(p.Plan.Steps) {
			observed = true
			break
		}
		if result.State != "running" {
			t.Fatal("filing finished before suspension")
		}
	}
	if !observed {
		t.Fatal("never observed running filing")
	}
	stepObserved := false
	for attempt := 0; attempt < 1000; attempt++ {
		if err := a.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
		receiptDir := filepath.Join(a.dir, "state", "filings", req.Key)
		entries, err := os.ReadDir(receiptDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".prepared") {
				done := strings.TrimSuffix(entry.Name(), ".prepared") + ".done"
				if _, err := os.Stat(filepath.Join(receiptDir, done)); os.IsNotExist(err) {
					stepObserved = true
					break
				}
			}
		}
		if stepObserved {
			break
		}
		if err := a.cmd.Process.Signal(syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Microsecond)
	}
	if !stepObserved {
		t.Fatal("never suspended an outstanding prepared step")
	}
	t.Cleanup(func() { _ = a.cmd.Process.Signal(syscall.SIGCONT) })
	time.Sleep(11 * time.Second)
	signals := []syscall.Signal{syscall.SIGCONT, syscall.SIGTERM}
	if terminateWhileStopped {
		signals = []syscall.Signal{syscall.SIGTERM, syscall.SIGCONT}
	}
	for _, sig := range signals {
		if err := a.cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
	}
	resent := false
	select {
	case <-a.done:
	case <-time.After(16 * time.Second):
		if !terminateWhileStopped || runtime.GOOS != "darwin" {
			t.Fatalf("shutdown exceeded bound after continuation: %s", a.logs.String())
		}
		if resp, _ := a.get(t, "/health", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("the resumed agent neither drained nor kept serving: %d", resp.StatusCode)
		}
		resent = true
		if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-a.done:
		case <-time.After(16 * time.Second):
			t.Fatalf("shutdown exceeded bound after a second SIGTERM: %s", a.logs.String())
		}
	}
	<-finished
	if a.err != nil {
		t.Fatalf("shutdown: %v", a.err)
	}
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	res, body := putFiling(t, a, req)
	if !resent && (res.State != "partial" || res.ErrorCode != protocol.CodeDraining) || resent && res.State != "partial" && res.State != "done" {
		t.Fatalf("shutdown result %s", body)
	}
	entries, err := os.ReadDir(a.roots["tv"])
	if err != nil || len(entries) != len(res.Steps) {
		t.Fatalf("shutdown receipt mismatch files=%d steps=%d err=%v", len(entries), len(res.Steps), err)
	}
	_, again := putFiling(t, a, req)
	if !bytes.Equal(body, again) {
		t.Fatal("shutdown replay changed")
	}
}
