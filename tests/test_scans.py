import json
import time

from atrium import deletions, gmail_labels, proposals, rebalance, rules, scans
from helpers import add_message, filed, memory_db, unit

DAY = 86400


def namer(cfg, folder, domains, terms):
    return "Named"


def fresh():
    conn = memory_db()
    scans.init_schemas(conn)
    return conn


def mixed_folder(conn, folder="Mixed"):
    uid = 1
    for i in range(30):
        filed(conn, uid, folder, "a@x.test", "x.test", subject=f"order {i}", subject_tmpl="order <n>", date_ts=i)
        uid += 1
    for i in range(30):
        filed(conn, uid, folder, "b@y.test", "y.test", subject=f"digest {i}", subject_tmpl="digest <n>", date_ts=i)
        uid += 1
    return uid


def test_split_surfaces_only_after_two_runs_and_resets_when_it_disappears():
    conn = fresh()
    mixed_folder(conn)
    first = rebalance.scan(conn, None, ts=0, namer=namer)
    assert first == {"candidates": 1, "created": 0}
    assert rebalance.scan(conn, None, ts=DAY, namer=namer)["created"] == 0
    second = rebalance.scan(conn, None, ts=rebalance.RUN_GAP_SECONDS + 1, namer=namer)
    assert second["created"] == 1
    [p] = proposals.list(conn, "pending", "rebalance_split")
    group = p["preview"]["groups"][0]
    assert p["preview"]["folder"] == "Mixed" and group["name"] == "Mixed/Named" and group["count"] == 30
    assert len(group["messages"]) == 30 and group["senders"][0]["label"] in ("x.test", "y.test")
    assert group["terms"] and group["subjects"]
    payload = proposals.get(conn, p["id"])["payload"]
    assert len(payload["groups"][0]["messages"]) == 30
    again = rebalance.scan(conn, None, ts=2 * rebalance.RUN_GAP_SECONDS + 2, namer=namer)
    assert again["created"] == 0 and proposals.pending_count(conn) == 1

    gone = fresh()
    mixed_folder(gone)
    rebalance.scan(gone, None, ts=0, namer=namer)
    gone.execute("DELETE FROM messages")
    rebalance.scan(gone, None, ts=1, namer=namer)
    assert gone.execute("SELECT COUNT(*) FROM rebalance_candidates").fetchone()[0] == 0


def test_small_folder_or_small_groups_do_not_split():
    conn = fresh()
    for i in range(30):
        filed(conn, i, "Small", f"u{i}@d{i}.test", f"d{i}.test", subject_tmpl=f"t{i}")
    for i in range(30, 70):
        filed(conn, i, "Spread", f"u{i}@d{i}.test", f"d{i}.test", subject_tmpl=f"t{i}")
    assert rebalance.detect(conn) == []


def test_embedding_clusters_propose_split_for_mixed_senders():
    conn = fresh()
    for i in range(60):
        vector = unit(0 if i < 30 else 1)
        filed(conn, i, "Topics", f"u{i}@d{i}.test", f"d{i}.test", vector=vector, subject_tmpl=f"t{i}")
    [cand] = rebalance.detect(conn)
    assert cand.level == "embed" and len(cand.groups) == 1 and len(cand.groups[0].ids) == 30


def test_merge_proposed_when_sender_mail_lives_in_another_folder():
    conn = fresh()
    uid = 1
    for i in range(35):
        filed(conn, uid, "Src", f"u{i}@d{i}.test", f"d{i}.test", subject_tmpl=f"t{i}")
        uid += 1
    for _ in range(5):
        filed(conn, uid, "Src", "s@z.test", "z.test", subject_tmpl="notice")
        uid += 1
    for _ in range(12):
        filed(conn, uid, "Dst", "s@z.test", "z.test", subject_tmpl="notice")
        uid += 1
    found = rebalance.detect(conn)
    merges = [c for c in found if c.kind == "rebalance_merge"]
    assert len(merges) == 1 and merges[0].into == "Dst" and merges[0].folder == "Src"
    rebalance.scan(conn, None, ts=0, namer=namer)
    rebalance.scan(conn, None, ts=rebalance.RUN_GAP_SECONDS + 1, namer=namer)
    [p] = proposals.list(conn, "pending", "rebalance_merge")
    assert p["preview"]["into"] == "Dst" and p["preview"]["sources"] == [{"folder": "Src", "count": 5}]
    assert p["preview"]["into_exists"] and p["preview"]["group"]["count"] == 5


def test_merge_requires_ninety_percent_share():
    conn = fresh()
    uid = 1
    for i in range(35):
        filed(conn, uid, "Src", f"u{i}@d{i}.test", f"d{i}.test", subject_tmpl=f"t{i}")
        uid += 1
    for _ in range(5):
        filed(conn, uid, "Src", "s@z.test", "z.test")
        uid += 1
    for folder, n in (("Dst", 9), ("Other", 3)):
        for _ in range(n):
            filed(conn, uid, folder, "s@z.test", "z.test")
            uid += 1
    assert [c for c in rebalance.detect(conn) if c.kind == "rebalance_merge"] == []


def delete_events(conn, ids):
    for mid in ids:
        conn.execute("UPDATE messages SET gone_at = 1 WHERE id = ?", (mid,))
        conn.execute("INSERT INTO events (ts, account, kind, message_id) VALUES (1, 'icloud', 'delete', ?)", (mid,))


def group_messages(conn, uid, addr, tmpl, count, **fields):
    return [
        add_message(conn, uid + i, from_addr=addr, from_domain=addr.split("@")[1], subject_tmpl=tmpl, subject=tmpl, seen=1, date_ts=100, **fields)
        for i in range(count)
    ]


def test_deletion_threshold_and_backlog():
    conn = fresh()
    hit = group_messages(conn, 1, "n@news.test", "weekly", 6)
    delete_events(conn, hit[:5])
    near = group_messages(conn, 20, "m@mix.test", "daily", 10)
    delete_events(conn, near[:6])
    few = group_messages(conn, 40, "f@few.test", "rare", 4)
    delete_events(conn, few)
    assert deletions.scan(conn)["created"] == 1
    [p] = proposals.list(conn, "pending", "deletion_rule")
    assert p["preview"]["backlog"] == 1
    assert p["preview"]["rule"]["when"][0] == {"field": "from_addr", "op": "eq", "value": "n@news.test"}
    full = proposals.get(conn, p["id"])
    assert full["payload"]["rule"]["action"] == "delete" and len(full["payload"]["messages"]) == 1
    rules.validate_rule(full["payload"]["rule"])
    assert deletions.scan(conn)["created"] == 0
    proposals.dismiss(conn, p["id"], suppress=True)
    assert deletions.scan(conn)["created"] == 0 and len(proposals.list(conn)) == 1


def test_otp_templates_propose_age_limited_rule():
    conn = fresh()
    old = time.time() - 10 * DAY
    codes = group_messages(conn, 1, "no@otp.test", "your code is <n>", 6, k_otp6=1)
    for mid in codes[:5]:
        conn.execute("UPDATE messages SET date_ts = ? WHERE id = ?", (old, mid))
    conn.execute("UPDATE messages SET date_ts = ? WHERE id = ?", (time.time(), codes[5]))
    assert deletions.scan(conn)["created"] == 1
    [p] = proposals.list(conn, kind="deletion_rule")
    assert p["preview"]["reason"] == "otp" and p["preview"]["older_than_days"] == 7
    assert p["preview"]["backlog"] == 5
    assert {"field": "age_days", "op": "gte", "value": 7} in p["preview"]["rule"]["when"]


def test_existing_rule_suppresses_deletion_proposal():
    conn = fresh()
    hit = group_messages(conn, 1, "n@news.test", "weekly", 6)
    delete_events(conn, hit[:5])
    conn.execute(
        "INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (0, 'mine', NULL, 'delete', NULL, ?)",
        (json.dumps([{"field": "from_addr", "op": "eq", "value": "n@news.test"}]),),
    )
    assert deletions.scan(conn)["created"] == 0


def label_messages(conn, uid, labels, count, addr="a@g.test"):
    for i in range(count):
        add_message(
            conn, uid + i, account="gmail", mailbox_id=2, from_addr=addr, from_domain="g.test", labels=json.dumps(labels)
        )
    return uid + count


def test_label_merge_proposals_parent_sibling_and_exclusions():
    conn = fresh()
    uid = label_messages(conn, 1, ["Top/Small"], 3)
    uid = label_messages(conn, uid, ["Big"], 12, addr="big@g.test")
    uid = label_messages(conn, uid, ["Solo"], 2, addr="big@g.test")
    uid = label_messages(conn, uid, ["Lonely"], 2, addr="nobody@g.test")
    uid = label_messages(conn, uid, ["Fam"], 2)
    uid = label_messages(conn, uid, ["Fam/Kid"], 2)
    uid = label_messages(conn, uid, ["\\Important", "[Gmail]/Starred"], 2)
    assert gmail_labels.scan(conn)["created"] == 3
    got = {(p["preview"]["from"], p["preview"]["into"]) for p in proposals.list(conn, kind="gmail_label_merge")}
    assert got == {("Top/Small", "Top"), ("Solo", "Big"), ("Fam/Kid", "Fam")}
    assert gmail_labels.scan(conn)["created"] == 0
    [p] = [x for x in proposals.list(conn) if x["preview"]["from"] == "Solo"]
    assert p["preview"]["from_count"] == 2 and p["preview"]["into_count"] == 12 and len(p["preview"]["messages"]) == 2


def test_fingerprint_dedupe_failed_reopens_and_dismiss_suppress():
    conn = fresh()
    first = proposals.create(conn, "k", "icloud", "t", {}, {}, "fp")
    assert first and proposals.create(conn, "k", "icloud", "t", {}, {}, "fp") is None
    proposals.dismiss(conn, first)
    assert proposals.create(conn, "k", "icloud", "t", {}, {}, "fp") is None
    other = proposals.create(conn, "k", "icloud", "t", {}, {}, "fp2")
    proposals.dismiss(conn, other, suppress=True)
    assert proposals.get(conn, other)["status"] == "suppressed"
    assert proposals.create(conn, "k", "icloud", "t", {}, {}, "fp2") is None
    third = proposals.create(conn, "k", "icloud", "t", {}, {}, "fp3")
    conn.execute("UPDATE proposals SET status = 'failed' WHERE id = ?", (third,))
    assert proposals.create(conn, "k", "icloud", "t2", {}, {}, "fp3") == third
    assert proposals.get(conn, third)["status"] == "pending"
    assert proposals.counts_by_kind(conn) == {"k": 1}


def test_scan_all_runs_every_scanner():
    conn = memory_db()
    out = scans.scan_all(conn, None)
    assert set(out) == {"rebalance", "deletions", "gmail_labels"}


def test_cli_scan_all_and_list(tmp_path, monkeypatch, capsys):
    from atrium import cli

    monkeypatch.setenv("ATRIUM_DB", str(tmp_path / "a.db"))
    monkeypatch.setenv("ATRIUM_CACHE_DIR", str(tmp_path / "c"))
    assert cli.main(["proposals", "scan-all"]) == 0
    assert set(json.loads(capsys.readouterr().out)) == {"rebalance", "deletions", "gmail_labels"}
    assert cli.main(["proposals", "list"]) == 0
    assert json.loads(capsys.readouterr().out) == []
    for argv in (["rebalance", "scan"], ["deletions", "scan"], ["gmail-labels", "scan"]):
        assert cli.main(argv) == 0
        assert json.loads(capsys.readouterr().out) == {"candidates": 0, "created": 0}


def test_suggest_name_falls_back_on_empty_or_malformed_reply(monkeypatch):
    cfg = type("Cfg", (), {"chat_url": "http://llm.test"})()
    for reply in ({"choices": []}, {}, {"choices": [{"message": {"content": "[]"}}]}, {"choices": [{"message": {"content": None}}]}):
        monkeypatch.setattr(rebalance.llm, "post_json", lambda url, body, reply=reply: reply)
        assert rebalance.suggest_name(cfg, "Shopping", ["shoply.test"], ["order"]) == "shoply.test"
        assert rebalance.suggest_name(cfg, "Shopping", [], []) == "Split"
