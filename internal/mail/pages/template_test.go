package pages

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

func renderFragment(t *testing.T, name, context string, mutate func(c tpltest.Case)) string {
	t.Helper()
	c := tpltest.Load(t, context+".context.json")
	if mutate != nil {
		mutate(c)
	}
	var out bytes.Buffer
	if err := goldenEngine(t, c).Fragment(&out, name, goldenFrame(c)); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestZeroConfidenceRendersAsZeroPercent(t *testing.T) {
	row := renderFragment(t, "_triage_row.gohtml", "_triage_row", func(c tpltest.Case) {
		c.Context["row"].(map[string]any)["confidence"] = 0.0
	})
	contains(t, row, `<span class="sr-only">confidence </span>0%`)
	card := renderFragment(t, "_decision_card.gohtml", "_decision_card@stage-llm", func(c tpltest.Case) {
		c.Context["message"].(map[string]any)["decision"].(map[string]any)["confidence"] = 0.0
	})
	contains(t, card, `tabular-nums">0%</span>`)
	none := renderFragment(t, "_triage_row.gohtml", "_triage_row", func(c tpltest.Case) {
		c.Context["row"].(map[string]any)["confidence"] = nil
	})
	lacks(t, none, "confidence </span>")
}

func TestCorrectSlotsAreEmptyWithoutAForm(t *testing.T) {
	card := renderFragment(t, "_decision_card.gohtml", "_decision_card@none", nil)
	contains(t, card, `<div id="correct-slot-message-501" class="empty:hidden"></div>`)
	row := renderFragment(t, "_triage_row.gohtml", "_triage_row", func(c tpltest.Case) {
		delete(c.Context, "correct")
		delete(c.Context, "toast")
	})
	contains(t, row, `<div id="correct-slot-row-501" class="empty:hidden lg:col-span-4"></div>`)
	lacks(t, row, "hx-swap-oob")
}

func TestInlineToastKeepsTheSpaceBeforeItsDescription(t *testing.T) {
	card := renderFragment(t, "_decision_card.gohtml", "_decision_card", nil)
	contains(t, card, `<span class="font-medium">Saved</span> He said`)
}

func TestMessageBodyKeepsItsWhitespace(t *testing.T) {
	body := "  indented\n\n\ttabbed   line\n"
	got := renderFragment(t, "_message.gohtml", "_message", func(c tpltest.Case) {
		c.Context["message"].(map[string]any)["body"] = body
	})
	want := `<div class="max-w-prose whitespace-pre-wrap break-words text-sm/relaxed">` + body + `</div>`
	if !strings.Contains(got, want) {
		t.Errorf("the body was reflowed:\n%s", got)
	}
}

func TestFlagJudgmentIsTriState(t *testing.T) {
	got := renderFragment(t, "_message.gohtml", "_message", nil)
	contains(t, got, "Deadline or payment: not judged", `line-through decoration-muted-foreground/50">Security event</span><span class="sr-only">: no</span>`, "Sender is a contact")
}

func TestStageBarListsOnlyNonZeroStages(t *testing.T) {
	c := tpltest.Load(t, "triage.context.json")
	var out bytes.Buffer
	if err := goldenEngine(t, c).Page(&out, "triage.gohtml", goldenFrame(c)); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), `aria-label="By stage: Correction 1, Rule 2, Learned 5, 9B 3, Fallback 1"`)
	if n := strings.Count(out.String(), `style="flex-grow: `); n != 10 {
		t.Errorf("stage segments = %d, want 5 per summary", n)
	}
}
