from atrium import imap


def test_collapse_labels_to_deepest_leaf():
    labels = ["\\Inbox", "\\Important", "Personal", "Personal/Friends", "Finance"]
    assert imap.collapse_labels(labels) == ["Finance", "Personal/Friends"]
    assert imap.collapse_labels(["A", "A/B", "A/B/C"]) == ["A/B/C"]
    assert imap.collapse_labels(["A", "AB"]) == ["A", "AB"]
    assert imap.collapse_labels(["\\Inbox"]) == []


def test_utf7_decode():
    assert imap.utf7_decode("Caf&AOk-") == "Café"
    assert imap.utf7_decode("A&-B") == "A&B"
    assert imap.utf7_decode("plain") == "plain"


def test_parse_list_quoted_and_atom_names():
    lines = [
        b'(\\HasNoChildren \\Trash) "/" "Deleted Messages"',
        b'(\\HasNoChildren) "/" INBOX',
        b'(\\HasChildren \\Noselect) "/" "[Gmail]"',
        b'(\\HasNoChildren) "/" "Caf&AOk-/Sub"',
    ]
    boxes = imap.parse_list(lines)
    assert [b.name for b in boxes] == ["Deleted Messages", "INBOX", "[Gmail]", "Café/Sub"]
    assert boxes[0].flags == ("\\HasNoChildren", "\\Trash")
    assert boxes[3].imap_name == "Caf&AOk-/Sub"
    assert boxes[0].delimiter == "/"


def test_parse_labels_mixed_quoting():
    text = '\\Inbox "\\\\Important" "Personal/Friends" "Caf&AOk-"'
    assert imap.parse_labels(text) == ["\\Inbox", "\\Important", "Personal/Friends", "Café"]


def test_parse_status():
    data = [b"INBOX (MESSAGES 3 UIDNEXT 9 UIDVALIDITY 77 HIGHESTMODSEQ 1234)"]
    assert imap.parse_status(data) == {"uidvalidity": 77, "uidnext": 9, "highestmodseq": 1234, "messages": 3}
    assert imap.parse_status([b"INBOX (MESSAGES 3 UIDNEXT 9 UIDVALIDITY 77)"])["highestmodseq"] is None


def test_parse_fetch_full_gmail_style():
    data = [
        (
            b"1 (X-GM-MSGID 111 X-GM-THRID 222 UID 5 RFC822.SIZE 900 INTERNALDATE \"05-Oct-2026 10:11:12 +0000\" "
            b"BODYSTRUCTURE ((\"text\" \"plain\" (\"charset\" \"utf-8\") NIL NIL \"7bit\" 10 1)"
            b"(\"application\" \"pdf\" (\"name\" \"Bill 1.pdf\") NIL NIL \"base64\" 5000 NIL (\"attachment\" (\"filename\" \"Bill 1.pdf\")))"
            b" \"mixed\") BODY[HEADER] {9}",
            b"Subject: x",
        ),
        (b" BODY[TEXT]<0> {4}", b"body"),
        b' X-GM-LABELS (\\Inbox "\\\\Important" "Personal/Friends") FLAGS (\\Seen $Junk))',
    ]
    [item] = imap.parse_fetch(data)
    assert item["uid"] == 5
    assert item["gm_msgid"] == "111" and item["gm_thrid"] == "222"
    assert item["flags"] == ["\\Seen", "$Junk"]
    assert item["labels"] == ["\\Inbox", "\\Important", "Personal/Friends"]
    assert item["header"] == b"Subject: x" and item["text"] == b"body"
    assert item["size"] == 900
    assert item["attachments"] == [("application/pdf", "Bill 1.pdf")]


def test_parse_fetch_flag_only_lines_and_modseq():
    data = [b"1 (UID 5 MODSEQ (900) FLAGS (\\Seen \\Flagged))", b"2 (UID 7 MODSEQ (901) FLAGS ())"]
    items = imap.parse_fetch(data)
    assert [i["uid"] for i in items] == [5, 7]
    assert items[0]["flags"] == ["\\Seen", "\\Flagged"] and items[0]["modseq"] == 900
    assert items[1]["flags"] == [] and items[1]["labels"] is None


def test_fetch_items_request_shape():
    assert "X-GM-LABELS" in imap.fetch_items(True, False)
    assert "BODY.PEEK[HEADER]" in imap.fetch_items(False, True)
    assert "BODY[" not in imap.fetch_items(False, True).replace("BODY.PEEK[", "")
