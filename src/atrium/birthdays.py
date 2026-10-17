"""Upcoming birthdays and anniversaries from the address book (every contact with a date qualifies)."""

import time
from datetime import date

from . import contacts

DEFAULT_DAYS = 14
MAX_DAYS = 366
DISMISS_KIND = "birthday"


def occurrence(month, day, today):
    """Next date on/after today with this month/day; Feb 29 falls on Feb 28 in common years."""
    for year in (today.year, today.year + 1):
        try:
            when = date(year, month, day)
        except ValueError:
            when = date(year, 2, 28)
        if when >= today:
            return when
    raise AssertionError("unreachable")


def dismissed_keys(conn):
    return {(r["kind"], r["key"]): r["occurrence"] for r in conn.execute("SELECT kind, key, occurrence FROM watch_dismissed")}


def item_key(name, entry):
    return f"{name.casefold()}|{entry['kind']}|{entry['month']:02d}-{entry['day']:02d}"


def upcoming(cards, today, days=DEFAULT_DAYS, dismissed=None):
    """Dates within `days` of today, soonest first. A dismissal hides only the occurrence it was made for."""
    dismissed = dismissed or {}
    out = []
    for card in cards:
        for entry in card.get("dates") or []:
            when = occurrence(entry["month"], entry["day"], today)
            away = (when - today).days
            if away > days:
                continue
            key = item_key(card["name"], entry)
            if dismissed.get((DISMISS_KIND, key)) == when.isoformat():
                continue
            out.append(
                {
                    "name": card["name"],
                    "kind": entry["kind"],
                    "label": entry["label"],
                    "date": when.isoformat(),
                    "days_until": away,
                    "turning": when.year - entry["year"] if entry["year"] else None,
                    "emails": card["emails"],
                    "key": key,
                }
            )
    return sorted(out, key=lambda i: (i["days_until"], i["name"].casefold(), i["kind"]))


def upcoming_for(conn, cfg, today, days=DEFAULT_DAYS):
    """None when no contacts directory is configured."""
    if cfg.contacts_dir is None:
        return None
    return upcoming(contacts.load_contacts(cfg.contacts_dir), today, days, dismissed_keys(conn))


def dismiss(conn, key, occurrence_date):
    date.fromisoformat(occurrence_date)
    conn.execute(
        "INSERT INTO watch_dismissed (kind, key, occurrence, ts) VALUES (?, ?, ?, ?) "
        "ON CONFLICT(kind, key) DO UPDATE SET occurrence = excluded.occurrence, ts = excluded.ts",
        (DISMISS_KIND, key, occurrence_date, time.time()),
    )
