import importlib

# Imported on first use: tool modules import agent modules (scheduler, service)
# that themselves import tools.base, so loading them with the package is circular.
NAMES = (
    "actual", "calendar", "contacts", "context_ops", "drafts", "grafana", "mail", "..nightly", "notify",
    "notion_read", "notion_write", "openloops", "profile", "propose", "recall", "research", "run_code",
    "schedule", "skills", "watches", "web_fetch",
)
MODULES = None


def modules():
    global MODULES
    if MODULES is None:
        MODULES = tuple(importlib.import_module(n if n.startswith(".") else f".{n}", __name__) for n in NAMES)
    return MODULES


def all_tools():
    tools = [t for m in modules() for t in m.TOOLS]
    names = [t.name for t in tools]
    duplicates = sorted({n for n in names if names.count(n) > 1})
    if duplicates:
        raise ValueError(f"duplicate tool names: {', '.join(duplicates)}")
    return sorted(tools, key=lambda t: t.name)
