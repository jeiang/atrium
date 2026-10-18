package safety

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/config"
)

var (
	shadow  = config.Map(map[string]string{"ATRIUM_MODE": "shadow"})
	live    = config.Map(map[string]string{"ATRIUM_MODE": "live"})
	unset   = config.Map(map[string]string{})
	invalid = config.Map(map[string]string{"ATRIUM_MODE": "dry"})
)

func refused(err error) bool {
	var r WriteRefused
	return errors.As(err, &r)
}

func TestGate(t *testing.T) {
	dir := t.TempDir()
	paused := filepath.Join(dir, "pause")
	if err := SetPaused(paused, true); err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(dir, "absent", "pause")
	bg := context.Background()
	tests := []struct {
		name    string
		ctx     context.Context
		env     config.Env
		action  string
		target  string
		wantErr string
	}{
		{"default mode refuses", bg, unset, "move", "", "refusing mailbox write 'move': ATRIUM_MODE is not live"},
		{"shadow refuses", bg, shadow, "move", "", "refusing mailbox write 'move': ATRIUM_MODE is not live"},
		{"live allows", bg, live, "move", "", ""},
		{"invalid mode is an error", bg, invalid, "move", "", "ATRIUM_MODE must be one of ('shadow', 'live'), got 'dry'"},
		{"approved allows in shadow", ApprovedWrites(bg, "7"), shadow, "move", "", ""},
		{"approved skips the mode read", ApprovedWrites(bg, "7"), invalid, "move", "", ""},
		{"undo allows in shadow", ApprovedWrites(bg, "undo-1"), shadow, "move", "", ""},
		{"automatic not paused, live", AutomaticWrites(bg, running), live, "move", "", ""},
		{"automatic not paused, shadow", AutomaticWrites(bg, running), shadow, "move", "", "refusing mailbox write 'move': ATRIUM_MODE is not live"},
		{"automatic paused refuses live", AutomaticWrites(bg, paused), live, "move", "", "refusing automatic mailbox write 'move': atrium is paused"},
		{"pause wins over nested approval", ApprovedWrites(AutomaticWrites(bg, paused), "1"), live, "move", "", "refusing automatic mailbox write 'move': atrium is paused"},
		{"pause wins over approval in either order", AutomaticWrites(ApprovedWrites(bg, "1"), paused), live, "move", "", "refusing automatic mailbox write 'move': atrium is paused"},
		{"approval outside automatic ignores pause", ApprovedWrites(bg, "undo-1"), shadow, "move", "", ""},
		{"append outside operator send refuses", bg, unset, AppendAction, "Sent Messages", "refusing mailbox write 'IMAP APPEND': ATRIUM_MODE is not live"},
		{"operator send appends to sent", OperatorSend(bg, "Sent Messages"), unset, AppendAction, "Sent Messages", ""},
		{"operator send refuses other mailbox", OperatorSend(bg, "Sent Messages"), unset, AppendAction, "INBOX", "refusing mailbox write 'IMAP APPEND': ATRIUM_MODE is not live"},
		{"operator send refuses other verbs", OperatorSend(bg, "Sent Messages"), unset, "IMAP STORE", "Sent Messages", "refusing mailbox write 'IMAP STORE': ATRIUM_MODE is not live"},
		{"operator send refuses read-write select", OperatorSend(bg, "Sent Messages"), shadow, "IMAP SELECT read-write", "Sent Messages", "refusing mailbox write 'IMAP SELECT read-write': ATRIUM_MODE is not live"},
		{"operator send still invalid mode", OperatorSend(bg, "Sent Messages"), invalid, "IMAP STORE", "", "ATRIUM_MODE must be one of ('shadow', 'live'), got 'dry'"},
		{"operator send in live is live", OperatorSend(bg, "Sent Messages"), live, "IMAP STORE", "INBOX", ""},
		{"automatic paused refuses operator send", OperatorSend(AutomaticWrites(bg, paused), "Sent Messages"), live, AppendAction, "Sent Messages", "refusing automatic mailbox write 'IMAP APPEND': atrium is paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteGate(tt.ctx, tt.env, tt.action, tt.target)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("WriteGate = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("WriteGate = %v, want %q", err, tt.wantErr)
			}
			wantRefused := !strings.HasPrefix(tt.wantErr, "ATRIUM_MODE must be")
			if refused(err) != wantRefused {
				t.Fatalf("WriteGate error %T refused=%v, want %v", err, refused(err), wantRefused)
			}
		})
	}
}

func TestApprovedScopeOnlyInside(t *testing.T) {
	bg := context.Background()
	inner := ApprovedWrites(bg, "7")
	if id, ok := ApprovedProposal(inner); !ok || id != "7" {
		t.Fatalf("ApprovedProposal(inner) = %q, %v", id, ok)
	}
	if err := WriteGate(inner, shadow, "move", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ApprovedProposal(bg); ok {
		t.Fatal("approval leaked to the parent context")
	}
	if err := WriteGate(bg, shadow, "move", ""); !refused(err) {
		t.Fatalf("WriteGate outside approval = %v", err)
	}
}

func TestPauseTakesEffectWithinAutomaticScope(t *testing.T) {
	pause := filepath.Join(t.TempDir(), "pause")
	ctx := AutomaticWrites(context.Background(), pause)
	if err := WriteGate(ctx, live, "move", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetPaused(pause, true); err != nil {
		t.Fatal(err)
	}
	if err := WriteGate(ctx, live, "move", ""); !refused(err) {
		t.Fatalf("WriteGate while paused = %v", err)
	}
	if err := WriteGate(ApprovedWrites(context.Background(), "undo-1"), live, "move", ""); err != nil {
		t.Fatalf("undo outside automatic scope refused: %v", err)
	}
	if err := SetPaused(pause, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteGate(ctx, live, "move", ""); err != nil {
		t.Fatalf("WriteGate after resume = %v", err)
	}
}

func TestModeReadOnEveryCall(t *testing.T) {
	mode := "live"
	env := func(name string) (string, bool) {
		if name == "ATRIUM_MODE" {
			return mode, true
		}
		return "", false
	}
	if err := WriteGate(context.Background(), env, "move", ""); err != nil {
		t.Fatal(err)
	}
	mode = "shadow"
	if err := WriteGate(context.Background(), env, "move", ""); !refused(err) {
		t.Fatalf("WriteGate after switching to shadow = %v", err)
	}
}

func TestSetPaused(t *testing.T) {
	pause := filepath.Join(t.TempDir(), "state", "nested", "pause")
	if IsPaused(pause) {
		t.Fatal("paused before touch")
	}
	for range 2 {
		if err := SetPaused(pause, true); err != nil {
			t.Fatal(err)
		}
		if !IsPaused(pause) {
			t.Fatal("not paused after touch")
		}
	}
	for range 2 {
		if err := SetPaused(pause, false); err != nil {
			t.Fatal(err)
		}
		if IsPaused(pause) {
			t.Fatal("paused after unlink")
		}
	}
}

func TestPauseIsExistenceOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pause")
	if err := os.WriteFile(file, []byte("anything"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsPaused(file) {
		t.Fatal("non-empty pause file not paused")
	}
	if !IsPaused(dir) {
		t.Fatal("pause path that is a directory not paused")
	}
}
