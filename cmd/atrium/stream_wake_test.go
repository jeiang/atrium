package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
)

type streamFrame struct {
	id   int64
	kind string
	run  int64
}

func parseFrames(t *testing.T, stream string) []streamFrame {
	t.Helper()
	var out []streamFrame
	var cur streamFrame
	for _, line := range strings.Split(stream, "\n") {
		switch {
		case strings.HasPrefix(line, "id: "):
			cur.id, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
		case strings.HasPrefix(line, "event: "):
			cur.kind = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var d struct {
				RunID int64 `json:"run_id"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				t.Fatalf("frame data %q: %v", line, err)
			}
			cur.run = d.RunID
		case line == "":
			if cur.id != 0 {
				out = append(out, cur)
			}
			cur = streamFrame{}
		}
	}
	return out
}

func kindsOf(frames []streamFrame, run int64, skip ...string) []string {
	var kinds []string
	for _, f := range frames {
		if f.run == run && !slices.Contains(skip, f.kind) {
			kinds = append(kinds, f.kind)
		}
	}
	return kinds
}

func streamLines(body io.Reader) <-chan string {
	lines := make(chan string, 1024)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(body)
		for scan.Scan() {
			lines <- scan.Text()
		}
	}()
	return lines
}

func awaitLine(t *testing.T, lines <-chan string, within time.Duration, want string) {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended before %q", want)
			}
			if strings.Contains(line, want) {
				return
			}
		case <-timeout:
			t.Fatalf("no %q within %s", want, within)
		}
	}
}

func awaitDone(t *testing.T, lines <-chan string, within time.Duration, state string) {
	t.Helper()
	awaitLine(t, lines, within, "event: done")
	if data := strings.ReplaceAll(<-lines, " ", ""); data != `data:{"state":"`+state+`"}` {
		t.Fatalf("done frame %q, want state %s", data, state)
	}
}

func gate() (chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

func TestStreamShowsAParkedRunPromptly(t *testing.T) {
	s := newStreamSite(t)
	free := s.hold()
	s.api.Push(anthropictest.Reply("tool_use", anthropictest.ToolUse("ask-1", "ask_operator", map[string]any{"question": "Which account?"})))
	threadID, runID := s.start("park")
	waitRunning(t, s.d, runID)
	lines := streamLines(s.open(threadID, runID).Body)
	awaitLine(t, lines, 5*time.Second, "event: run_start")
	free()
	awaitDone(t, lines, 3*time.Second, "parked")
}

func TestStreamFollowsARunOfAnotherProcess(t *testing.T) {
	s := newStreamSite(t)
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Text("first")))
	threadID, firstRun := s.start("first")
	s.events(s.token, threadID, firstRun, "")
	free := s.hold()
	s.api.Push(anthropictest.Reply("end_turn", anthropictest.Thinking("pondering", "sig"), anthropictest.Text("from the other process")))
	cmd := exec.Command(os.Args[0], "agent", "run", "--thread", strconv.FormatInt(threadID, 10), "--text", "from another process")
	cmd.Env = append(os.Environ(), execMainEnv+"=1")
	for name, value := range s.f.env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		free()
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	var runID int64
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		runID = int64(asstCount(t, s.d, "SELECT COALESCE(MAX(r.id), 0) FROM runs r JOIN run_events e ON e.stream_run_id = r.id WHERE r.thread_id = ? AND r.id <> ?", threadID, firstRun))
		if runID != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent run process never started its run: %s", out.String())
		}
	}
	lines := streamLines(s.open(threadID, runID).Body)
	awaitLine(t, lines, 5*time.Second, "event: run_start")
	free()
	awaitLine(t, lines, 3*time.Second, "event: thinking")
	awaitLine(t, lines, 3*time.Second, "event: complete")
	awaitDone(t, lines, 3*time.Second, "done")
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("agent run: %v %s", err, out.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("agent run did not exit")
	}
}

func TestStreamCopiesDescendantEventsOnce(t *testing.T) {
	s := newStreamSite(t)
	childGate, openChild := gate()
	grandGate, openGrand := gate()
	t.Cleanup(openChild)
	t.Cleanup(openGrand)
	var mu sync.Mutex
	held := map[string]chan struct{}{"child-task": childGate, "grand-ask": grandGate}
	s.api.SetOnRequest(func(r anthropictest.Request) {
		mu.Lock()
		var wait chan struct{}
		for marker, ch := range held {
			if strings.Contains(string(r.Body), marker) {
				wait = ch
				delete(held, marker)
			}
		}
		mu.Unlock()
		if wait != nil {
			<-wait
		}
	})
	s.api.Push(research("child-task"),
		anthropictest.Reply("end_turn", anthropictest.Text("grandchild answer")),
		anthropictest.Reply("end_turn", anthropictest.Text("child digest")),
		anthropictest.Reply("end_turn", anthropictest.Text("root answer")))
	threadID, rootRun := s.start("root-ask")
	var childRun int64
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		childRun = int64(asstCount(t, s.d, "SELECT COALESCE(MAX(id), 0) FROM runs WHERE parent_run_id = ?", rootRun))
		if childRun != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the subagent never started")
		}
	}
	waitRunning(t, s.d, childRun)
	otherThread, grandRun := s.start("grand-ask")
	waitRunning(t, s.d, grandRun)
	if _, err := s.d.Write.Exec("UPDATE runs SET parent_run_id = ? WHERE id = ?", childRun, grandRun); err != nil {
		t.Fatal(err)
	}
	openGrand()
	grand, _ := s.events(s.token, otherThread, grandRun, "")
	openChild()
	root, _ := s.events(s.token, threadID, rootRun, "")
	child, _ := s.events(s.token, s.childThread(childRun), childRun, "")

	rootFrames, childFrames, grandFrames := parseFrames(t, root), parseFrames(t, child), parseFrames(t, grand)
	for name, frames := range map[string][]streamFrame{"root": rootFrames, "child": childFrames, "grandchild": grandFrames} {
		for i, f := range frames {
			if f.id != int64(i+1) {
				t.Fatalf("%s stream ids are not contiguous at %d: %+v", name, i, frames)
			}
		}
	}
	grandKinds := kindsOf(grandFrames, grandRun, "run_start", "complete")
	if len(grandKinds) == 0 || grandKinds[len(grandKinds)-1] != "run_done" {
		t.Fatalf("grandchild stream: %v", grandKinds)
	}
	if got := kindsOf(childFrames, grandRun); !slices.Equal(got, grandKinds) {
		t.Fatalf("grandchild events in the child stream %v, want %v", got, grandKinds)
	}
	if got := kindsOf(rootFrames, grandRun); !slices.Equal(got, grandKinds) {
		t.Fatalf("grandchild events in the root stream %v, want %v", got, grandKinds)
	}
	childKinds := kindsOf(childFrames, childRun, "complete")
	if got := kindsOf(rootFrames, childRun); !slices.Equal(got, childKinds) {
		t.Fatalf("child events in the root stream %v, want %v", got, childKinds)
	}
	var completes []int64
	grandDone, childDone := -1, -1
	for i, f := range rootFrames {
		switch {
		case f.kind == "complete":
			completes = append(completes, f.run)
		case f.kind == "run_done" && f.run == grandRun:
			grandDone = i
		case f.kind == "run_done" && f.run == childRun:
			childDone = i
		}
	}
	if !slices.Equal(completes, []int64{rootRun}) || rootFrames[len(rootFrames)-1].kind != "complete" {
		t.Fatalf("root terminal events %v: %+v", completes, rootFrames)
	}
	if grandDone < 0 || childDone < grandDone {
		t.Fatalf("root stream order: grandchild done at %d, child done at %d", grandDone, childDone)
	}

	expectAPI(t, s.do(http.MethodGet, fmt.Sprintf("/api/v1/threads/%d/runs/%d/events", otherThread, rootRun), s.token, nil), http.StatusNotFound, "not_found")
	browser := asstBrowse(t, s.base)
	if code, body, _ := browser.get(fmt.Sprintf("/chat/%d/events?run=%d", otherThread, rootRun), false); code != http.StatusNotFound {
		t.Fatalf("browser stream under another thread: %d %s", code, body)
	}
}

func (s *streamSite) childThread(runID int64) int64 {
	s.t.Helper()
	return int64(asstCount(s.t, s.d, "SELECT thread_id FROM runs WHERE id = ?", runID))
}
