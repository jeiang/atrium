package notionid

import (
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/notion_api.py:DASHED":               dashed,
		"src/atrium/notion_api.py:UUID_HEX":             uuidHex,
		"src/atrium/notion_api.py:ids_in_text:f41d7d52": idRe,
	})
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"00000000-0000-0000-0000-0000000000AA":      "00000000-0000-0000-0000-0000000000aa",
		" 0000000000000000000000000000AAbb ":        "00000000-0000-0000-0000-00000000aabb",
		"0000-00000000-0000-0000-0000-0000000000aa": "0000-00000000-0000-0000-0000-0000000000aa",
		"not an id":                        "not an id",
		"  spaced  ":                       "spaced",
		"000000000000000000000000000000aa": "00000000-0000-0000-0000-0000000000aa",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIDsInText(t *testing.T) {
	text := "see https://www.notion.so/Kitchen-0000000000000000000000000000aaaa and 00000000-0000-0000-0000-00000000AAAA, then 00000000-0000-0000-0000-000000000001"
	want := []string{"00000000-0000-0000-0000-00000000aaaa", "00000000-0000-0000-0000-000000000001"}
	if got := IDsInText(text); !slices.Equal(got, want) {
		t.Errorf("IDsInText = %v, want %v", got, want)
	}
	if got := IDsInText("nothing"); len(got) != 0 {
		t.Errorf("IDsInText(nothing) = %v", got)
	}
}
