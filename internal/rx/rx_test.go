package rx

import (
	"encoding/json"
	"errors"
	"fmt"
	goparser "go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type pyCase struct {
	pattern string
	flags   Flags
	op      string
	input   string
	args    []any
	want    string
}

// Expected values are what CPython 3.14 returns for the same call (spans as [start, end] code point pairs).
var pyCases = []pyCase{
	{`\w+`, 0, "finditer", "café ²³ x_y ǅ\u0301e", nil, `[[0,4],[5,7],[8,11],[12,13],[14,15]]`},
	{`\w`, 0, "finditer", "\u0301\u200d‿", nil, `[]`},
	{`\w+`, ASCII, "finditer", "café", nil, `[[0,3]]`},
	{`\w+`, Bytes, "finditer", "café", nil, `[[0,3]]`},
	{`\d+`, 0, "finditer", "٣٤٥ ０９ ²³ 𝟘", nil, `[[0,3],[4,6],[10,11]]`},
	{`\s+`, 0, "finditer", "a\x1cb\u2028c\u200bd", nil, `[[1,2],[3,4]]`},
	{`\b\w+\b`, 0, "finditer", "a\u200db x\u0301y", nil, `[[0,1],[2,3],[4,5],[6,7]]`},
	{`\b`, 0, "finditer", "", nil, `[]`},
	{`\B`, 0, "finditer", "", nil, `[[0,0]]`},
	{`\Bb\B`, 0, "finditer", "abc b", nil, `[[1,2]]`},
	{`foo(?=bar)`, 0, "search", "foobaz foobar", nil, `[[7,10]]`},
	{`foo(?!bar)`, 0, "search", "foobar foobaz", nil, `[[7,10]]`},
	{`(?<=\$)\d+`, 0, "search", "cost $42", nil, `[[6,8]]`},
	{`(?<!\$)\b\d+`, 0, "search", "$42 17", nil, `[[4,6]]`},
	{`(\w)\1`, 0, "search", "abccd", nil, `[[2,4],[2,3]]`},
	{`(?P<q>['"]).*?(?P=q)`, 0, "search", `say "hi' x" 'y'`, nil, `[[4,11],[4,5]]`},
	{`(a)\1`, IgnoreCase, "search", "aA", nil, `[[0,2],[0,1]]`},
	{`now\Z`, 0, "search", "now\n", nil, `null`},
	{`now$`, 0, "search", "now\n", nil, `[[0,3]]`},
	{`now$`, Multiline, "search", "now\nx", nil, `[[0,3]]`},
	{`now\z`, 0, "search", "now", nil, `[[0,3]]`},
	{`$`, 0, "finditer", "a\nb\n", nil, `[[3,3],[4,4]]`},
	{`$`, Multiline, "finditer", "a\nb\n", nil, `[[1,1],[3,3],[4,4]]`},
	{`^`, Multiline, "finditer", "a\nb\n", nil, `[[0,0],[2,2],[4,4]]`},
	{`a{,2}`, 0, "finditer", "aaa", nil, `[[0,2],[2,3],[3,3]]`},
	{`x{,}`, 0, "finditer", "xxx", nil, `[[0,3],[3,3]]`},
	{`a{2,}?`, 0, "search", "aaaa", nil, `[[0,2]]`},
	{`a{`, 0, "search", "a{", nil, `[[0,2]]`},
	{`(?P<x>a)(b)(?P<y>c)`, 0, "search", "abc", nil, `[[0,3],[0,1],[1,2],[2,3]]`},
	{`a*+a`, 0, "search", "aaa", nil, `null`},
	{`(?>a*)a`, 0, "search", "aaa", nil, `null`},
	{`a++b`, 0, "search", "aab", nil, `[[0,3]]`},
	{`(?i)amazon`, 0, "search", "AMAZON", nil, `[[0,6]]`},
	{`(?i:a)b`, 0, "search", "Ab AB", nil, `[[0,2]]`},
	{`(?-i:a)b`, IgnoreCase, "search", "AB aB", nil, `[[3,5]]`},
	{"a b # comment\n c", Verbose, "search", "abc", nil, `[[0,3]]`},
	{`a[ ]b`, Verbose, "search", "a b", nil, `[[0,3]]`},
	{`(?x) a b`, 0, "search", "ab", nil, `[[0,2]]`},
	{`s`, IgnoreCase, "finditer", "Sſs", nil, `[[0,1],[1,2],[2,3]]`},
	{`i`, IgnoreCase, "finditer", "Iıİi", nil, `[[0,1],[1,2],[2,3],[3,4]]`},
	{`[a-z]+`, IgnoreCase, "search", "ſ\u212a", nil, `[[0,2]]`},
	{`é`, IgnoreCase, "search", "É", nil, `[[0,1]]`},
	{`é`, ASCII | IgnoreCase, "search", "É", nil, `null`},
	{`k`, ASCII | IgnoreCase, "search", "\u212a", nil, `null`},
	{`k`, ASCII | IgnoreCase, "search", "K", nil, `[[0,1]]`},
	{`[a-z]`, ASCII | IgnoreCase, "search", "\u017f", nil, `null`},
	{`[^k]`, ASCII | IgnoreCase, "search", "\u212a", nil, `[[0,1]]`},
	{`x*`, 0, "sub", "abxd", []any{"-"}, `"-a-b--d-"`},
	{`|a`, 0, "findall", "a", nil, `["","a",""]`},
	{`(\w+) (\w+)`, 0, "sub", "hello world", []any{`\2 \1`}, `"world hello"`},
	{`(?P<w>\w+)`, 0, "sub", "ab cd", []any{`<\g<w>|\g<0>|\g<1>>`}, `"<ab|ab|ab> <cd|cd|cd>"`},
	{`(a)|(b)`, 0, "sub", "ab", []any{`[\1\2]`}, `"[a][b]"`},
	{`b`, 0, "sub", "abc", []any{`\n\.\\`}, `"a\n\\.\\c"`},
	{`b`, 0, "sub", "abab", []any{"X", 1}, `"aXab"`},
	{`(,)`, 0, "split", "a,b,,c", nil, `["a",",","b",",","",",","c"]`},
	{`x*`, 0, "split", "axbc", nil, `["","a","","b","c",""]`},
	{`,`, 0, "split", "a,b,c", []any{1}, `["a","b,c"]`},
	{`(<)?\w+(?(1)>|$)`, 0, "finditer", "<a> b <c", nil, `[[0,3],[7,8]]`},
	{`[^\W_]+`, 0, "findall", "a_b1 ü", nil, `["a","b1","ü"]`},
	{`[\W\d]`, 0, "findall", "a1!é", nil, `["1","!"]`},
	{`[^\S\n]+`, 0, "findall", "a \t\nb", nil, `[" \t"]`},
	{`(a)(b)?`, 0, "findall", "a ab", nil, `[["a",""],["a","b"]]`},
	{`😀`, 0, "search", "x😀y", nil, `[[1,2]]`},
	{`\w+`, 0, "search", "𝒜𝓁 x", nil, `[[0,2]]`},
	{`(?s).`, 0, "findall", "a\nb", nil, `["a","\n","b"]`},
	{`.`, 0, "findall", "a\nb", nil, `["a","b"]`},
	{`(?#comment)a`, 0, "search", "a", nil, `[[0,1]]`},
	{`\x41\u0042\U00000043\101\0`, 0, "search", "ABCA\x00", nil, `[[0,5]]`},
	{`[\b]`, 0, "search", "a\bb", nil, `[[1,2]]`},
	{`\\`, 0, "search", `a\b`, nil, `[[1,2]]`},
	{`^a`, 0, "search", "ba", []any{1, 2}, `null`},
	{`a$`, 0, "search", "ab", []any{0, 1}, `[[0,1]]`},
	{`(?<=b)a`, 0, "search", "ba", []any{1, 2}, `[[1,2]]`},
	{`a`, 0, "match", "ba", nil, `null`},
	{`a`, 0, "match", "ba", []any{1, 2}, `[[1,2]]`},
	{`a|ab`, 0, "fullmatch", "ab", nil, `[[0,2]]`},
	{`a`, 0, "fullmatch", "ab", []any{0, 1}, `[[0,1]]`},
	{`(?i)\Z\D|\w`, Multiline, "search", "12 ٣٤ ０9", nil, `[[0,1]]`},
	{`(?(2)x)b*(\w+)(?P<n>\w)`, 0, "search", "ab", nil, `[[0,2],[0,1],[1,2]]`},
	{`(?a:\w)`, 0, "search", "é", nil, `null`},
	{`(?u:é)`, ASCII | IgnoreCase, "search", "É", nil, `[[0,1]]`},
	{`[^😀]`, 0, "search", "😁", nil, `[[0,1]]`},
	{`[^😀]`, 0, "finditer", "a😁", nil, `[[0,1],[1,2]]`},
	{`[^😀]`, 0, "sub", "😁", []any{"-"}, `"-"`},
	{`[^😀]`, 0, "split", "😁", nil, `["",""]`},
	{`[^😀]`, 0, "search", "x😁", []any{1, 1 << 30}, `[[1,2]]`},
	{`[^😀]?b`, 0, "search", "😁b", nil, `[[0,2]]`},
	{`(?:x|[^😀])`, 0, "search", "😁", nil, `[[0,1]]`},
	{`[^\uffff]`, 0, "search", "\U00010000", nil, `[[0,1]]`},
	{`(?i)[^𐐀]`, 0, "search", "𐐁", nil, `[[0,1]]`},
	{`(?i)[Ș-Ț]`, 0, "finditer", "ȘȚșț", nil, `[[0,1],[1,2],[2,3],[3,4]]`},
	{`(?i)[Ꭰ-Ᏼ]+`, 0, "finditer", "ꭰᏼ", nil, `[[0,2]]`},
	{`(?i)[\u01a5-\u01a6]`, 0, "finditer", "\u01a5\u01a6\u01a4\u0280", nil, `[[0,1],[1,2],[2,3],[3,4]]`},
	{`(?i)[^\u0100-\u024f]`, 0, "finditer", "\u01a6\u023a\u0280a", nil, `[[3,4]]`},
	{`(?i)[\u012f-\u0130]`, 0, "finditer", "ı", nil, `[[0,1]]`},
	{`(?i)[İ]`, 0, "finditer", "ıiI", nil, `[[0,1],[1,2],[2,3]]`},
	{`(?i)[^İ]`, 0, "finditer", "ıxi", nil, `[[1,2]]`},
	{`a`, 0, "sub", "aaa", []any{"b", -1}, `"aaa"`},
	{`,`, 0, "split", "a,b", []any{-1}, `["a,b"]`},
}

func spans(m *Match) [][2]int {
	if m == nil {
		return nil
	}
	out := make([][2]int, 0, m.re.groups+1)
	for i := 0; i <= m.re.groups; i++ {
		s, e := m.Span(i)
		out = append(out, [2]int{s, e})
	}
	return out
}

func runPyCase(re *Regexp, c pyCase) (any, error) {
	intArg := func(i, def int) int {
		if i < len(c.args) {
			return c.args[i].(int)
		}
		return def
	}
	switch c.op {
	case "search", "match", "fullmatch":
		f := map[string]func(string, int, int) (*Match, error){"search": re.SearchRange, "match": re.MatchRange, "fullmatch": re.FullMatchRange}[c.op]
		m, err := f(c.input, intArg(0, 0), intArg(1, 1<<30))
		return spans(m), err
	case "finditer":
		ms, err := re.FindIter(c.input)
		out := [][2]int{}
		for _, m := range ms {
			out = append(out, [2]int{m.Start(), m.End()})
		}
		return out, err
	case "findall":
		if re.Groups() > 1 {
			return re.FindAllGroups(c.input)
		}
		return re.FindAll(c.input)
	case "sub":
		count := 0
		if len(c.args) > 1 {
			count = c.args[1].(int)
		}
		return re.Sub(c.args[0].(string), c.input, count)
	case "split":
		return re.Split(c.input, intArg(0, 0))
	}
	return nil, fmt.Errorf("unknown op %s", c.op)
}

func TestPythonSemantics(t *testing.T) {
	for _, c := range pyCases {
		t.Run(fmt.Sprintf("%s/%s/%q", c.op, c.pattern, c.input), func(t *testing.T) {
			re, err := Compile(c.pattern, c.flags)
			if err != nil {
				t.Fatal(err)
			}
			got, err := runPyCase(re, c)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.want), &w); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("got %s, Python %s", raw, c.want)
			}
		})
	}
}

func TestCompileErrors(t *testing.T) {
	for _, c := range []struct {
		pattern string
		flags   Flags
		want    string
	}{
		{`\p{L}`, 0, `bad escape \p at position 0`},
		{`(`, IgnoreCase, `missing ), unterminated subpattern at position 0`},
		{`(?<=a|bc)x`, 0, `look-behind requires fixed-width pattern`},
		{`a(?i)b`, 0, `global flags not at the start of the expression at position 1`},
		{`a**`, 0, `multiple repeat at position 2`},
		{`*a`, 0, `nothing to repeat at position 0`},
		{`(?P<n>a)(?P<n>b)`, 0, `redefinition of group name 'n' as group 2; was group 1 at position 12`},
		{`\1(a)`, 0, `invalid group reference 1 at position 1`},
		{`[z-a]`, 0, `bad character range z-a at position 1`},
		{`(?P=x)`, 0, `unknown group name 'x' at position 4`},
		{`\q`, 0, `bad escape \q at position 0`},
		{`(?<=(a)\1)b`, 0, `cannot refer to group defined in the same lookbehind subpattern at position 9`},
		{`\u0041`, Bytes, `bad escape \u at position 0`},
		{`\N{EM DASH}`, 0, `named Unicode escape \N{EM DASH} is not supported at position 0`},
		{"a\n(", 0, `missing ), unterminated subpattern at position 2 (line 2, column 1)`},
		{"ab\ncd\n[", 0, `unterminated character set at position 6 (line 3, column 1)`},
	} {
		_, err := Compile(c.pattern, c.flags)
		var e *Error
		if !errors.As(err, &e) || err.Error() != c.want {
			t.Errorf("Compile(%q) = %v, want %q", c.pattern, err, c.want)
		}
	}
}

func TestTemplateErrors(t *testing.T) {
	re := MustCompile(`(a)`, 0)
	for repl, want := range map[string]string{
		`\q`:     `bad escape \q at position 0`,
		`\3`:     `invalid group reference 3 at position 1`,
		`\g<x>`:  `unknown group name 'x'`,
		`\g<1`:   `missing >, unterminated name at position 3`,
		`\g`:     `missing < at position 2`,
		`\g<1x>`: `bad character in group name '1x' at position 3`,
	} {
		if _, err := re.Sub(repl, "a", 0); err == nil || err.Error() != want {
			t.Errorf("Sub(%q) = %v, want %q", repl, err, want)
		}
	}
}

func TestEscape(t *testing.T) {
	if got, want := Escape("a.b*c d-e_é\t#&~"), "a\\.b\\*c\\ d\\-e_é\\\t\\#\\&\\~"; got != want {
		t.Errorf("Escape = %q, want %q", got, want)
	}
}

func TestTimeout(t *testing.T) {
	re := MustCompile(`(a+)+$`, 0)
	input := strings.Repeat("a", 40) + "b"
	start := time.Now()
	m, err := re.Search(input)
	if !errors.Is(err, ErrTimeout) || m != nil {
		t.Fatalf("Search = %v, %v; want nil, ErrTimeout", m, err)
	}
	if elapsed := time.Since(start); elapsed > 20*MatchTimeout {
		t.Errorf("timed out after %v", elapsed)
	}
	if out, err := re.Sub("x", input, 0); !errors.Is(err, ErrTimeout) || out != input {
		t.Errorf("Sub = %q, %v; want the input unchanged and ErrTimeout", out, err)
	}
}

func TestCache(t *testing.T) {
	a := MustCompile(`cache-a`, 0)
	if MustCompile(`cache-a`, 0) != a {
		t.Error("same pattern and flags compiled twice")
	}
	if MustCompile(`cache-a`, IgnoreCase) == a {
		t.Error("flags are not part of the cache key")
	}
	for i := range cacheSize + 10 {
		MustCompile(`cache-`+strconv.Itoa(i), 0)
	}
	cache.Lock()
	n := cache.order.Len()
	cache.Unlock()
	if n > cacheSize {
		t.Errorf("cache holds %d entries, bound %d", n, cacheSize)
	}
}

func TestMustCompilePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustCompile accepted an invalid pattern")
		}
	}()
	MustCompile(`(`, 0)
}

func TestNamedGroups(t *testing.T) {
	re := MustCompile(`(?P<x>a)(b)?(?P<y>c)`, 0)
	re.GroupIndex()["x"] = 9
	if re.GroupIndex()["x"] != 1 {
		t.Error("GroupIndex exposes the cached map")
	}
	m, err := re.Search("ac")
	if err != nil || m == nil {
		t.Fatalf("Search = %v, %v", m, err)
	}
	if x, ok := m.Named("x"); !ok || x != "a" {
		t.Errorf("x = %q, %v", x, ok)
	}
	if _, ok := m.GroupOK(2); ok {
		t.Error("group 2 participated")
	}
	if y, _ := m.Named("y"); y != "c" {
		t.Errorf("y = %q", y)
	}
}

func TestNoRegexpImport(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate source file")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := goparser.ParseFile(fset, path, nil, goparser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "regexp" || strings.HasPrefix(p, "regexp/") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports %s; use internal/rx", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("walked %s, not the module root: %v", root, err)
	}
}
