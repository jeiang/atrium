package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func dirIdentity(t *testing.T, path string) protocol.Identity {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return protocol.Identity{Device: uint64(st.Dev), Inode: st.Ino}
}

func writeReceiptFile(t *testing.T, dir, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, ok := v.([]byte)
	if !ok {
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAgentRecoveryReportsTheTrueState(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	item := filepath.Join(a.roots["downloads"], "item")
	for _, name := range []string{"a.mkv", "b1.mkv", "b2.mkv", "c.mkv"} {
		writeMediaFile(t, filepath.Join(item, name), name)
	}
	plan1 := prepareFiling(t, a, torrentPrepare(place("downloads", "item/a.mkv", "a.mkv")))
	plan2 := prepareFiling(t, a, torrentPrepare(place("downloads", "item/b1.mkv", "b1.mkv"), place("downloads", "item/b2.mkv", "b2.mkv")))
	plan3 := prepareFiling(t, a, torrentPrepare(place("downloads", "item/c.mkv", "c.mkv")))
	a.terminate(t)
	filings := filepath.Join(a.dir, "state", "filings")
	prepared := func(p protocol.PrepareResponse) map[string]any {
		return map[string]any{"step": p.Plan.Steps[0], "parent": dirIdentity(t, a.roots["tv"]), "source_parent": dirIdentity(t, item)}
	}
	notStarted, linked, replaced := filingRequest("1", plan1), filingRequest("2", plan2), filingRequest("3", plan3)
	for _, c := range []struct {
		req  protocol.FilingRequest
		plan protocol.PrepareResponse
	}{{notStarted, plan1}, {linked, plan2}, {replaced, plan3}} {
		dir := filepath.Join(filings, c.req.Key)
		writeReceiptFile(t, dir, "plan.json", c.req)
		writeReceiptFile(t, dir, "step-0.prepared", prepared(c.plan))
	}
	if err := os.Link(filepath.Join(item, "b1.mkv"), filepath.Join(a.roots["tv"], "b1.mkv")); err != nil {
		t.Fatal(err)
	}
	writeMediaFile(t, filepath.Join(a.roots["tv"], "c.mkv"), "someone else")
	garbled := filingRequest("4", plan1)
	writeReceiptFile(t, filepath.Join(filings, garbled.Key), "plan.json", []byte("{"))
	outOfRange := filingRequest("5", plan1)
	writeReceiptFile(t, filepath.Join(filings, outOfRange.Key), "plan.json", outOfRange)
	writeReceiptFile(t, filepath.Join(filings, outOfRange.Key), "result.json", protocol.FilingResult{Key: outOfRange.Key, PlanDigest: outOfRange.PlanDigest, State: "done", Steps: []protocol.StepResult{{Index: 7, State: "done"}}})
	unadmitted := filingRequest("6", plan1)
	writeReceiptFile(t, filepath.Join(filings, unadmitted.Key), "step-0.prepared", prepared(plan1))
	empty := filingRequest("8", plan1)
	if err := os.MkdirAll(filepath.Join(filings, empty.Key), 0o700); err != nil {
		t.Fatal(err)
	}
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	for _, c := range []struct {
		req   protocol.FilingRequest
		state string
		steps int
	}{{notStarted, "failed", 0}, {linked, "partial", 1}, {replaced, "unknown", 0}} {
		res, body := putFiling(t, a, c.req)
		if res.State != c.state || len(res.Steps) != c.steps {
			t.Fatalf("recovered %s=%s want %s with %d steps", c.req.Key[:1], body, c.state, c.steps)
		}
	}
	if _, err := os.Lstat(filepath.Join(a.roots["tv"], "a.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery resumed an unstarted step")
	}
	if _, err := os.Lstat(filepath.Join(a.roots["tv"], "b2.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery resumed the next step")
	}
	undo := protocol.FilingRequest{Key: protocol.UndoKey(linked.Key), UndoOf: linked.Key}
	if res, body := putFiling(t, a, undo); res.State != "done" || len(res.Steps) != 1 {
		t.Fatalf("undo of the recovered partial filing: %s", body)
	}
	if _, err := os.Lstat(filepath.Join(a.roots["tv"], "b1.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("undo kept the recovered link")
	}
	for _, req := range []protocol.FilingRequest{garbled, outOfRange, unadmitted, empty} {
		res, _ := putFiling(t, a, req)
		if res.State != "unknown" || res.ErrorCode != protocol.CodeReceiptInvalid {
			t.Fatalf("quarantined receipt answered %+v", res)
		}
		if !strings.Contains(a.logs.String(), "receipt "+req.Key+" is unreadable") {
			t.Fatalf("quarantine not reported: %s", a.logs.String())
		}
	}
	if _, err := os.Stat(filepath.Join(filings, unadmitted.Key, "step-0.prepared")); err != nil {
		t.Fatalf("the receipt without a plan lost its evidence: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(a.roots["tv"], "a.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a receipt without a plan was executed again")
	}
}

func TestMediaAgentRecoveryOfRenameAndUnlinkSteps(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	imp := a.roots["import"]
	for _, rel := range []string{"r1/a.srt", "r2/b.srt", "c1/x.txt", "c2/y.txt"} {
		writeMediaFile(t, filepath.Join(imp, rel), rel)
	}
	rename := func(dir, name string) protocol.PrepareResponse {
		return prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: protocol.Path{Root: "import", Rel: dir}, Placements: []protocol.Placement{place("import", dir+"/"+name, name)}})
	}
	cleanup := func(dir string) protocol.PrepareResponse {
		return prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImportCleanup, Source: protocol.Path{Root: "import", Rel: dir}, Cleanup: true})
	}
	renamed, renameNot := filingRequest("1", rename("r1", "a.srt")), filingRequest("2", rename("r2", "b.srt"))
	unlinked, unlinkNot := filingRequest("3", cleanup("c1")), filingRequest("4", cleanup("c2"))
	a.terminate(t)
	filings := filepath.Join(a.dir, "state", "filings")
	for _, c := range []struct {
		req          protocol.FilingRequest
		parent, from string
	}{{renamed, a.roots["tv"], "r1"}, {renameNot, a.roots["tv"], "r2"}, {unlinked, filepath.Join(imp, "c1"), ""}, {unlinkNot, filepath.Join(imp, "c2"), ""}} {
		if c.req.Plan.Steps[0].Kind != map[bool]string{true: protocol.StepRename, false: protocol.StepUnlink}[c.from != ""] {
			t.Fatalf("unexpected first step %+v", c.req.Plan.Steps[0])
		}
		rec := map[string]any{"step": c.req.Plan.Steps[0], "parent": dirIdentity(t, c.parent)}
		if c.from != "" {
			rec["source_parent"] = dirIdentity(t, filepath.Join(imp, c.from))
		}
		dir := filepath.Join(filings, c.req.Key)
		writeReceiptFile(t, dir, "plan.json", c.req)
		writeReceiptFile(t, dir, "step-0.prepared", rec)
	}
	if err := os.Rename(filepath.Join(imp, "r1", "a.srt"), filepath.Join(a.roots["tv"], "a.srt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(imp, "c1", "x.txt")); err != nil {
		t.Fatal(err)
	}
	a = startMediaAgent(t, mediaAgentOpts{dir: a.dir})
	for _, c := range []struct {
		req   protocol.FilingRequest
		state string
		steps int
	}{{renamed, "done", 1}, {renameNot, "failed", 0}, {unlinked, "partial", 1}, {unlinkNot, "failed", 0}} {
		if res, body := putFiling(t, a, c.req); res.State != c.state || len(res.Steps) != c.steps {
			t.Fatalf("recovered %s=%s want %s with %d steps", c.req.Key[:1], body, c.state, c.steps)
		}
	}
	for path, present := range map[string]bool{
		filepath.Join(a.roots["tv"], "a.srt"): true, filepath.Join(imp, "r1", "a.srt"): false,
		filepath.Join(a.roots["tv"], "b.srt"): false, filepath.Join(imp, "r2", "b.srt"): true,
		filepath.Join(imp, "c1"): true, filepath.Join(imp, "c2", "y.txt"): true,
	} {
		if _, err := os.Lstat(path); (err == nil) != present {
			t.Fatalf("%s present=%v after recovery: %v", path, present, err)
		}
	}
}

func TestMediaAgentFailedAdmissionLeavesNoReceipt(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "ep.mkv"), "episode")
	p := prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "ep.mkv")))
	req := filingRequest("7", p)
	filings := filepath.Join(a.dir, "state", "filings")
	if err := os.Chmod(filings, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filings, 0o700) })
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	bodies := make([][]byte, 2)
	for i := range 2 {
		wg.Go(func() { statuses[i], bodies[i] = a.json(t, http.MethodPut, "/v1/filings/"+req.Key, req) })
	}
	wg.Wait()
	for i := range 2 {
		if statuses[i] != 503 || problemCode(t, bodies[i]) != protocol.CodeUnavailable {
			t.Fatalf("failed admission answered %d %s", statuses[i], bodies[i])
		}
	}
	if resp, body := a.get(t, "/v1/filings/"+req.Key, nil); resp.StatusCode != 404 {
		t.Fatalf("failed admission left a receipt: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Lstat(filepath.Join(a.roots["tv"], "ep.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused filing ran")
	}
	if err := os.Chmod(filings, 0o700); err != nil {
		t.Fatal(err)
	}
	if res, body := putFiling(t, a, req); res.State != "done" {
		t.Fatalf("retry after a failed admission: %s", body)
	}
}

func TestMediaAgentFilingOutlivesItsClient(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	var placements []protocol.Placement
	for i := range 1000 {
		name := fmt.Sprintf("sub-%04d.srt", i)
		writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", name), name)
		placements = append(placements, place("downloads", "item/"+name, name))
	}
	p := prepareFiling(t, a, torrentPrepare(placements...))
	req := filingRequest("8", p)
	payload, _ := json.Marshal(req)
	ctx, cancel := context.WithCancel(t.Context())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPut, a.base+"/v1/filings/"+req.Key, bytes.NewReader(payload))
	request.Host = mediaAgentHost
	request.Header.Set("Authorization", "Bearer "+a.token)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if resp, err := mediaAgentClient.Do(request); err == nil {
			_ = resp.Body.Close()
		}
	}()
	waitFor(t, func() bool {
		resp, b := a.get(t, "/v1/filings/"+req.Key+"?after=0&wait_seconds=1", nil)
		var result protocol.FilingResult
		return resp.StatusCode == 200 && json.Unmarshal(b, &result) == nil && len(result.Steps) > 0
	})
	cancel()
	<-finished
	var result protocol.FilingResult
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline) && result.State != "done"; {
		_, b := a.get(t, "/v1/filings/"+req.Key+"?after=999&wait_seconds=5", nil)
		if err := json.Unmarshal(b, &result); err != nil {
			t.Fatal(err)
		}
		if result.State != "running" && result.State != "done" {
			t.Fatalf("a dropped client stopped the filing: %+v", result.State)
		}
	}
	entries, err := os.ReadDir(a.roots["tv"])
	if result.State != "done" || len(result.Steps) != 1000 || err != nil || len(entries) != 1000 {
		t.Fatalf("filing after client drop: state=%s steps=%d files=%d", result.State, len(result.Steps), len(entries))
	}
}

func TestMediaAgentRefusesHandBuiltPlans(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "ep.mkv"), "episode")
	if err := os.MkdirAll(filepath.Join(a.roots["tv"], "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := prepareFiling(t, a, torrentPrepare(place("downloads", "item/ep.mkv", "Show/ep.mkv"))).Plan
	if len(base.Steps) != 1 || base.Steps[0].Kind != protocol.StepLink {
		t.Fatalf("base plan %+v", base)
	}
	clone := func(edit func(*protocol.FilingPlan)) protocol.FilingPlan {
		b, _ := json.Marshal(base)
		var p protocol.FilingPlan
		_ = json.Unmarshal(b, &p)
		edit(&p)
		return p
	}
	many := clone(func(p *protocol.FilingPlan) {
		for i := 1; i <= 1000; i++ {
			s := p.Steps[0]
			s.Index = i
			p.Steps = append(p.Steps, s)
		}
	})
	before := mediaTreeSnapshot(t, filepath.Join(a.dir, "library"))
	for i, c := range []struct {
		name   string
		plan   protocol.FilingPlan
		status int
		code   string
	}{
		{"source root", clone(func(p *protocol.FilingPlan) { p.Steps[0].Source.Path.Root = protocol.RootImport }), 400, protocol.CodeUnsafePath},
		{"unlink in library", clone(func(p *protocol.FilingPlan) {
			id := p.Steps[0].Source.Identity
			p.Steps[0] = protocol.Step{Kind: protocol.StepUnlink, Destination: p.Steps[0].Destination, Expect: &id}
		}), 400, protocol.CodeUnsafePath},
		{"dot dot", clone(func(p *protocol.FilingPlan) { p.Steps[0].Destination.Rel = "../movies/ep.mkv" }), 400, protocol.CodeUnsafePath},
		{"index", clone(func(p *protocol.FilingPlan) { p.Steps[0].Index = 3 }), 400, protocol.CodeUnsafePath},
		{"too many", many, 413, protocol.CodeRequestTooLarge},
		{"root revision", clone(func(p *protocol.FilingPlan) { p.RootRevision = strings.Repeat("0", 64) }), 409, protocol.CodeRootChanged},
		{"empty", clone(func(p *protocol.FilingPlan) { p.Steps = nil }), 400, protocol.CodeInvalidRequest},
	} {
		req := protocol.FilingRequest{Key: fmt.Sprintf("%064x", i+1), Plan: c.plan, PlanDigest: protocol.CanonicalDigest(c.plan)}
		status, body := a.json(t, http.MethodPut, "/v1/filings/"+req.Key, req)
		if status != c.status || problemCode(t, body) != c.code {
			t.Fatalf("%s: %d %s", c.name, status, body)
		}
		if resp, _ := a.get(t, "/v1/filings/"+req.Key, nil); resp.StatusCode != 404 {
			t.Fatalf("%s: refused plan left a receipt", c.name)
		}
	}
	if !reflect.DeepEqual(before, mediaTreeSnapshot(t, filepath.Join(a.dir, "library"))) {
		t.Fatal("refused plans changed the library")
	}
	outside := t.TempDir()
	show := filepath.Join(a.roots["tv"], "Show")
	if err := os.Remove(show); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, show); err != nil {
		t.Fatal(err)
	}
	res, body := putFiling(t, a, protocol.FilingRequest{Key: strings.Repeat("e", 64), Plan: base, PlanDigest: protocol.CanonicalDigest(base)})
	if res.State != "failed" || res.ErrorCode != protocol.CodeUnsafePath {
		t.Fatalf("symlinked parent: %s", body)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("filing followed a symlinked parent: %v %v", entries, err)
	}
}

func TestMediaAgentPrepareRefusesNameAliases(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "a.mkv"), "a")
	writeMediaFile(t, filepath.Join(a.roots["downloads"], "item", "b.mkv"), "b")
	before := mediaTreeSnapshot(t, a.dir)
	for _, names := range [][2]string{{"Show/a.mkv", "show/b.mkv"}, {"Caf\u00e9/a.mkv", "Cafe\u0301/b.mkv"}, {"STRASSE/a.mkv", "stra\u00dfe/b.mkv"}} {
		status, body := a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/a.mkv", names[0]), place("downloads", "item/b.mkv", names[1])))
		if status != 409 || problemCode(t, body) != protocol.CodeDestinationExists {
			t.Fatalf("prepare accepted aliased names %q: %d %s", names, status, body)
		}
	}
	status, body := a.json(t, http.MethodPost, "/v1/filing/prepare", torrentPrepare(place("downloads", "item/a.mkv", "Show/a.mkv"), place("downloads", "item/b.mkv", "Other/b.mkv")))
	if status != 200 {
		t.Fatalf("prepare refused distinct names: %d %s", status, body)
	}
	if !reflect.DeepEqual(before, mediaTreeSnapshot(t, a.dir)) {
		t.Fatal("prepare changed the filesystem")
	}
}

func TestMediaAgentGroupMembershipAndUndoChecks(t *testing.T) {
	t.Parallel()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	outsider := -1
	for gid := 60999; gid > 50000; gid-- {
		if gid != os.Getegid() && !slices.Contains(groups, gid) {
			outsider = gid
			break
		}
	}
	dir := t.TempDir()
	config, _, _ := writeMediaAgentConfig(t, dir, mediaAgentOpts{group: strconv.Itoa(outsider)})
	out, err := mediaAgentCmd(config).CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(out), "not a member of group") {
		t.Fatalf("agent started outside its group: %v %s", err, out)
	}
	other := -1
	for _, gid := range groups {
		if gid != os.Getgid() {
			other = gid
			break
		}
	}
	if other < 0 {
		t.Skip("the test user belongs to a single group")
	}
	a := startMediaAgent(t, mediaAgentOpts{group: strconv.Itoa(os.Getgid())})
	itemDir := filepath.Join(a.roots["import"], "item")
	src := filepath.Join(itemDir, "ep.mkv")
	writeMediaFile(t, src, "episode")
	if err := os.Lchown(src, -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(itemDir, -1, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := prepareFiling(t, a, protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: protocol.Path{Root: "import", Rel: "item"}, Placements: []protocol.Placement{place("import", "item/ep.mkv", "ep.mkv")}})
	req := filingRequest("c", p)
	if res, body := putFiling(t, a, req); res.State != "done" {
		t.Fatalf("forward rename: %s", body)
	}
	if res, body := putFiling(t, a, protocol.FilingRequest{Key: protocol.UndoKey(req.Key), UndoOf: req.Key}); res.State != "done" {
		t.Fatalf("undo applied a check the forward rename did not: %s", body)
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "episode" {
		t.Fatalf("undo did not restore the import: %v", err)
	}
}

func TestMediaAgentRequestBodyHasADeadline(t *testing.T) {
	t.Parallel()
	a := startMediaAgent(t, mediaAgentOpts{})
	conn, err := net.Dial("tcp", a.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "POST /v1/source HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"source\":", mediaAgentHost, a.token)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("a trickled body held the connection: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || problemCode(t, body) != protocol.CodeInvalidRequest || time.Since(start) > 30*time.Second {
		t.Fatalf("trickled body: %d %s after %s", resp.StatusCode, body, time.Since(start))
	}
}
