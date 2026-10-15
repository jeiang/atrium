from .tools import all_tools, run_code
from .tools.base import CHAT, CRON, SUB_WEB

WEB_SEARCH_KINDS = frozenset({CHAT, CRON, SUB_WEB})
WEB_SEARCH = {"type": "web_search_20260318", "name": "web_search", "max_uses": 5}


def tools_for(kind):
    tools = all_tools()
    return [run_code.with_stubs(t, tools, kind) if t.name == "run_code" else t for t in tools if kind in t.contexts]


def anthropic_tool_params(tools):
    return [{"name": t.name, "description": t.description, "input_schema": t.input_schema} for t in tools]


def server_tools_for(kind):
    return [dict(WEB_SEARCH)] if kind in WEB_SEARCH_KINDS else []
