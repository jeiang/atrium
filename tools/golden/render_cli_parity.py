import os

os.environ.pop("FORCE_COLOR", None)
os.environ["NO_COLOR"] = "1"

import argparse
import contextlib
import io
import json
import sys

from common import GOLDEN, ROOT, out_dir, use_source_tree, write_json

use_source_tree()

from atrium import cli  # noqa: E402

CLI_GOLDEN = GOLDEN / "cli"
EXCLUDED_TOP = {
    "money": "money module lands after cutover; the production binary answers invalid choice (exit 2)",
    "codemode-worker": "code mode is gone; the production binary keeps the command only as an inert unit target that exits 1 with a removed-in-v2 message",
}
EXCLUDED_PATHS = {
    ("notion", "wiki-plan"): "wiki-plan is not ported in Phase A; the binary prints `atrium notion wiki-plan is not ported in v2` and exits 1",
    ("calendar", "candidates"): "lands with the event candidates slice (#136); the binary prints `atrium calendar candidates is not ported in v2` and exits 1",
}
GO_ONLY = [
    {"command": "--version", "args": [], "usage": "atrium --version", "status": "go-only", "note": "prints `atrium <version>` and exits 0; Python had no version flag", "cases": []},
    {"command": "GET /healthz", "args": [], "usage": "GET /healthz", "status": "go-only", "note": "unauthenticated liveness endpoint of `atrium serve`, answers 200", "cases": []},
]
NOTES = {
    ("agent", "run"): "intentional divergence: the unported `code` run kind is dropped from --kind choices; go_stdout/go_stderr hold the output Python's parser gives without it",
    ("agent", "scheduler"): "runs the scheduler worker until signalled; no output",
    ("sync",): "exit 1 when an account's credentials are missing",
    ("watch",): "runs until signalled; exit 1 when an account's credentials are missing",
    ("live", "undo"): "exit 1 with `error: ...` on an UndoError",
    ("live", "backlog"): "exit 2 with `error: ...` on a BacklogError",
    ("rules", "import"): "exit 2 with `error: ...` on a RuleError, exit 1 (traceback) on malformed JSON",
    ("shadow-report",): "exit 1 (traceback) on a malformed --since",
    ("proposals", "approve"): "exit 1 (traceback) on an unknown id or a rejected selection",
    ("proposals", "dismiss"): "exit 0 and prints {id, dismissed}",
    ("proposals", "undo"): "exit 1 (traceback) on an unknown or not-undoable proposal",
    ("serve",): "proxy mode without --allowed-host or ATRIUM_PUBLIC_HOST exits 1 with the message on stderr",
    ("calendar", "google-auth"): "exit 1 with `error: ...` on a CalendarError; prints the consent URL, then GOOGLE_REFRESH_TOKEN=...",
}


def nodes(parser, path=()):
    yield path, parser
    for action in parser._actions:
        if isinstance(action, argparse._SubParsersAction):
            for name, child in action.choices.items():
                yield from nodes(child, (*path, name))


def describe(action):
    if isinstance(action, argparse._HelpAction):
        return None
    if isinstance(action, argparse._SubParsersAction):
        return {"kind": "subcommand", "choices": list(action.choices), "required": action.required}
    out = {"kind": "option" if action.option_strings else "positional", "name": "/".join(action.option_strings) or action.dest}
    if action.choices is not None:
        out["choices"] = list(action.choices)
    if action.required and action.option_strings:
        out["required"] = True
    if action.type is not None:
        out["type"] = action.type.__name__
    if action.default not in (None, [], argparse.SUPPRESS):
        out["default"] = action.default
    if action.nargs is not None:
        out["nargs"] = action.nargs
    if isinstance(action, argparse._AppendAction):
        out["repeatable"] = True
    if isinstance(action, (argparse._StoreTrueAction, argparse._StoreFalseAction)):
        out["flag"] = True
    return out


def case_path(root, argv):
    node, path = root, []
    for token in argv:
        sub = next((a for a in node._actions if isinstance(a, argparse._SubParsersAction)), None)
        if sub is None or token not in sub.choices:
            break
        node = sub.choices[token]
        path.append(token)
    return tuple(path)


def shape(text):
    if not text:
        return "empty"
    if text.startswith("usage:"):
        return "usage"
    try:
        value = json.loads(text)
    except ValueError:
        return "text"
    return "json " + ("array" if isinstance(value, list) else "object" if isinstance(value, dict) else "value")


def parse_output(argv, drop_code):
    saved = cli.KINDS
    if drop_code:
        cli.KINDS = tuple(k for k in saved if k != "code")
    out, err = io.StringIO(), io.StringIO()
    try:
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                cli.parser().parse_args(argv)
                code = 0
            except SystemExit as e:
                code = e.code
    finally:
        cli.KINDS = saved
    return code, out.getvalue(), err.getvalue().replace(str(ROOT) + "/", "")


def status_of(path):
    if path and path[0] in EXCLUDED_TOP:
        return "excluded-until-ported", EXCLUDED_TOP[path[0]]
    if path in EXCLUDED_PATHS:
        return "excluded-until-ported", EXCLUDED_PATHS[path]
    return "ported", NOTES.get(path, "")


def build():
    root = cli.parser()
    index = json.loads((CLI_GOLDEN / "index.json").read_text())
    by_path = {}
    for case in index:
        by_path.setdefault(case_path(root, case["argv"][1:]), []).append(case)
    rows = []
    for path, node in nodes(root):
        status, note = status_of(path)
        actions = [d for d in map(describe, node._actions) if d]
        cases = []
        for case in by_path.get(path, []):
            row = {
                "name": case["name"],
                "argv": " ".join(case["argv"][1:]),
                "exit_code": case["exit_code"],
                "stdout": shape((CLI_GOLDEN / case["stdout"]).read_text()),
                "stderr": shape((CLI_GOLDEN / case["stderr"]).read_text()),
            }
            if status == "ported" and case["name"].startswith("argparse-") and path[:2] == ("agent", "run"):
                code, out, err = parse_output(case["argv"][1:], False)
                gocode, goout, goerr = parse_output(case["argv"][1:], True)
                if (out, err) != (goout, goerr):
                    row["go_stdout"], row["go_stderr"] = goout, goerr
            cases.append(row)
        rows.append({
            "command": " ".join(path) or "(root)",
            "path": list(path),
            "usage": node.format_usage().strip().replace("\n", " ").replace("   ", " "),
            "args": actions,
            "status": status,
            "note": note,
            "cases": cases,
        })
    return rows + GO_ONLY


def markdown(rows):
    lines = [
        "# CLI parity table",
        "",
        "Generated by `tools/golden/render_cli_parity.py` from the frozen Python parser and `index.json`; checked by `TestCLIParity` in `cmd/atrium`.",
        "",
        "| command | status | cases (exit code, stdout shape, stderr shape) | note |",
        "| --- | --- | --- | --- |",
    ]
    for row in rows:
        cases = "<br>".join(
            f"`{c['argv'] or '(none)'}` → {c['exit_code']}, {c['stdout']}, {c['stderr']}" + (" (Go differs)" if "go_stderr" in c or "go_stdout" in c else "")
            for c in row["cases"]
        )
        lines.append(f"| `{row['command']}` | {row['status']} | {cases} | {row['note']} |")
    return "\n".join(lines) + "\n"


def main():
    rows = build()
    target = out_dir("cli")
    write_json(target / "parity.json", {"commands": rows})
    (target / "parity.md").write_text(markdown(rows), encoding="utf-8")


if __name__ == "__main__":
    main()
