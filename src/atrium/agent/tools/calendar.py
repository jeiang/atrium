import json
import uuid

from ... import calendar, calendar_proposals
from .. import writes
from ._args import text_arg
from .base import CHAT, CODE, CRON, NIGHTLY, PROPOSE, READ, SUB, WRITE, NotConfigured, Tool, ToolContext, ToolError

TIME_NOTE = "ISO-8601 (2026-10-16T09:00 or 2026-10-16); times without an offset are in the operator's timezone."
EVENT_PROPERTIES = {
    "calendar": {"type": "string", "enum": list(calendar.TARGETS)},
    "title": {"type": "string"},
    "start": {"type": "string", "description": TIME_NOTE},
    "end": {"type": "string", "description": "Exclusive end, same format. Default: one hour (all-day: one day) after start."},
    "all_day": {"type": "boolean", "description": "start/end are dates"},
    "location": {"type": "string"},
    "notes": {"type": "string"},
}


def _wrap(fn):
    try:
        return fn()
    except calendar.NotConfigured as e:
        raise NotConfigured(str(e)) from e
    except calendar.CalendarError as e:
        raise ToolError(str(e)) from e


def _spec(ctx, args, uid=None):
    # Only known fields are read, so a stray "attendees" argument can never reach a provider.
    raw = {k: text_arg(args, k) for k in ("end", "location", "notes")}
    raw["title"] = text_arg(args, "title", required=True)
    raw["start"] = text_arg(args, "start", required=True)
    raw["all_day"] = bool(args.get("all_day"))
    target = args.get("calendar")
    if target not in calendar.TARGETS:
        raise ToolError(f"calendar must be one of {', '.join(calendar.TARGETS)}")
    return target, calendar.normalize_spec(raw, calendar.tz_of(ctx.env), uid)


def calendar_list(ctx: ToolContext, args: dict):
    start = text_arg(args, "start", required=True)
    end = text_arg(args, "end", required=True)
    return _wrap(lambda: calendar.list_events(ctx.env, start, end, text_arg(args, "calendar")))


def calendar_create(ctx: ToolContext, args: dict):
    target, spec = _wrap(lambda: _spec(ctx, args))

    def run():
        ref = calendar.create_event(ctx.env, target, spec, calendar.tz_of(ctx.env))
        calendar.track(ctx.conn, spec, target)
        return ref

    ref = _wrap(run)
    writes.record(
        ctx,
        "calendar_create",
        f"{target}:{spec['uid']}",
        None,
        {"calendar": target, "spec": spec},
        {"op": "calendar_delete", "calendar": target, "uid": spec["uid"]},
    )
    return {**ref, "title": spec["title"], "start": spec["start"], "end": spec["end"]}


def calendar_delete(ctx: ToolContext, args: dict):
    uid = text_arg(args, "uid", required=True)
    row = calendar.tracked(ctx.conn, uid)
    if row is None:
        raise ToolError("only events created through atrium can be deleted, and this uid is not one of them")
    spec = json.loads(row["spec"])

    def run():
        calendar.delete_event(ctx.env, row["calendar"], uid)
        calendar.untrack(ctx.conn, uid)

    _wrap(run)
    writes.record(
        ctx,
        "calendar_delete",
        f"{row['calendar']}:{uid}",
        {"calendar": row["calendar"], "spec": spec},
        None,
        {"op": "calendar_recreate", "calendar": row["calendar"], "spec": spec},
    )
    return {"deleted": uid, "calendar": row["calendar"]}


def event_propose(ctx: ToolContext, args: dict):
    target = args.get("calendar")
    raw = {k: text_arg(args, k) for k in ("title", "start", "end", "location", "notes")}
    raw["all_day"] = bool(args.get("all_day"))
    raw["calendar"] = target
    source = {}
    if isinstance(args.get("source_message_id"), int):
        source["message_id"] = args["source_message_id"]
    try:
        proposal_id = calendar_proposals.propose(ctx.conn, ctx.env, raw, source)
    except calendar_proposals.ProposalError as e:
        raise ToolError(str(e)) from e
    if proposal_id is None:
        return {"proposed": False, "reason": "an identical proposal already exists"}
    if source.get("message_id"):
        from ... import event_candidates

        event_candidates.mark(ctx.conn, source["message_id"], event_candidates.DRAFTED)
    return {"proposed": True, "proposal_id": proposal_id}


def undo_delete(conn, cfg, env, info):
    try:
        calendar.delete_event(env, info["calendar"], info["uid"])
    except calendar.CalendarError as e:
        raise writes.UndoError(str(e)) from e
    calendar.untrack(conn, info["uid"])
    return {"deleted": info["uid"]}


def undo_recreate(conn, cfg, env, info):
    # Google keeps deleted event ids reserved, so the restored event gets a fresh id.
    spec = {**info["spec"], "uid": uuid.uuid4().hex}
    try:
        calendar.create_event(env, info["calendar"], spec, calendar.tz_of(env))
    except calendar.CalendarError as e:
        raise writes.UndoError(str(e)) from e
    calendar.track(conn, spec, info["calendar"])
    return {"created": spec["uid"]}


writes.register_undoer("calendar_delete", undo_delete)
writes.register_undoer("calendar_recreate", undo_recreate)

TOOLS = [
    Tool(
        name="calendar_list",
        description=(
            "List calendar events between start and end (end exclusive), merged from the iCloud calendar mirror and "
            "the primary Google calendar, recurring events expanded. Optional calendar filters by calendar name "
            "or 'google'. Returns uid, calendar, start, end, title, location."
        ),
        input_schema={
            "type": "object",
            "properties": {
                "start": {"type": "string", "description": TIME_NOTE},
                "end": {"type": "string", "description": TIME_NOTE},
                "calendar": {"type": "string"},
            },
            "required": ["start", "end"],
            "additionalProperties": False,
        },
        handler=calendar_list,
        effect=READ,
        contexts=frozenset({CHAT, CRON, NIGHTLY, SUB, CODE}),
    ),
    Tool(
        name="calendar_create",
        description=(
            "Create an event on the operator's own iCloud or Google calendar. Only for events the operator attends "
            "and time blocks. There is no attendees parameter: invitations are never sent. Call only when the "
            "operator asked for this event in chat."
        ),
        input_schema={
            "type": "object",
            "properties": EVENT_PROPERTIES,
            "required": ["calendar", "title", "start"],
            "additionalProperties": False,
        },
        handler=calendar_create,
        effect=WRITE,
        contexts=frozenset({CHAT}),
    ),
    Tool(
        name="calendar_delete",
        description="Delete an event that atrium created earlier (by its uid from calendar_create). Other events cannot be deleted.",
        input_schema={
            "type": "object",
            "properties": {"uid": {"type": "string"}},
            "required": ["uid"],
            "additionalProperties": False,
        },
        handler=calendar_delete,
        effect=WRITE,
        contexts=frozenset({CHAT}),
    ),
    Tool(
        name="event_propose",
        description=(
            "Propose a calendar event for the operator to approve on the Proposals page (nothing is created until "
            "they approve). Use for events found in mail. No attendees. Pass source_message_id when the event comes "
            "from a mail."
        ),
        input_schema={
            "type": "object",
            "properties": {**EVENT_PROPERTIES, "source_message_id": {"type": "integer"}},
            "required": ["calendar", "title", "start"],
            "additionalProperties": False,
        },
        handler=event_propose,
        effect=PROPOSE,
        contexts=frozenset({CHAT, CRON}),
    ),
]
