import json

from atrium import sync
from atrium.config import GMAIL_ALL_MAIL
from fake_imap import FakeBox, FakeClient, raw
from helpers import make_config, memory_db


def setup(tmp_path, boxes, flags=None, **client_kw):
    conn = memory_db()
    conn.execute("DELETE FROM mailboxes")
    return conn, make_config(tmp_path), FakeClient(boxes, flags, **client_kw)


def icloud_boxes():
    return {
        "INBOX": FakeBox(),
        "Bills": FakeBox(),
        "Misc": FakeBox(),
        "Deleted Messages": FakeBox(),
        "Sent Messages": FakeBox(),
    }


FLAGS = {"Deleted Messages": ("\\Trash",), "Sent Messages": ("\\Sent",)}


def rows(conn, where="1=1"):
    return conn.execute(f"SELECT * FROM messages WHERE {where} ORDER BY id").fetchall()


def test_roles():
    assert sync.mailbox_role("INBOX") == "inbox"
    assert sync.mailbox_role("Misc") == "misc"
    assert sync.mailbox_role("Bills/Utilities") == "filed"
    assert sync.mailbox_role("Whatever", ("\\Trash",)) == "trash"
    assert sync.mailbox_role("Junk") == "junk"
    assert sync.mailbox_role("[Gmail]/All Mail", (), "gmail") == "allmail"
    assert sync.mailbox_role("INBOX", (), "gmail") == "system"


def test_message_state_gmail_leaf_and_inbox():
    state = sync.message_state("gmail", "allmail", "x", ["\\Seen"], ["\\Inbox", "A", "A/B"])
    assert (state["in_inbox"], state["seen"], state["folder"], state["location"]) == (1, 1, "A/B", "A/B")
    multi = sync.message_state("gmail", "allmail", "x", [], ["A", "C"])
    assert multi["folder"] is None and multi["location"] == "A|C" and multi["seen"] == 0
    sent = sync.message_state("gmail", "allmail", "x", [], ["\\Sent", "A"])
    assert sent["sent"] == 1


def test_first_sync_skips_system_mailboxes_and_stores_raw(tmp_path):
    boxes = icloud_boxes()
    boxes["INBOX"].add(*raw("one"), flags=["\\Seen"])
    boxes["Bills"].add(*raw("two", sender="b@bills.test"))
    boxes["Sent Messages"].add(*raw("sent"))
    conn, cfg, client = setup(tmp_path, boxes, FLAGS)
    stats = sync.sync_account(conn, cfg, "icloud", client)
    assert stats.new == 2 and stats.mailboxes == 3
    names = {r["name"] for r in conn.execute("SELECT name FROM mailboxes")}
    assert names == {"INBOX", "Bills", "Misc"}
    inbox_row, bills_row = sorted(rows(conn), key=lambda r: r["location"])[::-1][0], rows(conn, "location = 'Bills'")[0]
    assert rows(conn, "location = 'INBOX'")[0]["in_inbox"] == 1
    assert bills_row["folder"] == "Bills" and bills_row["in_inbox"] == 0
    assert bills_row["from_domain"] == "bills.test"
    assert (cfg.cache_dir / bills_row["raw_path"]).read_bytes().startswith(b"From: b@bills.test")
    assert conn.execute("SELECT COUNT(*) FROM message_fts").fetchone()[0] == 2
    mbx = conn.execute("SELECT * FROM mailboxes WHERE name = 'INBOX'").fetchone()
    assert (mbx["uidvalidity"], mbx["highest_uid"], mbx["highestmodseq"]) == (1, 1, boxes["INBOX"].modseq)
    assert inbox_row is not None


def test_inbox_is_synced_last(tmp_path):
    conn, cfg, client = setup(tmp_path, icloud_boxes(), FLAGS)
    order = []
    original = client.select
    client.select = lambda name: (order.append(name), original(name))[1]
    sync.sync_account(conn, cfg, "icloud", client)
    assert order[-1] == "INBOX"


def test_incremental_sync_fetches_only_new_and_flag_deltas(tmp_path):
    boxes = icloud_boxes()
    u1 = boxes["INBOX"].add(*raw("one"))
    conn, cfg, client = setup(tmp_path, boxes, FLAGS)
    sync.sync_account(conn, cfg, "icloud", client)
    client.fetches.clear()
    assert sync.sync_account(conn, cfg, "icloud", client).new == 0
    assert client.fetches == []
    boxes["INBOX"].add(*raw("two"))
    boxes["INBOX"].set_flags(u1, flags=["\\Seen"])
    stats = sync.sync_account(conn, cfg, "icloud", client)
    assert stats.new == 1 and stats.flag_changes == 1
    changed = [f for f in client.fetches if f[2] is not None]
    assert changed and changed[0][2] == 2
    assert rows(conn, "uid = 1 AND location = 'INBOX'")[0]["seen"] == 1
    event = conn.execute("SELECT * FROM events WHERE kind = 'flags'").fetchone()
    assert json.loads(event["detail"])["flags"] == [[], ["\\Seen"]]


def test_no_condstore_falls_back_to_full_flag_scan(tmp_path):
    boxes = icloud_boxes()
    u1 = boxes["INBOX"].add(*raw("one"))
    conn, cfg, client = setup(tmp_path, boxes, FLAGS, capabilities=("IDLE",))
    sync.sync_account(conn, cfg, "icloud", client)
    boxes["INBOX"].set_flags(u1, flags=["\\Flagged"])
    client.fetches.clear()
    assert sync.sync_account(conn, cfg, "icloud", client).flag_changes == 1
    assert client.fetches[0][2] is None


def test_move_between_mailboxes_recorded_as_event(tmp_path):
    boxes = icloud_boxes()
    header, text = raw("move me")
    u1 = boxes["INBOX"].add(header, text, flags=["\\Seen"])
    conn, cfg, client = setup(tmp_path, boxes, FLAGS)
    sync.sync_account(conn, cfg, "icloud", client)
    boxes["INBOX"].remove(u1)
    boxes["Bills"].add(header, text, flags=["\\Seen"])
    stats = sync.sync_account(conn, cfg, "icloud", client)
    assert len(stats.gone_ids) == 1
    [event] = conn.execute("SELECT * FROM events WHERE kind = 'move'").fetchall()
    assert json.loads(event["detail"])["from"] == "INBOX" and json.loads(event["detail"])["to"] == "Bills"
    gone = rows(conn, "gone_at IS NOT NULL")
    assert len(gone) == 1 and gone[0]["location"] == "INBOX"


def test_disappearance_without_counterpart_is_delete(tmp_path):
    boxes = icloud_boxes()
    u1 = boxes["Bills"].add(*raw("bye"))
    conn, cfg, client = setup(tmp_path, boxes, FLAGS)
    sync.sync_account(conn, cfg, "icloud", client)
    boxes["Bills"].remove(u1)
    sync.sync_account(conn, cfg, "icloud", client)
    assert conn.execute("SELECT COUNT(*) FROM events WHERE kind = 'delete'").fetchone()[0] == 1


def test_uidvalidity_change_invalidates_without_events(tmp_path):
    boxes = icloud_boxes()
    boxes["Bills"].add(*raw("a"))
    conn, cfg, client = setup(tmp_path, boxes, FLAGS)
    sync.sync_account(conn, cfg, "icloud", client)
    boxes["Bills"].validity = 2
    sync.sync_account(conn, cfg, "icloud", client)
    assert conn.execute("SELECT COUNT(*) FROM events").fetchone()[0] == 0
    live = rows(conn, "gone_at IS NULL")
    assert len(live) == 1 and live[0]["uidvalidity"] == 2
    assert len(rows(conn)) == 2


def test_gmail_labels_categories_and_label_events(tmp_path):
    box = FakeBox()
    u1 = box.add(*raw("n1"), flags=["\\Seen"], labels=["\\Inbox", "Personal", "Personal/Friends"])
    u2 = box.add(*raw("n2"), flags=["\\Seen"], labels=["\\Inbox"])
    conn, cfg, client = setup(tmp_path, {GMAIL_ALL_MAIL: box, "INBOX": FakeBox(), "[Gmail]/Sent Mail": FakeBox()})
    client.category_uids = {"updates": [u1], "promotions": [u2]}
    sync.sync_account(conn, cfg, "gmail", client)
    first = rows(conn, f"uid = {u1}")[0]
    assert (first["folder"], first["in_inbox"], first["category"], first["gm_msgid"]) == ("Personal/Friends", 1, "updates", f"g{u1}")
    assert rows(conn, f"uid = {u2}")[0]["folder"] is None
    assert {r["name"] for r in conn.execute("SELECT name FROM mailboxes")} == {GMAIL_ALL_MAIL}
    box.set_flags(u2, labels=["Alerts/Porkbun"])
    sync.sync_account(conn, cfg, "gmail", client)
    second = rows(conn, f"uid = {u2}")[0]
    assert (second["folder"], second["in_inbox"]) == ("Alerts/Porkbun", 0)
    assert conn.execute("SELECT COUNT(*) FROM events WHERE kind = 'labels'").fetchone()[0] == 1


def test_exclusive_serializes_concurrent_passes(tmp_path):
    import threading

    from atrium import db

    path = tmp_path / "atrium.db"
    entered = threading.Event()

    def second():
        with db.exclusive(path):
            entered.set()

    with db.exclusive(path):
        worker = threading.Thread(target=second)
        worker.start()
        assert not entered.wait(0.3)
    worker.join(5)
    assert entered.is_set()
