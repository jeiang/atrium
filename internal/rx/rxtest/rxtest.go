// Package rxtest runs rx patterns against the regex corpus generated from the frozen Python app
// (testdata/corpora/regex_manifest.json and regex*.jsonl). A slice that ports patterns passes its compiled
// patterns to Run keyed by manifest id; Run checks the pattern text and flags against the manifest and every
// recorded search, match, fullmatch, finditer and call result.
package rxtest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jeiang/atrium/internal/rx"
)

// Dir is the absolute path of testdata/corpora in this checkout.
func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("rxtest: cannot locate source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "corpora")
}

// Pattern is one regex_manifest.json pattern.
type Pattern struct {
	ID         string         `json:"id"`
	Site       string         `json:"site"`
	Call       string         `json:"call"`
	Pattern    string         `json:"pattern"`
	Bytes      bool           `json:"bytes"`
	FlagNames  []string       `json:"flags"`
	Groups     int            `json:"groups"`
	GroupNames map[string]int `json:"group_names"`
	Features   []string       `json:"features"`
	Error      string         `json:"error"`
	Warnings   []string       `json:"warnings"`
}

// Flags maps the manifest's Python flag names (and the bytes marker) to rx flags.
func (p Pattern) Flags() (rx.Flags, error) {
	var f rx.Flags
	if p.Bytes {
		f |= rx.Bytes
	}
	for _, n := range p.FlagNames {
		switch n {
		case "IGNORECASE":
			f |= rx.IgnoreCase
		case "MULTILINE":
			f |= rx.Multiline
		case "DOTALL":
			f |= rx.DotAll
		case "VERBOSE":
			f |= rx.Verbose
		case "ASCII":
			f |= rx.ASCII
		default:
			return 0, fmt.Errorf("unknown flag %q", n)
		}
	}
	return f, nil
}

// Group is a recorded group span; nil in Match.Groups when the group did not participate.
type Group struct {
	Span [2]int `json:"span"`
	Text string `json:"text"`
}

// Match is a recorded Python match.
type Match struct {
	Span   [2]int   `json:"span"`
	Text   string   `json:"text"`
	Groups []*Group `json:"groups"`
}

// Call is a recorded call made with the pattern by the Python source at At.
type Call struct {
	At     string          `json:"at"`
	Op     string          `json:"op"`
	Args   map[string]any  `json:"args"`
	Result json.RawMessage `json:"result"`
	N      *int            `json:"n"`
}

// Record is one regex.jsonl line: Python's results for one pattern on one input.
type Record struct {
	ID        string  `json:"id"`
	Input     string  `json:"input"`
	Search    *Match  `json:"search"`
	Match     *Match  `json:"match"`
	FullMatch *Match  `json:"fullmatch"`
	FindIter  []Match `json:"finditer"`
	Calls     []Call  `json:"calls"`
}

// Corpus is the loaded manifest and records.
type Corpus struct {
	Patterns []Pattern
	byID     map[string]int
	Records  map[string][]Record
	// Lines is the number of records read from every regex*.jsonl.
	Lines int
}

// Pattern returns the manifest pattern with id.
func (c *Corpus) Pattern(id string) (Pattern, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Pattern{}, false
	}
	return c.Patterns[i], true
}

var (
	loadOnce sync.Once
	loaded   *Corpus
	loadErr  error
)

// Load reads the corpus once per test binary.
func Load(tb testing.TB) *Corpus {
	tb.Helper()
	loadOnce.Do(func() { loaded, loadErr = load(Dir()) })
	if loadErr != nil {
		tb.Fatalf("rxtest: %v", loadErr)
	}
	return loaded
}

func load(dir string) (*Corpus, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "regex_manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Patterns []Pattern `json:"patterns"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("regex_manifest.json: %w", err)
	}
	c := &Corpus{Patterns: manifest.Patterns, byID: map[string]int{}, Records: map[string][]Record{}}
	for i, p := range c.Patterns {
		c.byID[p.ID] = i
	}
	files, err := filepath.Glob(filepath.Join(dir, "regex*.jsonl"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no regex*.jsonl corpus")
	}
	for _, name := range files {
		if err := c.loadRecords(name); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Corpus) loadRecords(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var r Record
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			return fmt.Errorf("%s:%d: %w", filepath.Base(name), line, err)
		}
		if _, ok := c.byID[r.ID]; !ok {
			return fmt.Errorf("%s:%d: id %q is not in the manifest", filepath.Base(name), line, r.ID)
		}
		c.Records[r.ID] = append(c.Records[r.ID], r)
		c.Lines++
	}
	return sc.Err()
}

// Check compares re with every record of the manifest pattern id. It returns the number of records that agree,
// the number of records, and one message per disagreement.
func (c *Corpus) Check(id string, re *rx.Regexp) (pass, total int, failures []string) {
	p, ok := c.Pattern(id)
	if !ok {
		return 0, 0, []string{fmt.Sprintf("%s: not in the manifest", id)}
	}
	flags, err := p.Flags()
	if err != nil {
		return 0, 0, []string{fmt.Sprintf("%s: %v", id, err)}
	}
	if re.String() != p.Pattern || re.Flags() != flags {
		return 0, 0, []string{fmt.Sprintf("%s: compiled %q with flags %v, manifest has %q with %v", id, re.String(), re.Flags(), p.Pattern, flags)}
	}
	if re.Groups() != p.Groups || !maps.Equal(re.GroupIndex(), p.GroupNames) {
		failures = append(failures, fmt.Sprintf("%s: groups %d %v, Python %d %v", id, re.Groups(), re.GroupIndex(), p.Groups, p.GroupNames))
	}
	for _, r := range c.Records[id] {
		total++
		diffs := checkRecord(re, r)
		if len(diffs) == 0 {
			pass++
			continue
		}
		for _, d := range diffs {
			failures = append(failures, fmt.Sprintf("%s input %q: %s", id, r.Input, d))
		}
	}
	return pass, total, failures
}

// Run checks each compiled pattern against its manifest id in a subtest and logs per-pattern pass counts.
func Run(t *testing.T, patterns map[string]*rx.Regexp) {
	t.Helper()
	c := Load(t)
	for _, id := range slices.Sorted(maps.Keys(patterns)) {
		t.Run(id, func(t *testing.T) {
			pass, total, failures := c.Check(id, patterns[id])
			for _, f := range failures {
				t.Error(f)
			}
			t.Logf("%s: %d/%d records pass", id, pass, total)
		})
	}
}

func toMatch(m *rx.Match, groups int) *Match {
	if m == nil {
		return nil
	}
	out := &Match{Span: [2]int{m.Start(), m.End()}, Text: m.Text(), Groups: make([]*Group, groups)}
	for i := 1; i <= groups; i++ {
		s, e := m.Span(i)
		if s < 0 {
			continue
		}
		out.Groups[i-1] = &Group{Span: [2]int{s, e}, Text: m.Group(i)}
	}
	return out
}

func toMatches(ms []*rx.Match, groups int) []Match {
	out := make([]Match, 0, len(ms))
	for _, m := range ms {
		out = append(out, *toMatch(m, groups))
	}
	return out
}

func checkRecord(re *rx.Regexp, r Record) []string {
	var diffs []string
	diff := func(what string, want, got any, err error) {
		if err != nil {
			diffs = append(diffs, fmt.Sprintf("%s: %v", what, err))
			return
		}
		if !jsonEqual(want, got) {
			diffs = append(diffs, fmt.Sprintf("%s: Python %s, rx %s", what, mustJSON(want), mustJSON(got)))
		}
	}
	g := re.Groups()
	m, err := re.Search(r.Input)
	diff("search", r.Search, toMatch(m, g), err)
	m, err = re.Match(r.Input)
	diff("match", r.Match, toMatch(m, g), err)
	m, err = re.FullMatch(r.Input)
	diff("fullmatch", r.FullMatch, toMatch(m, g), err)
	ms, err := re.FindIter(r.Input)
	diff("finditer", nonNil(r.FindIter), toMatches(ms, g), err)
	for _, call := range r.Calls {
		got, err := runCall(re, r.Input, call)
		var want any
		if uerr := json.Unmarshal(call.Result, &want); uerr != nil {
			diffs = append(diffs, fmt.Sprintf("%s %s: %v", call.Op, call.At, uerr))
			continue
		}
		if call.N != nil {
			want = map[string]any{"result": want, "n": *call.N}
		}
		diff(fmt.Sprintf("%s %s %v", call.Op, call.At, call.Args), want, got, err)
	}
	return diffs
}

func nonNil(ms []Match) []Match {
	if ms == nil {
		return []Match{}
	}
	return ms
}

func intArg(args map[string]any, name string, def int) int {
	v, ok := args[name]
	if !ok {
		return def
	}
	f, ok := v.(float64)
	if !ok {
		return def
	}
	return int(f)
}

func runCall(re *rx.Regexp, input string, call Call) (any, error) {
	pos := intArg(call.Args, "pos", 0)
	endpos := intArg(call.Args, "endpos", math.MaxInt)
	g := re.Groups()
	switch call.Op {
	case "search":
		m, err := re.SearchRange(input, pos, endpos)
		return toMatch(m, g), err
	case "match":
		m, err := re.MatchRange(input, pos, endpos)
		return toMatch(m, g), err
	case "fullmatch":
		m, err := re.FullMatchRange(input, pos, endpos)
		return toMatch(m, g), err
	case "finditer":
		ms, err := re.FindIterRange(input, pos, endpos)
		return toMatches(ms, g), err
	case "findall":
		if g > 1 {
			out, err := re.FindAllGroups(input)
			if out == nil {
				out = [][]string{}
			}
			return out, err
		}
		out, err := re.FindAll(input)
		if out == nil {
			out = []string{}
		}
		return out, err
	case "sub", "subn":
		repl, ok := call.Args["repl"].(string)
		if !ok {
			return nil, fmt.Errorf("repl %v is not a template", call.Args["repl"])
		}
		out, n, err := re.SubN(repl, input, intArg(call.Args, "count", 0))
		if call.Op == "subn" {
			return map[string]any{"result": out, "n": n}, err
		}
		return out, err
	case "split":
		return re.Split(input, intArg(call.Args, "maxsplit", 0))
	}
	return nil, fmt.Errorf("unknown op %q", call.Op)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return strings.TrimSpace(string(b))
}

func jsonEqual(a, b any) bool {
	var x, y any
	if json.Unmarshal([]byte(mustJSON(a)), &x) != nil || json.Unmarshal([]byte(mustJSON(b)), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
