from . import db, mailops, proposals, ruleedit, run
from .config import GMAIL, INBOX
from .imap import utf7_encode
from .proposals import SelectionError, record_action

REBALANCE_SPLIT = "rebalance_split"
REBALANCE_MERGE = "rebalance_merge"
DELETION_RULE = "deletion_rule"
GMAIL_LABEL_MERGE = "gmail_label_merge"
MAX_NAME_CHARS = 200


def valid_name(value):
    name = (value or "").strip()
    if not name or len(name) > MAX_NAME_CHARS or "|" in name or name.casefold() == INBOX.casefold():
        raise SelectionError(f"invalid folder name {value!r}")
    if any(ord(c) < 32 for c in name):
        raise SelectionError(f"invalid folder name {value!r}")
    return name


def chosen_snapshots(messages, wanted):
    if wanted is None:
        return [*messages]
    known = {m["id"] for m in messages}
    ids = {int(i) for i in wanted}
    if not ids <= known:
        raise SelectionError("selection names messages outside the proposal")
    return [m for m in messages if m["id"] in ids]


def resolve_groups(groups, selection):
    picks = (selection or {}).get("groups")
    if picks is not None and len(picks) != len(groups):
        raise SelectionError("selection does not match the proposal groups")
    plan = []
    for i, group in enumerate(groups):
        pick = picks[i] if picks is not None else {}
        name = valid_name(pick.get("name") or group["dest"])
        plan.append((group, name, chosen_snapshots(group["messages"], pick.get("message_ids"))))
    return plan


def log_skips(conn, proposal_id, skipped):
    if skipped:
        record_action(
            conn, proposal_id, {"op": "skip", "messages": skipped}, ok=False, error="changed since the proposal"
        )


def move_items(conn, client, proposal_id, account, items, dest_imap):
    moved = 0
    for (src, validity), group in mailops.group_by_mailbox(items).items():
        by_uid = {i["uid"]: i for i in group}
        action = {"op": "move", "account": account, "src_imap": src, "src_uidvalidity": validity, "dest_imap": dest_imap}
        try:
            for batch, dest_validity, mapping in mailops.move_uids(client, src, dest_imap, sorted(by_uid)):
                entries = [
                    {"id": by_uid[u]["id"], "msgid": by_uid[u]["msgid"], "uid": u, "dest_uid": mapping.get(u)}
                    for u in batch
                ]
                undo = {
                    "op": "move_back",
                    "account": account,
                    "src_imap": src,
                    "dest_imap": dest_imap,
                    "dest_uidvalidity": dest_validity,
                    "items": entries,
                }
                record_action(
                    conn, proposal_id, {**action, "dest_uidvalidity": dest_validity, "items": entries}, undo
                )
                moved += len(batch)
        except mailops.PartialMove as e:
            record_action(
                conn,
                proposal_id,
                {**action, "dest_uidvalidity": e.validity, "copied": {str(k): v for k, v in e.mapping.items()}},
                ok=False,
                error=str(e),
            )
            raise
    return moved


def relabel_items(conn, client, proposal_id, items, add, remove):
    changed = 0
    for (added, removed), group in mailops.relabel_plan(items, add, remove).items():
        if not added and not removed:
            continue
        for (imap_name, validity), part in mailops.group_by_mailbox(group).items():
            uids = sorted(i["uid"] for i in part)
            mailops.store_labels(client, imap_name, uids, added, removed)
            record = {
                "op": "gmail_labels",
                "imap_name": imap_name,
                "uidvalidity": validity,
                "uids": uids,
                "ids": [i["id"] for i in part],
            }
            undo = {**record, "add": [*removed], "remove": [*added]}
            record_action(conn, proposal_id, {**record, "add": [*added], "remove": [*removed]}, undo)
            changed += len(part)
    return changed


def trash_items(conn, client, proposal_id, account, items):
    trash = mailops.trash_mailbox(client, account)
    if account != GMAIL:
        return move_items(conn, client, proposal_id, account, items, trash)
    trashed = 0
    for (imap_name, validity), part in mailops.group_by_mailbox(items).items():
        by_uid = {i["uid"]: i for i in part}
        client.select_rw(imap_name)
        for batch in mailops.chunks(sorted(by_uid)):
            copied = mailops.copy_uids(client, trash, batch)
            dest_validity, mapping = copied if copied else (None, {})
            entries = [{"id": by_uid[u]["id"], "uid": u, "dest_uid": mapping.get(u)} for u in batch]
            known = [e["dest_uid"] for e in entries if e["dest_uid"] is not None]
            undo = None
            if known and len(known) == len(entries):
                undo = {"op": "gmail_untrash", "trash_imap": trash, "dest_uidvalidity": dest_validity, "uids": known}
            record_action(
                conn,
                proposal_id,
                {"op": "trash", "imap_name": imap_name, "uidvalidity": validity, "dest_imap": trash, "items": entries},
                undo,
            )
            trashed += len(batch)
    return trashed


def execute_moves(conn, cfg, proposal, selection):
    payload, account, pid = proposal["payload"], proposal["account"], proposal["id"]
    plan = resolve_groups(payload["groups"], selection)
    moved, skipped, dests = 0, [], []
    with mailops.session(cfg, account) as client:
        for _, name, snaps in plan:
            if not snaps:
                continue
            items, skips = mailops.verify(conn, client, account, snaps)
            skipped.extend(skips)
            log_skips(conn, pid, skips)
            if not items:
                continue
            if account == GMAIL:
                moved += relabel_items(conn, client, pid, items, [name], [payload["folder"]])
            else:
                dest = utf7_encode(name)
                if mailops.ensure_folder(client, dest):
                    record_action(
                        conn,
                        pid,
                        {"op": "create_folder", "imap_name": dest},
                        {"op": "delete_folder", "account": account, "imap_name": dest},
                    )
                moved += move_items(conn, client, pid, account, items, dest)
            dests.append(name)
    return {
        "summary": f"{moved} moved, {len(skipped)} skipped as changed",
        "moved": moved,
        "skipped": skipped,
        "destinations": dests,
    }


def execute_deletion(conn, cfg, proposal, selection):
    payload, account, pid = proposal["payload"], proposal["account"], proposal["id"]
    snaps = chosen_snapshots(payload["messages"], (selection or {}).get("message_ids"))
    rule = payload["rule"]
    created = False
    if not conn.execute("SELECT 1 FROM rules WHERE id = ?", (rule["id"],)).fetchone():
        ruleedit.create_rule(conn, rule)
        created = True
        record_action(conn, pid, {"op": "rule_create", "id": rule["id"]}, {"op": "rule_delete", "id": rule["id"]})
    trashed, skipped = 0, []
    if snaps:
        with mailops.session(cfg, account) as client:
            items, skipped = mailops.verify(conn, client, account, snaps)
            log_skips(conn, pid, skipped)
            if items:
                trashed = trash_items(conn, client, pid, account, items)
    return {
        "summary": f"rule {'created' if created else 'already present'}, {trashed} moved to Trash, "
        f"{len(skipped)} skipped as changed",
        "rule": rule["id"],
        "trashed": trashed,
        "skipped": skipped,
    }


def execute_label_merge(conn, cfg, proposal, selection):
    payload, account, pid = proposal["payload"], proposal["account"], proposal["id"]
    label, into = payload["label"], payload["into"]
    snaps = chosen_snapshots(payload["messages"], (selection or {}).get("message_ids"))
    with mailops.session(cfg, account) as client:
        items, skipped = mailops.verify(conn, client, account, snaps)
        log_skips(conn, pid, skipped)
        moved = relabel_items(conn, client, pid, items, [into], [label])
        remaining = client.status(utf7_encode(label))["messages"]
        deleted = False
        if remaining == 0:
            deleted = mailops.delete_folder_if_empty(client, utf7_encode(label))
            if deleted:
                record_action(conn, pid, {"op": "delete_label", "imap_name": utf7_encode(label)})
    return {
        "summary": f"{moved} relabelled, label {'deleted' if deleted else 'kept'}, {len(skipped)} skipped as changed",
        "relabelled": moved,
        "label_deleted": deleted,
        "skipped": skipped,
    }


def undo_move_back(conn, cfg, proposal, undo):
    account, dest = undo["account"], undo["dest_imap"]
    with mailops.session(cfg, account) as client:
        if undo["dest_uidvalidity"] is not None and client.status(dest)["uidvalidity"] != undo["dest_uidvalidity"]:
            raise mailops.MailOpError("destination UIDVALIDITY changed")
        client.select(dest)
        uids, missing = [], 0
        for entry in undo["items"]:
            uid = entry["dest_uid"]
            if uid is None:
                found = client.uid_search("HEADER", "Message-ID", entry["msgid"]) if entry["msgid"] else []
                uid = found[0] if len(found) == 1 else None
            if uid is None:
                missing += 1
            else:
                uids.append(uid)
        present = set()
        for chunk in mailops.chunks(uids):
            present.update(client.uid_search("UID", mailops.uid_set(chunk)))
        usable = sorted(u for u in uids if u in present)
        missing += len(uids) - len(usable)
        if not usable:
            raise mailops.MailOpError("no message left in the destination to move back")
        restored = sum(len(batch) for batch, _, _ in mailops.move_uids(client, dest, undo["src_imap"], usable))
    return {"restored": restored, "missing": missing}


def undo_delete_folder(conn, cfg, proposal, undo):
    with mailops.session(cfg, undo["account"]) as client:
        return {"deleted": mailops.delete_folder_if_empty(client, undo["imap_name"])}


def undo_gmail_labels(conn, cfg, proposal, undo):
    with mailops.session(cfg, GMAIL) as client:
        if client.status(undo["imap_name"])["uidvalidity"] != undo["uidvalidity"]:
            raise mailops.MailOpError("UIDVALIDITY changed")
        mailops.store_labels(client, undo["imap_name"], undo["uids"], undo["add"], undo["remove"])
    return {"messages": len(undo["uids"])}


def undo_gmail_untrash(conn, cfg, proposal, undo):
    with mailops.session(cfg, GMAIL) as client:
        trash = undo["trash_imap"]
        if undo["dest_uidvalidity"] is not None and client.status(trash)["uidvalidity"] != undo["dest_uidvalidity"]:
            raise mailops.MailOpError("Trash UIDVALIDITY changed")
        mailops.store_labels(client, trash, undo["uids"], remove=[mailops.TRASH_LABEL])
    return {"messages": len(undo["uids"])}


def undo_rule_delete(conn, cfg, proposal, undo):
    return {"deleted": ruleedit.delete_rule(conn, undo["id"])}


def sync_after(conn, cfg, account):
    with db.exclusive(cfg.db_path):
        run.sync_one(conn, cfg, account, opener=mailops.open_client)


def register():
    proposals.register_executor(REBALANCE_SPLIT, execute_moves)
    proposals.register_executor(REBALANCE_MERGE, execute_moves)
    proposals.register_executor(DELETION_RULE, execute_deletion)
    proposals.register_executor(GMAIL_LABEL_MERGE, execute_label_merge)
    proposals.register_undoer("move_back", undo_move_back)
    proposals.register_undoer("delete_folder", undo_delete_folder)
    proposals.register_undoer("gmail_labels", undo_gmail_labels)
    proposals.register_undoer("gmail_untrash", undo_gmail_untrash)
    proposals.register_undoer("rule_delete", undo_rule_delete)
    proposals.set_sync_hook(sync_after)
