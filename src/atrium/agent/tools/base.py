import sqlite3
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any

from ...config import Config

READ = "read"
WRITE = "write"
PROPOSE = "propose"
NOTIFY = "notify"
EFFECTS = (READ, WRITE, PROPOSE, NOTIFY)

CHAT = "chat"
CRON = "cron"
NIGHTLY = "nightly"
# A research subagent: the run's tool allowlist (from the operator-defined agent) narrows this further.
SUB = "sub"
CODE = "code"
KINDS = (CHAT, CRON, NIGHTLY, SUB, CODE)
ALL_KINDS = frozenset(KINDS)


class ToolError(Exception):
    pass


class NotConfigured(ToolError):
    pass


@dataclass(frozen=True)
class Tool:
    name: str
    description: str
    input_schema: dict
    handler: Callable[["ToolContext", dict], Any]
    effect: str = READ
    contexts: frozenset[str] = frozenset({CHAT, CRON, NIGHTLY})


@dataclass
class ToolContext:
    conn: sqlite3.Connection
    cfg: Config
    env: Mapping[str, str]
    kind: str
    thread_id: int
    run_id: int
    allowed_urls: set[str]
    embed: Callable[[list[str]], list[list[float]]] | None
    now: Callable[[], float]
    client: Any = None
