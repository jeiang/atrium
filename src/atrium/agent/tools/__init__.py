from .. import nightly
from . import actual, calendar, contacts, context_ops, drafts, grafana, mail, notify, notion_read, notion_write, openloops, profile, propose, recall, research, run_code, schedule, skills, watches, web_fetch

MODULES = (actual, calendar, contacts, context_ops, drafts, grafana, mail, nightly, notify, notion_read, notion_write, openloops, profile, propose, recall, research, run_code, schedule, skills, watches, web_fetch)


def all_tools():
    tools = [t for m in MODULES for t in m.TOOLS]
    names = [t.name for t in tools]
    duplicates = sorted({n for n in names if names.count(n) > 1})
    if duplicates:
        raise ValueError(f"duplicate tool names: {', '.join(duplicates)}")
    return sorted(tools, key=lambda t: t.name)
