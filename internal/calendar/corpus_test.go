package calendar

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

type corpusError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type corpusRecord struct {
	Fn          string          `json:"fn"`
	File        string          `json:"file"`
	TZ          string          `json:"tz"`
	Calendar    string          `json:"calendar"`
	WindowStart string          `json:"window_start"`
	WindowEnd   string          `json:"window_end"`
	UID         string          `json:"uid"`
	DTStamp     string          `json:"dtstamp"`
	Input       json.RawMessage `json:"input"`
	Output      json.RawMessage `json:"output"`
	Error       *corpusError    `json:"error"`
}

func corpusDir() string { return filepath.Join(golden.Dir(), "..", "corpora") }

func loadCorpus(t *testing.T) []corpusRecord {
	t.Helper()
	file, err := os.Open(filepath.Join(corpusDir(), "calendar.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var out []corpusRecord
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 1<<20), 16<<20)
	for scan.Scan() {
		var r corpusRecord
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func processZone(t *testing.T) {
	t.Helper()
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	saved := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = saved })
}

func zoneOf(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func errType(err error) string {
	var c interface{ PyClass() string }
	if errors.As(err, &c) {
		return c.PyClass()
	}
	return "?"
}

func jsonValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	v, err := py.JSONLoads(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func sameJSON(t *testing.T, want json.RawMessage, got any) bool {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	diffs, err := golden.Diff(want, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) > 0 {
		t.Errorf("%s", strings.Join(diffs, "\n"))
		return false
	}
	return true
}

func noteBody(note string) string {
	const marker = " not expanded: "
	if i := strings.Index(note, marker); strings.HasPrefix(note, "recurrence of ") && i >= 0 {
		return note[:i+len(marker)]
	}
	return note
}

func TestCorpus(t *testing.T) {
	processZone(t)
	records := loadCorpus(t)
	if len(records) == 0 {
		t.Fatal("empty corpus")
	}
	failures := 0
	for i, r := range records {
		if failures > 12 {
			t.Fatal("too many failures")
		}
		name := r.Fn + "/" + r.File + "/" + r.TZ
		ok := t.Run(fmt.Sprintf("%s#%d", name, i), func(t *testing.T) { check(t, r) })
		if !ok {
			failures++
		}
	}
}

func check(t *testing.T, r corpusRecord) {
	switch r.Fn {
	case "events_in_ics":
		checkEvents(t, r)
	case "parse_when":
		var in []any
		if err := json.Unmarshal(r.Input, &in); err != nil {
			t.Fatal(err)
		}
		tz := zoneOf(t, in[1].(string))
		v, err := ParseWhen(in[0], tz, in[2].(bool))
		if r.Error != nil {
			if err == nil {
				t.Fatalf("want error %q", r.Error.Message)
			}
			if err.Error() != r.Error.Message || errType(err) != r.Error.Type {
				t.Errorf("error %s %q, want %s %q", errType(err), err, r.Error.Type, r.Error.Message)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		if e := json.Unmarshal(r.Output, &want); e != nil {
			t.Fatal(e)
		}
		kind := "datetime"
		if v.isDate() {
			kind = "date"
		}
		if kind != want["type"] || v.iso() != want["iso"] {
			t.Errorf("got %s %s, want %s %s", kind, v.iso(), want["type"], want["iso"])
		}
	case "normalize_spec":
		var in []json.RawMessage
		if err := json.Unmarshal(r.Input, &in); err != nil {
			t.Fatal(err)
		}
		raw, _ := jsonValue(t, in[0]).(py.Dict)
		var tzName string
		_ = json.Unmarshal(in[1], &tzName)
		spec, err := NormalizeSpec(raw, zoneOf(t, tzName), r.UID)
		if r.Error != nil {
			if err == nil || err.Error() != r.Error.Message || errType(err) != r.Error.Type {
				t.Errorf("error %v, want %s %q", err, r.Error.Type, r.Error.Message)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, r.Output, spec.Dict())
	case "google_body":
		spec, tz := specInput(t, r)
		body, err := GoogleBody(spec, tz)
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, r.Output, body)
	case "build_ics":
		spec, tz := specInput(t, r)
		stamp, err := time.Parse(time.RFC3339, r.DTStamp)
		if err != nil {
			t.Fatal(err)
		}
		text, err := BuildICS(spec, tz, stamp)
		if err != nil {
			t.Fatal(err)
		}
		var want string
		if err := json.Unmarshal(r.Output, &want); err != nil {
			t.Fatal(err)
		}
		if got, wantLines := contentLines(text), contentLines(want); !reflect.DeepEqual(got, wantLines) {
			t.Errorf("ics lines %q, want %q", got, wantLines)
		}
	default:
		t.Fatalf("unknown fn %s", r.Fn)
	}
}

func contentLines(text string) []string {
	unfolded := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n ", ""), "\r\n\t", "")
	lines := strings.Split(strings.TrimSpace(unfolded), "\r\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	sorted := append([]string(nil), lines...)
	slices.Sort(sorted)
	return sorted
}

func specInput(t *testing.T, r corpusRecord) (Spec, *time.Location) {
	t.Helper()
	var in []json.RawMessage
	if err := json.Unmarshal(r.Input, &in); err != nil {
		t.Fatal(err)
	}
	spec, err := SpecOf(jsonValue(t, in[0]))
	if err != nil {
		t.Fatal(err)
	}
	var tzName string
	_ = json.Unmarshal(in[1], &tzName)
	return spec, zoneOf(t, tzName)
}

func checkEvents(t *testing.T, r corpusRecord) {
	data, err := os.ReadFile(filepath.Join(corpusDir(), r.File))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := time.Parse(time.RFC3339, r.WindowStart)
	if err != nil {
		t.Fatal(err)
	}
	we, err := time.Parse(time.RFC3339, r.WindowEnd)
	if err != nil {
		t.Fatal(err)
	}
	tz := zoneOf(t, r.TZ)
	notes := []string{}
	rows, err := EventsInICS(data, r.Calendar, ws.In(tz), we.In(tz), tz, &notes)
	if r.Error != nil {
		if err == nil || errType(err) != r.Error.Type {
			t.Fatalf("error %v, want %s", err, r.Error.Type)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Events []map[string]string `json:"events"`
		Notes  []string            `json:"notes"`
	}
	if err := json.Unmarshal(r.Output, &want); err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	for _, row := range rows {
		got = append(got, map[string]string{
			"uid": row.UID, "calendar": row.Calendar, "start": row.Start, "end": row.End, "title": row.Title, "location": row.Location,
		})
	}
	if len(got) != len(want.Events) || (len(got) > 0 && !reflect.DeepEqual(got, want.Events)) {
		t.Errorf("events\n got %v\nwant %v", got, want.Events)
	}
	gotNotes := make([]string, len(notes))
	for i, n := range notes {
		gotNotes[i] = noteBody(n)
	}
	wantNotes := make([]string, len(want.Notes))
	for i, n := range want.Notes {
		wantNotes[i] = noteBody(n)
	}
	if len(gotNotes) != len(wantNotes) || (len(gotNotes) > 0 && !reflect.DeepEqual(gotNotes, wantNotes)) {
		t.Errorf("notes %q, want %q", notes, want.Notes)
	}
}
