import contextlib
import json
import logging
from dataclasses import dataclass, field

from . import executors, live, mailops, rules, safety
from .config import (
    DELETION_INTERVAL_SECONDS,
    GMAIL,
    INBOX,
    WRITE_ATTEMPTS,
    WRITE_CAP,
)
from .db import now
from .executors import SelectionError
from .imap import collapse_labels
from .sync import SYSTEM_FLAGS, SYSTEM_NAMES, SYSTEM_ROLES

log = logging.getLogger("atrium.applier")

ATTEMPTS = {"attempts": WRITE_ATTEMPTS}
NOT_BLOCKED = (
    "NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest "
    "AND a.kind IN ('file', 'trash') AND a.ok IS NOT 0) "
    "AND (SELECT COUNT(*) FROM auto_actions a WHERE a.account = m.account AND a.digest = m.digest "
    "AND a.kind IN ('file', 'trash') AND a.ok = 0) < :attempts"
)
NO_CORRECTION = "NOT EXISTS (SELECT 1 FROM corrections c WHERE c.message_id = m.id)"
INBOX_CANDIDATES = f"""
SELECT m.*, d.id AS decision_id, d.action AS decision_action, d.dest AS decision_dest
FROM messages m JOIN decisions d ON d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = m.id)
WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 AND m.sent = 0
  AND m.gone_at IS NULL AND (d.action = 'delete' OR (d.action = 'file' AND d.dest IS NOT NULL))
  AND m.id NOT IN (SELECT message_id FROM live_baseline)
  AND {NO_CORRECTION} AND {NOT_BLOCKED}
ORDER BY m.id
"""
CORRECTION_CANDIDATES = """
SELECT m.*, c.folder AS target
FROM corrections c JOIN messages m ON m.id = c.message_id
WHERE c.account = :account AND m.gone_at IS NULL AND m.sent = 0
  AND NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'correct'
    AND a.dest = c.folder AND a.ok IS NOT 0)
  AND (SELECT COUNT(*) FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'correct'
    AND a.dest = c.folder AND a.ok = 0) < :attempts
ORDER BY m.id
"""
FLAG_CANDIDATES = """
SELECT m.* FROM messages m JOIN flag_decisions f ON f.message_id = m.id
WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 0 AND m.flagged = 0 AND m.sent = 0
  AND m.gone_at IS NULL AND f.contacts_match = 1
  AND NOT EXISTS (SELECT 1 FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'flag' AND a.ok IS NOT 0)
  AND (SELECT COUNT(*) FROM auto_actions a WHERE a.message_id = m.id AND a.kind = 'flag' AND a.ok = 0) < :attempts
ORDER BY m.id
"""
DELETION_CANDIDATES = f"""
SELECT m.* FROM messages m JOIN mailboxes mb ON mb.id = m.mailbox_id
WHERE m.account = :account AND m.gone_at IS NULL AND m.sent = 0 AND m.flagged = 0
  AND mb.role NOT IN ({",".join(f"'{r}'" for r in sorted(SYSTEM_ROLES | {"system"}))})
  AND {NO_CORRECTION} AND {NOT_BLOCKED}
ORDER BY m.id
"""
PRIOR_LABEL = (
    "SELECT dest FROM auto_actions WHERE account = ? AND digest = ? AND kind IN ('file', 'correct') "
    "AND ok = 1 AND undone_ts IS NULL ORDER BY id DESC LIMIT 1"
)


@dataclass
class Intent:
    row: object
    kind: str
    check: object
    dest: str | None = None
    decision_id: int | None = None
    remove: tuple = ()


@dataclass
class Budget:
    left: int
    deferred: int = 0

    def take(self, rows, what):
        kept = rows[: self.left]
        extra = len(rows) - len(kept)
        if extra:
            self.deferred += extra
            log.warning("write cap reached: deferring %d %s to a later pass", extra, what)
        self.left -= len(kept)
        return kept


@dataclass
class Outcome:
    written: int = 0
    skipped: int = 0
    failed: int = 0
    by_kind: dict = field(default_factory=dict)
    ran: bool = False

    def count(self, kind, n):
        self.written += n
        self.by_kind[kind] = self.by_kind.get(kind, 0) + n


def in_inbox(account, item):
    if account == GMAIL:
        return mailops.INBOX_LABEL in item["labels"]
    return item["imap_name"].casefold() == INBOX.casefold()


def read_inbox(account, item):
    if mailops.SEEN not in item["flags"]:
        return "unread"
    if mailops.FLAGGED in item["flags"]:
        return "flagged"
    return None if in_inbox(account, item) else "left the inbox"


def unread_inbox(account, item):
    if mailops.SEEN in item["flags"]:
        return "read"
    if mailops.FLAGGED in item["flags"]:
        return "flagged"
    return None if in_inbox(account, item) else "left the inbox"


def unflagged(account, item):
    return "flagged" if mailops.FLAGGED in item["flags"] else None


def unflagged_read(account, item):
    if mailops.FLAGGED in item["flags"]:
        return "flagged"
    return None if mailops.SEEN in item["flags"] else "unread"


def anywhere(account, item):
    return None


def protected_names(client):
    names = set()
    for info in client.list_mailboxes():
        if {f.lower() for f in info.flags} & set(SYSTEM_FLAGS) or info.name.casefold() in SYSTEM_NAMES:
            names.add(info.imap_name)
    return names


def safe_label(name):
    try:
        executors.valid_name(name)
    except SelectionError:
        return False
    return name.casefold() not in SYSTEM_NAMES and not name.startswith("[Gmail]")


@contextlib.contextmanager
def session(cfg, account, opener):
    client = opener(cfg, account)
    try:
        yield client
    finally:
        client.close()


def resync(conn, cfg, account, opener):
    from .sync import sync_account

    client = opener(cfg, account)
    try:
        sync_account(conn, cfg, account, client)
    finally:
        client.close()


def before_of(item):
    return {
        "imap_name": item["imap_name"],
        "uidvalidity": item["uidvalidity"],
        "uid": item["uid"],
        "msgid": item["msgid"],
        "flags": item["flags"],
        "labels": item["labels"],
    }


def move_undo(account, item, dest_imap, dest_validity, dest_uid):
    return {
        "op": "move_back",
        "account": account,
        "src_imap": item["imap_name"],
        "dest_imap": dest_imap,
        "dest_uidvalidity": dest_validity,
        "items": [{"id": item["id"], "msgid": item["msgid"], "uid": item["uid"], "dest_uid": dest_uid}],
    }


def move_group(conn, client, account, src, dest_imap, entries, outcome):
    by_uid = {item["uid"]: (item, intent, aid) for item, intent, aid in entries}
    remaining = sorted(by_uid)
    try:
        for batch, dest_validity, mapping in mailops.move_uids(client, src, dest_imap, remaining):
            for uid in batch:
                item, intent, aid = by_uid[uid]
                dest_uid = mapping.get(uid)
                after = {"imap_name": dest_imap, "uidvalidity": dest_validity, "uid": dest_uid}
                undo = move_undo(account, item, dest_imap, dest_validity, dest_uid)
                live.finish(conn, aid, after=after, undo=undo)
                outcome.count(intent.kind, 1)
            remaining = [u for u in remaining if u not in batch]
    except mailops.PartialMove as e:
        failing, rest = remaining[: mailops.BATCH], remaining[mailops.BATCH :]
        for uid in failing:
            after = {"imap_name": dest_imap, "uidvalidity": e.validity, "uid": e.mapping.get(uid)}
            live.hold(conn, by_uid[uid][2], str(e), after)
        outcome.failed += len(failing)
        for uid in rest:
            live.discard(conn, by_uid[uid][2])
        log.error("%s: partial move from %s to %s: %s", account, src, dest_imap, e)
    except Exception as e:
        failing, rest = remaining[: mailops.BATCH], remaining[mailops.BATCH :]
        for uid in failing:
            live.fail(conn, by_uid[uid][2], f"{type(e).__name__}: {e}")
        outcome.failed += len(failing)
        for uid in rest:
            live.discard(conn, by_uid[uid][2])
        log.error("%s: move from %s to %s failed: %s", account, src, dest_imap, e)


def label_group(conn, client, account, imap_name, validity, added, removed, entries, outcome):
    by_uid = {item["uid"]: (item, intent, aid) for item, intent, aid in entries}
    uids = sorted(by_uid)
    for chunk in mailops.chunks(uids):
        try:
            mailops.store_labels(client, imap_name, chunk, added, removed)
        except Exception as e:
            for uid in chunk:
                live.fail(conn, by_uid[uid][2], f"{type(e).__name__}: {e}")
            outcome.failed += len(chunk)
            for uid in uids[uids.index(chunk[-1]) + 1 :]:
                live.discard(conn, by_uid[uid][2])
            log.error("%s: relabel in %s failed: %s", account, imap_name, e)
            return
        for uid in chunk:
            item, intent, aid = by_uid[uid]
            after = {"add": [*added], "remove": [*removed]}
            undo = {
                "op": "gmail_labels",
                "imap_name": imap_name,
                "uidvalidity": validity,
                "uids": [uid],
                "add": [*removed],
                "remove": [*added],
            }
            live.finish(conn, aid, after=after, undo=undo)
            outcome.count(intent.kind, 1)


def gmail_trash_group(conn, client, account, imap_name, validity, trash, entries, outcome):
    by_uid = {item["uid"]: (item, intent, aid) for item, intent, aid in entries}
    uids = sorted(by_uid)
    client.select_rw(imap_name)
    for chunk in mailops.chunks(uids):
        try:
            copied = mailops.copy_uids(client, trash, chunk)
        except Exception as e:
            for uid in chunk:
                live.fail(conn, by_uid[uid][2], f"{type(e).__name__}: {e}")
            outcome.failed += len(chunk)
            for uid in uids[uids.index(chunk[-1]) + 1 :]:
                live.discard(conn, by_uid[uid][2])
            log.error("%s: trash copy failed: %s", account, e)
            return
        dest_validity, mapping = copied if copied else (None, {})
        for uid in chunk:
            item, intent, aid = by_uid[uid]
            dest_uid = mapping.get(uid)
            undo = None
            if dest_uid is not None:
                undo = {"op": "gmail_untrash", "trash_imap": trash, "dest_uidvalidity": dest_validity, "uids": [dest_uid]}
            live.finish(conn, aid, after={"imap_name": trash, "uidvalidity": dest_validity, "uid": dest_uid}, undo=undo)
            outcome.count(intent.kind, 1)


def flag_group(conn, client, account, imap_name, validity, entries, outcome):
    by_uid = {item["uid"]: (item, intent, aid) for item, intent, aid in entries}
    uids = sorted(by_uid)
    for chunk in mailops.chunks(uids):
        try:
            mailops.store_flag(client, imap_name, chunk, mailops.FLAGGED)
        except Exception as e:
            for uid in chunk:
                live.fail(conn, by_uid[uid][2], f"{type(e).__name__}: {e}")
            outcome.failed += len(chunk)
            for uid in uids[uids.index(chunk[-1]) + 1 :]:
                live.discard(conn, by_uid[uid][2])
            log.error("%s: flagging in %s failed: %s", account, imap_name, e)
            return
        for uid in chunk:
            item, intent, aid = by_uid[uid]
            undo = {
                "op": "unflag",
                "account": account,
                "imap_name": imap_name,
                "uidvalidity": validity,
                "uids": [uid],
            }
            live.finish(conn, aid, after={"flag": mailops.FLAGGED}, undo=undo)
            outcome.count(intent.kind, 1)


def execute(conn, client, account, intents, outcome):
    if not intents:
        return
    by_id = {i.row["id"]: i for i in intents}
    items, skipped = mailops.verify(conn, client, account, mailops.snapshots(conn, list(by_id)))
    for s in skipped:
        intent = by_id[s["id"]]
        live.skip(
            conn, account, intent.row["id"], intent.row["digest"], intent.kind, None, intent.dest, s["reason"],
            intent.decision_id,
        )
        outcome.skipped += 1
    protected = protected_names(client)
    folders = {} if account == GMAIL else {info.name: info.imap_name for info in client.list_mailboxes()}
    trash = None
    moves, labels, trashes, flagging = {}, {}, {}, {}
    for item in items:
        intent = by_id[item["id"]]
        reason = intent.check(account, item)
        if reason is None and item["imap_name"] in protected:
            reason = "protected mailbox"
        dest_imap = None
        added, removed = (), ()
        if reason is None and intent.kind in (live.FILE, live.CORRECT):
            if not safe_label(intent.dest or ""):
                reason = "unsafe destination"
            elif account == GMAIL:
                added = () if intent.dest in item["labels"] else (intent.dest,)
                removed = tuple(
                    l for l in (*intent.remove, mailops.INBOX_LABEL) if l in item["labels"] and l != intent.dest
                )
                if not added and not removed:
                    reason = "already there"
            else:
                dest_imap = folders.get(intent.dest)
                if dest_imap is None or dest_imap in protected:
                    reason = "no such folder"
                elif dest_imap == item["imap_name"]:
                    reason = "already there"
        if reason is None and intent.kind == live.TRASH:
            try:
                trash = trash or mailops.trash_mailbox(client, account)
            except mailops.MailOpError as e:
                reason = str(e)
        if reason is not None:
            if reason != "already there":
                live.skip(
                    conn, account, intent.row["id"], intent.row["digest"], intent.kind, item["imap_name"],
                    intent.dest, reason, intent.decision_id,
                )
                outcome.skipped += 1
            continue
        dest = intent.dest if intent.kind != live.TRASH else trash
        aid = live.begin(
            conn, account, item["id"], intent.row["digest"], intent.kind, item["imap_name"], dest, before_of(item),
            intent.decision_id,
        )
        if aid is None:
            continue
        entry = (item, intent, aid)
        key = (item["imap_name"], item["uidvalidity"])
        if intent.kind == live.FLAG:
            flagging.setdefault(key, []).append(entry)
        elif intent.kind == live.TRASH:
            (trashes if account == GMAIL else moves).setdefault((*key, trash), []).append(entry)
        elif account == GMAIL:
            labels.setdefault((*key, added, removed), []).append(entry)
        else:
            moves.setdefault((*key, dest_imap), []).append(entry)
    for (src, _, dest_imap), entries in moves.items():
        move_group(conn, client, account, src, dest_imap, entries, outcome)
    for (imap_name, validity, added, removed), entries in labels.items():
        label_group(conn, client, account, imap_name, validity, [*added], [*removed], entries, outcome)
    for (imap_name, validity, dest_trash), entries in trashes.items():
        gmail_trash_group(conn, client, account, imap_name, validity, dest_trash, entries, outcome)
    for (imap_name, validity), entries in flagging.items():
        flag_group(conn, client, account, imap_name, validity, entries, outcome)


def inbox_intents(conn, account, budget):
    rows = conn.execute(INBOX_CANDIDATES, {"account": account, **ATTEMPTS}).fetchall()
    out = []
    for row in budget.take(rows, "inbox decisions"):
        if row["decision_action"] == "delete":
            out.append(Intent(row, live.TRASH, read_inbox, decision_id=row["decision_id"]))
        else:
            out.append(Intent(row, live.FILE, read_inbox, row["decision_dest"], row["decision_id"]))
    return out


def correction_intents(conn, account, budget):
    rows = conn.execute(CORRECTION_CANDIDATES, {"account": account, **ATTEMPTS}).fetchall()
    pending = []
    for row in rows:
        if account == GMAIL:
            at_target = row["target"] in collapse_labels(json.loads(row["labels"]))
        else:
            at_target = row["location"] == row["target"]
        if not at_target:
            pending.append(row)
    out = []
    for row in budget.take(pending, "corrections"):
        remove = ()
        if account == GMAIL:
            prior = conn.execute(PRIOR_LABEL, (account, row["digest"])).fetchone()
            remove = (prior["dest"],) if prior and prior["dest"] else ()
        out.append(Intent(row, live.CORRECT, anywhere, row["target"], remove=remove))
    return out


def flag_intents(conn, account, budget):
    rows = conn.execute(FLAG_CANDIDATES, {"account": account, **ATTEMPTS}).fetchall()
    return [Intent(row, live.FLAG, unread_inbox) for row in budget.take(rows, "flags")]


def has_age(rule):
    return any(c["field"] == rules.AGE_FIELD for c in rule.conditions)


def deletion_intents(conn, account, budget):
    ruleset = rules.load_rules(conn, account)
    if not any(r.action == "delete" for r in ruleset):
        return []
    rows = conn.execute(DELETION_CANDIDATES, {"account": account, **ATTEMPTS}).fetchall()
    matched = []
    for row in rows:
        rule = rules.first_match(row, ruleset)
        if rule is not None and rule.action == "delete" and (row["seen"] or has_age(rule)):
            matched.append((row, rule))
    out = []
    for row, rule in budget.take(matched, "deletions"):
        out.append(Intent(row, live.TRASH, unflagged if has_age(rule) else unflagged_read, decision_id=None))
    return out


def deletion_due(conn, account):
    last = live.get_state(conn, f"{live.DELETION_KEY}:{account}")
    return last is None or now() - float(last) >= DELETION_INTERVAL_SECONDS


def run_pass(conn, cfg, account, opener, build, label):
    outcome = Outcome()
    if cfg.mode != safety.LIVE:
        return outcome
    if safety.is_paused(cfg.pause_path):
        log.info("%s: paused, automatic writes skipped", account)
        return outcome
    outcome.ran = True
    budget = Budget(WRITE_CAP)
    with safety.automatic_writes(cfg.pause_path):
        live.recover(conn)
        intents = build(budget)
        if intents:
            with session(cfg, account, opener) as client:
                execute(conn, client, account, intents, outcome)
    if outcome.written:
        resync(conn, cfg, account, opener)
    if outcome.written or outcome.skipped or outcome.failed or budget.deferred:
        log.info(
            "%s: %s applied=%s skipped=%d failed=%d deferred=%d",
            account, label, outcome.by_kind, outcome.skipped, outcome.failed, budget.deferred,
        )
    return outcome


def apply_account(conn, cfg, account, opener):
    return run_pass(
        conn,
        cfg,
        account,
        opener,
        lambda budget: [
            *correction_intents(conn, account, budget),
            *inbox_intents(conn, account, budget),
            *flag_intents(conn, account, budget),
        ],
        "live pass",
    )


def apply_corrections(conn, cfg, account, opener, message_ids):
    wanted = set(message_ids)
    return run_pass(
        conn,
        cfg,
        account,
        opener,
        lambda budget: [i for i in correction_intents(conn, account, Budget(budget.left)) if i.row["id"] in wanted],
        "corrections",
    )


def deletion_pass(conn, cfg, account, opener, force=False):
    if not force and not deletion_due(conn, account):
        return Outcome()
    outcome = run_pass(conn, cfg, account, opener, lambda budget: deletion_intents(conn, account, budget), "deletion pass")
    if outcome.ran:
        live.set_state(conn, f"{live.DELETION_KEY}:{account}", now())
    return outcome


def apply_all(conn, cfg, account, opener):
    return apply_account(conn, cfg, account, opener), deletion_pass(conn, cfg, account, opener)
