import json
import logging
import time

import pytest

from atrium import applier, backlog, cli, corrections, db, executors, live, mailops, proposals, rules, run, safety, sync
from atrium.config import GMAIL, ICLOUD
from fake_imap import FakeBox, raw
from fake_imap_write import WriteClient
from helpers import make_config, memory_db, prepare

ICLOUD_FLAGS = {"Deleted Messages": ("\\Trash",), "Sent Messages": ("\\Sent",)}
GMAIL_FLAGS = {"[Gmail]/Trash": ("\\Trash",)}
ALL_MAIL = "[Gmail]/All Mail"
SEEN = "\\Seen"
FLAGGED = "\\Flagged"
INBOX_LABEL = "\\Inbox"


class Crash(BaseException):
    pass


class World:
    def __init__(self, tmp_path, monkeypatch, account, client, mode="live", file_db=False):
        monkeypatch.setenv("ATRIUM_MODE", mode)
        self.account = account
        self.client = client
        static = tmp_path / "static"
        (static / "basecoat").mkdir(parents=True, exist_ok=True)
        (static / "app.css").write_text("body{}")
        self.cfg = make_config(tmp_path, mode=mode, static_dir=static)
        if file_db:
            self.conn = prepare(db.connect(self.cfg.db_path))
        else:
            self.conn = memory_db()
        self.conn.execute("DELETE FROM mailboxes")
        self.opener = lambda cfg, name: client
        monkeypatch.setattr(mailops, "open_client", self.opener)
        executors.register()
        sync.sync_account(self.conn, self.cfg, account, client)
        live.ensure_golive(self.conn)
        self.count = 0

    @property
    def inbox_box(self):
        return self.client.boxes[ALL_MAIL if self.account == GMAIL else "INBOX"]

    def arrive(self, subject, flags=(SEEN,), labels=None, box=None, sender="a@shop.test", resync=True):
        target = self.client.boxes[box] if box else self.inbox_box
        if self.account == GMAIL and labels is None:
            labels = [INBOX_LABEL]
        uid = target.add(*raw(subject, sender=sender, msgid=subject), flags=flags, labels=labels)
        if resync:
            self.sync()
        return uid

    def sync(self):
        sync.sync_account(self.conn, self.cfg, self.account, self.client)

    def mid(self, subject):
        return self.conn.execute(
            "SELECT id FROM messages WHERE subject = ? AND gone_at IS NULL ORDER BY id DESC", (subject,)
        ).fetchone()["id"]

    def decide(self, subject, action="file", dest="Bills", stage="learned"):
        mid = self.mid(subject)
        self.conn.execute(
            "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "
            "VALUES (?, ?, 1, ?, 'k', ?, ?, 0.9, '{}')",
            (mid, self.account, stage, action, dest),
        )
        return mid

    def apply(self):
        return applier.apply_account(self.conn, self.cfg, self.account, self.opener)

    def actions(self):
        return [dict(r) for r in self.conn.execute("SELECT * FROM auto_actions ORDER BY id")]

    def verbs(self):
        return [w[0] for w in self.client.writes]


def icloud(tmp_path, monkeypatch, seed=(), mode="live", file_db=False):
    boxes = {
        "INBOX": FakeBox(),
        "Bills": FakeBox(validity=3),
        "Misc": FakeBox(validity=4),
        "Deleted Messages": FakeBox(validity=9),
        "Sent Messages": FakeBox(validity=5),
    }
    for subject, flags in seed:
        boxes["INBOX"].add(*raw(subject, msgid=subject), flags=flags)
    return World(tmp_path, monkeypatch, ICLOUD, WriteClient(boxes, ICLOUD_FLAGS), mode, file_db)


def gmail(tmp_path, monkeypatch, seed=(), mode="live"):
    boxes = {ALL_MAIL: FakeBox(), "[Gmail]/Trash": FakeBox(validity=5)}
    for subject, flags, labels in seed:
        boxes[ALL_MAIL].add(*raw(subject, msgid=subject), flags=flags, labels=labels)
    return World(tmp_path, monkeypatch, GMAIL, WriteClient(boxes, GMAIL_FLAGS, gmail=True), mode)


def uids(box):
    return sorted(box.messages)


def test_eligibility_and_icloud_move_with_undo_data(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("read")
    w.arrive("unread", flags=())
    w.arrive("flagged", flags=(SEEN, FLAGGED))
    w.arrive("undecided")
    for subject in ("read", "unread", "flagged"):
        w.decide(subject)
    out = w.apply()
    assert out.written == 1 and out.skipped == 0
    assert uids(w.client.boxes["INBOX"]) == [2, 3, 4]
    assert uids(w.client.boxes["Bills"]) == [1]
    assert w.verbs() == ["COPY", "STORE", "EXPUNGE"]
    assert w.client.writes[2][2] == "1"
    (row,) = w.actions()
    assert (row["kind"], row["ok"], row["dest"], row["src"]) == ("file", 1, "Bills", "INBOX")
    after, undo = json.loads(row["after"]), json.loads(row["undo"])
    assert after == {"imap_name": "Bills", "uidvalidity": 3, "uid": 1}
    assert undo["op"] == "move_back" and undo["items"][0]["dest_uid"] == 1
    assert json.loads(row["before"])["uid"] == 1
    assert w.conn.execute("SELECT gone_at FROM messages WHERE id = ?", (w.mid("read"),)).fetchone() is not None
    assert w.apply().written == 0


def test_messages_eligible_before_go_live_are_not_filed(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch, seed=[("old", (SEEN,))])
    w.arrive("new")
    w.decide("old")
    w.decide("new")
    out = w.apply()
    assert out.written == 1
    assert uids(w.client.boxes["INBOX"]) == [1]
    assert uids(w.client.boxes["Bills"]) == [1]
    baseline = {r[0] for r in w.conn.execute("SELECT message_id FROM live_baseline")}
    assert baseline == {w.mid("old")}


def test_gmail_relabel_archives_and_uncertain_stays(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("filed")
    w.arrive("uncertain")
    w.decide("filed", dest="Receipts")
    w.decide("uncertain", action="none", dest=None, stage="fallback")
    out = w.apply()
    assert out.written == 1
    messages = w.client.boxes[ALL_MAIL].messages
    assert messages[1]["labels"] == ["Receipts"]
    assert messages[2]["labels"] == [INBOX_LABEL]
    assert w.verbs() == ["STORE", "STORE"]
    (row,) = w.actions()
    assert json.loads(row["undo"])["op"] == "gmail_labels"


def test_gmail_label_already_present_only_archives(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("twice", labels=[INBOX_LABEL, "Receipts"])
    w.decide("twice", dest="Receipts")
    assert w.apply().written == 1
    assert w.client.boxes[ALL_MAIL].messages[1]["labels"] == ["Receipts"]
    assert len(w.client.writes) == 1


def test_routing_time_delete_decision_trashes(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("junk")
    w.decide("junk", action="delete", dest=None, stage="operator")
    assert w.apply().written == 1
    assert uids(w.client.boxes["Deleted Messages"]) == [1]
    assert uids(w.client.boxes["INBOX"]) == []
    (row,) = w.actions()
    assert row["kind"] == "trash"


def test_contacts_flag_sets_flag_never_clears_and_undo_is_not_reapplied(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("friend", flags=())
    w.arrive("stranger", flags=())
    mid = w.mid("friend")
    for subject, match in (("friend", 1), ("stranger", 0)):
        w.conn.execute(
            "INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, 'icloud', 1, ?)",
            (w.mid(subject), match),
        )
    assert w.apply().written == 1
    assert FLAGGED in w.client.boxes["INBOX"].messages[1]["flags"]
    assert FLAGGED not in w.client.boxes["INBOX"].messages[2]["flags"]
    assert w.conn.execute("SELECT flagged FROM messages WHERE id = ?", (mid,)).fetchone()[0] == 1
    assert w.apply().written == 0
    (row,) = w.actions()
    assert row["kind"] == "flag"
    live.undo(w.conn, w.cfg, row["id"])
    assert FLAGGED not in w.client.boxes["INBOX"].messages[1]["flags"]
    w.sync()
    assert w.apply().written == 0


def test_flag_skipped_when_message_was_read_on_server(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("friend", flags=())
    w.conn.execute(
        "INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, 'icloud', 1, 1)",
        (w.mid("friend"),),
    )
    w.client.boxes["INBOX"].set_flags(1, flags=[SEEN])
    out = w.apply()
    assert out.written == 0 and out.skipped == 1
    assert w.client.writes == []


def delete_rule(conn, account=None):
    when = [{"field": "from_domain", "op": "eq", "value": "spam.test"}, {"field": "age_days", "op": "gte", "value": 1}]
    rule = {"id": "r-del", "action": "delete", "when": when}
    if account:
        rule["account"] = account
    rules.import_rules(conn, {"rules": [rule]})


def age(w, subject, ts):
    w.conn.execute("UPDATE messages SET date_ts = ? WHERE subject = ?", (ts, subject))


def test_deletion_pass_spans_folders_applies_age_and_skips_flagged(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("in-old-unread", flags=(), sender="x@spam.test")
    w.arrive("bills-old", box="Bills", sender="x@spam.test")
    w.arrive("bills-keep", box="Bills", sender="x@fine.test")
    w.arrive("fresh", sender="x@spam.test")
    w.arrive("old-flagged", flags=(SEEN, FLAGGED), sender="x@spam.test")
    w.arrive("sent-old", box="Sent Messages", sender="x@spam.test")
    for subject in ("in-old-unread", "bills-old", "old-flagged"):
        age(w, subject, 1.0)
    age(w, "fresh", time.time())
    delete_rule(w.conn)
    out = applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener)
    assert out.ran and out.by_kind == {"trash": 2}
    assert uids(w.client.boxes["Deleted Messages"]) == [1, 2]
    assert uids(w.client.boxes["Bills"]) == [2]
    assert uids(w.client.boxes["INBOX"]) == [2, 3]
    assert uids(w.client.boxes["Sent Messages"]) == [1]
    assert not applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener).ran


def test_deletion_candidates_exclude_system_roles(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    for i, role in enumerate(("trash", "sent", "drafts", "junk"), start=1):
        w.conn.execute(
            "INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, 'icloud', ?, ?, ?)",
            (100 + i, role, role, role),
        )
        w.conn.execute(
            "INSERT INTO messages (account, mailbox_id, uidvalidity, uid, digest, first_seen, from_domain, date_ts) "
            "VALUES ('icloud', ?, 1, 1, ?, 0, 'spam.test', 1)",
            (100 + i, f"sys{i}"),
        )
    delete_rule(w.conn)
    assert applier.deletion_intents(w.conn, ICLOUD, applier.Budget(100)) == []


def test_gmail_deletion_pass_copies_to_trash_and_undo_untrashes(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("doomed", labels=["Receipts"], sender="x@spam.test")
    age(w, "doomed", 1.0)
    delete_rule(w.conn, GMAIL)
    out = applier.deletion_pass(w.conn, w.cfg, GMAIL, w.opener)
    assert out.by_kind == {"trash": 1}
    assert w.verbs() == ["COPY"]
    (row,) = w.actions()
    assert json.loads(row["undo"])["op"] == "gmail_untrash"
    assert live.undo(w.conn, w.cfg, row["id"])["undone"]


def test_write_cap_defers_the_excess_and_warns(tmp_path, monkeypatch, caplog):
    monkeypatch.setattr(applier, "WRITE_CAP", 2)
    w = icloud(tmp_path, monkeypatch)
    for i in range(5):
        w.arrive(f"m{i}")
        w.decide(f"m{i}")
    with caplog.at_level(logging.WARNING, logger="atrium.applier"):
        assert w.apply().written == 2
    assert "write cap reached: deferring 3" in caplog.text
    assert len(w.client.boxes["Bills"].messages) == 2
    assert w.apply().written == 2


def test_deletion_pass_honours_the_cap(tmp_path, monkeypatch, caplog):
    monkeypatch.setattr(applier, "WRITE_CAP", 1)
    w = icloud(tmp_path, monkeypatch)
    for i in range(3):
        w.arrive(f"d{i}", sender="x@spam.test")
        age(w, f"d{i}", 1.0)
    delete_rule(w.conn)
    with caplog.at_level(logging.WARNING, logger="atrium.applier"):
        out = applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener)
    assert out.written == 1
    assert "deferring 2" in caplog.text


def test_pause_skips_writes_keeps_decisions_and_resume_applies(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("held")
    w.decide("held")
    live.set_paused(w.cfg, True)
    out = w.apply()
    assert not out.ran and w.client.writes == []
    assert w.conn.execute("SELECT COUNT(*) FROM decisions").fetchone()[0] == 1
    assert not applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener).ran
    live.set_paused(w.cfg, False)
    assert w.apply().written == 1


def test_shadow_mode_refuses_writes(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch, mode="shadow")
    w.arrive("quiet")
    w.decide("quiet")
    assert not w.apply().ran
    assert w.client.writes == []
    with pytest.raises(safety.WriteRefused):
        w.client.mutate("STORE", "1", "+FLAGS.SILENT", "(\\Flagged)")


def test_gate_refuses_automatic_writes_while_paused_but_not_undo(tmp_path, monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    pause = tmp_path / "pause"
    with safety.automatic_writes(pause):
        safety.write_gate("move")
        safety.set_paused(pause, True)
        with pytest.raises(safety.WriteRefused):
            safety.write_gate("move")
    with safety.approved_writes("undo-1"):
        safety.write_gate("move")


def test_undo_moves_back_and_is_never_refiled(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("oops")
    w.decide("oops")
    w.apply()
    (row,) = w.actions()
    outcome = live.undo(w.conn, w.cfg, row["id"])
    assert outcome["undone"]
    assert uids(w.client.boxes["Bills"]) == []
    assert len(w.client.boxes["INBOX"].messages) == 1
    assert w.actions()[0]["undone_ts"] is not None
    with pytest.raises(live.UndoError):
        live.undo(w.conn, w.cfg, row["id"])
    w.sync()
    mid = w.mid("oops")
    w.conn.execute(
        "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest) VALUES (?, 'icloud', 2, 'learned', 'k', 'file', 'Bills')",
        (mid,),
    )
    assert w.apply().written == 0


def test_gmail_undo_restores_inbox_label(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("g")
    w.decide("g", dest="Receipts")
    w.apply()
    (row,) = w.actions()
    live.undo(w.conn, w.cfg, row["id"])
    assert sorted(w.client.boxes[ALL_MAIL].messages[1]["labels"]) == [INBOX_LABEL]
    w.sync()
    assert w.apply().written == 0


def test_server_side_change_since_decision_is_skipped(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    for subject in ("flipped", "vanished"):
        w.arrive(subject)
        w.decide(subject)
    w.client.boxes["INBOX"].set_flags(1, flags=[])
    w.client.boxes["INBOX"].remove(2)
    out = w.apply()
    assert out.written == 0 and out.skipped == 2
    assert w.client.writes == []
    errors = sorted(a["error"] for a in w.actions())
    assert errors == ["skipped: missing", "skipped: unread"]
    assert all(a["ok"] == 0 for a in w.actions())


def test_crash_after_write_does_not_double_apply(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("crashy")
    w.decide("crashy")

    def boom(*args, **kwargs):
        raise Crash

    monkeypatch.setattr(live, "finish", boom)
    with pytest.raises(Crash):
        w.apply()
    monkeypatch.undo()
    monkeypatch.setenv("ATRIUM_MODE", "live")
    assert w.verbs() == ["COPY", "STORE", "EXPUNGE"]
    assert w.actions()[0]["ok"] is None
    w.client.boxes["INBOX"].add(*raw("crashy", msgid="crashy"), flags=[SEEN])
    w.client.writes.clear()
    assert w.apply().written == 0
    assert w.client.writes == []
    assert w.actions()[0]["error"] == live.INTERRUPTED


def test_partial_move_is_held_not_retried(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("half")
    w.decide("half")
    original = w.client.mutate

    def flaky(verb, *args):
        if verb == "EXPUNGE":
            raise RuntimeError("connection reset")
        return original(verb, *args)

    monkeypatch.setattr(w.client, "mutate", flaky)
    out = w.apply()
    assert out.failed == 1 and out.written == 0
    (row,) = w.actions()
    assert row["ok"] is None and "copied but not expunged" in row["error"]
    assert w.apply().written == 0
    assert sum(1 for v in w.verbs() if v == "COPY") == 1


def test_failed_attempts_are_limited(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("stubborn")
    w.decide("stubborn", dest="Nowhere")
    for _ in range(applier.WRITE_ATTEMPTS + 2):
        w.apply()
    assert len(w.actions()) == applier.WRITE_ATTEMPTS
    assert all(a["error"] == "skipped: no such folder" for a in w.actions())


def test_correction_moves_icloud_message_even_when_unread_and_undo_clears_it(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("c1", flags=())
    mid = w.mid("c1")
    corrections.record_correction(w.conn, mid, "Bills")
    out = applier.apply_corrections(w.conn, w.cfg, ICLOUD, w.opener, [mid])
    assert out.written == 1
    assert uids(w.client.boxes["Bills"]) == [1]
    (row,) = w.actions()
    assert row["kind"] == "correct"
    live.undo(w.conn, w.cfg, row["id"])
    assert corrections.get_correction(w.conn, mid) is None
    assert uids(w.client.boxes["Bills"]) == []


def test_correction_to_unknown_folder_is_skipped(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("c2")
    mid = w.mid("c2")
    corrections.record_correction(w.conn, mid, "Nowhere")
    out = w.apply()
    assert out.written == 0 and out.skipped == 1
    assert w.client.writes == []


def test_correction_replaces_atriums_gmail_label_and_stays_archived(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("g2", labels=["Receipts"])
    mid = w.mid("g2")
    digest = w.conn.execute("SELECT digest FROM messages WHERE id = ?", (mid,)).fetchone()["digest"]
    prior = live.begin(w.conn, GMAIL, mid, digest, live.FILE, ALL_MAIL, "Receipts", {})
    live.finish(w.conn, prior)
    corrections.record_correction(w.conn, mid, "Taxes")
    out = w.apply()
    assert out.written == 1
    assert w.client.boxes[ALL_MAIL].messages[1]["labels"] == ["Taxes"]
    w.sync()
    assert w.apply().written == 0


def test_backlog_proposal_groups_selects_and_executes(tmp_path, monkeypatch):
    w = icloud(
        tmp_path,
        monkeypatch,
        seed=[("a1", (SEEN,)), ("a2", (SEEN,)), ("b1", (SEEN,)), ("unread", ()), ("flagged", (SEEN, FLAGGED))],
    )
    for subject, dest in (("a1", "Bills"), ("a2", "Bills"), ("b1", "Travel"), ("unread", "Bills"), ("flagged", "Bills")):
        w.decide(subject, dest=dest)
    w.arrive("after")
    w.decide("after")
    created = backlog.backlog(w.conn, w.cfg)
    (pid,) = created["created"]
    assert backlog.backlog(w.conn, w.cfg)["created"] == []
    proposal = proposals.get(w.conn, pid)
    assert proposal["kind"] == "inbox_backlog" and proposal["account"] == ICLOUD
    groups = proposal["payload"]["groups"]
    assert [(g["dest"], len(g["messages"])) for g in groups] == [("Bills", 2), ("Travel", 1)]
    assert [g["count"] for g in proposal["preview"]["groups"]] == [2, 1]
    assert w.apply().written == 1
    assert uids(w.client.boxes["Bills"]) == [1]
    ids = [m["id"] for m in groups[0]["messages"]]
    selection = {"groups": [{"name": "Bills", "message_ids": ids[:1]}, {"name": "Travel", "message_ids": []}]}
    done = proposals.approve_and_apply(w.conn, w.cfg, pid, selection)
    assert done["status"] == "applied" and done["result"]["moved"] == 1
    assert len(w.client.boxes["Bills"].messages) == 2
    assert w.client.created == []
    assert sorted(w.client.boxes["INBOX"].messages) == [2, 3, 4, 5]
    assert proposals.undo(w.conn, w.cfg, pid)["undone"]


def test_gmail_backlog_relabels_and_keeps_uncertain_out(tmp_path, monkeypatch):
    w = gmail(
        tmp_path,
        monkeypatch,
        seed=[("g1", (SEEN,), [INBOX_LABEL]), ("g2", (SEEN,), [INBOX_LABEL]), ("g3", (SEEN,), [INBOX_LABEL])],
    )
    w.decide("g1", dest="Receipts")
    w.decide("g2", dest="Receipts")
    w.decide("g3", action="none", dest=None, stage="fallback")
    (pid,) = backlog.backlog(w.conn, w.cfg)["created"]
    proposal = proposals.get(w.conn, pid)
    assert proposal["payload"]["folder"] == INBOX_LABEL
    assert [len(g["messages"]) for g in proposal["payload"]["groups"]] == [2]
    done = proposals.approve_and_apply(w.conn, w.cfg, pid)
    assert done["status"] == "applied"
    labels = {u: m["labels"] for u, m in w.client.boxes[ALL_MAIL].messages.items()}
    assert labels == {1: ["Receipts"], 2: ["Receipts"], 3: [INBOX_LABEL]}


def test_backlog_requires_go_live_baseline():
    conn = memory_db()
    with pytest.raises(backlog.BacklogError):
        backlog.backlog(conn)


def test_process_records_baseline_in_live_mode_only(tmp_path):
    for mode, expected in (("shadow", None), ("live", True)):
        conn = memory_db()
        cfg = make_config(tmp_path / mode, mode=mode)
        run.process(conn, cfg, [])
        assert (live.get_state(conn, live.GOLIVE_KEY) is not None) == bool(expected)


def test_cli_pause_resume_status_and_undo_errors(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("ATRIUM_DB", str(tmp_path / "atrium.db"))
    monkeypatch.setenv("ATRIUM_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.setenv("ATRIUM_MODE", "live")
    assert cli.main(["live", "pause"]) == 0
    assert (tmp_path / "pause").exists()
    assert json.loads(capsys.readouterr().out) == {"paused": True}
    assert cli.main(["live", "status"]) == 0
    assert json.loads(capsys.readouterr().out)["paused"] is True
    assert cli.main(["live", "resume"]) == 0
    assert not (tmp_path / "pause").exists()
    assert cli.main(["live", "undo", "999"]) == 1
    assert cli.main(["live", "backlog"]) == 2


HX = {"HX-Request": "true"}


@pytest.fixture
def web_world(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch, file_db=True)
    monkeypatch.setattr(run, "open_client", w.opener)
    w.arrive("shown")
    w.decide("shown")
    w.arrive("fix-me")
    w.decide("fix-me", dest="Misc")
    return w


def test_triage_shows_status_undo_and_header_controls(web_world):
    from helpers import web_client

    w = web_world
    w.apply()
    (row,) = [a for a in w.actions() if a["kind"] == "file"][:1]
    client = web_client(w.cfg)
    page = client.get("/triage?account=icloud").text
    assert "Applied" in page and f"/live/actions/{row['id']}/undo" in page
    assert "Pause" in page and "Live" in page and "routing is shadow" not in page
    undone = client.post(f"/live/actions/{row['id']}/undo", headers=HX).text
    assert "Undone" in undone and "/undo" not in undone
    paused = client.post("/live/pause", headers={"referer": "http://testserver/triage"}, follow_redirects=False)
    assert paused.status_code == 303 and paused.headers["location"] == "/triage"
    assert live.paused(w.cfg)
    assert "Resume" in client.get("/triage").text
    escaped = client.post("/live/resume", headers={"referer": "http://testserver//evil.test"}, follow_redirects=False)
    assert escaped.headers["location"] == "/triage"
    assert not live.paused(w.cfg)
    assert client.post("/live/actions/999/undo", headers=HX).status_code == 404


def test_ui_correction_moves_the_message_in_live_mode_and_defers_when_paused(web_world):
    from helpers import web_client

    w = web_world
    client = web_client(w.cfg)
    mid = w.mid("fix-me")
    live.set_paused(w.cfg, True)
    held = client.post(f"/messages/{mid}/correct", data={"dest": "Bills", "context": "row"}, headers=HX)
    assert "paused" in held.text and w.client.writes == []
    live.set_paused(w.cfg, False)
    again = client.post(f"/messages/{mid}/correct", data={"dest": "Bills", "context": "row"}, headers=HX)
    assert "The message was moved" in again.text
    assert uids(w.client.boxes["Bills"]) == [1]


def correct_before_go_live(w, subject):
    mid = w.mid(subject)
    corrections.record_correction(w.conn, mid, "Bills")
    w.conn.execute("UPDATE corrections SET ts = 1.0 WHERE message_id = ?", (mid,))
    return mid


def test_shadow_era_corrections_are_never_moved_automatically_and_enter_the_backlog(tmp_path, monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    boxes = {"INBOX": FakeBox(), "Bills": FakeBox(validity=3), "Misc": FakeBox(validity=4)}
    for subject, flags in (("read", (SEEN,)), ("unread", ()), ("flagged", (SEEN, FLAGGED))):
        boxes["INBOX"].add(*raw(subject, msgid=subject), flags=flags)
    w = World(tmp_path, monkeypatch, ICLOUD, WriteClient(boxes, ICLOUD_FLAGS))
    for subject in ("read", "unread", "flagged"):
        correct_before_go_live(w, subject)
    assert w.apply().written == 0
    assert w.client.writes == []
    (pid,) = backlog.backlog(w.conn, w.cfg)["created"]
    (group,) = proposals.get(w.conn, pid)["payload"]["groups"]
    assert group["dest"] == "Bills" and [m["id"] for m in group["messages"]] == [w.mid("read")]
    mid = w.mid("flagged")
    corrections.record_correction(w.conn, mid, "Bills")
    assert w.apply().written == 1
    assert uids(w.client.boxes["Bills"]) == [1]


def test_pre_go_live_correction_files_mail_that_becomes_eligible_later(tmp_path, monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    boxes = {"INBOX": FakeBox(), "Bills": FakeBox(validity=3), "Misc": FakeBox(validity=4)}
    boxes["INBOX"].add(*raw("later", msgid="later"), flags=[])
    w = World(tmp_path, monkeypatch, ICLOUD, WriteClient(boxes, ICLOUD_FLAGS))
    correct_before_go_live(w, "later")
    assert w.apply().written == 0
    boxes["INBOX"].set_flags(1, flags=[SEEN])
    w.sync()
    assert w.apply().written == 1
    assert uids(boxes["Bills"]) == [1]


def test_deletion_pass_reaches_mail_atrium_filed_but_not_undone_or_already_trashed(tmp_path, monkeypatch):
    w = icloud(tmp_path, monkeypatch)
    w.arrive("filed", sender="x@spam.test")
    w.decide("filed")
    w.apply()
    w.sync()
    age(w, "filed", 1.0)
    delete_rule(w.conn)
    out = applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener, force=True)
    assert out.by_kind == {"trash": 1}
    assert uids(w.client.boxes["Deleted Messages"]) == [1]
    trashed = [a for a in w.actions() if a["kind"] == "trash"][0]
    live.undo(w.conn, w.cfg, trashed["id"])
    w.sync()
    assert applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener, force=True).written == 0


def test_gmail_select_failure_does_not_strand_other_actions(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("doomed", flags=(SEEN,), sender="x@spam.test")
    w.arrive("friend", flags=())
    w.decide("doomed", action="delete", dest=None, stage="operator")
    w.conn.execute(
        "INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, 'gmail', 1, 1)",
        (w.mid("friend"),),
    )
    original = w.client.select_rw
    calls = []

    def flaky(name):
        calls.append(name)
        if len(calls) == 1:
            raise RuntimeError("NO [SERVERBUG] try later")
        return original(name)

    monkeypatch.setattr(w.client, "select_rw", flaky)
    out = w.apply()
    assert out.failed == 1 and out.written == 1
    assert not [a for a in w.actions() if a["ok"] is None]
    kinds = {a["kind"]: a["ok"] for a in w.actions()}
    assert kinds == {"trash": 0, "flag": 1}
    assert w.apply().written == 1


def test_gmail_partial_label_store_records_what_the_server_has_and_undo_reverses_it(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("half")
    w.decide("half", dest="Receipts")
    original = w.client.mutate

    def after_add(verb, *args):
        if verb == "STORE" and args[1] == "-X-GM-LABELS":
            raise RuntimeError("NO try later")
        return original(verb, *args)

    monkeypatch.setattr(w.client, "mutate", after_add)
    out = w.apply()
    assert out.written == 0 and out.failed == 1
    (row,) = w.actions()
    assert row["ok"] == 0 and row["error"].startswith("partial:")
    assert json.loads(row["undo"]) | {"uids": None} == {
        "op": "gmail_labels",
        "imap_name": ALL_MAIL,
        "uidvalidity": 1,
        "uids": None,
        "add": [],
        "remove": ["Receipts"],
    }
    monkeypatch.setattr(w.client, "mutate", original)
    live.undo(w.conn, w.cfg, row["id"])
    assert w.client.boxes[ALL_MAIL].messages[1]["labels"] == [INBOX_LABEL]


def test_gmail_trash_copy_that_failed_after_copying_still_gets_an_undo(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("gone", sender="x@spam.test")
    w.decide("gone", action="delete", dest=None, stage="operator")
    original = w.client.mutate

    def copy_then_no(verb, *args):
        result = original(verb, *args)
        if verb == "COPY":
            raise RuntimeError("NO connection dropped")
        return result

    monkeypatch.setattr(w.client, "mutate", copy_then_no)
    assert w.apply().written == 1
    (row,) = w.actions()
    assert row["ok"] == 1 and json.loads(row["undo"])["op"] == "gmail_untrash"
    monkeypatch.setattr(w.client, "mutate", original)
    assert live.undo(w.conn, w.cfg, row["id"])["undone"]


def test_actions_view_lists_every_automatic_action_with_undo_and_cli_lists_ids(tmp_path, monkeypatch, capsys):
    from helpers import web_client

    w = icloud(tmp_path, monkeypatch, file_db=True)
    w.arrive("friend", flags=(), sender="f@shop.test")
    w.conn.execute(
        "INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, 'icloud', 1, 1)",
        (w.mid("friend"),),
    )
    w.arrive("old-spam", sender="x@spam.test", box="Bills")
    age(w, "old-spam", 1.0)
    delete_rule(w.conn)
    w.apply()
    applier.deletion_pass(w.conn, w.cfg, ICLOUD, w.opener, force=True)
    ids = {a["kind"]: a["id"] for a in w.actions()}
    assert set(ids) == {"flag", "trash"}
    client = web_client(w.cfg)
    page = client.get("/activity").text
    assert "friend" in page and "old-spam" in page
    assert f"/activity/mail/{ids['flag']}/undo" in page
    assert "Activity" in client.get("/triage").text
    undone = client.post(f"/activity/mail/{ids['flag']}/undo", headers=HX).text
    assert "Undone" in undone and "/undo" not in undone
    monkeypatch.setenv("ATRIUM_DB", str(w.cfg.db_path))
    monkeypatch.setenv("ATRIUM_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.setenv("ATRIUM_MODE", "live")
    assert cli.main(["live", "actions"]) == 0
    listed = json.loads(capsys.readouterr().out)
    assert {a["id"] for a in listed} == set(ids.values())
    assert cli.main(["live", "actions", "--kind", "trash"]) == 0
    assert [a["id"] for a in json.loads(capsys.readouterr().out)] == [ids["trash"]]


CREDENTIALS = {
    "ICLOUD_MAIL_USERNAME": "u",
    "ICLOUD_APP_PASSWORD": "p",
    "GMAIL_ADDRESS": "g",
    "GMAIL_APP_PASSWORD": "p",
}


def test_baseline_is_taken_after_syncing_every_account(tmp_path, monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    monkeypatch.setattr(run.embed, "embed_pending", lambda *a, **k: 0)
    icloud_client = WriteClient({"INBOX": FakeBox()}, ICLOUD_FLAGS)
    gmail_client = WriteClient({ALL_MAIL: FakeBox(), "[Gmail]/Trash": FakeBox()}, GMAIL_FLAGS, gmail=True)
    gmail_client.boxes[ALL_MAIL].add(*raw("g-read", msgid="g-read"), flags=[SEEN], labels=[INBOX_LABEL])
    conn = memory_db()
    conn.execute("DELETE FROM mailboxes")
    cfg = make_config(tmp_path, mode="live", credentials=CREDENTIALS)
    clients = {ICLOUD: icloud_client, GMAIL: gmail_client}
    run.process(conn, cfg, [ICLOUD], lambda c, a: clients[a])
    gid = conn.execute("SELECT id FROM messages WHERE account = 'gmail'").fetchone()["id"]
    assert gid in {r[0] for r in conn.execute("SELECT message_id FROM live_baseline")}


def test_rebaseline_refreshes_baseline_and_backlog_proposal(tmp_path, monkeypatch, capsys):
    w = icloud(tmp_path, monkeypatch, seed=[("old", (SEEN,))], file_db=True)
    monkeypatch.setattr(run, "open_client", w.opener)
    monkeypatch.setenv("ATRIUM_DB", str(w.cfg.db_path))
    monkeypatch.setenv("ATRIUM_CACHE_DIR", str(tmp_path / "cache"))
    monkeypatch.setenv("ATRIUM_MODE", "live")
    monkeypatch.setenv("ICLOUD_MAIL_USERNAME", "u")
    monkeypatch.setenv("ICLOUD_APP_PASSWORD", "p")
    w.decide("old")
    (first,) = backlog.backlog(w.conn, w.cfg)["created"]
    w.arrive("rolled-forward")
    w.decide("rolled-forward")
    assert w.apply().written == 1
    w.client.boxes["INBOX"].add(*raw("again", msgid="again"), flags=[SEEN])
    w.sync()
    w.decide("again")
    assert cli.main(["live", "rebaseline"]) == 0
    out = json.loads(capsys.readouterr().out)
    assert out["baseline"] == 2
    assert proposals.get(w.conn, first)["status"] == "dismissed"
    (second,) = out["created"]
    names = [m["id"] for g in proposals.get(w.conn, second)["payload"]["groups"] for m in g["messages"]]
    assert sorted(names) == sorted([w.mid("old"), w.mid("again")])
    assert w.apply().written == 0


def test_gmail_partial_relabel_is_retried_and_one_undo_reverses_both_attempts(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("stuck")
    w.decide("stuck", dest="Receipts")
    original = w.client.mutate

    def remove_refused(verb, *args):
        if verb == "STORE" and args[1] == "-X-GM-LABELS":
            raise RuntimeError("NO try later")
        return original(verb, *args)

    monkeypatch.setattr(w.client, "mutate", remove_refused)
    out = w.apply()
    assert out.written == 0 and out.failed == 1
    assert w.client.boxes[ALL_MAIL].messages[1]["labels"] == [INBOX_LABEL, "Receipts"]
    w.sync()
    monkeypatch.setattr(w.client, "mutate", original)
    assert w.apply().written == 1
    assert w.client.boxes[ALL_MAIL].messages[1]["labels"] == ["Receipts"]
    rows = w.actions()
    assert [r["ok"] for r in rows] == [0, 1] and rows[0]["undo"] is None
    live.undo(w.conn, w.cfg, rows[1]["id"])
    assert sorted(w.client.boxes[ALL_MAIL].messages[1]["labels"]) == [INBOX_LABEL]
    w.sync()
    assert w.apply().written == 0


def test_partial_relabel_stops_after_the_attempt_limit_but_stays_undoable(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("stubborn")
    w.decide("stubborn", dest="Receipts")
    original = w.client.mutate

    def remove_refused(verb, *args):
        if verb == "STORE" and args[1] == "-X-GM-LABELS":
            raise RuntimeError("NO")
        return original(verb, *args)

    monkeypatch.setattr(w.client, "mutate", remove_refused)
    for _ in range(applier.WRITE_ATTEMPTS + 2):
        w.sync()
        w.apply()
    rows = w.actions()
    assert len(rows) == applier.WRITE_ATTEMPTS
    holder = [r for r in rows if r["undo"] is not None]
    assert len(holder) == 1 and live.get(w.conn, holder[0]["id"])["undoable"]
    monkeypatch.setattr(w.client, "mutate", original)
    live.undo(w.conn, w.cfg, holder[0]["id"])
    assert sorted(w.client.boxes[ALL_MAIL].messages[1]["labels"]) == [INBOX_LABEL]


def test_trash_lookup_ignores_a_different_message_with_the_same_message_id(tmp_path, monkeypatch):
    w = gmail(tmp_path, monkeypatch)
    w.arrive("dup", sender="x@spam.test")
    w.decide("dup", action="delete", dest=None, stage="operator")
    w.client.boxes["[Gmail]/Trash"].add(*raw("dup", sender="other@x.test", msgid="dup", body="different body"))
    original = w.client.mutate

    def copy_without_trace(verb, *args):
        if verb == "COPY":
            raise RuntimeError("NO nothing was copied")
        return original(verb, *args)

    monkeypatch.setattr(w.client, "mutate", copy_without_trace)
    out = w.apply()
    assert out.written == 0 and out.failed == 1
    (row,) = w.actions()
    assert row["ok"] == 0 and row["undo"] is None
