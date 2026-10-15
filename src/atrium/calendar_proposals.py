import hashlib
import json
import os

from . import calendar

KIND = "calendar_event"
ACCOUNT = "calendar"
UNDO_OP = "calendar_delete"


class ProposalError(ValueError):
    pass


def _create(*args):
    from . import proposals

    return proposals.create(*args)


def _fingerprint(*parts):
    return hashlib.sha256(json.dumps(parts).encode()).hexdigest()[:32]


def propose(conn, env, raw, source=None, key=None, create=_create):
    """Queue an event proposal. `raw` holds calendar/title/start/end/all_day/location/notes; attendees are ignored.

    `key` replaces the content fingerprint (an invitation's UID, so a re-sent invite dedupes).
    """
    target = raw.get("calendar") or calendar.ICLOUD
    if target not in calendar.TARGETS:
        raise ProposalError(f"calendar must be one of {', '.join(calendar.TARGETS)}")
    try:
        spec = calendar.normalize_spec(raw, calendar.tz_of(env))
    except calendar.CalendarError as e:
        raise ProposalError(str(e)) from e
    payload = {"calendar": target, "spec": spec, "source": source or {}}
    preview = {
        "calendar": target,
        "title": spec["title"],
        "start": spec["start"],
        "end": spec["end"],
        "all_day": spec["all_day"],
        "location": spec["location"],
        "notes": spec["notes"],
        "source": source or {},
    }
    fingerprint = _fingerprint(KIND, key) if key else _fingerprint(KIND, target, spec["title"], spec["start"], spec["end"])
    when = spec["start"][:16].replace("T", " ") if not spec["all_day"] else spec["start"]
    return create(conn, KIND, ACCOUNT, f"Add to calendar: {spec['title']} ({when})", payload, preview, fingerprint)


def _payload(proposal):
    payload = proposal["payload"]
    return json.loads(payload) if isinstance(payload, str) else payload


def execute(conn, cfg, proposal, selection=None):
    from . import proposals

    p = _payload(proposal)
    env = os.environ
    ref = calendar.create_event(env, p["calendar"], p["spec"], calendar.tz_of(env))
    calendar.track(conn, p["spec"], p["calendar"], proposal["id"])
    proposals.record_action(
        conn, proposal["id"], {"type": KIND, **ref}, {"op": UNDO_OP, "calendar": p["calendar"], "uid": p["spec"]["uid"]}
    )
    return {"summary": f"Added to the {p['calendar']} calendar", **ref}


def undo_delete(conn, cfg, proposal, info):
    calendar.delete_event(os.environ, info["calendar"], info["uid"])
    calendar.untrack(conn, info["uid"])
    return {"deleted": info["uid"]}


def register(register_executor=None, register_undoer=None):
    if register_executor is None or register_undoer is None:
        from . import proposals

        register_executor = register_executor or proposals.register_executor
        register_undoer = register_undoer or proposals.register_undoer
    register_executor(KIND, execute)
    register_undoer(UNDO_OP, undo_delete)
