"""Calendar providers: the vdirsyncer iCloud mirror (read), Google Calendar (read/write), iCloud CalDAV (write).

Events never carry attendees: specs only hold title/start/end/location/notes, and nothing here emits
ATTENDEE, ORGANIZER or invitation mail (Google inserts use sendUpdates="none").
"""

import json
import time
import uuid
from datetime import UTC, date, datetime, time as dtime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from icalendar import Calendar, Event
from icalendar.prop import vDDDLists

from .config import tz_name

ICLOUD = "icloud"
GOOGLE = "google"
TARGETS = (ICLOUD, GOOGLE)
CALDAV_URL = "https://caldav.icloud.com/"
TOKEN_URI = "https://oauth2.googleapis.com/token"
SCOPE = "https://www.googleapis.com/auth/calendar.events"
MAX_EVENTS = 200
MAX_OCCURRENCES = 2000
DEFAULT_MINUTES = 60

SCHEMA = """
CREATE TABLE IF NOT EXISTS agent_events (
    uid TEXT PRIMARY KEY,
    calendar TEXT NOT NULL,
    title TEXT NOT NULL,
    start TEXT NOT NULL,
    end TEXT NOT NULL,
    spec TEXT NOT NULL,
    created_ts REAL NOT NULL,
    deleted_ts REAL,
    proposal_id INTEGER
);
"""


class CalendarError(Exception):
    pass


class NotConfigured(CalendarError):
    pass


def init_schema(conn):
    conn.executescript(SCHEMA)


def tz_of(env):
    return ZoneInfo(tz_name(env))


def _require(env, *names):
    missing = [n for n in names if not env.get(n)]
    if missing:
        raise NotConfigured(f"{', '.join(missing)} not set")


# ---------- time handling ----------


def _wall(value, tz):
    return value.replace(tzinfo=tz).astimezone(UTC).astimezone(tz)


def parse_when(value, tz, all_day=False):
    """ISO date/datetime string -> date (all day) or aware datetime (naive input is read in tz)."""
    if not isinstance(value, str) or not value.strip():
        raise CalendarError("start and end must be ISO-8601 strings")
    text = value.strip()
    try:
        if all_day:
            return datetime.fromisoformat(text).date() if "T" in text or " " in text else date.fromisoformat(text)
        parsed = datetime.fromisoformat(text)
    except ValueError as e:
        raise CalendarError(f"bad date/time {value!r}: {e}") from e
    if "T" not in text and " " not in text:
        return _wall(datetime.combine(parsed.date(), dtime.min), tz)
    return parsed if parsed.tzinfo else _wall(parsed, tz)


def as_dt(value, tz):
    """date or datetime (floating or aware) -> aware datetime."""
    if isinstance(value, datetime):
        return value if value.tzinfo else value.replace(tzinfo=tz)
    return datetime.combine(value, dtime.min, tz)


def _utc(value):
    return value.astimezone(UTC) if isinstance(value, datetime) else value


def _after(start, exact, tz, days=0):
    if not isinstance(start, datetime):
        return start + timedelta(days=days) + exact
    return _utc(as_dt(start, tz) + timedelta(days=days)) + exact


def fmt(value, tz):
    if isinstance(value, datetime):
        return _utc(as_dt(value, tz)).astimezone(tz).isoformat(timespec="seconds")
    return value.isoformat()


def normalize_spec(raw, tz, uid=None):
    """Validated event spec with ISO-string times. Attendees and any unknown keys are dropped."""
    title = (raw.get("title") or "").strip()
    if not title:
        raise CalendarError("title is required")
    all_day = bool(raw.get("all_day"))
    start = parse_when(raw.get("start"), tz, all_day)
    if raw.get("end"):
        end = parse_when(raw["end"], tz, all_day)
    elif all_day:
        end = start + timedelta(days=1)
    else:
        end = _after(start, timedelta(minutes=DEFAULT_MINUTES), tz).astimezone(start.tzinfo)
    if _utc(end) <= _utc(start):
        raise CalendarError("end must be after start")
    return {
        "uid": uid or raw.get("uid") or uuid.uuid4().hex,
        "title": title,
        "start": start.isoformat(),
        "end": end.isoformat(),
        "all_day": all_day,
        "location": (raw.get("location") or "").strip(),
        "notes": (raw.get("notes") or "").strip(),
    }


def _bounds(spec, tz):
    return parse_when(spec["start"], tz, spec["all_day"]), parse_when(spec["end"], tz, spec["all_day"])


# ---------- mirror (read) ----------


def _end_of(comp, start, tz):
    if comp.get("DTEND") is not None:
        return comp["DTEND"].dt
    if comp.get("DURATION") is not None:
        span = comp["DURATION"].dt
        return _after(start, span - timedelta(days=span.days), tz, span.days)
    return start + timedelta(days=1) if not isinstance(start, datetime) else start


def _occurrence_end(comp, start, end, occ, tz):
    if comp.get("DTEND") is None:
        return _end_of(comp, occ, tz)
    if not isinstance(occ, datetime):
        return occ + (end - start)
    return _after(occ, _utc(as_dt(end, tz)) - _utc(as_dt(start, tz)), tz)


def _ex_dates(comp, name):
    found = comp.get(name)
    if found is None:
        return []
    out = []
    for item in found if isinstance(found, list) else [found]:
        if isinstance(item, vDDDLists):
            out.extend(d.dt for d in item.dts)
    return out


def _naive_local(value, tz):
    return as_dt(value, tz).astimezone(tz).replace(tzinfo=None)


def _longest_span(master, start, end, tz):
    if not isinstance(start, datetime):
        return _naive_local(end, tz) - _naive_local(start, tz)
    if master.get("DTEND") is None and master.get("DURATION") is not None:
        span = master["DURATION"].dt
        return span + timedelta(hours=2) if span.days else span
    return _utc(as_dt(end, tz)) - _utc(as_dt(start, tz))


def _occurrences(master, start, window_start, window_end, tz, notes):
    """Start values of the master's instances that could overlap the window."""
    from dateutil import rrule

    if master.get("RRULE") is None and master.get("RDATE") is None:
        return [start]
    pad = _longest_span(master, start, _end_of(master, start, tz), tz)
    aware = isinstance(start, datetime) and start.tzinfo is not None
    dts = start if aware else (start if isinstance(start, datetime) else datetime.combine(start, dtime.min))
    rset = rrule.rruleset()
    try:
        rules = master.get("RRULE")
        for r in rules if isinstance(rules, list) else ([rules] if rules is not None else []):
            rset.rrule(rrule.rrulestr(r.to_ical().decode(), dtstart=dts))
        for extra in _ex_dates(master, "RDATE"):
            rset.rdate(extra if aware else _naive_local(extra, tz))
        for gone in _ex_dates(master, "EXDATE"):
            rset.exdate(gone if aware else _naive_local(gone, tz))
        if isinstance(start, datetime):
            lo = _utc(window_start) - pad
            lo, hi = (lo, _utc(window_end)) if aware else (_naive_local(lo, tz), _naive_local(window_end, tz))
        else:
            lo, hi = _naive_local(window_start, tz) - pad, _naive_local(window_end, tz)
        found = rset.between(lo, hi, inc=True)
    except (ValueError, TypeError) as e:
        notes.append(f"recurrence of {master.get('UID')} not expanded: {e}")
        return [start]
    if len(found) > MAX_OCCURRENCES:
        notes.append(f"{master.get('UID')}: more than {MAX_OCCURRENCES} occurrences in window")
        found = found[:MAX_OCCURRENCES]
    if isinstance(start, datetime):
        return found
    return [d.date() for d in found]


def _overlaps(s, e, window_start, window_end):
    s, e, window_start, window_end = (_utc(v) for v in (s, e, window_start, window_end))
    return s < window_end and (e > window_start or s >= window_start)


def _event_row(uid, calendar, start, end, comp, tz):
    return {
        "uid": uid,
        "calendar": calendar,
        "start": fmt(start, tz),
        "end": fmt(end, tz),
        "title": str(comp.get("SUMMARY") or ""),
        "location": str(comp.get("LOCATION") or ""),
    }


def events_in_ics(data, calendar, window_start, window_end, tz, notes):
    cal = Calendar.from_ical(data)
    masters, overrides = {}, {}
    for comp in cal.walk("VEVENT"):
        uid = str(comp.get("UID") or "")
        if comp.get("RECURRENCE-ID") is not None:
            overrides.setdefault(uid, []).append(comp)
        elif str(comp.get("STATUS") or "").upper() != "CANCELLED":
            masters[uid] = comp
    out = []
    for uid, comp in masters.items():
        if comp.get("DTSTART") is None:
            continue
        start = comp["DTSTART"].dt
        end = _end_of(comp, start, tz)
        skip = {as_dt(o["RECURRENCE-ID"].dt, tz).astimezone(UTC) for o in overrides.get(uid, [])}
        for occ in _occurrences(comp, start, window_start, window_end, tz, notes):
            if as_dt(occ, tz).astimezone(UTC) in skip:
                continue
            occ_end = _occurrence_end(comp, start, end, occ, tz)
            if _overlaps(as_dt(occ, tz), as_dt(occ_end, tz), window_start, window_end):
                out.append(_event_row(uid, calendar, occ, occ_end, comp, tz))
    for uid, group in overrides.items():
        for comp in group:
            if comp.get("DTSTART") is None or str(comp.get("STATUS") or "").upper() == "CANCELLED":
                continue
            start = comp["DTSTART"].dt
            end = _end_of(comp, start, tz)
            if _overlaps(as_dt(start, tz), as_dt(end, tz), window_start, window_end):
                out.append(_event_row(uid, calendar, start, end, comp, tz))
    return out


def _collection_name(root, path):
    folder = path.parent
    label = folder / "displayname"
    if label.is_file():
        try:
            text = label.read_text().strip()
        except OSError:
            text = ""
        if text:
            return text
    rel = path.relative_to(root).parent
    return rel.parts[0] if rel.parts else root.name


def mirror_events(directory, window_start, window_end, tz, calendar=None):
    root = Path(directory)
    if not root.is_dir():
        raise NotConfigured(f"calendar mirror {root} is not a directory")
    wanted = (calendar or "").lower()
    out, notes = [], []
    for path in sorted(root.rglob("*.ics")):
        name = _collection_name(root, path)
        if wanted and wanted not in (name.lower(), path.relative_to(root).parts[0].lower()):
            continue
        try:
            out.extend(events_in_ics(path.read_bytes(), name, window_start, window_end, tz, notes))
        except (ValueError, OSError, KeyError) as e:
            notes.append(f"{path.name}: unreadable ({type(e).__name__})")
    return out, notes


# ---------- Google ----------


def default_google_service(env):
    _require(env, "GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET", "GOOGLE_REFRESH_TOKEN")
    from google.oauth2.credentials import Credentials
    from googleapiclient.discovery import build

    creds = Credentials(
        None,
        refresh_token=env["GOOGLE_REFRESH_TOKEN"],
        client_id=env["GOOGLE_OAUTH_CLIENT_ID"],
        client_secret=env["GOOGLE_OAUTH_CLIENT_SECRET"],
        token_uri=TOKEN_URI,
        scopes=[SCOPE],
    )
    return build("calendar", "v3", credentials=creds, cache_discovery=False)


GOOGLE_FACTORY = [default_google_service]


def google_events(service, window_start, window_end, tz):
    out, token = [], None
    while len(out) < MAX_EVENTS * 2:
        reply = (
            service.events()
            .list(
                calendarId="primary",
                timeMin=window_start.isoformat(),
                timeMax=window_end.isoformat(),
                singleEvents=True,
                orderBy="startTime",
                maxResults=250,
                pageToken=token,
            )
            .execute()
        )
        for item in reply.get("items", []):
            if item.get("status") == "cancelled":
                continue
            s, e = item.get("start", {}), item.get("end", {})
            start = s.get("dateTime") or s.get("date") or ""
            end = e.get("dateTime") or e.get("date") or ""
            if "dateTime" in s:
                start = datetime.fromisoformat(start).astimezone(tz).isoformat(timespec="seconds")
            if "dateTime" in e:
                end = datetime.fromisoformat(end).astimezone(tz).isoformat(timespec="seconds")
            out.append(
                {
                    "uid": item.get("id", ""),
                    "calendar": GOOGLE,
                    "start": start,
                    "end": end,
                    "title": item.get("summary", ""),
                    "location": item.get("location", ""),
                }
            )
        token = reply.get("nextPageToken")
        if not token:
            break
    return out


def google_body(spec, tz):
    start, end = _bounds(spec, tz)
    if spec["all_day"]:
        times = {"start": {"date": start.isoformat()}, "end": {"date": end.isoformat()}}
    else:
        zone = str(tz)
        times = {
            "start": {"dateTime": start.isoformat(), "timeZone": zone},
            "end": {"dateTime": end.isoformat(), "timeZone": zone},
        }
    body = {"id": spec["uid"], "summary": spec["title"], **times}
    if spec["location"]:
        body["location"] = spec["location"]
    if spec["notes"]:
        body["description"] = spec["notes"]
    return body


def _http_status(error):
    return getattr(getattr(error, "resp", None), "status", None)


# ---------- iCloud CalDAV ----------


def default_icloud_calendar(env):
    # CalDAV authenticates with the Apple ID, which is not the IMAP short name.
    _require(env, "ICLOUD_APPLE_ID", "ICLOUD_APP_PASSWORD")
    import caldav

    client = caldav.DAVClient(url=CALDAV_URL, username=env["ICLOUD_APPLE_ID"], password=env["ICLOUD_APP_PASSWORD"])
    calendars = client.principal().calendars()
    named = (env.get("ATRIUM_ICLOUD_CALENDAR") or "").strip().lower()
    for cal in calendars:
        if named and (cal.name or "").lower() == named:
            return cal
    if named:
        raise CalendarError(f"iCloud calendar {named!r} not found")
    for cal in calendars:
        try:
            if "VEVENT" in (cal.get_supported_components() or ["VEVENT"]):
                return cal
        except Exception:
            return cal
    raise CalendarError("no iCloud event calendar found")


ICLOUD_FACTORY = [default_icloud_calendar]


def build_ics(spec, tz):
    start, end = _bounds(spec, tz)
    cal = Calendar()
    cal.add("prodid", "-//atrium//calendar//EN")
    cal.add("version", "2.0")
    ev = Event()
    ev.add("uid", spec["uid"])
    ev.add("dtstamp", datetime.now(UTC))
    ev.add("summary", spec["title"])
    if spec["all_day"]:
        ev.add("dtstart", start)
        ev.add("dtend", end)
    else:
        ev.add("dtstart", start.astimezone(UTC))
        ev.add("dtend", end.astimezone(UTC))
    if spec["location"]:
        ev.add("location", spec["location"])
    if spec["notes"]:
        ev.add("description", spec["notes"])
    cal.add_component(ev)
    return cal.to_ical().decode()


# ---------- create / delete ----------


def create_event(env, calendar, spec, tz):
    """Create a spec (from normalize_spec) on `calendar`; returns the remote reference."""
    if calendar == GOOGLE:
        service = GOOGLE_FACTORY[0](env)
        try:
            service.events().insert(calendarId="primary", body=google_body(spec, tz), sendUpdates="none").execute()
        except Exception as e:
            if _http_status(e) != 409:  # 409: same client-chosen id already inserted
                raise
    elif calendar == ICLOUD:
        ICLOUD_FACTORY[0](env).save_event(build_ics(spec, tz))
    else:
        raise CalendarError(f"calendar must be one of {', '.join(TARGETS)}")
    return {"calendar": calendar, "uid": spec["uid"]}


def delete_event(env, calendar, uid):
    if calendar == GOOGLE:
        service = GOOGLE_FACTORY[0](env)
        try:
            service.events().delete(calendarId="primary", eventId=uid, sendUpdates="none").execute()
        except Exception as e:
            if _http_status(e) not in (404, 410):
                raise
    elif calendar == ICLOUD:
        from caldav.lib.error import NotFoundError

        try:
            ICLOUD_FACTORY[0](env).event_by_uid(uid).delete()
        except NotFoundError:
            pass
    else:
        raise CalendarError(f"calendar must be one of {', '.join(TARGETS)}")


def track(conn, spec, calendar, proposal_id=None):
    conn.execute(
        "INSERT OR REPLACE INTO agent_events (uid, calendar, title, start, end, spec, created_ts, deleted_ts, proposal_id) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)",
        (spec["uid"], calendar, spec["title"], spec["start"], spec["end"], json.dumps(spec), time.time(), proposal_id),
    )


def tracked(conn, uid):
    return conn.execute("SELECT * FROM agent_events WHERE uid = ? AND deleted_ts IS NULL", (uid,)).fetchone()


def untrack(conn, uid):
    conn.execute("UPDATE agent_events SET deleted_ts = ? WHERE uid = ?", (time.time(), uid))


# ---------- list ----------


def list_events(env, start, end, calendar=None):
    tz = tz_of(env)
    window_start, window_end = as_dt(parse_when(start, tz), tz), as_dt(parse_when(end, tz), tz)
    if window_end <= window_start:
        raise CalendarError("end must be after start")
    wanted = (calendar or "").strip().lower() or None
    events, notes, reached = [], [], 0
    mirror_dir = env.get("ATRIUM_CALENDAR_DIR")
    if wanted != GOOGLE:
        if mirror_dir:
            try:
                found, extra = mirror_events(mirror_dir, window_start, window_end, tz, wanted)
                events += found
                notes += extra
                reached += 1
            except NotConfigured as e:
                notes.append(str(e))
        else:
            notes.append("ATRIUM_CALENDAR_DIR not set: iCloud mirror skipped")
    if wanted in (None, GOOGLE):
        try:
            events += google_events(GOOGLE_FACTORY[0](env), window_start, window_end, tz)
            reached += 1
        except NotConfigured as e:
            notes.append(f"google: {e}")
        except Exception as e:
            notes.append(f"google: {type(e).__name__}: {e}")
    if not reached:
        raise NotConfigured("; ".join(notes) or "no calendar source configured")
    events.sort(key=lambda ev: (as_dt(parse_when(ev["start"], tz, "T" not in ev["start"]), tz), ev["title"]))
    truncated = len(events) > MAX_EVENTS
    return {"events": events[:MAX_EVENTS], "truncated": truncated, "notes": notes}
