#!/usr/bin/env python3
import sys
from datetime import UTC, date, datetime
from pathlib import Path
from zoneinfo import ZoneInfo

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, error, use_source_tree, versions  # noqa: E402

use_source_tree()

from atrium import calendar  # noqa: E402

DTSTAMP = datetime(2026, 10, 18, 12, 0, 0, tzinfo=UTC)


def ics(*events, extra=""):
    return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//golden//EN\n" + extra + "".join(f"BEGIN:VEVENT\n{e.strip()}\nEND:VEVENT\n" for e in events) + "END:VCALENDAR\n"


NEW_YORK_VTIMEZONE = """BEGIN:VTIMEZONE
TZID:Eastern Standard Time
BEGIN:STANDARD
DTSTART:16011104T020000
RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=11
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:16010311T020000
RRULE:FREQ=YEARLY;BYDAY=2SU;BYMONTH=3
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
END:VTIMEZONE
"""

FILES = {
    "single_utc": ics(
        """UID:dentist-1
DTSTART:20261016T130000Z
DTEND:20261016T140000Z
SUMMARY:Dentist
LOCATION:Main St"""
    ),
    "weekly_exdate_override": ics(
        """UID:standup
DTSTART;TZID=America/Port_of_Spain:20260907T090000
DTEND;TZID=America/Port_of_Spain:20260907T093000
RRULE:FREQ=WEEKLY;BYDAY=MO
EXDATE;TZID=America/Port_of_Spain:20261012T090000
SUMMARY:Standup""",
        """UID:standup
RECURRENCE-ID;TZID=America/Port_of_Spain:20261019T090000
DTSTART;TZID=America/Port_of_Spain:20261020T100000
DTEND;TZID=America/Port_of_Spain:20261020T103000
SUMMARY:Standup (moved)""",
    ),
    "all_day_yearly": ics(
        """UID:bday
DTSTART;VALUE=DATE:20200120
RRULE:FREQ=YEARLY
SUMMARY:Ada's birthday"""
    ),
    "all_day_multi_and_single": ics(
        """UID:trip
DTSTART;VALUE=DATE:20261030
DTEND;VALUE=DATE:20261103
SUMMARY:Trip
LOCATION:Tobago""",
        """UID:holiday
DTSTART;VALUE=DATE:20261101
SUMMARY:Holiday""",
    ),
    "all_day_weekly_exdate": ics(
        """UID:bins
DTSTART;VALUE=DATE:20261005
DTEND;VALUE=DATE:20261006
RRULE:FREQ=WEEKLY;COUNT=6
EXDATE;VALUE=DATE:20261019,20261026
SUMMARY:Bins"""
    ),
    "new_york_weekly_across_dst": ics(
        """UID:ny-weekly
DTSTART;TZID=America/New_York:20260302T090000
DTEND;TZID=America/New_York:20260302T100000
RRULE:FREQ=WEEKLY;BYDAY=MO,SU
SUMMARY:NY weekly""",
        """UID:ny-fall
DTSTART;TZID=America/New_York:20261026T090000
DTEND;TZID=America/New_York:20261026T093000
RRULE:FREQ=DAILY;UNTIL=20261104T140000Z
SUMMARY:NY fall daily""",
    ),
    "new_york_nonexistent_and_ambiguous": ics(
        """UID:ny-0230
DTSTART;TZID=America/New_York:20260306T023000
DTEND;TZID=America/New_York:20260306T030000
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Gap 02:30""",
        """UID:ny-0130
DTSTART;TZID=America/New_York:20261030T013000
DTEND;TZID=America/New_York:20261030T020000
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Overlap 01:30""",
    ),
    "london_across_dst": ics(
        """UID:london
DTSTART;TZID=Europe/London:20260327T013000
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:London 01:30""",
        """UID:london-fall
DTSTART;TZID=Europe/London:20261023T013000
DURATION:PT45M
RRULE:FREQ=DAILY;COUNT=4
EXDATE;TZID=Europe/London:20261024T013000
SUMMARY:London fall""",
    ),
    "utc_rrule_in_local_view": ics(
        """UID:utc-daily
DTSTART:20261030T150000Z
DTEND:20261030T153000Z
RRULE:FREQ=DAILY;COUNT=5
EXDATE:20261101T150000Z
SUMMARY:UTC daily"""
    ),
    "floating": ics(
        """UID:floating
DTSTART:20261028T080000
DTEND:20261028T083000
RRULE:FREQ=DAILY;INTERVAL=2;COUNT=4
EXDATE:20261030T080000
SUMMARY:Floating""",
        """UID:floating-single
DTSTART:20261029T230000
DTEND:20261030T010000
SUMMARY:Floating overnight""",
    ),
    "rules_variety": ics(
        """UID:last-day
DTSTART;TZID=America/Port_of_Spain:20260131T170000
DTEND;TZID=America/Port_of_Spain:20260131T180000
RRULE:FREQ=MONTHLY;BYMONTHDAY=-1
SUMMARY:Month end""",
        """UID:second-tue
DTSTART;TZID=America/Port_of_Spain:20260113T120000
DTEND;TZID=America/Port_of_Spain:20260113T130000
RRULE:FREQ=MONTHLY;BYDAY=TU;BYSETPOS=2
SUMMARY:Second Tuesday""",
        """UID:biweekly
DTSTART;TZID=America/Port_of_Spain:20260901T070000
DTEND;TZID=America/Port_of_Spain:20260901T080000
RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,TH;UNTIL=20261231T235959Z
SUMMARY:Biweekly""",
        """UID:rdate
DTSTART;TZID=America/Port_of_Spain:20261002T150000
DTEND;TZID=America/Port_of_Spain:20261002T160000
RDATE;TZID=America/Port_of_Spain:20261022T150000,20261105T150000
SUMMARY:Extra dates""",
        """UID:rdate-and-rule
DTSTART;TZID=America/Port_of_Spain:20261001T060000
DTEND;TZID=America/Port_of_Spain:20261001T061500
RRULE:FREQ=WEEKLY;COUNT=3
RDATE;TZID=America/Port_of_Spain:20261025T060000
EXDATE;TZID=America/Port_of_Spain:20261008T060000
SUMMARY:Rule plus date""",
    ),
    "cancelled_and_overrides": ics(
        """UID:gone
STATUS:CANCELLED
DTSTART:20261020T120000Z
DTEND:20261020T130000Z
SUMMARY:Cancelled""",
        """UID:series
DTSTART;TZID=America/Port_of_Spain:20261019T140000
DTEND;TZID=America/Port_of_Spain:20261019T150000
RRULE:FREQ=DAILY;COUNT=5
SUMMARY:Series""",
        """UID:series
RECURRENCE-ID;TZID=America/Port_of_Spain:20261020T140000
STATUS:CANCELLED
DTSTART;TZID=America/Port_of_Spain:20261020T140000
DTEND;TZID=America/Port_of_Spain:20261020T150000
SUMMARY:Series (cancelled one)""",
        """UID:series
RECURRENCE-ID;TZID=America/Port_of_Spain:20261021T140000
DTSTART;TZID=America/Port_of_Spain:20261201T140000
DTEND;TZID=America/Port_of_Spain:20261201T150000
SUMMARY:Series (moved out)""",
        """UID:series
RECURRENCE-ID:20261022T180000Z
DTSTART;TZID=America/Port_of_Spain:20261022T160000
DTEND;TZID=America/Port_of_Spain:20261022T170000
SUMMARY:Series (moved later, UTC id)""",
        """UID:orphan
RECURRENCE-ID;TZID=America/Port_of_Spain:20261023T090000
DTSTART;TZID=America/Port_of_Spain:20261023T090000
DTEND;TZID=America/Port_of_Spain:20261023T100000
SUMMARY:Override without master""",
    ),
    "missing_parts": ics(
        """UID:no-start
SUMMARY:No DTSTART""",
        """DTSTART:20261021T100000Z
SUMMARY:No UID""",
        """UID:no-end
DTSTART:20261021T110000Z
SUMMARY:No DTEND""",
        """UID:zero-length
DTSTART:20261021T000000Z
DTEND:20261021T000000Z""",
    ),
    "windows_tzid": ics(
        """UID:outlook
DTSTART;TZID=Eastern Standard Time:20261030T090000
DTEND;TZID=Eastern Standard Time:20261030T100000
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Outlook meeting""",
        extra=NEW_YORK_VTIMEZONE,
    ),
    "bad_rrule": ics(
        """UID:bad-rule
DTSTART:20261020T120000Z
DTEND:20261020T130000Z
RRULE:FREQ=FORTNIGHTLY
SUMMARY:Bad rule"""
    ),
    "too_many_occurrences": ics(
        """UID:every-minute
DTSTART:20261019T000000Z
DTEND:20261019T000100Z
RRULE:FREQ=MINUTELY
SUMMARY:Every minute"""
    ),
    "unicode_and_escapes": ics(
        """UID:unicode
DTSTART:20261021T150000Z
DTEND:20261021T160000Z
SUMMARY:Café\\, crème\\; brûlée \\n line
LOCATION:Port of Spain\\, Trinidad"""
    ),
    "not_ics": "this is not a calendar\n",
}

WINDOWS = [
    ("America/Port_of_Spain", "2026-10-15T00:00", "2026-10-29T00:00"),
    ("America/Port_of_Spain", "2026-10-28T00:00", "2026-11-06T00:00"),
    ("America/New_York", "2026-03-01T00:00", "2026-03-16T00:00"),
    ("America/New_York", "2026-10-25T00:00", "2026-11-06T00:00"),
    ("Europe/London", "2026-03-26T00:00", "2026-04-01T00:00"),
    ("Europe/London", "2026-10-22T00:00", "2026-10-28T00:00"),
    ("America/Port_of_Spain", "2027-01-15T00:00", "2027-01-25T00:00"),
]
SINGLE_WINDOW = frozenset({"too_many_occurrences", "not_ics"})

PARSE_WHEN = [
    ("2026-10-20T09:00", False),
    ("2026-10-20 09:00", False),
    ("2026-10-20T09:00:30.5", False),
    ("2026-10-20", False),
    ("2026-10-20T09:00-04:00", False),
    ("2026-10-20T09:00Z", False),
    ("2026-03-08T02:30", False),
    ("2026-11-01T01:30", False),
    ("  2026-10-20T09:00  ", False),
    ("2026-10-20", True),
    ("2026-10-20T09:00", True),
    ("2026-10-20 23:59", True),
    ("20261020", False),
    ("2026-W43-2", False),
    ("2026-13-01", False),
    ("next tuesday", False),
    ("next tuesday", True),
    ("", False),
    ("   ", False),
    (None, False),
    (20261020, False),
]

SPECS = [
    {"title": " Lunch ", "start": "2026-10-20T12:00", "location": " Cafe ", "notes": " bring card ", "attendees": ["x@example.test"]},
    {"title": "Call", "start": "2026-10-20T12:00", "end": "2026-10-20T12:30"},
    {"title": "Call", "start": "2026-10-20T12:00", "end": "2026-10-20T12:00"},
    {"title": "Call", "start": "2026-10-20T12:00", "end": "2026-10-20T11:00"},
    {"title": "Trip", "start": "2026-10-30", "end": "2026-11-03", "all_day": True},
    {"title": "Holiday", "start": "2026-11-01", "all_day": True},
    {"title": "Holiday", "start": "2026-11-01T10:00", "all_day": 1},
    {"title": "Date only timed", "start": "2026-11-01"},
    {"title": "Offset", "start": "2026-11-01T09:00:00+01:00", "end": "2026-11-01T10:00"},
    {"title": "Gap", "start": "2026-03-08T02:30"},
    {"title": "Overlap", "start": "2026-11-01T01:30"},
    {"title": "", "start": "2026-10-20T12:00"},
    {"title": "   ", "start": "2026-10-20T12:00"},
    {"start": "2026-10-20T12:00"},
    {"title": "No start"},
    {"title": "Bad", "start": "tomorrow"},
    {"title": "Given uid", "start": "2026-10-20T12:00", "uid": "from-raw"},
    {"title": "None fields", "start": "2026-10-20T12:00", "location": None, "notes": None, "end": None},
]
SPEC_ZONES = ("America/Port_of_Spain", "America/New_York")


class FixedNow(datetime):
    @classmethod
    def now(cls, tz=None):
        return DTSTAMP if tz is None else DTSTAMP.astimezone(tz)


def describe(value):
    if isinstance(value, datetime):
        return {"type": "datetime", "iso": value.isoformat(), "tz": str(value.tzinfo) if value.tzinfo else None}
    if isinstance(value, date):
        return {"type": "date", "iso": value.isoformat()}
    return {"type": type(value).__name__, "repr": repr(value)}


def build():
    records = []
    for name in sorted(FILES):
        data = FILES[name].replace("\n", "\r\n").encode()
        for zone, start, end in WINDOWS[:1] if name in SINGLE_WINDOW else WINDOWS:
            tz = ZoneInfo(zone)
            window_start = calendar.as_dt(calendar.parse_when(start, tz), tz)
            window_end = calendar.as_dt(calendar.parse_when(end, tz), tz)
            notes = []
            record = {"fn": "events_in_ics", "file": f"calendar/{name}.ics", "calendar": "Home", "tz": zone, "window_start": window_start.isoformat(), "window_end": window_end.isoformat()}
            try:
                record["output"] = {"events": calendar.events_in_ics(data, "Home", window_start, window_end, tz, notes), "notes": notes}
            except (ValueError, KeyError) as e:
                record["error"] = error(e)
            records.append(record)
    for zone in SPEC_ZONES:
        tz = ZoneInfo(zone)
        for value, all_day in PARSE_WHEN:
            try:
                result = {"output": describe(calendar.parse_when(value, tz, all_day))}
            except calendar.CalendarError as e:
                result = {"error": error(e)}
            records.append({"fn": "parse_when", "input": [value, zone, all_day], **result})
        for raw in SPECS:
            try:
                spec = calendar.normalize_spec(raw, tz, uid=None if "uid" in raw else "golden-uid")
            except calendar.CalendarError as e:
                records.append({"fn": "normalize_spec", "input": [raw, zone], "uid": None if "uid" in raw else "golden-uid", "error": error(e)})
                continue
            records.append({"fn": "normalize_spec", "input": [raw, zone], "uid": None if "uid" in raw else "golden-uid", "output": spec})
            records.append({"fn": "google_body", "input": [spec, zone], "output": calendar.google_body(spec, tz)})
            calendar.datetime = FixedNow
            try:
                body = calendar.build_ics(spec, tz)
            finally:
                calendar.datetime = datetime
            records.append({"fn": "build_ics", "input": [spec, zone], "dtstamp": DTSTAMP.isoformat(), "output": body})
    return records


if __name__ == "__main__":
    fixtures = {f"{name}.ics": text.replace("\n", "\r\n").encode() for name, text in FILES.items()}
    emit(
        "calendar",
        build(),
        {
            "source": "src/atrium/calendar.py",
            "functions": ["events_in_ics", "parse_when", "normalize_spec", "google_body", "build_ics"],
            "versions": versions("icalendar", "python-dateutil"),
            "windows": [{"tz": z, "start": s, "end": e} for z, s, e in WINDOWS],
        },
        fixtures,
    )
