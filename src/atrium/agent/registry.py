from .tools import all_tools, research, run_code
from .tools.base import CHAT, CRON, READ, SUB

WEB_SEARCH_KINDS = frozenset({CHAT, CRON})
WEB_SEARCH = {"type": "web_search_20260318", "name": "web_search", "max_uses": 5}
WEB_SEARCH_NAME = WEB_SEARCH["name"]


def tools_for(kind):
    tools = all_tools()
    return [run_code.with_stubs(t, tools, kind) if t.name == run_code.TOOL_NAME else t for t in tools if kind in t.contexts]


def run_tools(kind, allow=None, conn=None):
    """Tools offered to one run: `tools_for(kind)` narrowed to `allow` (a subagent's allowlist), so a child never
    sees more than its agent definition grants. With `conn`, the research tool is bound to the enabled subagents
    (and dropped when there are none)."""
    found = []
    for t in tools_for(kind):
        if allow is not None and t.name not in allow:
            continue
        if t.name == research.TOOL_NAME and conn is not None:
            t = research.with_agents(t, conn)
            if t is None:
                continue
        found.append(t)
    return found


def subagent_pool():
    """Read-only tools an operator may put on a subagent's allowlist, sorted by name."""
    return [t for t in all_tools() if t.effect == READ and SUB in t.contexts]


def anthropic_tool_params(tools):
    return [{"name": t.name, "description": t.description, "input_schema": t.input_schema} for t in tools]


def server_tools_for(kind, allow=None):
    if kind in WEB_SEARCH_KINDS or (kind == SUB and allow is not None and WEB_SEARCH_NAME in allow):
        return [dict(WEB_SEARCH)]
    return []
