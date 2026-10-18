package scheduler

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

type corpusError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type corpusRecord struct {
	Fn         string          `json:"fn"`
	Input      []any           `json:"input"`
	EnforceMin bool            `json:"enforce_min"`
	LocalAfter string          `json:"local_after"`
	Output     json.RawMessage `json:"output"`
	Error      *corpusError    `json:"error"`
}

type specDict struct {
	Kind      string   `json:"kind"`
	TZ        string   `json:"tz"`
	OnceAt    *float64 `json:"once_at"`
	Minutes   []int    `json:"minutes"`
	Hours     []int    `json:"hours"`
	DOM       []int    `json:"dom"`
	Months    []int    `json:"months"`
	DOW       []int    `json:"dow"`
	Recurring bool     `json:"recurring"`
}

type occurrence struct {
	TS    float64 `json:"ts"`
	Local string  `json:"local"`
}

func toSpecDict(s Spec) specDict {
	d := specDict{Kind: s.Kind, TZ: s.TZ, Minutes: s.Minutes, Hours: s.Hours, DOM: s.DOM, Months: s.Months, DOW: s.DOW, Recurring: s.Recurring()}
	if s.Kind == KindOnce {
		d.OnceAt = &s.OnceAt
	}
	return d
}

func pyISO(ts float64, loc *time.Location) string {
	return localTime(ts, loc).Format("2006-01-02T15:04:05-07:00")
}

func errorOf(err error) *corpusError {
	var se Error
	if !errors.As(err, &se) {
		return &corpusError{Message: err.Error(), Type: fmt.Sprintf("%T", err)}
	}
	return &corpusError{Message: string(se), Type: "ScheduleError"}
}

func jsonEqual(t *testing.T, want json.RawMessage, got any) bool {
	t.Helper()
	var w any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(w, g)
}

func (r corpusRecord) check(t *testing.T) error {
	text, tz := r.Input[0].(string), r.Input[1].(string)
	var after float64
	if len(r.Input) > 2 {
		after = r.Input[2].(float64)
	}
	var got any
	var gotErr *corpusError
	switch r.Fn {
	case "parse":
		s, err := Parse(text, tz)
		if err != nil {
			gotErr = errorOf(err)
		} else {
			got = toSpecDict(s)
		}
	case "describe":
		got = Describe(text, tz)
	case "next_after":
		s, err := Parse(text, tz)
		if err != nil {
			return err
		}
		if l := pyISO(after, s.Loc); l != r.LocalAfter {
			return fmt.Errorf("local_after %s, want %s", l, r.LocalAfter)
		}
		chain := []occurrence{}
		current := after
		for range 6 {
			next, ok := s.NextAfter(current)
			if !ok {
				break
			}
			current = next
			chain = append(chain, occurrence{next, pyISO(next, s.Loc)})
		}
		got = chain
	case "min_gap":
		s, err := Parse(text, tz)
		if err != nil {
			return err
		}
		if gap, ok := s.MinGap(after); ok {
			got = gap
		}
	case "validate":
		_, due, err := Validate(text, tz, after, r.EnforceMin)
		if err != nil {
			gotErr = errorOf(err)
		} else {
			got = due
		}
	default:
		return fmt.Errorf("unknown fn %q", r.Fn)
	}
	if r.Error != nil || gotErr != nil {
		if r.Error == nil || gotErr == nil || *r.Error != *gotErr {
			return fmt.Errorf("error %+v (output %v), want error %+v", gotErr, got, r.Error)
		}
		return nil
	}
	if !jsonEqual(t, r.Output, got) {
		raw, _ := json.Marshal(got)
		return fmt.Errorf("got %s, want %s", raw, r.Output)
	}
	return nil
}

func TestSchedulerCorpus(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "testdata", "corpora", "scheduler.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<20)
	total, failed := 0, 0
	for scanner.Scan() {
		var r corpusRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		total++
		if err := r.check(t); err != nil {
			failed++
			t.Errorf("%s%q enforce_min=%v: %v", r.Fn, r.Input, r.EnforceMin, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if total != 7370 {
		t.Errorf("read %d records, want 7370", total)
	}
	t.Logf("%d/%d records pass", total-failed, total)
}

func TestTimePattern(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/agent/scheduler.py:99": timePattern})
}
