package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
)

func stub(name string, effect tool.Effect) tool.Tool {
	return tool.Tool{
		Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(context.Context, *tool.Context, tool.Args) (any, error) { return nil, nil },
		Effect:  effect, Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
	}
}

func TestValidateAnnotationsRejectsMissingFlags(t *testing.T) {
	if err := ValidateAnnotations([]tool.Tool{stub("plain", tool.Read)}); err == nil {
		t.Fatal("a tool without annotations passed the check")
	}
	annotated := stub("annotated", tool.Read)
	annotated.Annotated = true
	if err := ValidateAnnotations([]tool.Tool{annotated}); err != nil {
		t.Fatalf("annotated tool failed: %v", err)
	}
}

func TestWriteUndoRequiresAnUndoOp(t *testing.T) {
	r := New()
	if err := r.Add(stub("write_without_undo", tool.WriteUndo)); err == nil {
		t.Fatal("write_undo without an undo op was accepted")
	}
	withUndo := stub("write_with_undo", tool.WriteUndo)
	withUndo.Undo = "test_restore"
	if err := r.Add(withUndo); err != nil {
		t.Fatalf("write_undo with an undo op failed: %v", err)
	}
}

func TestPolicyMostRestrictiveWins(t *testing.T) {
	rules := []Rule{
		{Kind: tool.Chat, Effect: tool.Write, Decision: Ask},
		{Kind: tool.Chat, Effect: tool.Write, Decision: Denied},
		{Kind: tool.Cron, Effect: tool.WriteUndo, Decision: Denied},
		{Kind: tool.Chat, Effect: tool.Read, Decision: Ask},
	}
	if got := Decide(rules, tool.Chat, tool.Write); got != Denied {
		t.Errorf("chat write = %v, want denied", got)
	}
	if got := Decide(rules, tool.Cron, tool.WriteUndo); got != Denied {
		t.Errorf("cron write_undo = %v, want denied", got)
	}
	if got := Decide(rules, tool.Chat, tool.Read); got != Ask {
		t.Errorf("chat read = %v, want ask", got)
	}
	if got := Decide(rules, tool.Cron, tool.Read); got != Allowed {
		t.Errorf("cron read = %v, want allowed", got)
	}
	for _, kind := range []tool.Kind{tool.Cron, tool.Nightly, tool.Sub} {
		if got := Decide(Policy, kind, tool.WriteUndo); got != Denied {
			t.Errorf("%s write_undo = %v, want denied", kind, got)
		}
	}
}

func TestTaintDropsEgressAndPush(t *testing.T) {
	r := New()
	mail := stub("mail_read", tool.Read)
	mail.Flags.PrivateData = true
	web := stub("web_fetch", tool.Read)
	web.Flags.Web = true
	untrusted := stub("issue_read", tool.Read)
	untrusted.Flags.UntrustedOutput = true
	egress := stub("link_post", tool.Read)
	egress.Flags.ExternalEffect = true
	push := stub("notify_operator", tool.Notify)
	if err := r.Add(mail, web, untrusted, egress, push); err != nil {
		t.Fatal(err)
	}
	names := func(set Set) []string {
		var out []string
		for _, tl := range set.Dispatch {
			out = append(out, tl.Name)
		}
		return out
	}
	before, err := r.Admit(context.Background(), 1, tool.Chat, All(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names(before), ","), "link_post") || !strings.Contains(strings.Join(names(before), ","), "notify_operator") {
		t.Fatalf("egress and push tools missing before taint: %v", names(before))
	}
	r.Taint(1, mail)
	after, err := r.Admit(context.Background(), 1, tool.Chat, All(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names(after) {
		if name == "link_post" || name == "notify_operator" {
			t.Errorf("%s survived private-data taint", name)
		}
	}
	if !strings.Contains(strings.Join(names(after), ","), "issue_read") {
		t.Errorf("untrusted read was dropped by private-data taint: %v", names(after))
	}
	other, err := r.Admit(context.Background(), 2, tool.Chat, All(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names(other), ","), "link_post") {
		t.Errorf("thread 2 inherited thread 1's taint: %v", names(other))
	}
	r.Taint(3, untrusted)
	untainted, err := r.Admit(context.Background(), 3, tool.Chat, All(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names(untainted), ","), "link_post") {
		t.Errorf("untrusted_output alone tainted the thread: %v", names(untainted))
	}
}

func TestWebSearchAllowedCallers(t *testing.T) {
	set := Set{Server: []tool.Tool{webSearchTool()}}
	raw, err := set.Params(5, nil, false)
	if err != nil || len(raw) != 1 {
		t.Fatalf("params: %v %d", err, len(raw))
	}
	if !strings.Contains(string(raw[0]), `"allowed_callers":["direct"]`) {
		t.Errorf("allowed_callers missing: %s", raw[0])
	}
	raw, err = set.Params(5, nil, true)
	if err != nil || len(raw) != 1 {
		t.Fatalf("params: %v %d", err, len(raw))
	}
	if strings.Contains(string(raw[0]), "allowed_callers") {
		t.Errorf("allowed_callers present with dynamic filtering: %s", raw[0])
	}
}
