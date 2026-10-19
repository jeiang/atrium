package tool_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

func TestBoundedInt(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	cases := []struct {
		name  string
		args  py.Dict
		want  int64
		fails string
	}{
		{"default", py.Dict{}, 10, ""},
		{"in range", py.Dict{{Key: "n", Value: int64(7)}}, 7, ""},
		{"clamped high", py.Dict{{Key: "n", Value: int64(500)}}, 100, ""},
		{"clamped low", py.Dict{{Key: "n", Value: int64(-3)}}, 1, ""},
		{"float truncates", py.Dict{{Key: "n", Value: 7.9}}, 7, ""},
		{"negative float truncates toward zero", py.Dict{{Key: "n", Value: -0.5}}, 1, ""},
		{"big int", py.Dict{{Key: "n", Value: huge}}, 100, ""},
		{"negative big int", py.Dict{{Key: "n", Value: new(big.Int).Neg(huge)}}, 1, ""},
		{"huge float", py.Dict{{Key: "n", Value: 1e300}}, 100, ""},
		{"bool", py.Dict{{Key: "n", Value: true}}, 0, "n must be a number"},
		{"string", py.Dict{{Key: "n", Value: "5"}}, 0, "n must be a number"},
		{"null", py.Dict{{Key: "n", Value: nil}}, 0, "n must be a number"},
		{"nan", py.Dict{{Key: "n", Value: math.NaN()}}, 0, "ValueError"},
		{"inf", py.Dict{{Key: "n", Value: math.Inf(1)}}, 0, "OverflowError"},
	}
	for _, c := range cases {
		got, err := tool.BoundedInt(c.args, "n", 10, 100, 1)
		if c.fails != "" {
			if err == nil || err.Error() != c.fails {
				t.Errorf("%s: error %v, want %q", c.name, err, c.fails)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	_, err := tool.BoundedInt(py.Dict{{Key: "n", Value: "x"}}, "n", 1, 2, 1)
	if _, ok := tool.Message(err); !ok {
		t.Errorf("a non-number is a ToolError, got %v", err)
	}
	_, err = tool.BoundedInt(py.Dict{{Key: "n", Value: math.NaN()}}, "n", 1, 2, 1)
	var raised *tool.Raised
	if !errors.As(err, &raised) || raised.Class != "ValueError" {
		t.Errorf("NaN raises ValueError, got %v", err)
	}
}

func TestTextArg(t *testing.T) {
	cases := []struct {
		name     string
		args     py.Dict
		required bool
		text     string
		ok       bool
		err      string
	}{
		{"absent", py.Dict{}, false, "", false, ""},
		{"absent required", py.Dict{}, true, "", false, "q is required"},
		{"null required", py.Dict{{Key: "q", Value: nil}}, true, "", false, "q is required"},
		{"empty required", py.Dict{{Key: "q", Value: ""}}, true, "", false, "q is required"},
		{"blank is None even when required", py.Dict{{Key: "q", Value: " \t "}}, true, "", false, ""},
		{"stripped", py.Dict{{Key: "q", Value: "  hi\u00a0"}}, false, "hi", true, ""},
		{"number", py.Dict{{Key: "q", Value: int64(3)}}, false, "", false, "q must be a string"},
		{"false", py.Dict{{Key: "q", Value: false}}, false, "", false, "q must be a string"},
	}
	for _, c := range cases {
		text, ok, err := tool.TextArg(c.args, "q", c.required)
		if (err != nil) != (c.err != "") || (err != nil && err.Error() != c.err) || text != c.text || ok != c.ok {
			t.Errorf("%s: got %q, %v, %v; want %q, %v, %q", c.name, text, ok, err, c.text, c.ok, c.err)
		}
	}
}

func TestClip(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
	}{
		{"", 5, ""},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcd…"},
		{"héllo wörld", 6, "héllo…"},
		{"😀😀😀😀", 3, "😀😀…"},
	} {
		if got := tool.Clip(c.in, c.limit); got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestMessage(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
		ok   bool
	}{
		{tool.Error("bad"), "bad", true},
		{tool.Error(""), "ToolError", true},
		{tool.NotConfigured(""), "NotConfigured", true},
		{tool.NotConfigured("no key"), "no key", true},
		{errors.Join(errors.New("x"), tool.Error("wrapped")), "wrapped", true},
		{errors.New("boom"), "", false},
		{&tool.Raised{Class: "KeyError"}, "", false},
	} {
		got, ok := tool.Message(c.err)
		if got != c.want || ok != c.ok {
			t.Errorf("Message(%v) = %q, %v; want %q, %v", c.err, got, ok, c.want, c.ok)
		}
	}
}

func TestKinds(t *testing.T) {
	if !tool.DefaultKinds.Has(tool.Chat) || tool.DefaultKinds.Has(tool.Sub) {
		t.Error("DefaultKinds is chat, cron, nightly")
	}
	if !tool.Cron.Unattended() || !tool.Nightly.Unattended() || tool.Chat.Unattended() || tool.Sub.Unattended() {
		t.Error("only cron and nightly are unattended")
	}
	if tool.Kind("code").Valid() || !tool.Sub.Valid() {
		t.Error("code is not a ported kind")
	}
}
