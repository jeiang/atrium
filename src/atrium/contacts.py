import re
from pathlib import Path

from .flags import unfold

ESCAPE = re.compile(r"\\([nN,;\\])")
SPLIT = re.compile(r"(?<!\\);")


def unescape(value):
    return ESCAPE.sub(lambda m: "\n" if m.group(1) in "nN" else m.group(1), value).strip()


def parse_vcards(text):
    cards, card = [], None
    for line in unfold(text).splitlines():
        head, sep, value = line.partition(":")
        if not sep:
            continue
        name = head.split(";")[0].split(".")[-1].upper()
        if name == "BEGIN" and value.strip().upper() == "VCARD":
            card = {"fn": "", "n": "", "emails": [], "phones": [], "org": ""}
        elif name == "END" and card is not None:
            cards.append(finish(card))
            card = None
        elif card is not None:
            collect(card, name, value)
    return cards


def collect(card, name, value):
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


def finish(card):
    return {
        "name": card["fn"] or card["n"] or (card["emails"][0] if card["emails"] else ""),
        "emails": card["emails"],
        "phones": card["phones"],
        "org": card["org"],
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
