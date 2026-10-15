import re
from pathlib import Path

from .flags import unfold

ESCAPE = re.compile(r"\\([nN,;\\])")
SPLIT = re.compile(r"(?<!\\);")
DATE = re.compile(r"^(?:(\d{4})-?|--)(\d{2})-?(\d{2})(?!\d)")
UNKNOWN_YEARS = {1604, 1900}  # Apple Contacts stores a placeholder year when none was entered
APPLE_LABEL = re.compile(r"^_\$!<(.*)>!\$_$")
BIRTHDAY, ANNIVERSARY = "birthday", "anniversary"


def unescape(value):
    return ESCAPE.sub(lambda m: "\n" if m.group(1) in "nN" else m.group(1), value).strip()


def parse_date(value):
    """vCard date -> {"month", "day", "year"} (year None when unknown), or None. Handles YYYYMMDD, YYYY-MM-DD, --MMDD, --MM-DD."""
    m = DATE.match(value.strip())
    if not m:
        return None
    year, month, day = (int(g) if g else None for g in m.groups())
    if not (1 <= month <= 12 and 1 <= day <= (29 if month == 2 else 30 if month in (4, 6, 9, 11) else 31)):
        return None
    if year in UNKNOWN_YEARS or year == 0:
        year = None
    return {"month": month, "day": day, "year": year}


def date_kind(label):
    low = label.casefold()
    if "anniversary" in low:
        return ANNIVERSARY
    if "birthday" in low or "birth" in low:
        return BIRTHDAY
    return label.strip() or "date"


def clean_label(text):
    text = unescape(text)
    m = APPLE_LABEL.match(text)
    return (m.group(1) if m else text).strip()


def parse_vcards(text):
    cards, card = [], None
    for line in unfold(text).splitlines():
        head, sep, value = line.partition(":")
        if not sep:
            continue
        prop = head.split(";")[0]
        group, _, name = prop.rpartition(".")
        name = name.upper()
        if name == "BEGIN" and value.strip().upper() == "VCARD":
            card = {"fn": "", "n": "", "emails": [], "phones": [], "org": "", "dates": [], "labels": {}}
        elif name == "END" and card is not None:
            cards.append(finish(card))
            card = None
        elif card is not None:
            collect(card, name, value, group.lower())
    return cards


def collect(card, name, value, group=""):
    if name == "FN":
        card["fn"] = unescape(value)
    elif name == "N":
        family, given, *_ = [*SPLIT.split(value), "", ""]
        card["n"] = " ".join(p for p in (unescape(given), unescape(family)) if p)
    elif name == "EMAIL":
        email = unescape(value).removeprefix("mailto:").lower()
        if "@" in email and email not in card["emails"]:
            card["emails"].append(email)
    elif name == "TEL":
        phone = unescape(value).removeprefix("tel:")
        if phone and phone not in card["phones"]:
            card["phones"].append(phone)
    elif name == "ORG":
        card["org"] = ", ".join(p for p in (unescape(x) for x in SPLIT.split(value)) if p)
    elif name in ("BDAY", "ANNIVERSARY", "X-ANNIVERSARY", "X-ABDATE"):
        found = parse_date(value)
        if found:
            kind = {"BDAY": BIRTHDAY, "ANNIVERSARY": ANNIVERSARY, "X-ANNIVERSARY": ANNIVERSARY}.get(name)
            card["dates"].append({"kind": kind, "group": group, **found})
    elif name == "X-ABLABEL" and group:
        card["labels"][group] = clean_label(value)


def finish_dates(card):
    out = []
    for item in card["dates"]:
        label = card["labels"].get(item["group"], "") if item["group"] else ""
        kind = item["kind"] or date_kind(label)
        entry = {"kind": kind, "label": label or kind, "month": item["month"], "day": item["day"], "year": item["year"]}
        if entry not in out:
            out.append(entry)
    return out


def finish(card):
    return {
        "name": card["fn"] or card["n"] or (card["emails"][0] if card["emails"] else ""),
        "emails": card["emails"],
        "phones": card["phones"],
        "org": card["org"],
        "dates": finish_dates(card),
    }


def load_contacts(directory):
    if directory is None or not Path(directory).is_dir():
        return []
    found = []
    for path in sorted(Path(directory).rglob("*.vcf")):
        try:
            found.extend(c for c in parse_vcards(path.read_text(errors="replace")) if c["name"])
        except OSError:
            continue
    return sorted(found, key=lambda c: c["name"].casefold())


def search_contacts(directory, query="", limit=None):
    return filter_contacts(load_contacts(directory), query, limit)


def filter_contacts(cards, query="", limit=None):
    terms = (query or "").casefold().split()
    out = []
    for card in cards:
        haystack = " ".join([card["name"], card["org"], *card["emails"], *card["phones"]]).casefold()
        if all(t in haystack for t in terms):
            out.append(card)
            if limit and len(out) >= limit:
                break
    return out
