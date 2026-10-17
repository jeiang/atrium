"""Code mode host: runs model-written restricted Python in a Monty worker.

The worker (`codemode_worker`) owns the interpreter and has no tool implementations; each external function
call comes back over JSON lines and is dispatched here through the canonical tool registry, restricted to
read-only tools whose contexts include `code` and the caller's kind.

Where the worker runs depends on `ATRIUM_CODEMODE_SOCKET`:
- set (production): connect to a systemd socket-activated template unit (`atrium-codemode@`), one fresh
  instance per connection under DynamicUser with no network, a read-only filesystem, no access to the
  service's state directory or its /proc, and MemoryMax/CPUQuota/RuntimeMaxSec limits. That separate uid and
  sandbox is the security boundary; Monty is not one.
- unset (development, tests): a plain child process with an empty environment and best-effort rlimits only.
  It shares the parent's uid and is NOT a security boundary.
"""

import json
import keyword
import logging
import os
import selectors
import socket
import subprocess
import sys
import time

from . import context, journal
from .tools.base import CODE, READ, ToolError
from .tools.run_code import INTRO, TOOL_NAME

log = logging.getLogger(__name__)

MAX_CALLS = 24
WALL_SECONDS = 30.0
OUTPUT_BYTES = 8192
RESULT_CHARS = 32768
MAX_LINE_BYTES = 4 << 20
MONTY_LIMITS = {"max_memory": 64 << 20, "max_duration_secs": 2.0, "max_allocations": 5_000_000, "max_recursion_depth": 200}
RLIMITS = {"RLIMIT_AS": 4 << 30, "RLIMIT_CPU": 20}
TRUNCATED = "\n[output truncated]"
STUB_DESCRIPTION_CHARS = 120

def eligible(tools, kind):
    return sorted(
        (t for t in tools if t.name != TOOL_NAME and t.effect == READ and CODE in t.contexts and kind in t.contexts),
        key=lambda t: t.name,
    )


def py_type(schema):
    if not isinstance(schema, dict):
        return "Any"
    if "enum" in schema:
        return "Literal[" + ", ".join(repr(v) for v in schema["enum"]) + "]"
    for key in ("anyOf", "oneOf"):
        if key in schema:
            return " | ".join(dict.fromkeys(py_type(s) for s in schema[key]))
    kind = schema.get("type")
    if isinstance(kind, list):
        return " | ".join(dict.fromkeys(py_type({**schema, "type": k}) for k in kind))
    if kind == "array":
        return f"list[{py_type(schema.get('items'))}]"
    return {"string": "str", "integer": "int", "number": "float", "boolean": "bool", "object": "dict[str, Any]", "null": "None"}.get(kind, "Any")


def parameters(tool):
    """(required names, optional names) in schema order; names that are not Python identifiers are not exposed."""
    props = tool.input_schema.get("properties") or {}
    usable = [n for n in props if n.isidentifier() and not keyword.iskeyword(n)]
    required = set(tool.input_schema.get("required") or [])
    return [n for n in usable if n in required], [n for n in usable if n not in required]


def stub(tool):
    props = tool.input_schema.get("properties") or {}
    required, optional = parameters(tool)
    parts = [f"{n}: {py_type(props[n])}" for n in required]
    keyword_only = []
    for n in optional:
        schema = props[n]
        if "default" in schema:
            keyword_only.append(f"{n}: {py_type(schema)} = {schema['default']!r}")
        else:
            keyword_only.append(f"{n}: {py_type(schema)} | None = None")
    if keyword_only:
        parts += ["*", *keyword_only]
    first = tool.description.strip().splitlines()[0] if tool.description.strip() else ""
    first = first.split(". ")[0].rstrip(".")
    if len(first) > STUB_DESCRIPTION_CHARS:
        first = first[: STUB_DESCRIPTION_CHARS - 1] + "…"
    return f"def {tool.name}({', '.join(parts)}) -> Any: ...  # {first}"


def description(tools, kind):
    return INTRO + "\n".join(stub(t) for t in eligible(tools, kind))


def spawn_env():
    # PYTHONPATH carries only import locations (not secrets) so the worker finds atrium and Monty under Nix
    # or a venv; nothing else of the service environment is inherited.
    return {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "PYTHONPATH": os.pathsep.join(p for p in sys.path if p),
        "PYTHONDONTWRITEBYTECODE": "1",
    }


SOCKET_ENV = "ATRIUM_CODEMODE_SOCKET"


class ProcessChannel:
    def __init__(self, argv):
        try:
            self.proc = subprocess.Popen(
                argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=spawn_env(), close_fds=True, cwd="/"
            )
        except OSError as e:
            raise ToolError(f"could not start the code worker: {e}") from e

    def fileno(self):
        return self.proc.stdout.fileno()

    def write(self, data):
        self.proc.stdin.write(data)
        self.proc.stdin.flush()

    def close(self):
        self.proc.kill()
        self.proc.wait()
        self.proc.stdout.close()
        try:
            self.proc.stdin.close()
        except OSError:
            pass


class SocketChannel:
    """One connection to the socket-activated worker; closing it ends the worker instance."""

    def __init__(self, path):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(WALL_SECONDS)
        try:
            self.sock.connect(path)
        except OSError as e:
            self.sock.close()
            raise ToolError(f"could not reach the code worker socket: {e}") from e

    def fileno(self):
        return self.sock.fileno()

    def write(self, data):
        self.sock.sendall(data)

    def close(self):
        self.sock.close()


def start_worker(argv=None, socket_path=None):
    socket_path = socket_path or os.environ.get(SOCKET_ENV)
    if argv is None and socket_path:
        return SocketChannel(socket_path)
    return ProcessChannel(argv or [sys.executable, "-m", "atrium.agent.codemode_worker"])


class WorkerGone(Exception):
    pass


class WorkerTimeout(Exception):
    pass


class LineReader:
    def __init__(self, fd):
        self.fd = fd
        self.buf = b""
        self.selector = selectors.DefaultSelector()
        self.selector.register(fd, selectors.EVENT_READ)

    def read(self, deadline):
        while b"\n" not in self.buf:
            if len(self.buf) > MAX_LINE_BYTES:
                raise WorkerGone("worker message too large")
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not self.selector.select(remaining):
                raise WorkerTimeout()
            chunk = os.read(self.fd, 65536)
            if not chunk:
                raise WorkerGone("worker exited")
            self.buf += chunk
        line, _, self.buf = self.buf.partition(b"\n")
        try:
            return json.loads(line)
        except ValueError as e:
            raise WorkerGone("worker sent invalid data") from e

    def close(self):
        self.selector.close()


class Host:
    """Dispatches the worker's external calls through the registry with journaling."""

    def __init__(self, ctx, tools):
        self.ctx = ctx
        self.by_name = {t.name: t for t in tools}
        self.calls = 0
        row = ctx.conn.execute(
            "SELECT tool_use_id FROM tool_calls WHERE run_id = ? AND name = ? AND state = ? ORDER BY id DESC LIMIT 1",
            (ctx.run_id, TOOL_NAME, journal.STARTED),
        ).fetchone()
        self.parent = row["tool_use_id"] if row else TOOL_NAME
        self.prefix = f"{self.parent}/code/"
        self.seq = ctx.conn.execute("SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_use_id LIKE ?", (ctx.run_id, self.prefix + "%")).fetchone()[0]

    def arguments(self, tool, args, kwargs):
        required, optional = parameters(tool)
        if len(args) > len(required):
            raise ToolError(f"{tool.name} takes at most {len(required)} positional arguments")
        merged = dict(zip(required, args))
        for key, value in kwargs.items():
            if key in merged:
                raise ToolError(f"{tool.name}: duplicate argument {key}")
            if key not in required and key not in optional:
                raise ToolError(f"{tool.name}: unknown argument {key}")
            merged[key] = value
        missing = [n for n in required if n not in merged]
        if missing:
            raise ToolError(f"{tool.name}: missing argument {', '.join(missing)}")
        return merged

    def call(self, name, args, kwargs):
        if self.calls >= MAX_CALLS:
            return {"error": f"tool call limit ({MAX_CALLS}) reached"}
        self.calls += 1
        tool = self.by_name.get(name)
        if tool is None:
            return {"error": f"unknown function {name}"}
        conn, ctx = self.ctx.conn, self.ctx
        self.seq += 1
        try:
            merged = self.arguments(tool, args, kwargs)
        except ToolError as e:
            merged, error = {"args": args, "kwargs": kwargs}, str(e)
        else:
            error = None
        call_id = journal.start_tool_call(conn, ctx.run_id, f"{self.prefix}{self.seq}", name, merged, journal.SAFE, ctx.now())
        value = None
        if error is None:
            try:
                value = json.loads(json.dumps(tool.handler(ctx, merged), ensure_ascii=False, default=str))
            except ToolError as e:
                error = str(e) or type(e).__name__
            except Exception as e:
                log.exception("code-mode tool %s failed", name)
                error = f"{name} failed: {type(e).__name__}"
        result_id = None
        if error is None:
            body = context.serialize(value)
            if len(body) > RESULT_CHARS:
                error = f"{name} result too large ({len(body)} chars, max {RESULT_CHARS}); narrow the query"
            else:
                result_id, _ = context.store(conn, ctx.run_id, call_id, name, body, ctx.now())
        if error is not None:
            journal.finish_tool_call(conn, call_id, journal.ERROR, ctx.now(), error=error)
            return {"error": error}
        journal.finish_tool_call(conn, call_id, journal.OK, ctx.now(), result_id=result_id)
        return {"ok": value}


def clip(text):
    raw = text.encode()
    if len(raw) <= OUTPUT_BYTES:
        return text
    return raw[:OUTPUT_BYTES].decode(errors="ignore") + TRUNCATED


def render(final):
    output = final.get("output") or ""
    result = final.get("result")
    if result is not None:
        output += ("\n" if output and not output.endswith("\n") else "") + result
    return clip(output)


def execute(ctx, code, tools, argv=None, socket_path=None):
    """Run `code` in a worker with `tools` as external functions; returns {"output", "tool_calls"[, "error"]}."""
    host = Host(ctx, tools)
    spec = {
        "code": code,
        "functions": [t.name for t in tools],
        "limits": MONTY_LIMITS,
        "output_cap": OUTPUT_BYTES * 2,
        "rlimits": RLIMITS,
    }
    channel = start_worker(argv, socket_path)
    reader = LineReader(channel.fileno())
    deadline = time.monotonic() + WALL_SECONDS
    error = None
    final = {}
    try:
        channel.write((json.dumps(spec) + "\n").encode())
        while True:
            message = reader.read(deadline)
            if "call" in message:
                call = message["call"]
                reply = host.call(str(call.get("name")), list(call.get("args") or []), dict(call.get("kwargs") or {}))
                channel.write((json.dumps(reply, ensure_ascii=False) + "\n").encode())
            elif "done" in message:
                final = message["done"]
                break
            elif "failed" in message:
                final = message["failed"]
                error = final.get("error") or "script failed"
                break
            else:
                raise WorkerGone("unexpected worker message")
    except WorkerTimeout:
        error = f"wall-clock limit of {WALL_SECONDS:g}s exceeded; worker killed"
    except (WorkerGone, OSError) as e:
        error = f"code worker failed: {e or 'channel closed'}"
    finally:
        reader.close()
        channel.close()
    out = {"output": render(final), "tool_calls": host.calls}
    if error:
        out["error"] = error
    return out
