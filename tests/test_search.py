from atrium import ask, embed, search
from atrium.http import ServiceUnavailable
from helpers import add_message, filed, inbox, make_config, memory_db, unit


def hit_ids(result):
    return [h["id"] for h in result["hits"]]


def fake_embed(vector):
    return lambda url, texts: [vector for _ in texts]


def down(url, texts):
    raise ServiceUnavailable("offline")


def test_fts_query_escapes_and_prefixes():
    assert search.fts_query('hello world') == '"hello"* "world"*'
    assert search.fts_query('foo" OR 1=1 -- NEAR(') == '"foo"* "OR"* "1"* "1"* "NEAR"*'
    assert search.fts_query('"exact phrase" tail') == '"exact phrase" "tail"*'
    assert search.fts_query('"single"') == '"single"'
    assert search.fts_query("a_b c*d") == '"a"* "b"* "c"* "d"*'
    assert search.fts_query("   ") == ""
    assert len(search.fts_query(" ".join("w" * 3 for _ in range(40))).split()) == search.MAX_TERMS


def test_hostile_query_does_not_raise():
    conn = memory_db()
    filed(conn, 1, "Alpha", "a@x.test", subject="hello", body="body")
    for q in ['"', "AND", "NEAR(", "col:x", "-", "*", "'; DROP TABLE messages;--"]:
        search.search(conn, make_config(__import__("pathlib").Path("/tmp")), q, embed_texts=down)


def test_rrf_order():
    fused = search.rrf([[1, 2, 3], [3, 4, 1]])
    assert [i for i, _ in fused] == [1, 3, 2, 4]
    assert fused[0][1] == 1 / 61 + 1 / 63
    assert search.rrf([[5], [6]])[0][0] == 5


def test_lexical_only_when_embed_down(tmp_path):
    conn = memory_db()
    a = filed(conn, 1, "Alpha", "a@x.test", subject="Invoice march", body="please pay the invoice today")
    filed(conn, 2, "Alpha", "b@x.test", subject="Lunch", body="sandwich")
    result = search.search(conn, make_config(tmp_path), "invo", embed_texts=down)
    assert hit_ids(result) == [a]
    assert result["semantic"] is False and "offline" in result["error"]
    hit = result["hits"][0]
    assert hit["lexical_rank"] == 1 and hit["semantic_rank"] is None
    assert any(highlighted and "invoice" in text.lower() for text, highlighted in hit["snippet_parts"])
    assert "\x02" not in hit["snippet"]


def test_hybrid_fuses_and_keeps_branch_ranks(tmp_path):
    conn = memory_db()
    lex_only = filed(conn, 1, "Alpha", "a@x.test", subject="report", body="report", vector=unit(5))
    both = filed(conn, 2, "Alpha", "b@x.test", subject="report summary", body="report", vector=unit(1))
    sem_only = filed(conn, 3, "Alpha", "c@x.test", subject="other", body="nothing", vector=unit(1, 2, 0.1))
    result = search.search(conn, make_config(tmp_path), "report", embed_texts=fake_embed(unit(1)))
    assert result["semantic"] is True
    assert hit_ids(result)[0] == both
    by_id = {h["id"]: h for h in result["hits"]}
    assert by_id[lex_only]["semantic_rank"] is not None
    assert by_id[sem_only]["lexical_rank"] is None and by_id[sem_only]["semantic_rank"] == 2
    assert by_id[sem_only]["snippet"] == "nothing"


def test_query_prompt_used(tmp_path):
    conn = memory_db()
    seen = []

    def capture(url, texts):
        seen.extend(texts)
        return [unit(0)]

    search.search(conn, make_config(tmp_path), "where is my parcel", embed_texts=capture)
    assert seen == ["task: search result | query: where is my parcel"]
    assert embed.query_text("x").startswith("task: search result | query: ")


def test_filters_apply(tmp_path):
    conn = memory_db()
    cfg = make_config(tmp_path)
    base = dict(subject="topic", body="topic")
    a = filed(conn, 1, "Alpha", "ann@one.test", "one.test", date_ts=1_700_000_000, seen=1, **base)
    b = filed(conn, 2, "Beta", "bob@two.test", "two.test", date_ts=1_800_000_000, seen=0, flagged=1,
              att_types='["application/pdf"]', **base)
    g = add_message(conn, 3, account="gmail", mailbox_id=2, from_addr="cy@one.test", from_domain="one.test",
                    subject="topic", body="topic", folder="Gamma", location="Gamma", date_ts=1_750_000_000)

    def ids(**f):
        return set(hit_ids(search.search(conn, cfg, "topic", f, embed_texts=down)))

    assert ids() == {a, b, g}
    assert ids(account="gmail") == {g}
    assert ids(folder="Beta") == {b}
    assert ids(**{"from": "ONE.test"}) == {a, g}
    assert ids(**{"from": "bob@"}) == {b}
    assert ids(date_from="2025-01-01") == {b, g}
    assert ids(date_to="2023-11-15") == {a}
    assert ids(has_attachment=True) == {b}
    assert ids(unread="on") == {b, g}
    assert ids(flagged="1") == {b}
    assert ids(account="icloud", unread=True, flagged=True) == {b}


def test_filters_apply_to_vector_branch(tmp_path):
    conn = memory_db()
    cfg = make_config(tmp_path)
    for i in range(30):
        filed(conn, 100 + i, "Noise", f"n{i}@x.test", subject="n", vector=unit(1, 2, 0.01 * i))
    target = filed(conn, 1, "Target", "t@x.test", subject="t", vector=unit(3))
    result = search.search(conn, cfg, "zzzz", {"folder": "Target"}, embed_texts=fake_embed(unit(1)))
    assert hit_ids(result) == [target]


def test_vector_branch_falls_back_to_exact_past_knn_cap(tmp_path, monkeypatch):
    monkeypatch.setattr(embed, "KNN_MAX", 8)
    conn = memory_db()
    cfg = make_config(tmp_path)
    for i in range(30):
        filed(conn, 100 + i, "Noise", f"n{i}@x.test", subject="n", vector=unit(1, 2, 0.01 * i))
    target = filed(conn, 1, "Target", "t@x.test", subject="t", vector=unit(3))
    result = search.search(conn, cfg, "zzzz", {"folder": "Target"}, embed_texts=fake_embed(unit(1)))
    assert hit_ids(result) == [target]


def test_corrected_folder_is_searchable(tmp_path):
    from atrium.corrections import record_correction

    conn = memory_db()
    m = inbox(conn, 1, subject="topic", body="topic")
    record_correction(conn, m, "Moved")
    result = search.search(conn, make_config(tmp_path), "topic", {"folder": "Moved"}, embed_texts=down)
    assert hit_ids(result) == [m]
    assert result["hits"][0]["folder"] == "Moved"


def test_empty_query_lists_recent(tmp_path):
    conn = memory_db()
    old = filed(conn, 1, "A", "a@x.test", date_ts=1.0, body="old body")
    new = filed(conn, 2, "A", "b@x.test", date_ts=2.0, body="new body")
    result = search.search(conn, make_config(tmp_path), "", limit=5)
    assert hit_ids(result) == [new, old]
    assert result["hits"][0]["snippet"] == "new body"


def sources_for(conn, tmp_path, n):
    for i in range(n):
        filed(conn, 1 + i, "Alpha", f"s{i}@x.test", subject=f"parcel {i}", body=f"parcel body {i}")
    return make_config(tmp_path)


def test_ask_strips_citations_outside_sources(tmp_path):
    conn = memory_db()
    cfg = sources_for(conn, tmp_path, 2)

    def chat(url, system, user, schema, max_tokens):
        assert "[1] Subject:" in user and "[2] Subject:" in user and "[3] Subject:" not in user
        return {"answer": "It arrived [1] and shipped [7], see [2, 9].", "citations": [2, 9, 1]}

    result = ask.ask(conn, cfg, "parcel", chat=chat, embed_texts=down)
    ids = {s["n"]: s["message_id"] for s in result["sources"]}
    assert result["answer"] == "It arrived [1] and shipped, see [2]."
    assert result["citations"] == [{"n": 1, "message_id": ids[1]}, {"n": 2, "message_id": ids[2]}]
    assert result["rejected"] == [7, 9]
    assert [s["cite"] for s in result["segments"] if "cite" in s] == [1, 2]
    assert result["error"] is None


def test_ask_citation_array_only_valid(tmp_path):
    conn = memory_db()
    cfg = sources_for(conn, tmp_path, 1)
    chat = lambda *a, **k: {"answer": "plain", "citations": [1, 4, 0]}
    result = ask.ask(conn, cfg, "parcel", chat=chat, embed_texts=down)
    assert [c["n"] for c in result["citations"]] == [1]
    assert result["rejected"] == [0, 4]


def test_ask_handles_chat_down_and_no_sources(tmp_path):
    conn = memory_db()
    cfg = sources_for(conn, tmp_path, 1)

    def chat(*a, **k):
        raise ServiceUnavailable("chat down")

    result = ask.ask(conn, cfg, "parcel", chat=chat, embed_texts=down)
    assert "chat down" in result["error"] and len(result["sources"]) == 1 and result["answer"] == ""
    empty = ask.ask(conn, cfg, "nothingmatches", chat=chat, embed_texts=down)
    assert empty["sources"] == [] and empty["error"] is None
