#!/usr/bin/env python3
import ast
import collections
import hashlib
import json
import re
import re._constants as sre_constants
import re._parser as sre_parse
import sys
import warnings
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from common import FREEZE_COMMIT  # noqa: E402

from regex_inputs import (  # noqa: E402
    BINDINGS,
    CALLABLE_REPL,
    GENERIC_BYTES_INPUTS,
    GENERIC_INPUTS,
    INDIRECT_USES,
    INPUTS,
    NOT_PATTERN_CALLS,
    USE_BINDINGS,
)

ROOT = Path(__file__).resolve().parents[3]
SRC = ROOT / "src" / "atrium"
OUT = ROOT / "testdata" / "corpora"
MANIFEST = OUT / "regex_manifest.json"
CORPUS = OUT / "regex.jsonl"
PYTHON = (3, 14)
CALLS = ("compile", "escape", "findall", "finditer", "fullmatch", "match", "search", "split", "sub", "subn")
GREP = re.compile(r"\bre\.(" + "|".join(CALLS) + r")\b(\s*\()?")
HIDDEN_IMPORT = re.compile(r"^\s*(?:from\s+re\s+import|import\s+re\s+as\b|import\s+re\s*,|import\s+.*,\s*re\b|(?:from\s+)?import\s+regex\b)", re.M)
FLAG_NAMES = ("ASCII", "IGNORECASE", "LOCALE", "MULTILINE", "DOTALL", "VERBOSE")
METHOD_PARAMS = {
    "search": ("string", "pos", "endpos"),
    "match": ("string", "pos", "endpos"),
    "fullmatch": ("string", "pos", "endpos"),
    "finditer": ("string", "pos", "endpos"),
    "findall": ("string", "pos", "endpos"),
    "sub": ("repl", "string", "count"),
    "subn": ("repl", "string", "count"),
    "split": ("string", "maxsplit"),
}
FUNCTION_PARAMS = {op: ("pattern", "string") if params[1] == "pos" else ("pattern", *params) for op, params in METHOD_PARAMS.items()}
DEFAULTS = {"pos": 0, "endpos": sys.maxsize, "count": 0, "maxsplit": 0}
PATTERN_ONLY_METHODS = {"sub", "subn", "finditer", "findall", "fullmatch", "search", "match"}
SHAPING_OPS = {"sub", "subn", "split", "findall"}


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


def where(path, node):
    """file:line, informational only; ids never contain a line number."""
    return f"{rel(path)}:{node.lineno}"


def digest(*parts):
    return hashlib.sha1("\0".join(parts).encode("utf-8", "surrogatepass")).hexdigest()[:8]


def enclosing(tree):
    """id(Call node) -> dotted names of the defs and classes around it ("<module>" at top level)."""
    out = {}

    def visit(node, names):
        for child in ast.iter_child_nodes(node):
            if isinstance(child, ast.Call):
                out[id(child)] = ".".join(names) or "<module>"
            visit(child, [*names, child.name] if isinstance(child, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)) else names)

    visit(tree, [])
    return out


def dedupe(seen, base):
    seen[base] = seen.get(base, 0) + 1
    return base if seen[base] == 1 else f"{base}#{seen[base]}"


class Module:
    def __init__(self, path):
        self.path = path
        self.file = rel(path)
        self.tree = ast.parse(path.read_text(), str(path))
        self.env = module_env(self.tree)
        self.functions = {s.name for s in self.tree.body if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef))}
        self.enclosing = enclosing(self.tree)
        self.keys = self.call_keys()
        self.assigned = {id(s.value): s.targets[0].id for s in self.tree.body if isinstance(s, ast.Assign) and len(s.targets) == 1 and isinstance(s.targets[0], ast.Name)}
        self.modules, self.imported = {}, {}
        for node in ast.walk(self.tree):
            if isinstance(node, ast.Import):
                for a in node.names:
                    self.modules[a.asname or a.name.split(".")[0]] = resolve_module(path, 0, a.name)
            elif isinstance(node, ast.ImportFrom):
                for a in node.names:
                    sub = resolve_module(path, node.level, f"{node.module}.{a.name}" if node.module else a.name)
                    if sub:
                        self.modules[a.asname or a.name] = sub
                    else:
                        self.imported[a.asname or a.name] = (resolve_module(path, node.level, node.module), a.name)


    def call_keys(self):
        """Stable id of every call: <file>:<enclosing def>:<sha1 of its source>, #2.. for repeats in line order."""
        calls = sorted((n for n in ast.walk(self.tree) if isinstance(n, ast.Call)), key=lambda n: (n.lineno, n.col_offset))
        seen = {}
        return {id(n): dedupe(seen, f"{self.file}:{self.enclosing[id(n)]}:{digest(ast.unparse(n))}") for n in calls}


def resolve_module(path, level, dotted):
    if level:
        base = path.parent
        for _ in range(level - 1):
            base = base.parent
    elif dotted and dotted.split(".")[0] == "atrium":
        base, dotted = SRC, ".".join(dotted.split(".")[1:])
    else:
        return None
    target = base.joinpath(*dotted.split(".")) if dotted else base
    for candidate in (target.with_suffix(".py"), target / "__init__.py"):
        if candidate.is_file():
            return rel(candidate)
    return None


def ast_sites(modules):
    """Every re call, with its stable id: <file>:<NAME> for a module-level assignment, otherwise
    <file>:<enclosing def>:<sha1(pattern + flags)[:8]> (#2.. for repeats in line order). Sorted by id."""
    sites = []
    for mod in modules.values():
        found = [n for n in ast.walk(mod.tree) if is_re_call(n)]
        found.sort(key=lambda n: (n.lineno, n.col_offset))
        seen = {}
        for node in found:
            site = {"mod": mod, "line": node.lineno, "col": node.col_offset, "call": node.func.attr, "node": node, "name": mod.assigned.get(id(node))}
            if site["name"]:
                site["id"] = dedupe(seen, f"{mod.file}:{site['name']}")
                if seen[site["id"]] > 1:
                    raise SystemExit(f"{site['id']}: module-level name assigned from several re calls")
            else:
                site["id"] = dedupe(seen, f"{mod.file}:{mod.enclosing[id(node)]}:{pattern_digest(site)}")
            mod.keys[id(node)] = site["id"]
            sites.append(site)
    sites.sort(key=lambda s: (s["mod"].file, s["id"]))
    return sites


def pattern_digest(site):
    node, env = site["node"], site["mod"].env
    flags_node = flags_arg(site) if site["call"] != "escape" else None
    try:
        pattern = evaluate(node.args[0], env)
        flags = evaluate(flags_node, env) if flags_node is not None else 0
        return digest(text(pattern), ",".join(flag_names(flags)))
    except Unbound:
        return digest(ast.unparse(node.args[0]), ast.unparse(flags_node) if flags_node is not None else "")


def receiver(mod, node, compiled):
    if isinstance(node, ast.Name):
        if (mod.file, node.id) in compiled:
            return compiled[(mod.file, node.id)]
        if node.id in mod.imported:
            return compiled.get(mod.imported[node.id])
    if isinstance(node, ast.Attribute) and isinstance(node.value, ast.Name) and node.value.id in mod.modules:
        return compiled.get((mod.modules[node.value.id], node.attr))
    return None


def is_module_receiver(mod, node):
    return isinstance(node, ast.Name) and node.id in mod.modules and node.id != "re"


def method_calls(modules, compiled):
    direct, indirect, unresolved = [], [], []
    for mod in modules.values():
        for node in ast.walk(mod.tree):
            if not (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr in METHOD_PARAMS):
                continue
            recv = node.func.value
            if isinstance(recv, ast.Name) and recv.id == "re":
                continue
            at = mod.keys[id(node)]
            key = receiver(mod, recv, compiled)
            if key:
                direct.append((key, mod, node))
            elif at in INDIRECT_USES:
                indirect.append((at, mod, node))
            elif node.func.attr in PATTERN_ONLY_METHODS and not is_module_receiver(mod, recv):
                unresolved.append((at, ast.unparse(node)[:100]))
    return direct, indirect, unresolved


def value_references(modules):
    found = []
    for mod in modules.values():
        funcs = {id(n.func) for n in ast.walk(mod.tree) if isinstance(n, ast.Call)}
        for n in ast.walk(mod.tree):
            if isinstance(n, ast.Attribute) and isinstance(n.value, ast.Name) and n.value.id == "re" and n.attr in CALLS and id(n) not in funcs:
                found.append(f"{where(mod.path, n)}: re.{n.attr} used as a value; the manifest only records calls")
    return found


def grep_sites():
    found, values, hidden = collections.Counter(), [], []
    for path in source_files():
        content = path.read_text()
        for n, line in enumerate(content.splitlines(), 1):
            for m in GREP.finditer(line):
                if m.group(2):
                    found[(rel(path), n, m.group(1))] += 1
                else:
                    values.append(f"{rel(path)}:{n}: re.{m.group(1)} without a call")
        if HIDDEN_IMPORT.search(content):
            hidden.append(rel(path))
    return found, values, hidden


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
    at_names = {c.AT_END_STRING: "end_of_string_Z", c.AT_BEGINNING_STRING: "start_of_string_A", c.AT_END: "dollar", c.AT_BOUNDARY: "word_boundary", c.AT_NON_BOUNDARY: "word_boundary"}
    category_names = {
        c.CATEGORY_WORD: "word_class",
        c.CATEGORY_NOT_WORD: "word_class",
        c.CATEGORY_DIGIT: "digit_class",
        c.CATEGORY_NOT_DIGIT: "digit_class",
        c.CATEGORY_SPACE: "space_class",
        c.CATEGORY_NOT_SPACE: "space_class",
    }
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
        elif op is c.AT and av in at_names:
            found.add(at_names[av])
        elif op is c.CATEGORY and av in category_names:
            found.add(category_names[av])
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


def call_args(mod, node, params, scope):
    out = {}
    for i, name in enumerate(params):
        if name in ("pattern", "string"):
            continue
        value = arg(node, i, name)
        if value is None:
            continue
        function_name = isinstance(value, ast.Name) and value.id in mod.functions and value.id not in scope
        if name == "repl" and (isinstance(value, ast.Lambda) or function_name or mod.keys[id(node)] in CALLABLE_REPL):
            return None
        try:
            out[name] = evaluate(value, {**mod.env, **scope})
        except Unbound:
            raise SystemExit(f"{where(mod.path, node)}: argument {name}={ast.unparse(value)} needs USE_BINDINGS, INDIRECT_USES locals or CALLABLE_REPL in regex_inputs.py") from None
        if name == "repl" and callable(out[name]):
            return None
    return out


def run(compiled, op, subject, args):
    kw = {k: v for k, v in args.items() if k != "repl"}
    if op in ("sub", "subn"):
        result, n = compiled.subn(args["repl"], subject, **kw)
        return {"result": text(result), "n": n} if op == "subn" else {"result": text(result)}
    if op == "split":
        return {"result": [None if x is None else text(x) for x in compiled.split(subject, **kw)]}
    if op == "findall":
        return {"result": [[text(x) for x in f] if isinstance(f, tuple) else text(f) for f in compiled.findall(subject, *positional(kw))]}
    if op == "finditer":
        return {"result": [match_json(m) for m in compiled.finditer(subject, *positional(kw))]}
    return {"result": match_json(getattr(compiled, op)(subject, *positional(kw)))}


def positional(kw):
    return (kw.get("pos", 0), kw.get("endpos", sys.maxsize))


def json_args(args):
    return {k: text(v) if isinstance(v, (str, bytes)) else v for k, v in sorted(args.items()) if DEFAULTS.get(k, object()) != v}


def instantiate(site):
    node, env = site["node"], site["mod"].env
    key = site["id"]
    try:
        evaluate(node.args[0], env)
        if site["call"] != "escape":
            flags_node = flags_arg(site)
            if flags_node is not None:
                evaluate(flags_node, env)
        return key, [(None, {})], False
    except Unbound:
        if key not in BINDINGS:
            raise SystemExit(f"{key}: dynamic {site['call']} argument needs BINDINGS in regex_inputs.py") from None
        return key, list(BINDINGS[key].items()), True


def flags_arg(site):
    call = site["call"]
    index = 1 if call == "compile" else len(FUNCTION_PARAMS[call])
    return arg(site["node"], index, "flags")


def build():
    if sys.version_info[:2] != PYTHON:
        raise SystemExit(f"generate with Python {PYTHON[0]}.{PYTHON[1]} (the frozen app's), not {sys.version.split()[0]}")
    modules = {rel(p): Module(p) for p in source_files()}
    sites_out, specs, compiled_by_id, compiled_names = [], {}, {}, {}
    for site in ast_sites(modules):
        mod, node, call = site["mod"], site["node"], site["call"]
        key, bindings, dynamic = instantiate(site)
        entry = {"id": key, "site": where(mod.path, node), "file": mod.file, "line": site["line"], "col": site["col"], "call": call, "pattern_source": ast.unparse(node.args[0]), "dynamic": dynamic}
        if site["name"]:
            entry["name"] = site["name"]
            compiled_names[(mod.file, site["name"])] = key
        if call == "escape":
            entry["escape"] = [
                {"binding": label, "locals": local, "input": text(evaluate(node.args[0], {**mod.env, **local})), "output": text(evaluate(node, {**mod.env, **local}))}
                for label, local in bindings
            ]
            sites_out.append(entry)
            continue
        flags_node = flags_arg(site)
        if flags_node is not None:
            entry["flags_source"] = ast.unparse(flags_node)
        entry["patterns"] = []
        for label, local in bindings:
            scope = {**mod.env, **local}
            pid = key if label is None else f"{key}[{label}]"
            pattern = evaluate(node.args[0], scope)
            flags = evaluate(flags_node, scope) if flags_node is not None else 0
            entry["patterns"].append(pid)
            spec = {"id": pid, "site": where(mod.path, node), "site_id": key, "call": call, "pattern": text(pattern), "bytes": isinstance(pattern, bytes), "flags": flag_names(flags), "uses": []}
            if local:
                spec["locals"] = local
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always")
                try:
                    compiled_by_id[pid] = re.compile(pattern, flags)
                except re.error as e:
                    spec["error"] = str(e)
            if caught:
                spec["warnings"] = sorted({f"{w.category.__name__}: {w.message}" for w in caught})
            if pid in compiled_by_id:
                spec["groups"] = compiled_by_id[pid].groups
                spec["group_names"] = dict(sorted(compiled_by_id[pid].groupindex.items()))
                spec["features"] = features(pattern, flags)
            if call != "compile":
                spec["uses"].append(use_entry(mod, node, call, FUNCTION_PARAMS[call], local, key))
            specs[pid] = spec
        sites_out.append(entry)

    direct, indirect, unresolved = method_calls(modules, compiled_names)
    unknown = [f"{at}: {source}" for at, source in unresolved if at not in NOT_PATTERN_CALLS]
    if unknown:
        raise SystemExit("pattern method calls with an unknown receiver; list them in INDIRECT_USES or NOT_PATTERN_CALLS:\n" + "\n".join(unknown))
    stale = sorted(NOT_PATTERN_CALLS - {at for at, _ in unresolved})
    if stale:
        raise SystemExit(f"NOT_PATTERN_CALLS lists call sites that are not unresolved pattern method calls: {stale}")
    for key, mod, node in direct:
        for pid in [p for p in specs if specs[p]["site_id"] == key]:
            for local in USE_BINDINGS.get(mod.keys[id(node)], [{}]):
                specs[pid]["uses"].append(use_entry(mod, node, node.func.attr, METHOD_PARAMS[node.func.attr], local, None))
    for at, mod, node in indirect:
        for flow in INDIRECT_USES[at]:
            pids = [p for p in specs if p in flow["patterns"] or specs[p]["site_id"] in flow["patterns"]]
            if not pids:
                raise SystemExit(f"INDIRECT_USES[{at}]: {flow['patterns']} matches no pattern id")
            for pid in pids:
                specs[pid]["uses"].append(use_entry(mod, node, node.func.attr, METHOD_PARAMS[node.func.attr], flow.get("locals", {}), None))
    stale = sorted(set(USE_BINDINGS) - {m.keys[id(n)] for _, m, n in direct}) + sorted(set(INDIRECT_USES) - {at for at, _, _ in indirect})
    stale += sorted(CALLABLE_REPL - {m.keys[id(n)] for _, m, n in [*direct, *indirect]} - {s["id"] for s in sites_out})
    if stale:
        raise SystemExit(f"regex_inputs.py has use bindings for unknown call sites: {stale}")
    for spec in specs.values():
        spec["uses"].sort(key=lambda u: (u["at"], u["op"], json.dumps(u.get("locals", {}), sort_keys=True)))

    records, used_inputs = [], set()
    for pid, spec in specs.items():
        if pid not in compiled_by_id:
            continue
        compiled = compiled_by_id[pid]
        inputs = INPUTS.get(pid, INPUTS.get(spec["site_id"]))
        if not inputs:
            raise SystemExit(f"{pid}: no corpus inputs in regex_inputs.py")
        used_inputs.add(pid if pid in INPUTS else spec["site_id"])
        recorded = [u for u in spec["uses"] if u.get("args") is not None and (u["op"] in SHAPING_OPS or u["args"])]
        seen = set()
        for subject in [*inputs, *(GENERIC_BYTES_INPUTS if spec["bytes"] else GENERIC_INPUTS)]:
            if spec["bytes"] and isinstance(subject, str):
                subject = subject.encode("latin-1")
            if subject in seen:
                continue
            seen.add(subject)
            record = {
                "id": pid,
                "input": text(subject),
                "search": match_json(compiled.search(subject)),
                "match": match_json(compiled.match(subject)),
                "fullmatch": match_json(compiled.fullmatch(subject)),
                "finditer": [match_json(m) for m in compiled.finditer(subject)],
            }
            calls = [{"at": u["at"], "op": u["op"], **({"args": u["args"]} if u["args"] else {}), **run(compiled, u["op"], subject, u["_args"])} for u in recorded]
            if calls:
                record["calls"] = calls
            records.append(record)
    for spec in specs.values():
        for u in spec["uses"]:
            u.pop("_args", None)
    stale = sorted(set(INPUTS) - used_inputs)
    if stale:
        raise SystemExit(f"regex_inputs.py has inputs for unknown pattern ids: {stale}")
    stale = sorted(set(BINDINGS) - {s["id"] for s in sites_out if s["dynamic"]})
    if stale:
        raise SystemExit(f"regex_inputs.py has bindings for static or unknown sites: {stale}")
    patterns_out = list(specs.values())
    lookaround = sorted({p["site_id"] for p in patterns_out if {"lookahead", "negative_lookahead", "lookbehind", "negative_lookbehind", "backreference"} & set(p.get("features", ()))})
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
    return manifest_text, corpus_text, modules


def use_entry(mod, node, op, params, local, site):
    args = call_args(mod, node, params, local)
    entry = {"at": site or mod.keys[id(node)], "where": where(mod.path, node), "op": op, "source": ast.unparse(node)}
    if local and site is None:
        entry["locals"] = local
    if args is None:
        entry["repl"] = "callable"
        entry["args"] = None
    else:
        entry["args"] = json_args(args)
    entry["_args"] = args
    return entry


def coverage_errors(manifest, modules):
    errors = []
    found, values, hidden = grep_sites()
    found = collections.Counter({k: sum(v for (f, _, c), v in found.items() if (f, c) == k) for k in {(f, c) for f, _, c in found}})
    listed = collections.Counter((s["file"], s["call"]) for s in manifest["sites"])
    for key in sorted(set(found) | set(listed)):
        if found[key] != listed[key]:
            errors.append(f"{key[0]} re.{key[1]}: {found[key]} in source, {listed[key]} in manifest")
    ast_count = len(ast_sites(modules))
    if ast_count != sum(found.values()):
        errors.append(f"AST finds {ast_count} re calls, text scan finds {sum(found.values())}")
    errors += values + value_references(modules)
    errors += [f"{f}: imports re under another name or imports regex; the manifest scan cannot see those calls" for f in hidden]
    return errors


INFORMATIONAL = {"site", "line", "col", "where"}


def informational_free(value):
    """The manifest without file:line fields, so a Python edit that only moves code does not fail --check."""
    if isinstance(value, dict):
        return {k: informational_free(v) for k, v in value.items() if k not in INFORMATIONAL}
    if isinstance(value, list):
        return [informational_free(v) for v in value]
    return value


def main():
    manifest_text, corpus_text, modules = build()
    if sys.argv[1:] == ["--check"]:
        errors = coverage_errors(json.loads(MANIFEST.read_text()), modules)
        if informational_free(json.loads(MANIFEST.read_text())) != informational_free(json.loads(manifest_text)):
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
    errors = coverage_errors(json.loads(manifest_text), modules)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        raise SystemExit(1)


if __name__ == "__main__":
    main()
