"""Code-mode worker: hosts the Monty interpreter in its own process.

It holds no tool implementations and no secrets. It runs either as a child process or as a socket-activated
systemd instance (stdin and stdout are then the same connection). Protocol (JSON lines):
  stdin first line: {"code", "functions", "limits", "output_cap", "rlimits"}
  stdout: {"call": {"name", "args", "kwargs"}} -> reply line {"ok": v} | {"error": msg}
  stdout: {"done": {"output", "result"}} | {"failed": {"output", "error"}}
Monty's own stdout is captured by a print callback, so the protocol channel is a private dup of fd 1.
"""

import json
import os
import sys

TRUNCATED = "\n[output truncated]"


class Collector:
    def __init__(self, cap):
        self.cap = cap
        self.parts = []
        self.size = 0

    def __call__(self, stream, text):
        if self.size < self.cap:
            self.parts.append(text)
            self.size += len(text)

    def text(self):
        return "".join(self.parts)[: self.cap + 1]


def apply_rlimits(rlimits):
    try:
        import resource
    except ImportError:
        return
    for name, value in rlimits.items():
        limit = getattr(resource, name, None)
        if limit is None:
            continue
        try:
            resource.setrlimit(limit, (value, value))
        except (ValueError, OSError):
            pass  # not every platform enforces every limit; the sandbox around the worker is the outer boundary


def main():
    out = os.fdopen(os.dup(1), "w", encoding="utf-8", buffering=1)
    os.dup2(2, 1)
    sys.stdout = sys.stderr
    spec = json.loads(sys.stdin.readline())
    apply_rlimits(spec.get("rlimits") or {})
    import pydantic_monty

    def send(message):
        out.write(json.dumps(message, ensure_ascii=False) + "\n")
        out.flush()

    def proxy(name):
        def call(*args, **kwargs):
            send({"call": {"name": name, "args": list(args), "kwargs": kwargs}})
            line = sys.stdin.readline()
            if not line:
                raise RuntimeError("host closed the channel")
            reply = json.loads(line)
            if "error" in reply:
                raise RuntimeError(reply["error"])
            return reply["ok"]

        return call

    collector = Collector(spec["output_cap"])
    try:
        monty = pydantic_monty.Monty(spec["code"])
        result = monty.run(
            limits=spec.get("limits") or None,
            external_functions={name: proxy(name) for name in spec["functions"]},
            print_callback=collector,
        )
        rendered = None if result is None else json.dumps(result, ensure_ascii=False, default=repr)
        send({"done": {"output": collector.text(), "result": rendered}})
    except pydantic_monty.MontyError as e:
        send({"failed": {"output": collector.text(), "error": e.display("type-msg") if hasattr(e, "display") else str(e)}})
    except Exception as e:
        send({"failed": {"output": collector.text(), "error": f"{type(e).__name__}: {e}"}})


if __name__ == "__main__":
    main()
