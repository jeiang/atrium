from datetime import datetime

from .. import scheduler
from ._args import text_arg
from .base import CHAT, READ, WRITE, Tool, ToolContext, ToolError

SCHEDULE_HELP = (
    "ISO datetime for a one-off ('2026-11-02T09:00'), 'daily HH:MM', 'weekly mon HH:MM', or a 5-field cron expression "
    "('0 9 * * 1-5'). Times are local. Recurring schedules must be at least 1 hour apart."
)
CHAT_ONLY = frozenset({CHAT})


def describe(job):
    due = job["next_due"]
    return {
        "id": job["id"],
        "name": job["name"],
        "prompt": job["prompt"],
        "schedule": job["schedule"],
        "enabled": bool(job["enabled"]),
        "next_due": datetime.fromtimestamp(due, scheduler.zone(job["tz"])).isoformat(timespec="minutes") if due else None,
        "created_by": job["created_by"],
    }


def agent_job(conn, args):
    job_id = args.get("id")
    if isinstance(job_id, bool) or not isinstance(job_id, int):
        raise ToolError("id must be an integer")
    job = scheduler.get_job(conn, job_id)
    if job is None or job["cancelled"]:
        raise ToolError(f"no active schedule {job_id}")
    if job["kind"] == scheduler.NIGHTLY:
        raise ToolError("the nightly run is managed by the operator")
    return job


def schedule_create(ctx: ToolContext, args: dict):
    try:
        job_id = scheduler.create_job(
            ctx.conn,
            name=text_arg(args, "name", required=True),
            prompt=text_arg(args, "prompt", required=True),
            schedule=text_arg(args, "schedule", required=True),
            tz=scheduler.tz_name(ctx.env),
            now=ctx.now(),
            created_by=scheduler.AGENT,
            thread_id=ctx.thread_id,
        )
    except scheduler.ScheduleError as e:
        raise ToolError(str(e)) from e
    return describe(scheduler.get_job(ctx.conn, job_id))


def schedule_update(ctx: ToolContext, args: dict):
    job = agent_job(ctx.conn, args)
    enabled = args.get("enabled")
    if enabled is not None and not isinstance(enabled, bool):
        raise ToolError("enabled must be a boolean")
    try:
        updated = scheduler.update_job(
            ctx.conn,
            job["id"],
            ctx.now(),
            name=text_arg(args, "name"),
            prompt=text_arg(args, "prompt"),
            schedule=text_arg(args, "schedule"),
            enabled=enabled,
        )
    except scheduler.ScheduleError as e:
        raise ToolError(str(e)) from e
    return describe(updated)


def schedule_cancel(ctx: ToolContext, args: dict):
    job = agent_job(ctx.conn, args)
    scheduler.cancel_job(ctx.conn, job["id"])
    return {"cancelled": job["id"]}


def schedule_list(ctx: ToolContext, args: dict):
    return {"schedules": [describe(j) for j in scheduler.list_jobs(ctx.conn) if j["kind"] != scheduler.NIGHTLY]}


def object_schema(properties, required=()):
    return {"type": "object", "properties": properties, "required": list(required), "additionalProperties": False}


TOOLS = [
    Tool(
        name="schedule_create",
        description=(
            "Create a scheduled run: at the given time a new thread starts with `prompt` and read, notify and propose tools only. "
            f"The prompt must be self-contained. {SCHEDULE_HELP} At most {scheduler.MAX_ACTIVE_JOBS} active schedules."
        ),
        input_schema=object_schema(
            {"name": {"type": "string"}, "prompt": {"type": "string"}, "schedule": {"type": "string", "description": SCHEDULE_HELP}},
            ("name", "prompt", "schedule"),
        ),
        handler=schedule_create,
        effect=WRITE,
        contexts=CHAT_ONLY,
    ),
    Tool(
        name="schedule_update",
        description="Change a schedule's name, prompt, schedule or enabled flag by id.",
        input_schema=object_schema(
            {
                "id": {"type": "integer"},
                "name": {"type": "string"},
                "prompt": {"type": "string"},
                "schedule": {"type": "string", "description": SCHEDULE_HELP},
                "enabled": {"type": "boolean"},
            },
            ("id",),
        ),
        handler=schedule_update,
        effect=WRITE,
        contexts=CHAT_ONLY,
    ),
    Tool(
        name="schedule_cancel",
        description="Cancel a schedule by id. Its past runs and threads are kept.",
        input_schema=object_schema({"id": {"type": "integer"}}, ("id",)),
        handler=schedule_cancel,
        effect=WRITE,
        contexts=CHAT_ONLY,
    ),
    Tool(
        name="schedule_list",
        description="List scheduled runs with their next due time.",
        input_schema=object_schema({}),
        handler=schedule_list,
        effect=READ,
        contexts=CHAT_ONLY,
    ),
]
