import base64
import imaplib
import re
import time
from dataclasses import dataclass

from .config import BODY_CAP_BYTES, IMAP_PORT
from .safety import APPEND_ACTION, write_gate

READ_VERBS = frozenset({"SEARCH", "FETCH"})
COPYUID = "COPYUID"
LIST_LINE = re.compile(rb'\((?P<flags>[^)]*)\)\s+(?P<delim>"(?:[^"\\]|\\.)*"|NIL)\s+(?P<name>.+)$', re.S)
TOKEN = re.compile(r'\(|\)|"((?:[^"\\]|\\.)*)"|([^\s()"]+)')


@dataclass(frozen=True)
class MailboxInfo:
    name: str
    imap_name: str
    flags: tuple
    delimiter: str


def utf7_decode(value):
    def decode(m):
        chunk = m.group(1)
        if chunk == "":
            return "&"
        chunk = chunk.replace(",", "/")
        chunk += "=" * (-len(chunk) % 4)
        return base64.b64decode(chunk).decode("utf-16-be")

    return re.sub(r"&([^-]*)-", decode, value)


def utf7_encode(value):
    out = []
    pending = []

    def flush():
        if pending:
            chunk = base64.b64encode("".join(pending).encode("utf-16-be")).decode("ascii")
            out.append("&" + chunk.rstrip("=").replace("/", ",") + "-")
            pending.clear()

    for ch in value:
        if ch == "&":
            flush()
            out.append("&-")
        elif 0x20 <= ord(ch) <= 0x7E:
            flush()
            out.append(ch)
        else:
            pending.append(ch)
    flush()
    return "".join(out)


def expand_uid_set(text):
    uids = []
    for part in text.split(","):
        if ":" in part:
            low, high = (int(x) for x in part.split(":"))
            uids.extend(range(min(low, high), max(low, high) + 1))
        elif part:
            uids.append(int(part))
    return uids


def parse_copyuid(data):
    text = " ".join(d.decode("latin1") if isinstance(d, bytes) else str(d) for d in data if d)
    m = re.search(r"COPYUID (\d+) ([\d:,]+) ([\d:,]+)", text)
    if not m:
        return None
    return int(m.group(1)), dict(zip(expand_uid_set(m.group(2)), expand_uid_set(m.group(3)), strict=False))


def _unquote(token):
    return re.sub(r"\\(.)", r"\1", token)


def parse_list(lines):
    found = []
    for line in lines:
        if not isinstance(line, bytes):
            continue
        m = LIST_LINE.match(line)
        if not m:
            continue
        name = m.group("name").decode("latin1").strip()
        if name.startswith('"') and name.endswith('"'):
            name = _unquote(name[1:-1])
        delim = m.group("delim").decode("latin1")
        delim = "" if delim == "NIL" else _unquote(delim[1:-1])
        flags = tuple(m.group("flags").decode("latin1").split())
        found.append(MailboxInfo(utf7_decode(name), name, flags, delim))
    return found


def collapse_labels(labels):
    user = {label for label in labels if not label.startswith("\\")}
    return sorted(
        label for label in user if not any(other.startswith(label + "/") for other in user if other != label)
    )


def parse_labels(text):
    labels = []
    for m in re.finditer(r'"((?:[^"\\]|\\.)*)"|(\S+)', text):
        raw = _unquote(m.group(1)) if m.group(1) is not None else m.group(2)
        labels.append(utf7_decode(raw))
    return labels


def _balanced(text, start):
    depth = 0
    in_quote = False
    i = start
    while i < len(text):
        c = text[i]
        if in_quote:
            if c == "\\":
                i += 1
            elif c == '"':
                in_quote = False
        elif c == '"':
            in_quote = True
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                return text[start : i + 1]
        i += 1
    return None


def _section(meta, key):
    m = re.search(r"\b" + re.escape(key) + r" \(", meta)
    if not m:
        return None
    block = _balanced(meta, m.end() - 1)
    return block[1:-1] if block else None


def parse_sexp(text):
    tokens = []
    for m in TOKEN.finditer(text):
        g = m.group(0)
        if g in "()":
            tokens.append(g)
        elif m.group(1) is not None:
            tokens.append(("s", _unquote(m.group(1))))
        else:
            tokens.append(("a", g))
    pos = 0

    def parse():
        nonlocal pos
        token = tokens[pos]
        pos += 1
        if token == "(":
            out = []
            while tokens[pos] != ")":
                out.append(parse())
            pos += 1
            return out
        return token[1]

    return parse()


def _flatten(node, out):
    if isinstance(node, list):
        for child in node:
            _flatten(child, out)
    else:
        out.append(node)


def attachments_from_structure(structure):
    found = []

    def walk(node):
        if not isinstance(node, list) or not node:
            return
        if isinstance(node[0], list):
            for child in node:
                walk(child)
            return
        if len(node) < 2 or not isinstance(node[0], str) or not isinstance(node[1], str):
            return
        kind = f"{node[0]}/{node[1]}".lower()
        flat = []
        _flatten(node[2:], flat)
        name = ""
        disposition = False
        for i, item in enumerate(flat):
            if not isinstance(item, str):
                continue
            low = item.lower()
            if low in ("filename", "name") and i + 1 < len(flat) and isinstance(flat[i + 1], str):
                name = flat[i + 1]
            if low == "attachment":
                disposition = True
        if kind == "message/rfc822":
            if disposition:
                found.append((kind, name))
        elif name or disposition:
            found.append((kind, name))

    walk(structure)
    return found


def structure_attachments(meta):
    m = re.search(r"BODYSTRUCTURE ", meta)
    if not m or m.end() >= len(meta) or meta[m.end()] != "(":
        return None
    block = _balanced(meta, m.end())
    if block is None:
        return None
    try:
        return attachments_from_structure(parse_sexp(block))
    except (IndexError, ValueError):
        return None


def parse_fetch(data):
    groups = []
    current = None
    for item in data:
        if isinstance(item, tuple):
            prefix, literal = item
            prefix = prefix.decode("latin1")
            if re.match(r"\s*\d+ \(", prefix):
                current = {"meta": prefix, "header": b"", "text": b""}
                groups.append(current)
            elif current is not None:
                current["meta"] += prefix
            if current is None:
                continue
            m = re.search(r"BODY\[(HEADER|TEXT)\]", prefix)
            if m:
                current["header" if m.group(1) == "HEADER" else "text"] = literal
        elif isinstance(item, bytes):
            line = item.decode("latin1")
            if re.match(r"\s*\d+ \(", line):
                current = {"meta": line, "header": b"", "text": b""}
                groups.append(current)
            elif current is not None:
                current["meta"] += " " + line
    return [_fetched(g) for g in groups if re.search(r"\bUID (\d+)", g["meta"])]


def _fetched(group):
    meta = group["meta"]
    flags = _section(meta, "FLAGS")
    labels = _section(meta, "X-GM-LABELS")
    modseq = re.search(r"MODSEQ \((\d+)\)", meta)
    size = re.search(r"RFC822\.SIZE (\d+)", meta)
    gm_msgid = re.search(r"X-GM-MSGID (\d+)", meta)
    gm_thrid = re.search(r"X-GM-THRID (\d+)", meta)
    internal = re.search(r'INTERNALDATE "([^"]+)"', meta)
    return {
        "uid": int(re.search(r"\bUID (\d+)", meta).group(1)),
        "flags": flags.split() if flags else [],
        "labels": parse_labels(labels) if labels is not None else None,
        "modseq": int(modseq.group(1)) if modseq else None,
        "size": int(size.group(1)) if size else 0,
        "gm_msgid": gm_msgid.group(1) if gm_msgid else None,
        "gm_thrid": gm_thrid.group(1) if gm_thrid else None,
        "internaldate": internal.group(1) if internal else "",
        "attachments": structure_attachments(meta),
        "header": group["header"],
        "text": group["text"],
    }


def parse_status(data):
    text = " ".join(d.decode("latin1") if isinstance(d, bytes) else str(d) for d in data)
    values = {}
    for key in ("UIDVALIDITY", "UIDNEXT", "HIGHESTMODSEQ", "MESSAGES"):
        m = re.search(key + r" (\d+)", text)
        values[key.lower()] = int(m.group(1)) if m else None
    return values


def uid_set(uids):
    return ",".join(str(u) for u in uids)


class ImapClient:
    def __init__(self, host, user, password):
        self.host = host
        self._user = user
        self._password = password
        self._conn = None
        self.capabilities = frozenset()

    def connect(self):
        self._conn = imaplib.IMAP4_SSL(self.host, IMAP_PORT)
        self._conn.login(self._user, self._password)
        self.capabilities = frozenset(str(c).upper() for c in self._conn.capabilities)
        return self

    def close(self):
        if self._conn is None:
            return
        try:
            self._conn.logout()
        except (imaplib.IMAP4.error, OSError):
            pass
        self._conn = None

    def list_mailboxes(self):
        typ, data = self._conn.list()
        _ok(typ, data)
        return parse_list(data)

    def status(self, imap_name):
        attrs = "UIDVALIDITY UIDNEXT MESSAGES"
        if "CONDSTORE" in self.capabilities:
            attrs += " HIGHESTMODSEQ"
        typ, data = self._conn.status(quote_name(imap_name), f"({attrs})")
        _ok(typ, data)
        return parse_status(data)

    def select(self, imap_name):
        typ, data = self._conn.select(quote_name(imap_name), readonly=True)
        _ok(typ, data)

    def uid_search(self, *criteria):
        typ, data = self._conn.uid("SEARCH", None, *criteria)
        _ok(typ, data)
        return [int(x) for x in (data[0] or b"").split()]

    def uid_fetch(self, uids, items, changedsince=None):
        if changedsince is not None:
            items = f"{items} (CHANGEDSINCE {changedsince})"
        typ, data = self._conn.uid("FETCH", uid_set(uids), items)
        _ok(typ, data)
        return parse_fetch(data)

    def select_rw(self, imap_name):
        write_gate("IMAP SELECT read-write")
        typ, data = self._conn.select(quote_name(imap_name), readonly=False)
        _ok(typ, data)

    def mutate(self, verb, *args):
        write_gate(f"IMAP {verb}")
        self._conn.untagged_responses.pop(COPYUID, None)
        typ, data = self._conn.uid(verb, *args)
        _ok(typ, data)
        copied = self._conn.untagged_responses.pop(COPYUID, [])
        return [*data, *(b"COPYUID " + c for c in copied)]

    def append(self, imap_name, raw, flags="(\\Seen)"):
        write_gate(APPEND_ACTION, target=imap_name)
        typ, data = self._conn.append(quote_name(imap_name), flags, imaplib.Time2Internaldate(time.time()), raw)
        _ok(typ, data)
        return data

    def mutate_mailbox(self, verb, imap_name):
        write_gate(f"IMAP {verb}")
        typ, data = getattr(self._conn, verb.lower())(quote_name(imap_name))
        _ok(typ, data)
        return data

    def idle_changes(self, duration):
        with self._conn.idle(duration=duration) as idler:
            for typ, _ in idler:
                if typ in ("EXISTS", "EXPUNGE", "FETCH", "RECENT"):
                    return True
        return False

    def noop(self):
        self._conn.noop()


def quote_name(name):
    return '"' + name.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _ok(typ, data):
    if typ != "OK":
        raise imaplib.IMAP4.error(f"IMAP command failed: {typ} {data}")


def fetch_items(gmail, with_body):
    items = ["UID", "FLAGS"]
    if gmail:
        items += ["X-GM-LABELS"]
    if with_body:
        items += ["INTERNALDATE", "RFC822.SIZE", "BODYSTRUCTURE"]
        if gmail:
            items += ["X-GM-MSGID", "X-GM-THRID"]
        items += ["BODY.PEEK[HEADER]", f"BODY.PEEK[TEXT]<0.{BODY_CAP_BYTES}>"]
    return "(" + " ".join(items) + ")"
