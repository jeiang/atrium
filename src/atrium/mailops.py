import contextlib
import email

from . import features
from .config import GMAIL
from .imap import parse_copyuid, quote_name, uid_set, utf7_encode
from .run import open_client

BATCH = 100
DELETED_FLAG = "(\\Deleted)"
INBOX_LABEL = "\\Inbox"
TRASH_LABEL = "\\Trash"
TRASH_NAMES = frozenset({"deleted messages", "deleted items", "trash", "bin"})
VERIFY_ITEMS = "(UID FLAGS BODY.PEEK[HEADER])"
VERIFY_ITEMS_GMAIL = "(UID FLAGS X-GM-LABELS BODY.PEEK[HEADER])"


class MailOpError(RuntimeError):
    pass


class PartialMove(MailOpError):
    def __init__(self, message, validity, mapping):
        super().__init__(message)
        self.validity = validity
        self.mapping = mapping


@contextlib.contextmanager
def session(cfg, account):
    client = open_client(cfg, account)
    try:
        yield client
    finally:
        client.close()


def chunks(items, size=BATCH):
    items = [*items]
    for start in range(0, len(items), size):
        yield items[start : start + size]


def snapshots(conn, ids):
    out = []
    for chunk in chunks(ids, 500):
        marks = ",".join("?" * len(chunk))
        rows = conn.execute(
            "SELECT m.id, m.uid, m.uidvalidity, m.msgid, m.digest, mb.name AS mailbox, mb.imap_name "
            f"FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id IN ({marks})",
            chunk,
        ).fetchall()
        out.extend(dict(r) for r in rows)
    return sorted(out, key=lambda s: s["id"])


def server_msgid(header):
    msg = email.message_from_bytes(header)
    return features.normalize_msgid(features.header(msg, "Message-ID"))


def verify(conn, client, account, snaps):
    ok, skipped = [], []
    survivors = {}
    for snap in snaps:
        row = conn.execute(
            "SELECT m.gone_at, m.uid, m.uidvalidity, m.digest, mb.imap_name FROM messages m "
            "JOIN mailboxes mb ON mb.id = m.mailbox_id WHERE m.id = ?",
            (snap["id"],),
        ).fetchone()
        reason = None
        if row is None or row["gone_at"] is not None:
            reason = "gone"
        elif (row["uid"], row["uidvalidity"], row["imap_name"]) != (snap["uid"], snap["uidvalidity"], snap["imap_name"]):
            reason = "moved"
        elif row["digest"] != snap["digest"]:
            reason = "changed"
        if reason:
            skipped.append({"id": snap["id"], "reason": reason})
        else:
            survivors.setdefault((snap["imap_name"], snap["uidvalidity"]), []).append(snap)
    gmail = account == GMAIL
    items = VERIFY_ITEMS_GMAIL if gmail else VERIFY_ITEMS
    for (imap_name, validity), group in survivors.items():
        if client.status(imap_name)["uidvalidity"] != validity:
            skipped.extend({"id": s["id"], "reason": "uidvalidity"} for s in group)
            continue
        client.select(imap_name)
        present = set()
        for chunk in chunks([s["uid"] for s in group]):
            present.update(client.uid_search("UID", uid_set(chunk)))
        live = [s for s in group if s["uid"] in present]
        skipped.extend({"id": s["id"], "reason": "missing"} for s in group if s["uid"] not in present)
        fetched = {}
        for chunk in chunks([s["uid"] for s in live]):
            for item in client.uid_fetch(chunk, items):
                fetched[item["uid"]] = item
        for snap in live:
            item = fetched.get(snap["uid"])
            if item is None:
                skipped.append({"id": snap["id"], "reason": "missing"})
            elif snap["msgid"] and server_msgid(item["header"]) != snap["msgid"]:
                skipped.append({"id": snap["id"], "reason": "msgid"})
            else:
                ok.append({**snap, "flags": item["flags"], "labels": item["labels"] or []})
    return sorted(ok, key=lambda s: s["id"]), skipped


def group_by_mailbox(items):
    groups = {}
    for item in items:
        groups.setdefault((item["imap_name"], item["uidvalidity"]), []).append(item)
    return groups


def trash_mailbox(client, account):
    found = None
    for info in client.list_mailboxes():
        if TRASH_LABEL.lower() in {f.lower() for f in info.flags}:
            return info.imap_name
        if account != GMAIL and info.name.casefold() in TRASH_NAMES:
            found = found or info.imap_name
    if found is None:
        raise MailOpError("no Trash mailbox found")
    return found


def ensure_folder(client, imap_name):
    if imap_name in {info.imap_name for info in client.list_mailboxes()}:
        return False
    client.mutate_mailbox("CREATE", imap_name)
    client.mutate_mailbox("SUBSCRIBE", imap_name)
    return True


def delete_folder_if_empty(client, imap_name):
    if client.status(imap_name)["messages"]:
        return False
    client.mutate_mailbox("DELETE", imap_name)
    return True


def copy_uids(client, dest_imap, uids):
    data = client.mutate("COPY", uid_set(uids), quote_name(dest_imap))
    return parse_copyuid(data)


def move_uids(client, src_imap, dest_imap, uids):
    if "UIDPLUS" not in client.capabilities:
        raise MailOpError("server lacks UIDPLUS: refusing to move")
    client.select_rw(src_imap)
    for batch in chunks(uids):
        wanted = uid_set(batch)
        copied = copy_uids(client, dest_imap, batch)
        validity, mapping = copied if copied else (None, {})
        try:
            client.mutate("STORE", wanted, "+FLAGS.SILENT", DELETED_FLAG)
            client.mutate("EXPUNGE", wanted)
        except Exception as e:
            raise PartialMove(f"copied but not expunged: {e}", validity, mapping) from e
        yield batch, validity, mapping


def label_list(labels):
    return "(" + " ".join(l if l.startswith("\\") else quote_name(utf7_encode(l)) for l in labels) + ")"


def store_labels(client, imap_name, uids, add=(), remove=()):
    client.select_rw(imap_name)
    for batch in chunks(uids):
        wanted = uid_set(batch)
        if add:
            client.mutate("STORE", wanted, "+X-GM-LABELS", label_list(add))
        if remove:
            client.mutate("STORE", wanted, "-X-GM-LABELS", label_list(remove))


def archive_uids(client, imap_name, uids):
    store_labels(client, imap_name, uids, remove=[INBOX_LABEL])


def relabel_plan(items, add, remove):
    groups = {}
    for item in items:
        have = set(item["labels"])
        key = (tuple(l for l in add if l not in have), tuple(l for l in remove if l in have))
        groups.setdefault(key, []).append(item)
    return groups
