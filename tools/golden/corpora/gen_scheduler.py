#!/usr/bin/env python3
import sys
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, error, use_source_tree  # noqa: E402

use_source_tree()

from atrium.agent import scheduler  # noqa: E402

ZONES = ("America/Port_of_Spain", "America/New_York", "Europe/London")
CHAIN = 6

# (zone the instant is written in, local wall time); each is used as `after`/`now` in every zone.
AFTERS = [
    ("America/Port_of_Spain", "2026-10-15T12:00:00"),
    ("America/New_York", "2026-03-07T12:00:00"),
    ("America/New_York", "2026-03-08T01:59:00"),
    ("America/New_York", "2026-10-31T12:00:00"),
    ("America/New_York", "2026-11-01T01:30:00"),
    ("Europe/London", "2026-03-28T12:00:00"),
    ("Europe/London", "2026-10-24T12:00:00"),
    ("Europe/London", "2026-10-25T01:30:00"),
    ("America/Port_of_Spain", "2026-12-31T23:59:00"),
    ("America/Port_of_Spain", "2028-02-28T12:00:00"),
]

SCHEDULES = [
    "daily 07:30",
    "daily 12:30",
    "daily 00:00",
    "daily 23:59",
    "daily 02:30",
    "daily 01:30",
    "daily 01:00",
    "daily 02:00",
    "daily 7:05",
    "DAILY 07:30",
    "  daily   07:30  ",
    "weekly mon 09:00",
    "weekly Thursday 13:00",
    "weekly SUN 02:30",
    "weekly sat 01:30",
    "weekly mondays 08:00",
    "0 9 * * 1-5",
    "*/15 13 * * *",
    "30 8 1 * *",
    "0 8 * * 7",
    "0 8 * * 0",
    "0 9 * * MON-FRI",
    "0 9 * * mon,wed,fri",
    "0 9 * JAN,jul *",
    "0 9 * jan-mar *",
    "0 9 1,15 * *",
    "0 9 1 * mon",
    "0 9 13 * 5",
    "0 9 31 * *",
    "0 9 29 2 *",
    "0 9 30 2 *",
    "30 2 * * *",
    "30 1 * * *",
    "0 1-3 * * *",
    "0 */6 * * *",
    "0 0-23/4 * * *",
    "5/15 1 * * *",
    "0 2 8 3 *",
    "0 1 1 11 *",
    "0 1 25 10 *",
    "0 * * * *",
    "*/30 * * * *",
    "0,30 9 * * *",
    "* * * * *",
    "0 9 * * 0-7",
    "0 9 * * */2",
    "15 10 L * *",
    "2026-11-02T09:00",
    "2026-11-02 09:00",
    "2026-11-02",
    "2026-11-02T09:00:00-04:00",
    "2026-11-02T09:00Z",
    "2026-03-08T02:30",
    "2026-11-01T01:30",
    "2026-03-29T01:30",
    "2026-10-25T01:30",
    "2026-10-01T09:00",
    "2026-10-15T12:05",
    "",
    "   ",
    "daily 25:00",
    "daily 24:00",
    "daily 07:60",
    "daily 7:5",
    "daily 07:30 extra",
    "daily",
    "weekly funday 09:00",
    "weekly mon",
    "weekly mo 09:00",
    "61 * * * *",
    "* 24 * * *",
    "* * 0 * *",
    "* * 32 * *",
    "* * * 13 *",
    "* * * * 8",
    "0 9 * * 7-0",
    "0 9 * * fri-mon",
    "* * * *",
    "1 2 3 4 5 6",
    "tomorrow-ish",
    "*/0 * * * *",
    "*/x * * * *",
    "1-2-3 * * * *",
    "-1 * * * *",
    "a * * * *",
    ", * * * *",
    "0 9 * * sunday",
]

TZ_ERRORS = [("daily 07:30", "Mars/Olympus_Mons"), ("daily 07:30", ""), ("daily 07:30", "UTC"), ("0 9 * * *", "Asia/Kolkata")]


def stamp(zone, wall):
    return datetime.fromisoformat(wall).replace(tzinfo=ZoneInfo(zone)).timestamp()


def local(ts, zone):
    return datetime.fromtimestamp(ts, ZoneInfo(zone)).isoformat()


def spec_dict(spec):
    def ordered(values):
        return None if values is None else sorted(values)

    return {
        "kind": spec.kind,
        "tz": spec.tz.key,
        "once_at": spec.once_at,
        "minutes": sorted(spec.minutes),
        "hours": sorted(spec.hours),
        "dom": ordered(spec.dom),
        "months": ordered(spec.months),
        "dow": ordered(spec.dow),
        "recurring": spec.recurring,
    }


def build():
    records = []
    afters = [stamp(zone, wall) for zone, wall in AFTERS]
    for text in SCHEDULES:
        for zone in ZONES:
            try:
                spec = scheduler.parse(text, zone)
            except scheduler.ScheduleError as e:
                records.append({"fn": "parse", "input": [text, zone], "error": error(e)})
                records.append({"fn": "describe", "input": [text, zone], "output": scheduler.describe(text, zone)})
                continue
            records.append({"fn": "parse", "input": [text, zone], "output": spec_dict(spec)})
            records.append({"fn": "describe", "input": [text, zone], "output": scheduler.describe(text, zone)})
            for after in afters:
                chain, current = [], after
                for _ in range(CHAIN):
                    current = spec.next_after(current)
                    if current is None:
                        break
                    chain.append({"ts": current, "local": local(current, zone)})
                records.append({"fn": "next_after", "input": [text, zone, after], "local_after": local(after, zone), "output": chain})
                records.append({"fn": "min_gap", "input": [text, zone, after], "output": spec.min_gap(after)})
                for enforce_min in (True, False):
                    try:
                        _, due = scheduler.validate(text, zone, after, enforce_min=enforce_min)
                        result = {"output": due}
                    except scheduler.ScheduleError as e:
                        result = {"error": error(e)}
                    records.append({"fn": "validate", "input": [text, zone, after], "enforce_min": enforce_min, **result})
    for text, zone in TZ_ERRORS:
        try:
            result = {"output": spec_dict(scheduler.parse(text, zone))}
        except scheduler.ScheduleError as e:
            result = {"error": error(e)}
        records.append({"fn": "parse", "input": [text, zone], **result})
        records.append({"fn": "describe", "input": [text, zone], "output": scheduler.describe(text, zone)})
    return records


if __name__ == "__main__":
    emit(
        "scheduler",
        build(),
        {
            "source": "src/atrium/agent/scheduler.py",
            "functions": ["parse", "describe", "Spec.next_after", "Spec.min_gap", "validate"],
            "zones": list(ZONES),
            "afters": [{"zone": z, "wall": w, "ts": stamp(z, w)} for z, w in AFTERS],
            "chain": CHAIN,
        },
    )
