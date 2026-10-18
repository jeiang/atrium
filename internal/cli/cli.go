package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

type Handler func(env *Env) int

type Command struct {
	Name     string
	Help     string
	Setup    func(fs *flag.FlagSet) Handler
	Commands []*Command
}

func (c *Command) Add(cmds ...*Command) {
	c.Commands = append(c.Commands, cmds...)
}

type Env struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
	prog   string
	fs     *flag.FlagSet
}

func (e *Env) UsageError(format string, a ...any) int {
	writeLeafUsage(e.Stderr, e.prog, e.fs)
	printf(e.Stderr, "%s: error: %s\n", e.prog, fmt.Sprintf(format, a...))
	return ExitUsage
}

// App is the root command. A non-empty Version makes a root `--version`
// print "<name> <Version>"; it stays out of the help text, which mirrors the
// Python argparse parser.
type App struct {
	Version string
	root    Command
	setup   func(stderr io.Writer)
}

func New(name string, setup func(stderr io.Writer)) *App {
	return &App{root: Command{Name: name}, setup: setup}
}

func (a *App) Add(cmds ...*Command) {
	a.root.Add(cmds...)
}

func (a *App) Run(args []string, stdout, stderr io.Writer) int {
	cmd, prog := &a.root, a.root.Name
	for cmd.Setup == nil {
		if len(args) == 0 {
			writeGroupUsage(stderr, prog, cmd)
			printf(stderr, "%s: error: the following arguments are required: command\n", prog)
			return ExitUsage
		}
		arg := args[0]
		if isHelp(arg) {
			writeGroupHelp(stdout, prog, cmd)
			return ExitOK
		}
		if cmd == &a.root && arg == "--version" && a.Version != "" {
			printf(stdout, "%s %s\n", prog, a.Version)
			return ExitOK
		}
		next := cmd.find(arg)
		if next == nil {
			writeGroupUsage(stderr, prog, cmd)
			if strings.HasPrefix(arg, "-") {
				printf(stderr, "%s: error: unrecognized arguments: %s\n", prog, arg)
			} else {
				printf(stderr, "%s: error: argument command: invalid choice: '%s' (choose from %s)\n", prog, arg, strings.Join(cmd.names(), ", "))
			}
			return ExitUsage
		}
		cmd, prog, args = next, prog+" "+next.Name, args[1:]
	}
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	handler := cmd.Setup(fs)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeLeafHelp(stdout, prog, cmd, fs)
			return ExitOK
		}
		writeLeafUsage(stderr, prog, fs)
		printf(stderr, "%s: error: %s\n", prog, err)
		return ExitUsage
	}
	if a.setup != nil {
		a.setup(stderr)
	}
	return handler(&Env{Args: positional, Stdout: stdout, Stderr: stderr, prog: prog, fs: fs})
}

func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if endedByTerminator(fs, args[:len(args)-len(rest)]) {
			return append(positional, rest...), nil
		}
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func endedByTerminator(fs *flag.FlagSet, consumed []string) bool {
	n := len(consumed)
	if n == 0 || consumed[n-1] != "--" {
		return false
	}
	if n == 1 {
		return true
	}
	name, isFlag := strings.CutPrefix(consumed[n-2], "-")
	if !isFlag || strings.Contains(name, "=") {
		return true
	}
	f := fs.Lookup(strings.TrimPrefix(name, "-"))
	if f == nil {
		return true
	}
	b, isBool := f.Value.(interface{ IsBoolFlag() bool })
	return isBool && b.IsBoolFlag()
}

func (c *Command) find(name string) *Command {
	for _, sub := range c.Commands {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

func (c *Command) names() []string {
	names := make([]string, len(c.Commands))
	for i, sub := range c.Commands {
		names[i] = sub.Name
	}
	return names
}

func isHelp(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

func writeGroupUsage(w io.Writer, prog string, c *Command) {
	printf(w, "usage: %s [-h] {%s} ...\n", prog, strings.Join(c.names(), ","))
}

func writeGroupHelp(w io.Writer, prog string, c *Command) {
	writeGroupUsage(w, prog, c)
	if c.Help != "" {
		printf(w, "\n%s\n", c.Help)
	}
	printf(w, "\ncommands:\n")
	width := 0
	for _, sub := range c.Commands {
		width = max(width, len(sub.Name))
	}
	for _, sub := range c.Commands {
		printf(w, "  %-*s  %s\n", width, sub.Name, sub.Help)
	}
	printf(w, "%s", optionsHelp)
}

func writeLeafUsage(w io.Writer, prog string, fs *flag.FlagSet) {
	usage := "usage: " + prog + " [-h]"
	fs.VisitAll(func(f *flag.Flag) {
		usage += " [--" + f.Name + "]"
	})
	printf(w, "%s\n", usage)
}

func writeLeafHelp(w io.Writer, prog string, c *Command, fs *flag.FlagSet) {
	writeLeafUsage(w, prog, fs)
	if c.Help != "" {
		printf(w, "\n%s\n", c.Help)
	}
	printf(w, "%s", optionsHelp)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
}

const optionsHelp = "\noptions:\n  -h, --help  show this help message and exit\n"

func printf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
