import json

import pytest

from atrium import executors, mailops, proposals, safety, scans, sync
from atrium.imap import quote_name, utf7_encode
from fake_imap import FakeBox, raw
from fake_imap_write import WriteClient
from helpers import make_config, memory_db

FLAGS = {"Deleted Messages": ("\\Trash",)}
GMAIL_FLAGS = {"[Gmail]/Trash": ("\\Trash",)}


def icloud_world(tmp_path, monkeypatch, count=4, capabilities=("IDLE", "CONDSTORE", "UIDPLUS")):
    boxes = {"INBOX": FakeBox(), "Bills": FakeBox(validity=3), "Deleted Messages": FakeBox(validity=9)}
    for i in range(count):
        boxes["Bills"].add(*raw(f"invoice {i}", sender="shop@bills.test", msgid=f"m{i}"))
    client = WriteClient(boxes, FLAGS, capabilities)
    return world(tmp_path, monkeypatch, "icloud", client)


def gmail_world(tmp_path, monkeypatch, labelled):
    boxes = {"[Gmail]/All Mail": FakeBox(), "[Gmail]/Trash": FakeBox(validity=5)}
    for i, labels in enumerate(labelled):
        boxes["[Gmail]/All Mail"].add(*raw(f"note {i}", sender="a@gm.test", msgid=f"g{i}"), labels=labels)
    client = WriteClient(boxes, GMAIL_FLAGS, gmail=True)
    return world(tmp_path, monkeypatch, "gmail", client)


def world(tmp_path, monkeypatch, account, client):
    conn = memory_db()
    conn.execute("DELETE FROM mailboxes")
    scans.init_schemas(conn)
    cfg = make_config(tmp_path)
    monkeypatch.setattr(mailops, "open_client", lambda c, a: client)
    sync.sync_account(conn, cfg, account, client)
    executors.register()
    return conn, cfg, client


def make_proposal(conn, kind, account, payload, preview=None, fingerprint="fp"):
    return proposals.create(conn, kind, account, "title", payload, preview or {}, fingerprint)


def message_ids(conn, mailbox):
    return [
        r["id"]
        for r in conn.execute(
            "SELECT m.id FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id "
            "WHERE b.name = ? AND m.gone_at IS NULL ORDER BY m.id",
            (mailbox,),
        )
    ]


def split_payload(conn, ids, dest="Bills/Shop"):
    return {"folder": "Bills", "groups": [{"dest": dest, "messages": mailops.snapshots(conn, ids)}]}


def test_split_moves_with_uid_expunge_creates_folder_and_logs_undo(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    assert cfg.mode == "shadow"
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["status"] == "applied" and done["result"]["moved"] == 4
    assert [w[0] for w in client.writes][:2] == ["CREATE", "SUBSCRIBE"]
    verbs = [w[0] for w in client.writes[2:]]
    assert verbs == ["COPY", "STORE", "EXPUNGE"]
    store = client.writes[3]
    assert store[3:] == ("+FLAGS.SILENT", "(\\Deleted)")
    assert client.writes[4][2] == store[2]
    assert not client.boxes["Bills"].messages and len(client.boxes["Bills/Shop"].messages) == 4
    logged = proposals.actions(conn, pid)
    move = next(a for a in logged if a["action"]["op"] == "move")
    assert move["undo"]["op"] == "move_back" and move["undo"]["dest_uidvalidity"] == 7
    assert all(i["dest_uid"] is not None and i["msgid"] for i in move["undo"]["items"])
    assert any(a["action"]["op"] == "create_folder" and a["undo"] for a in logged)
    assert conn.execute("SELECT COUNT(*) FROM messages m JOIN mailboxes b ON b.id = m.mailbox_id "
                        "WHERE b.name = 'Bills/Shop' AND m.gone_at IS NULL").fetchone()[0] == 4


def test_gate_still_refuses_outside_approved_writes(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    monkeypatch.setenv("ATRIUM_MODE", "shadow")
    with pytest.raises(safety.WriteRefused):
        mailops.ensure_folder(client, "Nope")
    with safety.approved_writes(1):
        mailops.ensure_folder(client, "Nope")
    with pytest.raises(safety.WriteRefused):
        client.select_rw("Bills")


def test_undo_restores_messages_and_removes_created_folder(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    proposals.approve_and_apply(conn, cfg, pid)
    assert proposals.get(conn, pid)["undoable"]
    outcome = proposals.undo(conn, cfg, pid)
    assert outcome["undone"] and all(o["ok"] for o in outcome["outcomes"])
    assert len(client.boxes["Bills"].messages) == 4
    assert "Bills/Shop" in client.deleted
    assert not proposals.get(conn, pid)["undoable"]
    assert len(message_ids(conn, "Bills")) == 4


def test_changed_messages_are_skipped_by_snapshot_verification(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    conn.execute("UPDATE messages SET digest = 'other' WHERE id = ?", (ids[0],))
    conn.execute("UPDATE messages SET gone_at = 1 WHERE id = ?", (ids[1],))
    client.boxes["Bills"].messages[conn.execute("SELECT uid FROM messages WHERE id = ?", (ids[2],)).fetchone()[0]][
        "header"
    ] = raw("x", msgid="different")[0]
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["result"]["moved"] == 1
    assert sorted(s["reason"] for s in done["result"]["skipped"]) == ["changed", "gone", "msgid"]
    assert len(client.boxes["Bills"].messages) == 3
    assert any(not a["ok"] and a["action"]["op"] == "skip" for a in proposals.actions(conn, pid))


def test_uidvalidity_change_skips_everything(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    client.boxes["Bills"].validity = 99
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["result"]["moved"] == 0 and {s["reason"] for s in done["result"]["skipped"]} == {"uidvalidity"}
    assert not any(w[0] == "COPY" for w in client.writes)


def test_move_refused_without_uidplus(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch, capabilities=("IDLE",))
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids, "Bills"))
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["status"] == "failed" and "UIDPLUS" in done["result"]["error"]
    assert not any(w[0] in ("COPY", "STORE", "EXPUNGE") for w in client.writes)


def test_selection_subset_and_rename_and_invalid(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    with pytest.raises(proposals.SelectionError):
        proposals.approve_and_apply(conn, cfg, pid, {"groups": [{"name": "INBOX"}]})
    with pytest.raises(proposals.SelectionError):
        proposals.approve_and_apply(conn, cfg, pid, {"groups": [{"message_ids": [10_000]}]})
    assert proposals.get(conn, pid)["status"] == "pending" and not client.writes
    done = proposals.approve_and_apply(conn, cfg, pid, {"groups": [{"name": "Bills/Renamed", "message_ids": ids[:2]}]})
    assert done["result"]["moved"] == 2 and len(client.boxes["Bills/Renamed"].messages) == 2
    assert len(client.boxes["Bills"].messages) == 2


def test_moves_are_batched_by_hundred():
    box = FakeBox()
    for i in range(250):
        box.add(*raw(f"s{i}", msgid=f"b{i}"))
    client = WriteClient({"A": box, "B": FakeBox()})
    with safety.approved_writes(1):
        batches = list(mailops.move_uids(client, "A", "B", sorted(box.messages)))
    assert [len(b) for b, _, _ in batches] == [100, 100, 50]
    assert sum(1 for w in client.writes if w[0] == "COPY") == 3
    assert not box.messages and len(client.boxes["B"].messages) == 250


def test_trash_discovery():
    client = WriteClient({"Deleted Messages": FakeBox(), "X": FakeBox()}, {})
    assert mailops.trash_mailbox(client, "icloud") == "Deleted Messages"
    flagged = WriteClient({"Bin2": FakeBox()}, {"Bin2": ("\\Trash",)})
    assert mailops.trash_mailbox(flagged, "gmail") == "Bin2"
    with pytest.raises(mailops.MailOpError):
        mailops.trash_mailbox(WriteClient({"Other": FakeBox()}, {}), "gmail")


def test_label_merge_relabels_deletes_empty_label_and_undo_readds(tmp_path, monkeypatch):
    conn, cfg, client = gmail_world(tmp_path, monkeypatch, [["Parent/Small"], ["Parent/Small", "Other"], ["Parent"]])
    ids = [r["id"] for r in conn.execute("SELECT id FROM messages WHERE labels LIKE '%Parent/Small%' ORDER BY id")]
    payload = {"label": "Parent/Small", "into": "Parent", "messages": mailops.snapshots(conn, ids)}
    pid = make_proposal(conn, "gmail_label_merge", "gmail", payload)
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["result"]["relabelled"] == 2 and done["result"]["label_deleted"]
    assert client.deleted == [utf7_encode("Parent/Small")]
    store = [w for w in client.writes if w[0] == "STORE"]
    assert {w[3] for w in store} == {"+X-GM-LABELS", "-X-GM-LABELS"}
    assert all(quote_name("Parent") in w[4] or quote_name("Parent/Small") in w[4] for w in store)
    labels = [sorted(m["labels"]) for m in client.boxes["[Gmail]/All Mail"].messages.values()]
    assert labels == [["Parent"], ["Other", "Parent"], ["Parent"]]
    outcome = proposals.undo(conn, cfg, pid)
    assert outcome["undone"]
    labels = [sorted(m["labels"]) for m in client.boxes["[Gmail]/All Mail"].messages.values()]
    assert labels == [["Parent/Small"], ["Other", "Parent/Small"], ["Parent"]]


def test_label_kept_when_not_empty_after_relabel(tmp_path, monkeypatch):
    conn, cfg, client = gmail_world(tmp_path, monkeypatch, [["Parent/Small"], ["Parent/Small"]])
    ids = [r["id"] for r in conn.execute("SELECT id FROM messages ORDER BY id")]
    payload = {"label": "Parent/Small", "into": "Parent", "messages": mailops.snapshots(conn, ids)}
    pid = make_proposal(conn, "gmail_label_merge", "gmail", payload)
    conn.execute("UPDATE messages SET digest = 'other' WHERE id = ?", (ids[0],))
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["result"]["relabelled"] == 1 and not done["result"]["label_deleted"]
    assert client.deleted == []


def test_gmail_archive_removes_inbox_label():
    box = FakeBox()
    uid = box.add(*raw("a"), labels=["\\Inbox", "X"])
    client = WriteClient({"[Gmail]/All Mail": box}, gmail=True)
    with safety.approved_writes(1):
        mailops.archive_uids(client, "[Gmail]/All Mail", [uid])
    assert box.messages[uid]["labels"] == ["X"]
    assert client.writes[0][3:] == ("-X-GM-LABELS", "(\\Inbox)")


def deletion_payload(conn, ids):
    rule = {
        "id": "del-test",
        "account": "icloud",
        "action": "delete",
        "when": [{"field": "from_addr", "op": "eq", "value": "shop@bills.test"}],
    }
    return {"rule": rule, "messages": mailops.snapshots(conn, ids)}


def test_deletion_rule_creates_rule_trashes_backlog_and_undo(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "deletion_rule", "icloud", deletion_payload(conn, ids))
    done = proposals.approve_and_apply(conn, cfg, pid, {"message_ids": ids[:3]})
    assert done["result"]["trashed"] == 3
    assert conn.execute("SELECT action FROM rules WHERE id = 'del-test'").fetchone()[0] == "delete"
    assert len(client.boxes["Deleted Messages"].messages) == 3 and len(client.boxes["Bills"].messages) == 1
    outcome = proposals.undo(conn, cfg, pid)
    assert outcome["undone"] and len(client.boxes["Bills"].messages) == 4
    assert conn.execute("SELECT COUNT(*) FROM rules").fetchone()[0] == 0


def test_gmail_delete_copies_to_trash_and_undo_removes_trash_label(tmp_path, monkeypatch):
    conn, cfg, client = gmail_world(tmp_path, monkeypatch, [["X"], ["X"]])
    ids = [r["id"] for r in conn.execute("SELECT id FROM messages ORDER BY id")]
    payload = deletion_payload(conn, ids)
    payload["rule"]["account"] = "gmail"
    pid = make_proposal(conn, "deletion_rule", "gmail", payload)
    done = proposals.approve_and_apply(conn, cfg, pid)
    assert done["result"]["trashed"] == 2
    assert len(client.boxes["[Gmail]/Trash"].messages) == 2
    assert proposals.undo(conn, cfg, pid)["undone"]
    assert client.writes[-1][3:] == ("-X-GM-LABELS", "(\\Trash)")


def test_failed_apply_marks_failed_and_can_reapply(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    client.capabilities = frozenset()
    assert proposals.approve_and_apply(conn, cfg, pid)["status"] == "failed"
    client.capabilities = frozenset({"UIDPLUS"})
    assert proposals.approve_and_apply(conn, cfg, pid)["status"] == "applied"


def test_payload_survives_json_roundtrip(tmp_path, monkeypatch):
    conn, cfg, client = icloud_world(tmp_path, monkeypatch)
    ids = message_ids(conn, "Bills")
    pid = make_proposal(conn, "rebalance_split", "icloud", split_payload(conn, ids))
    stored = json.loads(conn.execute("SELECT payload FROM proposals WHERE id = ?", (pid,)).fetchone()[0])
    assert stored["groups"][0]["messages"][0]["imap_name"] == "Bills"
