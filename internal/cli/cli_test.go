package cli

import (
	"bytes"
	"flag"
	"io"
	"strings"
	"testing"
)

func fakeApp(setupCalls *int) *App {
	app := New("atrium", func(io.Writer) { *setupCalls++ })
	fake := &Command{Name: "fake", Help: "exit with --code"}
	fake.Setup = func(fs *flag.FlagSet) Handler {
		code := fs.Int("code", 0, "exit code to return")
		return func(env *Env) int {
			if len(env.Args) > 0 {
				return env.UsageError("unrecognized arguments: %s", strings.Join(env.Args, " "))
			}
			return *code
		}
	}
	group := &Command{Name: "group", Help: "nested commands"}
	group.Add(&Command{Name: "leaf", Help: "succeed", Setup: func(*flag.FlagSet) Handler {
		return func(env *Env) int { return ExitOK }
	}})
	app.Add(fake, group)
	return app
}

func TestRun(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		code   int
		setup  int
		stdout string
		stderr string
	}{
		{name: "no subcommand", args: nil, code: ExitUsage, stderr: "atrium: error: the following arguments are required: command"},
		{name: "help lists commands", args: []string{"--help"}, code: ExitOK, stdout: "  fake   exit with --code\n  group  nested commands\n"},
		{name: "short help", args: []string{"-h"}, code: ExitOK, stdout: "usage: atrium [-h] {fake,group} ..."},
		{name: "unknown command", args: []string{"nope"}, code: ExitUsage, stderr: "invalid choice: 'nope' (choose from fake, group)"},
		{name: "unknown flag before command", args: []string{"--nope"}, code: ExitUsage, stderr: "unrecognized arguments: --nope"},
		{name: "handler 0", args: []string{"fake"}, code: ExitOK, setup: 1},
		{name: "handler 1", args: []string{"fake", "--code", "1"}, code: ExitError, setup: 1},
		{name: "handler 2", args: []string{"fake", "--code=2"}, code: ExitUsage, setup: 1},
		{name: "bad flag value", args: []string{"fake", "--code", "x"}, code: ExitUsage, stderr: "atrium fake: error: invalid value"},
		{name: "unknown leaf flag", args: []string{"fake", "--bogus"}, code: ExitUsage, stderr: "usage: atrium fake [-h] [--code]"},
		{name: "handler usage error", args: []string{"fake", "extra"}, code: ExitUsage, setup: 1, stderr: "atrium fake: error: unrecognized arguments: extra"},
		{name: "leaf help", args: []string{"fake", "--help"}, code: ExitOK, stdout: "exit code to return"},
		{name: "group without subcommand", args: []string{"group"}, code: ExitUsage, stderr: "usage: atrium group [-h] {leaf} ..."},
		{name: "nested leaf", args: []string{"group", "leaf"}, code: ExitOK, setup: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := 0
			var stdout, stderr bytes.Buffer
			code := fakeApp(&setup).Run(tt.args, &stdout, &stderr)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.code, stderr.String())
			}
			if setup != tt.setup {
				t.Errorf("setup calls = %d, want %d", setup, tt.setup)
			}
			if !strings.Contains(stdout.String(), tt.stdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.stdout)
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderr)
			}
		})
	}
}
