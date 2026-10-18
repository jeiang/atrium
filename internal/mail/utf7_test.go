package mail

import (
	"reflect"
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

func TestUTF7Patterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{"src/atrium/imap.py:34": utf7ShiftRE})
}

func TestCollapseLabelsToDeepestLeaf(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{[]string{`\Inbox`, `\Important`, "Personal", "Personal/Friends", "Finance"}, []string{"Finance", "Personal/Friends"}},
		{[]string{"A", "A/B", "A/B/C"}, []string{"A/B/C"}},
		{[]string{"A", "AB"}, []string{"A", "AB"}},
		{[]string{`\Inbox`}, []string{}},
	}
	for _, tt := range tests {
		if got := CollapseLabels(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CollapseLabels(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUTF7Decode(t *testing.T) {
	for in, want := range map[string]string{"Caf&AOk-": "Café", "A&-B": "A&B", "plain": "plain", "&ZeVnLIqe-": "日本語", "a&b": "a&b"} {
		got, err := UTF7Decode(in)
		if err != nil || got != want {
			t.Errorf("UTF7Decode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"&A-", "&AO-", "&2D0-"} {
		if got, err := UTF7Decode(in); err == nil {
			t.Errorf("UTF7Decode(%q) = %q, want an error as Python raises", in, got)
		}
	}
}

func TestUTF7RoundtripAndEncode(t *testing.T) {
	for _, name := range []string{"Plain/Box", "Café & Co", "日本", "😀 mail"} {
		got, err := UTF7Decode(UTF7Encode(name))
		if err != nil || got != name {
			t.Errorf("round trip %q = %q, %v", name, got, err)
		}
	}
	for in, want := range map[string]string{"a&b": "a&-b", "Café": "Caf&AOk-", "日本語": "&ZeVnLIqe-", "~/x": "~/x"} {
		if got := UTF7Encode(in); got != want {
			t.Errorf("UTF7Encode(%q) = %q, want %q", in, got, want)
		}
	}
}
