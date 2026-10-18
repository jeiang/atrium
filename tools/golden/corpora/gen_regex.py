#!/usr/bin/env python3
import ast
import collections
import json
import re
import re._constants as sre_constants
import re._parser as sre_parse
import sys
import warnings
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from regex_inputs import BINDINGS, GENERIC_BYTES_INPUTS, GENERIC_INPUTS, INPUTS  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
SRC = ROOT / "src" / "atrium"
OUT = ROOT / "testdata" / "corpora"
MANIFEST = OUT / "regex_manifest.json"
CORPUS = OUT / "regex.jsonl"
FREEZE_COMMIT = "cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40"
PYTHON = (3, 14)
CALLS = ("compile", "escape", "findall", "finditer", "fullmatch", "match", "search", "split", "sub", "subn")
GREP = re.compile(r"\bre\.(" + "|".join(CALLS) + r")\(")
HIDDEN_IMPORT = re.compile(r"^\s*(?:from\s+re\s+import|import\s+re\s+as\b|import\s+re\s*,|import\s+.*,\s*re\b|(?:from\s+)?import\s+regex\b)", re.M)
FLAG_NAMES = ("ASCII", "IGNORECASE", "LOCALE", "MULTILINE", "DOTALL", "VERBOSE")
SUBJECT_ARG = {"findall": 1, "finditer": 1, "fullmatch": 1, "match": 1, "search": 1, "split": 1, "sub": 2, "subn": 2}
FLAGS_ARG = {"compile": 1, "findall": 2, "finditer": 2, "fullmatch": 2, "match": 2, "search": 2, "split": 3, "sub": 4, "subn": 4}
EXTRA_ARGS = {"sub": (("repl", 1), ("count", 3)), "subn": (("repl", 1), ("count", 3)), "split": (("maxsplit", 2),)}


class Unbound(Exception):
    pass


def source_files():
    return sorted(p for p in SRC.rglob("*.py") if "__pycache__" not in p.parts)


def rel(path):
    return path.relative_to(ROOT).as_posix()


def evaluate(node, env):
    try:
        return eval(compile(ast.Expression(node), "<site>", "eval"), {"__builtins__": {}}, dict(env))
    except Exception as e:
        raise Unbound(ast.unparse(node)) from e


def module_env(tree):
    env = {"re": re}
    for stmt in tree.body:
        if isinstance(stmt, ast.Assign) and len(stmt.targets) == 1 and isinstance(stmt.targets[0], ast.Name):
            try:
                env[stmt.targets[0].id] = evaluate(stmt.value, env)
            except Unbound:
                pass
    return env


def is_re_call(node):
    f = node.func if isinstance(node, ast.Call) else None
    return isinstance(f, ast.Attribute) and isinstance(f.value, ast.Name) and f.value.id == "re" and f.attr in CALLS


def ast_sites():
    sites = []
    for path in source_files():
        tree = ast.parse(path.read_text(), str(path))
        env = module_env(tree)
        names = {id(s.value): s.targets[0].id for s in tree.body if isinstance(s, ast.Assign) and len(s.targets) == 1 and isinstance(s.targets[0], ast.Name)}
        for node in ast.walk(tree):
            if is_re_call(node):
                sites.append({"file": rel(path), "line": node.lineno, "col": node.col_offset, "call": node.func.attr, "node": node, "env": env, "name": names.get(id(node))})
    sites.sort(key=lambda s: (s["file"], s["line"], s["col"]))
    return sites


def grep_sites():
    found = collections.Counter()
    hidden = []
    for path in source_files():
        text = path.read_text()
        for n, line in enumerate(text.splitlines(), 1):
            for m in GREP.finditer(line):
                found[(rel(path), n, m.group(1))] += 1
        if HIDDEN_IMPORT.search(text):
            hidden.append(rel(path))
    return found, hidden


def arg(node, index, keyword):
    if len(node.args) > index:
        return node.args[index]
    for kw in node.keywords:
        if kw.arg == keyword:
            return kw.value
    return None


def flag_names(value):
    return [name for name in FLAG_NAMES if value & getattr(re, name)]


def walk(item, notes):
    if isinstance(item, sre_parse.SubPattern):
        for op, av in item.data:
            notes.append((op, av))
            walk(av, notes)
    elif isinstance(item, (tuple, list)):
        if len(item) == 2 and isinstance(item[0], sre_constants._NamedIntConstant) and item[0] in sre_constants.OPCODES:
            notes.append(item)
        for x in item:
            walk(x, notes)


def features(pattern, flags):
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        parsed = sre_parse.parse(pattern, flags)
    notes = []
    walk(parsed, notes)
    c = sre_constants
    found = set()
    for op, av in notes:
        if op in (c.ASSERT, c.ASSERT_NOT):
            found.add(("" if op is c.ASSERT else "negative_") + ("lookahead" if av[0] == 1 else "lookbehind"))
        elif op in (c.GROUPREF, c.GROUPREF_IGNORE, c.GROUPREF_UNI_IGNORE, c.GROUPREF_LOC_IGNORE):
            found.add("backreference")
        elif op is c.GROUPREF_EXISTS:
            found.add("conditional")
        elif op is c.ATOMIC_GROUP:
            found.add("atomic_group")
        elif op in (c.POSSESSIVE_REPEAT, c.POSSESSIVE_REPEAT_ONE):
            found.add("possessive")
        elif op is c.MIN_REPEAT:
            found.add("lazy")
        elif op is c.SUBPATTERN and (av[1] or av[2]):
            found.add("inline_flags")
        elif op is c.AT:
            name = {c.AT_END_STRING: "end_of_string_Z", c.AT_BEGINNING_STRING: "start_of_string_A", c.AT_END: "dollar",
                    c.AT_BOUNDARY: "word_boundary", c.AT_NON_BOUNDARY: "word_boundary"}.get(av)
            if name:
                found.add(name)
        elif op is c.CATEGORY:
            name = {c.CATEGORY_WORD: "word_class", c.CATEGORY_NOT_WORD: "word_class", c.CATEGORY_DIGIT: "digit_class",
                    c.CATEGORY_NOT_DIGIT: "digit_class", c.CATEGORY_SPACE: "space_class", c.CATEGORY_NOT_SPACE: "space_class"}.get(av)
            if name:
                found.add(name)
    if parsed.state.flags & ~(flags | re.UNICODE):
        found.add("inline_flags")
    if parsed.state.groupdict:
        found.add("named_group")
    if re.search(r"(?<!\\)\{,\d+\}", text(pattern)):
        found.add("omitted_min_quantifier")
    return sorted(found)


def text(value):
    return value.decode("latin-1") if isinstance(value, bytes) else value


def match_json(m):
    if m is None:
        return None
    groups = []
    for i in range(1, (m.re.groups or 0) + 1):
        s, e = m.span(i)
        groups.append(None if s < 0 else {"span": [s, e], "text": text(m.group(i))})
    return {"span": list(m.span()), "text": text(m.group(0)), "groups": groups}


def call_result(compiled, op, subject, extra):
    if op in ("sub", "subn"):
        if not isinstance(extra["repl"], (str, bytes)):
            return None
        result, n = compiled.subn(extra["repl"], subject, count=extra.get("count") or 0)
        out = {"op": op, "repl": text(extra["repl"]), "result": text(result)}
        if extra.get("count"):
            out["count"] = extra["count"]
        if op == "subn":
            out["n"] = n
        return out
    if op == "split":
        return {"op": op, "maxsplit": extra.get("maxsplit") or 0, "result": [None if x is None else text(x) for x in compiled.split(subject, extra.get("maxsplit") or 0)]}
    if op == "findall":
        found = compiled.findall(subject)
        return {"op": op, "result": [[text(x) for x in f] if isinstance(f, tuple) else text(f) for f in found]}
    return None


def instantiate(site):
    node, env = site["node"], site["env"]
    key = f"{site['file']}:{site['line']}"
    bindings = BINDINGS.get(key)
    try:
        evaluate(node.args[0], env)
        if FLAGS_ARG.get(site["call"]) is not None and arg(node, FLAGS_ARG[site["call"]], "flags") is not None:
            evaluate(arg(node, FLAGS_ARG[site["call"]], "flags"), env)
        static = True
    except Unbound:
        static = False
    if static:
        return key, [(None, {})], False
    if not bindings:
        raise SystemExit(f"{key}: dynamic {site['call']} argument needs BINDINGS in regex_inputs.py")
    return key, list(bindings.items()), True


def build():
    if sys.version_info[:2] != PYTHON:
        raise SystemExit(f"generate with Python {PYTHON[0]}.{PYTHON[1]} (the frozen app's), not {sys.version.split()[0]}")
    sites_out, patterns_out, records = [], [], []
    used_inputs = set()
    for site in ast_sites():
        node, call = site["node"], site["call"]
        key, bindings, dynamic = instantiate(site)
        entry = {"site": key, "file": site["file"], "line": site["line"], "col": site["col"], "call": call, "pattern_source": ast.unparse(node.args[0])}
        flags_node = arg(node, FLAGS_ARG[call], "flags") if call in FLAGS_ARG else None
        if flags_node is not None:
            entry["flags_source"] = ast.unparse(flags_node)
        if site["name"]:
            entry["name"] = site["name"]
        entry["dynamic"] = dynamic
        if call == "escape":
            entry["escape"] = [{"binding": label, "locals": local, "input": text(evaluate(node.args[0], {**site["env"], **local})),
                                "output": text(evaluate(node, {**site["env"], **local}))} for label, local in bindings]
            sites_out.append(entry)
            continue
        entry["patterns"] = []
        for label, local in bindings:
            scope = {**site["env"], **local}
            pid = key if label is None else f"{key}[{label}]"
            pattern = evaluate(node.args[0], scope)
            flags = evaluate(flags_node, scope) if flags_node is not None else 0
            extra = {name: evaluate(arg(node, i, name), scope) for name, i in EXTRA_ARGS.get(call, ()) if arg(node, i, name) is not None and _bound(arg(node, i, name), scope)}
            if call in ("sub", "subn") and "repl" not in extra:
                extra["repl"] = None
            entry["patterns"].append(pid)
            spec = {"id": pid, "site": key, "call": call, "pattern": text(pattern), "bytes": isinstance(pattern, bytes), "flags": flag_names(flags)}
            if local:
                spec["locals"] = local
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                try:
                    compiled = re.compile(pattern, flags)
                except re.error as e:
                    spec["error"] = str(e)
                    compiled = None
            if caught:
                spec["warnings"] = sorted({f"{w.category.__name__}: {w.message}" for w in caught})
            if call in ("sub", "subn") and extra["repl"] is None:
                spec["repl"] = "callable"
            patterns_out.append(spec)
            if compiled is None:
                continue
            spec["groups"] = compiled.groups
            spec["group_names"] = dict(sorted(compiled.groupindex.items()))
            spec["features"] = features(pattern, flags)
            inputs = INPUTS.get(pid, INPUTS.get(key))
            if not inputs:
                raise SystemExit(f"{pid}: no corpus inputs in regex_inputs.py")
            used_inputs.add(pid if pid in INPUTS else key)
            generic = GENERIC_BYTES_INPUTS if isinstance(pattern, bytes) else GENERIC_INPUTS
            seen = set()
            for subject in [*inputs, *generic]:
                if isinstance(pattern, bytes) and isinstance(subject, str):
                    subject = subject.encode("latin-1")
                if subject in seen:
                    continue
                seen.add(subject)
                record = {"id": pid, "input": text(subject), "search": match_json(compiled.search(subject)),
                          "match": match_json(compiled.match(subject)), "fullmatch": match_json(compiled.fullmatch(subject)),
                          "finditer": [match_json(m) for m in compiled.finditer(subject)]}
                result = call_result(compiled, call, subject, extra)
                if result is not None:
                    record["call"] = result
                records.append(record)
        sites_out.append(entry)
    stale = sorted(set(INPUTS) - used_inputs)
    if stale:
        raise SystemExit(f"regex_inputs.py has inputs for unknown pattern ids: {stale}")
    stale = sorted(set(BINDINGS) - {s["site"] for s in sites_out if s["dynamic"]})
    if stale:
        raise SystemExit(f"regex_inputs.py has bindings for static or unknown sites: {stale}")
    lookaround = sorted({p["site"] for p in patterns_out if {"lookahead", "negative_lookahead", "lookbehind", "negative_lookbehind", "backreference"} & set(p.get("features", ()))})
    manifest = {
        "freeze_commit": FREEZE_COMMIT,
        "python": f"{PYTHON[0]}.{PYTHON[1]}",
        "source": "src/atrium",
        "call_sites": len(sites_out),
        "pattern_count": len(patterns_out),
        "lookaround_or_backreference_sites": lookaround,
        "sites": sites_out,
        "patterns": patterns_out,
    }
    manifest_text = json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    corpus_text = "".join(json.dumps(r, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n" for r in records)
    return manifest_text, corpus_text


def _bound(node, scope):
    try:
        evaluate(node, scope)
        return True
    except Unbound:
        return False


def coverage_errors(manifest):
    errors = []
    found, hidden = grep_sites()
    listed = collections.Counter((s["file"], s["line"], s["call"]) for s in manifest["sites"])
    for key in sorted(set(found) | set(listed)):
        if found[key] != listed[key]:
            errors.append(f"{key[0]}:{key[1]} re.{key[2]}: {found[key]} in source, {listed[key]} in manifest")
    if len(ast_sites()) != sum(found.values()):
        errors.append(f"AST finds {len(ast_sites())} re calls, text scan finds {sum(found.values())}")
    errors += [f"{f}: imports re under another name or imports regex; the manifest scan cannot see those calls" for f in hidden]
    return errors


def main():
    manifest_text, corpus_text = build()
    if sys.argv[1:] == ["--check"]:
        errors = coverage_errors(json.loads(MANIFEST.read_text()))
        if MANIFEST.read_text() != manifest_text:
            errors.append(f"{rel(MANIFEST)} differs from a fresh generation")
        if CORPUS.read_text() != corpus_text:
            errors.append(f"{rel(CORPUS)} differs from a fresh generation")
        if errors:
            print("\n".join(errors), file=sys.stderr)
            print("regenerate: python tools/golden/corpora/gen_regex.py", file=sys.stderr)
            raise SystemExit(1)
        manifest = json.loads(manifest_text)
        print(f"ok: {manifest['call_sites']} call sites, {manifest['pattern_count']} patterns, {corpus_text.count(chr(10))} corpus records")
        return
    if sys.argv[1:]:
        raise SystemExit("usage: gen_regex.py [--check]")
    OUT.mkdir(parents=True, exist_ok=True)
    MANIFEST.write_text(manifest_text)
    CORPUS.write_text(corpus_text)
    errors = coverage_errors(json.loads(manifest_text))
    if errors:
        print("\n".join(errors), file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
