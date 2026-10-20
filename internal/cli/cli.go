// Package cli is the subset of Python's argparse that cli.py's parser() uses: sub-parsers, store, store_true and
// append options, str and int positionals. Parsing, error texts, usage and help match Python 3.14 byte for byte
// for that subset. Help is never coloured and the width is $COLUMNS (else 80) minus 2, as on a non-terminal.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	ExitOK         = 0
	ExitError      = 1
	ExitUsage      = 2
	ExitHangup     = 129
	ExitInterrupt  = 130
	ExitTerminated = 143
)

// Handler runs a parsed leaf command; its result is the exit code.
type Handler func(env *Env) int

// Command is one parser: a leaf when Setup is set, else a group whose sub-parsers are Commands.
type Command struct {
	Name string
	// Help is add_parser's help=: the line the parent's help lists for this command; empty lists none.
	Help string
	// Dest is the dest of this group's subparsers action, named in its errors; empty is "command" at the root and
	// "<name>_command" (dashes as underscores) below it.
	Dest     string
	Setup    func(a *Args) Handler
	Commands []*Command
}

func (c *Command) Add(cmds ...*Command) {
	c.Commands = append(c.Commands, cmds...)
}

func (c *Command) find(name string) *Command {
	for _, sub := range c.Commands {
		if sub.Name == name {
			return sub
		}
	}
	return nil
}

// Opt holds add_argument's keywords besides the name and the default. Help is used verbatim: argparse's
// %-expansion of help strings is not ported.
type Opt struct {
	Help string
	// Metavar defaults to the upper-cased dest for options and to the dest for positionals.
	Metavar string
	// Choices are checked against the converted value; usage shows them as {a,b}.
	Choices  []string
	Required bool
}

type kind uint8

const (
	kindStore kind = iota
	kindStoreTrue
	kindAppend
	kindHelp
	kindVersion
	kindParsers
)

type action struct {
	Opt
	kind  kind
	name  string
	dest  string
	opts  []string
	isInt bool
	str   *string
	num   *int
	flag  *bool
	list  *[]string
	cmd   *Command
	seen  bool
}

// Args declares a leaf's arguments in add_argument order; each declaration returns the pointer the handler reads
// once parsing succeeded.
type Args struct {
	actions []*action
}

func (a *Args) option(name string, k kind, o Opt) *action {
	act := &action{Opt: o, kind: k, name: name, dest: strings.ReplaceAll(name, "-", "_"), opts: []string{"--" + name}}
	a.actions = append(a.actions, act)
	return act
}

func (a *Args) positional(name string, o Opt) *action {
	o.Required = true
	act := &action{Opt: o, kind: kindStore, name: name, dest: name}
	a.actions = append(a.actions, act)
	return act
}

// String is `--name VALUE`.
func (a *Args) String(name, def string, o Opt) *string {
	v := def
	a.option(name, kindStore, o).str = &v
	return &v
}

// Int is `--name VALUE` with type=int.
func (a *Args) Int(name string, def int, o Opt) *int {
	v := def
	act := a.option(name, kindStore, o)
	act.num, act.isInt = &v, true
	return &v
}

// Bool is `--name` with action="store_true".
func (a *Args) Bool(name string, o Opt) *bool {
	var v bool
	a.option(name, kindStoreTrue, o).flag = &v
	return &v
}

// Repeated is `--name VALUE` with action="append" and default=[].
func (a *Args) Repeated(name string, o Opt) *[]string {
	v := []string{}
	a.option(name, kindAppend, o).list = &v
	return &v
}

// Arg is a str positional.
func (a *Args) Arg(name string, o Opt) *string {
	var v string
	a.positional(name, o).str = &v
	return &v
}

// IntArg is a positional with type=int.
func (a *Args) IntArg(name string, o Opt) *int {
	var v int
	act := a.positional(name, o)
	act.num, act.isInt = &v, true
	return &v
}

// Has reports whether the argument declared as name was given on the command line, for options whose Python
// default is None.
func (a *Args) Has(name string) bool {
	for _, act := range a.actions {
		if act.name == name {
			return act.seen
		}
	}
	return false
}

// Env is what a handler runs with.
type Env struct {
	Ctx    context.Context
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	p      *parser
	width  int
}

// UsageError is parser.error(msg) on the leaf: its usage and "prog: error: msg" on stderr, exit 2.
func (e *Env) UsageError(format string, a ...any) int {
	printf(e.Stderr, "%s", e.p.usage(e.width))
	printf(e.Stderr, "%s: error: %s\n", e.p.prog, fmt.Sprintf(format, a...))
	return ExitUsage
}

// App is the root parser. A non-empty Version makes an exact root `--version` print "<name> <Version>"; it stays
// out of usage and help, which mirror the Python parser. Stdin nil is os.Stdin.
type App struct {
	Version string
	Stdin   io.Reader
	root    Command
	setup   func(stderr io.Writer)
}

// New is the root parser named name; setup runs after a successful parse, before the handler.
func New(name string, setup func(stderr io.Writer)) *App {
	return &App{root: Command{Name: name}, setup: setup}
}

func (a *App) Add(cmds ...*Command) {
	a.root.Add(cmds...)
}

// Run is parse_args(args) followed by the selected handler.
func (a *App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	s := &session{stdout: stdout, stderr: stderr, width: formatWidth()}
	root, _ := newParser(a.root.Name, &a.root, true)
	root.version = a.Version
	extras, err := s.parse(root, args)
	if err == nil && len(extras) > 0 {
		err = s.fail(root, "unrecognized arguments: "+strings.Join(extras, " "))
	}
	if code, ok := errors.AsType[exit](err); ok {
		return int(code)
	}
	if a.setup != nil {
		a.setup(stderr)
	}
	stdin := a.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	handlerCode := s.handler(&Env{Ctx: ctx, Stdin: stdin, Stdout: stdout, Stderr: stderr, p: s.leaf, width: s.width})
	if code, ok := signalExit(ctx, stderr); ok {
		return code
	}
	return handlerCode
}

// formatWidth is HelpFormatter's width: shutil.get_terminal_size().columns - 2 off a terminal.
func formatWidth() int {
	if n, ok := py.ParseInt(os.Getenv("COLUMNS")); ok && n > 0 && n <= 1<<20 {
		return int(n) - 2
	}
	return 80 - 2
}

type parser struct {
	prog    string
	actions []*action
	version string
}

var helpAction = Opt{Help: "show this help message and exit"}

func newParser(prog string, c *Command, root bool) (*parser, Handler) {
	p := &parser{prog: prog, actions: []*action{{Opt: helpAction, kind: kindHelp, dest: "help", opts: []string{"-h", "--help"}}}}
	if c.Setup != nil {
		var args Args
		h := c.Setup(&args)
		p.actions = append(p.actions, args.actions...)
		return p, h
	}
	dest := c.Dest
	if dest == "" {
		dest = "command"
		if !root {
			dest = strings.ReplaceAll(c.Name, "-", "_") + "_command"
		}
	}
	names := make([]string, len(c.Commands))
	for i, sub := range c.Commands {
		names[i] = sub.Name
	}
	p.actions = append(p.actions, &action{Opt: Opt{Choices: names, Required: true}, kind: kindParsers, dest: dest, cmd: c})
	return p, nil
}

func (p *parser) positionals() []*action {
	var out []*action
	for _, a := range p.actions {
		if len(a.opts) == 0 {
			out = append(out, a)
		}
	}
	return out
}

func (p *parser) lookup(opt string) *action {
	for _, a := range p.actions {
		if slices.Contains(a.opts, opt) {
			return a
		}
	}
	return nil
}

// exit ends parsing with an exit code once the parser has written its output.
type exit int

func (e exit) Error() string { return "exit status " + strconv.Itoa(int(e)) }

type session struct {
	stdout, stderr io.Writer
	width          int
	leaf           *parser
	handler        Handler
}

func (s *session) fail(p *parser, msg string) error {
	printf(s.stderr, "%s", p.usage(s.width))
	printf(s.stderr, "%s: error: %s\n", p.prog, msg)
	return exit(ExitUsage)
}

func (s *session) argError(p *parser, a *action, format string, args ...any) error {
	return s.fail(p, "argument "+a.displayName()+": "+fmt.Sprintf(format, args...))
}

// optTuple is one interpretation of an option string: _parse_optional's (action, option_string, sep, explicit_arg).
type optTuple struct {
	a           *action
	opt         string
	sep         string
	explicit    string
	hasExplicit bool
}

var negativeNumber = rx.MustCompile(`-\.?\d`, 0)

// parseOptional is _parse_optional: nil for a positional, else its interpretations (a nil action when no
// option of this parser matches).
func (p *parser) parseOptional(s string) []optTuple {
	if s == "" || s[0] != '-' {
		return nil
	}
	if p.version != "" && s == "--version" {
		return []optTuple{{a: &action{kind: kindVersion, dest: "version", opts: []string{s}}, opt: s}}
	}
	if a := p.lookup(s); a != nil {
		return []optTuple{{a: a, opt: s}}
	}
	if len(s) == 1 {
		return nil
	}
	if before, after, ok := strings.Cut(s, "="); ok {
		if a := p.lookup(before); a != nil {
			return []optTuple{{a: a, opt: before, sep: "=", explicit: after, hasExplicit: true}}
		}
	}
	if tuples := p.optionTuples(s); len(tuples) > 0 {
		return tuples
	}
	if m, _ := negativeNumber.Match(s); m != nil {
		return nil
	}
	if strings.Contains(s, " ") {
		return nil
	}
	return []optTuple{{opt: s}}
}

// optionTuples is _get_option_tuples with allow_abbrev.
func (p *parser) optionTuples(s string) []optTuple {
	prefix, explicit, has := strings.Cut(s, "=")
	sep := ""
	if has {
		sep = "="
	}
	_, size := utf8.DecodeRuneInString(s[1:])
	short, shortExplicit := s[:1+size], s[1+size:]
	var out []optTuple
	for _, a := range p.actions {
		for _, opt := range a.opts {
			switch {
			case s[1] != '-' && opt == short:
				out = append(out, optTuple{a: a, opt: opt, explicit: shortExplicit, hasExplicit: true})
			case strings.HasPrefix(opt, prefix):
				out = append(out, optTuple{a: a, opt: opt, sep: sep, explicit: explicit, hasExplicit: has})
			}
		}
	}
	return out
}

func (a *action) takesValue() bool {
	return a.kind == kindStore || a.kind == kindAppend
}

func (a *action) displayName() string {
	switch {
	case len(a.opts) > 0:
		return strings.Join(a.opts, "/")
	case a.Metavar != "":
		return a.Metavar
	}
	return a.dest
}

func (a *action) nargsPattern() string {
	if a.kind == kindParsers {
		return "(-*A[-AO]*)"
	}
	return "(-*A-*)"
}

// parse is _parse_known_args: the unrecognized arguments, or an exit once the parser wrote help or an error.
func (s *session) parse(p *parser, args []string) ([]string, error) {
	optIdx := map[int][]optTuple{}
	var b strings.Builder
	maxOpt := -1
	for i, arg := range args {
		if arg == "--" {
			b.WriteByte('-')
			b.WriteString(strings.Repeat("A", len(args)-i-1))
			break
		}
		if t := p.parseOptional(arg); t != nil {
			optIdx[i] = t
			maxOpt = i
			b.WriteByte('O')
		} else {
			b.WriteByte('A')
		}
	}
	pattern := b.String()
	var extras, subExtras []string
	positionals := p.positionals()

	take := func(a *action, strs []string) error {
		a.seen = true
		switch a.kind {
		case kindHelp:
			printf(s.stdout, "%s", p.help(s.width))
			return exit(ExitOK)
		case kindVersion:
			printf(s.stdout, "%s %s\n", p.prog, p.version)
			return exit(ExitOK)
		case kindStoreTrue:
			*a.flag = true
			return nil
		case kindParsers:
			name := strs[0]
			if err := s.checkChoice(p, a, name); err != nil {
				return err
			}
			sub, h := newParser(p.prog+" "+name, a.cmd.find(name), false)
			if h != nil {
				s.leaf, s.handler = sub, h
			}
			rest, err := s.parse(sub, strs[1:])
			subExtras = append(subExtras, rest...)
			return err
		}
		for i, str := range strs {
			if err := s.store(p, a, str, i == 0); err != nil {
				return err
			}
		}
		return nil
	}

	consumeOptional := func(start int) (int, error) {
		tuples := optIdx[start]
		if len(tuples) > 1 {
			opts := make([]string, len(tuples))
			for i, t := range tuples {
				opts[i] = t.opt
			}
			return 0, s.fail(p, fmt.Sprintf("ambiguous option: %s could match %s", args[start], strings.Join(opts, ", ")))
		}
		t := tuples[0]
		type taken struct {
			a    *action
			strs []string
		}
		var todo []taken
		stop := start + 1
		for {
			if t.a == nil {
				extras = append(extras, args[start])
				return start + 1, nil
			}
			if !t.hasExplicit {
				n := 0
				if t.a.takesValue() {
					if !strings.HasPrefix(pattern[start+1:], "A") {
						return 0, s.argError(p, t.a, "expected one argument")
					}
					n = 1
				}
				stop = start + 1 + n
				todo = append(todo, taken{t.a, args[start+1 : stop]})
				break
			}
			if t.a.takesValue() {
				todo = append(todo, taken{t.a, []string{t.explicit}})
				break
			}
			if t.opt[1] == '-' || t.explicit == "" || t.sep != "" || t.explicit[0] == '-' {
				return 0, s.argError(p, t.a, "ignored explicit argument %s", py.Repr(t.explicit))
			}
			todo = append(todo, taken{t.a, nil})
			_, size := utf8.DecodeRuneInString(t.explicit)
			opt := "-" + t.explicit[:size]
			a := p.lookup(opt)
			if a == nil {
				extras = append(extras, "-"+t.explicit)
				break
			}
			rest := t.explicit[size:]
			t = optTuple{a: a, opt: opt, explicit: rest, hasExplicit: rest != ""}
			if after, ok := strings.CutPrefix(rest, "="); ok {
				t.sep, t.explicit = "=", after
			}
		}
		for _, td := range todo {
			if err := take(td.a, td.strs); err != nil {
				return 0, err
			}
		}
		return stop, nil
	}

	consumePositionals := func(start int) (int, error) {
		counts := matchPartial(positionals, pattern[start:])
		for i, n := range counts {
			a := positionals[i]
			strs := slices.Clone(args[start : start+n])
			if a.kind == kindParsers {
				if pattern[start] == '-' {
					strs = strs[1:]
				}
			} else if strings.Contains(pattern[start:start+n], "-") {
				dash := slices.Index(strs, "--")
				strs = slices.Delete(strs, dash, dash+1)
			}
			start += n
			if err := take(a, strs); err != nil {
				return 0, err
			}
		}
		positionals = positionals[len(counts):]
		return start, nil
	}

	start := 0
	for start <= maxOpt {
		next := start
		for next <= maxOpt && optIdx[next] == nil {
			next++
		}
		if start != next {
			end, err := consumePositionals(start)
			if err != nil {
				return nil, err
			}
			if end > start {
				start = end
				continue
			}
		}
		if optIdx[start] == nil {
			extras = append(extras, args[start:next]...)
			start = next
		}
		var err error
		if start, err = consumeOptional(start); err != nil {
			return nil, err
		}
	}
	stop, err := consumePositionals(start)
	if err != nil {
		return nil, err
	}
	extras = append(extras, args[stop:]...)
	var required []string
	for _, a := range p.actions {
		if !a.seen && a.Required {
			required = append(required, a.displayName())
		}
	}
	if len(required) > 0 {
		return nil, s.fail(p, "the following arguments are required: "+strings.Join(required, ", "))
	}
	return append(extras, subExtras...), nil
}

// store converts and checks one value of a store or append action; only the first value of a positional that
// took several (around a "--") is kept.
func (s *session) store(p *parser, a *action, str string, keep bool) error {
	value := str
	var n int
	if a.isInt {
		v, ok := py.ParseInt(str)
		if !ok || int64(int(v)) != v {
			return s.argError(p, a, "invalid int value: %s", py.Repr(str))
		}
		n, value = int(v), strconv.Itoa(int(v))
	}
	if err := s.checkChoice(p, a, value); err != nil {
		return err
	}
	switch {
	case a.kind == kindAppend:
		*a.list = append(*a.list, str)
	case !keep:
	case a.isInt:
		*a.num = n
	default:
		*a.str = str
	}
	return nil
}

func (s *session) checkChoice(p *parser, a *action, value string) error {
	if a.Choices == nil || slices.Contains(a.Choices, value) {
		return nil
	}
	quoted := make([]string, len(a.Choices))
	for i, c := range a.Choices {
		quoted[i] = py.Repr(c)
	}
	return s.argError(p, a, "invalid choice: %s (choose from %s)", py.Repr(value), strings.Join(quoted, ", "))
}

// matchPartial is _match_arguments_partial: how many argument strings each leading positional takes.
func matchPartial(actions []*action, pattern string) []int {
	for i := len(actions); i > 0; i-- {
		var b strings.Builder
		for _, a := range actions[:i] {
			b.WriteString(a.nargsPattern())
		}
		m, _ := rx.MustCompile(b.String(), 0).Match(pattern)
		if m == nil {
			continue
		}
		counts := make([]int, i)
		for g := range counts {
			from, to := m.Span(g + 1)
			counts[g] = to - from
		}
		if m.End() < len(pattern) && pattern[m.End()] == 'O' {
			for len(counts) > 0 && counts[len(counts)-1] == 0 {
				counts = counts[:len(counts)-1]
			}
		}
		return counts
	}
	return nil
}

func (a *action) metavar() string {
	switch {
	case a.Metavar != "":
		return a.Metavar
	case a.Choices != nil:
		return "{" + strings.Join(a.Choices, ",") + "}"
	case len(a.opts) > 0:
		return py.Upper(a.dest)
	}
	return a.dest
}

func (a *action) formatArgs() string {
	if a.kind == kindParsers {
		return a.metavar() + " ..."
	}
	return a.metavar()
}

func (a *action) invocation() string {
	switch {
	case len(a.opts) == 0:
		return a.metavar()
	case a.takesValue():
		return strings.Join(a.opts, ", ") + " " + a.formatArgs()
	}
	return strings.Join(a.opts, ", ")
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// usage is format_usage: the usage line(s) and a newline.
func (p *parser) usage(width int) string {
	return p.formatUsage(width) + "\n"
}

// formatUsage is HelpFormatter._format_usage without its trailing blank line.
func (p *parser) formatUsage(width int) string {
	const prefix = "usage: "
	var optParts, posParts []string
	for _, a := range p.actions {
		if len(a.opts) == 0 {
			posParts = append(posParts, a.formatArgs())
			continue
		}
		part := a.opts[0]
		if a.takesValue() {
			part += " " + a.formatArgs()
		}
		if !a.Required {
			part = "[" + part + "]"
		}
		optParts = append(optParts, part)
	}
	prog := p.prog
	usage := strings.Join(append(append([]string{prog}, optParts...), posParts...), " ")
	if runes(prefix)+runes(usage) <= width {
		return prefix + usage
	}
	getLines := func(parts []string, indent string, first bool) []string {
		var lines, line []string
		lineLen := len(indent) - 1
		if first {
			lineLen = len(prefix) - 1
		}
		for _, part := range parts {
			n := runes(part)
			if lineLen+1+n > width && len(line) > 0 {
				lines = append(lines, indent+strings.Join(line, " "))
				line, lineLen = nil, len(indent)-1
			}
			line = append(line, part)
			lineLen += n + 1
		}
		if len(line) > 0 {
			lines = append(lines, indent+strings.Join(line, " "))
		}
		if first {
			lines[0] = lines[0][len(indent):]
		}
		return lines
	}
	var lines []string
	if float64(len(prefix)+runes(prog)) <= 0.75*float64(width) {
		indent := strings.Repeat(" ", len(prefix)+runes(prog)+1)
		switch {
		case len(optParts) > 0:
			lines = getLines(append([]string{prog}, optParts...), indent, true)
			lines = append(lines, getLines(posParts, indent, false)...)
		case len(posParts) > 0:
			lines = getLines(append([]string{prog}, posParts...), indent, true)
		default:
			lines = []string{prog}
		}
	} else {
		indent := strings.Repeat(" ", len(prefix))
		lines = getLines(append(slices.Clone(optParts), posParts...), indent, false)
		if len(lines) > 1 {
			lines = append(getLines(optParts, indent, false), getLines(posParts, indent, false)...)
		}
		lines = append([]string{prog}, lines...)
	}
	return prefix + strings.Join(lines, "\n")
}

type helpItem struct {
	invocation string
	help       string
	indent     int
	subs       []helpItem
}

var longBreak = rx.MustCompile(`\n\n\n+`, 0)

// help is format_help: usage, then the positional arguments and options sections.
func (p *parser) help(width int) string {
	maxHelp := min(24, max(width-20, 4))
	var positional, options []helpItem
	maxLen := 0
	for _, a := range p.actions {
		item := helpItem{invocation: a.invocation(), help: a.Help, indent: 2}
		maxLen = max(maxLen, runes(item.invocation)+2)
		if a.kind == kindParsers {
			for _, sub := range a.cmd.Commands {
				if sub.Help != "" {
					item.subs = append(item.subs, helpItem{invocation: sub.Name, help: sub.Help, indent: 4})
					maxLen = max(maxLen, runes(sub.Name)+4)
				}
			}
		}
		if len(a.opts) == 0 {
			positional = append(positional, item)
		} else {
			options = append(options, item)
		}
	}
	helpPos := min(maxLen+2, maxHelp)
	var b strings.Builder
	b.WriteString(p.formatUsage(width) + "\n\n")
	for _, section := range []struct {
		title string
		items []helpItem
	}{{"positional arguments", positional}, {"options", options}} {
		if len(section.items) == 0 {
			continue
		}
		b.WriteString("\n" + section.title + ":\n")
		for _, item := range section.items {
			formatItem(&b, item, helpPos, width)
		}
		b.WriteString("\n")
	}
	out, _ := longBreak.Sub("\n\n", b.String(), 0)
	return strings.Trim(out, "\n") + "\n"
}

// formatItem is HelpFormatter._format_action.
func formatItem(b *strings.Builder, item helpItem, helpPos, width int) {
	helpWidth := max(width-helpPos, 11)
	actionWidth := helpPos - item.indent - 2
	indent := strings.Repeat(" ", item.indent)
	indentFirst := 0
	switch {
	case item.help == "":
		b.WriteString(indent + item.invocation + "\n")
	case runes(item.invocation) <= actionWidth:
		b.WriteString(indent + item.invocation + strings.Repeat(" ", actionWidth-runes(item.invocation)) + "  ")
	default:
		b.WriteString(indent + item.invocation + "\n")
		indentFirst = helpPos
	}
	if item.help != "" {
		if py.Strip(item.help) != "" {
			lines := splitLines(item.help, helpWidth)
			b.WriteString(strings.Repeat(" ", indentFirst) + lines[0] + "\n")
			for _, line := range lines[1:] {
				b.WriteString(strings.Repeat(" ", helpPos) + line + "\n")
			}
		} else {
			b.WriteString("\n")
		}
	}
	for _, sub := range item.subs {
		formatItem(b, sub, helpPos, width)
	}
}

var (
	asciiSpace = rx.MustCompile(`\s+`, rx.ASCII)
	wordSep    = rx.MustCompile(`([\t\n\x0b\x0c\r ]+|(?<=[\w!"'&.,?])-{2,}(?=\w)|[^\t\n\x0b\x0c\r ]+?(?:-(?:(?<=[^\d\W]{2}-)|(?<=[^\d\W]-[^\d\W]-))(?=[^\d\W]-?[^\d\W])|(?=[\t\n\x0b\x0c\r ]|\z)|(?<=[\w!"'&.,?])(?=-{2,}\w)))`, 0)
)

// splitLines is HelpFormatter._split_lines: whitespace runs collapsed, then textwrap.wrap(text, width).
func splitLines(text string, width int) []string {
	text, _ = asciiSpace.Sub(" ", text, 0)
	split, _ := wordSep.Split(py.Strip(text), 0)
	var chunks [][]rune
	for i := len(split) - 1; i >= 0; i-- {
		if split[i] != "" {
			chunks = append(chunks, []rune(split[i]))
		}
	}
	blank := func(c []rune) bool { return py.Strip(string(c)) == "" }
	var lines []string
	for len(chunks) > 0 {
		var line [][]rune
		lineLen := 0
		if blank(chunks[len(chunks)-1]) && len(lines) > 0 {
			chunks = chunks[:len(chunks)-1]
		}
		for len(chunks) > 0 && lineLen+len(chunks[len(chunks)-1]) <= width {
			line = append(line, chunks[len(chunks)-1])
			lineLen += len(chunks[len(chunks)-1])
			chunks = chunks[:len(chunks)-1]
		}
		if len(chunks) > 0 && len(chunks[len(chunks)-1]) > width {
			line = breakLongWord(chunks, line, lineLen, width)
		}
		if len(line) > 0 && blank(line[len(line)-1]) {
			line = line[:len(line)-1]
		}
		if len(line) > 0 {
			var s strings.Builder
			for _, c := range line {
				s.WriteString(string(c))
			}
			lines = append(lines, s.String())
		}
	}
	return lines
}

// breakLongWord is TextWrapper._handle_long_word with break_long_words and break_on_hyphens; chunks is reversed
// and its last chunk is shortened in place.
func breakLongWord(chunks, line [][]rune, lineLen, width int) [][]rune {
	spaceLeft := 1
	if width >= 1 {
		spaceLeft = width - lineLen
	}
	if spaceLeft <= 0 {
		return line
	}
	chunk := chunks[len(chunks)-1]
	end := spaceLeft
	if len(chunk) > spaceLeft {
		if hyphen := lastIndex(chunk[:spaceLeft], '-'); hyphen > 0 && slices.ContainsFunc(chunk[:hyphen], func(r rune) bool { return r != '-' }) {
			end = hyphen + 1
		}
	}
	chunks[len(chunks)-1] = chunk[end:]
	return append(line, chunk[:end])
}

func lastIndex(rs []rune, r rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

func printf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}
