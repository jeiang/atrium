package mail

import (
	"context"
	"errors"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/safety"
)

var readMethods = map[string]bool{
	"Connect":       true,
	"Close":         true,
	"HasCapability": true,
	"ListMailboxes": true,
	"Status":        true,
	"Select":        true,
	"UIDSearch":     true,
	"UIDFetch":      true,
	"IdleChanges":   true,
	"Noop":          true,
}

var writeActions = map[string]string{
	"SelectRW":         "IMAP SELECT read-write",
	"Copy":             "IMAP COPY",
	"StoreFlags":       "IMAP STORE",
	"StoreLabels":      "IMAP STORE",
	"UIDExpunge":       "IMAP EXPUNGE",
	"Append":           "IMAP APPEND",
	"CreateMailbox":    "IMAP CREATE",
	"SubscribeMailbox": "IMAP SUBSCRIBE",
	"DeleteMailbox":    "IMAP DELETE",
}

type recorder struct{ calls []string }

func (r *recorder) touch(name string) { r.calls = append(r.calls, name) }

func (r *recorder) Connect(context.Context) error { r.touch("Connect"); return nil }
func (r *recorder) Close()                        { r.touch("Close") }
func (r *recorder) HasCapability(string) bool     { r.touch("HasCapability"); return false }
func (r *recorder) ListMailboxes(context.Context) ([]MailboxInfo, error) {
	r.touch("ListMailboxes")
	return nil, nil
}
func (r *recorder) Status(context.Context, string) (Status, error) {
	r.touch("Status")
	return Status{}, nil
}
func (r *recorder) Select(context.Context, string) error { r.touch("Select"); return nil }
func (r *recorder) UIDSearch(context.Context, SearchCriteria) ([]uint32, error) {
	r.touch("UIDSearch")
	return nil, nil
}
func (r *recorder) UIDFetch(context.Context, UIDSet, FetchItems) ([]Message, error) {
	r.touch("UIDFetch")
	return nil, nil
}
func (r *recorder) IdleChanges(context.Context, time.Duration) (bool, error) {
	r.touch("IdleChanges")
	return false, nil
}
func (r *recorder) Noop(context.Context) error             { r.touch("Noop"); return nil }
func (r *recorder) SelectRW(context.Context, string) error { r.touch("SelectRW"); return nil }
func (r *recorder) Copy(context.Context, UIDSet, string) (*CopyUID, error) {
	r.touch("Copy")
	return nil, nil
}
func (r *recorder) StoreFlags(context.Context, UIDSet, bool, ...string) error {
	r.touch("StoreFlags")
	return nil
}
func (r *recorder) StoreLabels(context.Context, UIDSet, bool, ...string) error {
	r.touch("StoreLabels")
	return nil
}
func (r *recorder) UIDExpunge(context.Context, UIDSet) error { r.touch("UIDExpunge"); return nil }
func (r *recorder) Append(context.Context, string, []byte, ...string) error {
	r.touch("Append")
	return nil
}
func (r *recorder) CreateMailbox(context.Context, string) error {
	r.touch("CreateMailbox")
	return nil
}
func (r *recorder) SubscribeMailbox(context.Context, string) error {
	r.touch("SubscribeMailbox")
	return nil
}
func (r *recorder) DeleteMailbox(context.Context, string) error {
	r.touch("DeleteMailbox")
	return nil
}

var (
	ctxType = reflect.TypeFor[context.Context]()
	errType = reflect.TypeFor[error]()
)

func clientMethods(t *testing.T) []reflect.Method {
	t.Helper()
	typ := reflect.TypeFor[Client]()
	var out []reflect.Method
	for i := range typ.NumMethod() {
		if m := typ.Method(i); m.IsExported() {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		t.Fatal("Client has no methods")
	}
	return out
}

func call(ctx context.Context, c Client, m reflect.Method) error {
	fn := reflect.ValueOf(c).MethodByName(m.Name)
	ft := fn.Type()
	args := make([]reflect.Value, 0, ft.NumIn())
	for i := range ft.NumIn() {
		in := ft.In(i)
		switch {
		case ft.IsVariadic() && i == ft.NumIn()-1:
		case in == ctxType:
			args = append(args, reflect.ValueOf(ctx))
		default:
			args = append(args, reflect.Zero(in))
		}
	}
	out := fn.Call(args)
	if len(out) == 0 || ft.Out(ft.NumOut()-1) != errType || out[len(out)-1].IsNil() {
		return nil
	}
	return out[len(out)-1].Interface().(error)
}

func TestEveryWriteMethodCallsTheGateFirst(t *testing.T) {
	env := config.Map(map[string]string{"ATRIUM_MODE": "shadow"})
	seen := map[string]bool{}
	for _, m := range clientMethods(t) {
		seen[m.Name] = true
		t.Run(m.Name, func(t *testing.T) {
			rec := &recorder{}
			err := call(context.Background(), Gated(rec, env), m)
			if readMethods[m.Name] {
				if err != nil || len(rec.calls) != 1 || rec.calls[0] != m.Name {
					t.Fatalf("read %s: err %v, calls %v", m.Name, err, rec.calls)
				}
				return
			}
			if m.Type.NumOut() == 0 || m.Type.Out(m.Type.NumOut()-1) != errType {
				t.Fatalf("write method %s cannot report a refusal: it must return error", m.Name)
			}
			var refused safety.WriteRefused
			if !errors.As(err, &refused) {
				t.Fatalf("%s in shadow mode: err %v, want WriteRefused (add it to readMethods only if it never writes)", m.Name, err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("%s touched the connection before the gate refused: %v", m.Name, rec.calls)
			}
			want, ok := writeActions[m.Name]
			if !ok {
				t.Fatalf("%s has no expected gate action in writeActions", m.Name)
			}
			if msg := "refusing mailbox write " + py.Repr(want) + ": ATRIUM_MODE is not live"; err.Error() != msg {
				t.Fatalf("%s refusal = %q, want %q", m.Name, err, msg)
			}
		})
	}
	for name := range readMethods {
		if !seen[name] {
			t.Errorf("readMethods lists %s, which Client does not have", name)
		}
	}
	for name := range writeActions {
		if !seen[name] {
			t.Errorf("writeActions lists %s, which Client does not have", name)
		}
	}
}

func TestGateRefusesAutomaticWritesWhilePaused(t *testing.T) {
	pause := filepath.Join(t.TempDir(), "pause")
	if err := safety.SetPaused(pause, true); err != nil {
		t.Fatal(err)
	}
	env := config.Map(map[string]string{"ATRIUM_MODE": "live"})
	ctx := safety.AutomaticWrites(context.Background(), pause)
	for _, m := range clientMethods(t) {
		if readMethods[m.Name] {
			continue
		}
		rec := &recorder{}
		err := call(ctx, Gated(rec, env), m)
		msg := "refusing automatic mailbox write " + py.Repr(writeActions[m.Name]) + ": atrium is paused"
		if err == nil || err.Error() != msg || len(rec.calls) != 0 {
			t.Errorf("%s while paused: err %v, calls %v; want %q and no calls", m.Name, err, rec.calls, msg)
		}
	}
}

func TestEveryMethodReachesTheConnectionWhenAllowed(t *testing.T) {
	bg := context.Background()
	shadow := config.Map(map[string]string{"ATRIUM_MODE": "shadow"})
	live := config.Map(map[string]string{"ATRIUM_MODE": "live"})
	scopes := []struct {
		name string
		ctx  context.Context
		env  config.Env
	}{
		{"live", bg, live},
		{"approved proposal in shadow", safety.ApprovedWrites(bg, "1"), shadow},
		{"undo in shadow", safety.ApprovedWrites(bg, "undo-1"), shadow},
	}
	for _, s := range scopes {
		for _, m := range clientMethods(t) {
			rec := &recorder{}
			if err := call(s.ctx, Gated(rec, s.env), m); err != nil || len(rec.calls) != 1 || rec.calls[0] != m.Name {
				t.Errorf("%s: %s err %v, calls %v", s.name, m.Name, err, rec.calls)
			}
		}
	}
}

func TestOperatorSendOnlyAppendsToSent(t *testing.T) {
	env := config.Map(map[string]string{"ATRIUM_MODE": "shadow"})
	ctx := safety.OperatorSend(context.Background(), "Sent Messages")
	rec := &recorder{}
	c := Gated(rec, env)
	if err := c.Append(ctx, "Sent Messages", []byte("raw"), `\Seen`); err != nil {
		t.Fatalf("append to Sent: %v", err)
	}
	if err := c.Append(ctx, "INBOX", []byte("raw"), `\Seen`); err == nil {
		t.Fatal("append to INBOX allowed under operator send")
	}
	if err := c.SelectRW(ctx, "Sent Messages"); err == nil {
		t.Fatal("read-write select allowed under operator send")
	}
	if err := c.StoreFlags(ctx, UIDs(1), true, `\Seen`); err == nil {
		t.Fatal("store allowed under operator send")
	}
	if want := []string{"Append"}; !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("calls = %v, want %v", rec.calls, want)
	}
}

func TestUIDSetString(t *testing.T) {
	tests := []struct {
		set  UIDSet
		want string
	}{
		{UIDs(1, 2, 30), "1,2,30"},
		{UIDs(7), "7"},
		{AllUIDs(), "1:*"},
	}
	for _, tt := range tests {
		if got := tt.set.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestNoMoveInMailPackages(t *testing.T) {
	banned := map[string]bool{"Move": true, "UIDMove": true}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		var s scanner.Scanner
		s.Init(fset.AddFile(path, -1, len(src)), src, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				return nil
			}
			switch tok {
			case token.IDENT:
				if banned[lit] {
					t.Errorf("%s: identifier %s", fset.Position(pos), lit)
				}
			case token.STRING:
				text, uerr := strconv.Unquote(lit)
				if uerr != nil {
					text = lit
				}
				for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
					if w == "MOVE" {
						t.Errorf("%s: string %s names the IMAP MOVE command", fset.Position(pos), lit)
					}
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}
