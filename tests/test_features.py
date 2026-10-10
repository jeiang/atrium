from atrium import features


def template(s):
    return features.subject_template(s)


def test_template_strips_reply_prefixes_and_numbers():
    assert template("Re: FWD: Order #12345 shipped") == "order <n> shipped"


def test_template_amounts_and_dates():
    assert template("Payment of $1,234.50 received") == "payment of <amt> received"
    assert template("Statement for March 5, 2026") == "statement for <date>"
    assert template("Due 2026-03-05") == "due <date>"


def test_template_token_with_digit():
    assert template("Your code ab12cd is ready") == "your code <n> is ready"


def test_extract_basic_fields():
    header = (
        b"From: Some One <Some.One@Mail.Example.test>\r\nTo: me@x.test\r\nSubject: Receipt $12.00\r\n"
        b"Message-ID: <abc@mx.example.test>\r\nList-Id: <list.example.test>\r\n"
        b"Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n"
    )
    f = features.extract(header, b"Your receipt total $12.00. Code 123456")
    assert f["from_addr"] == "some.one@mail.example.test"
    assert f["from_domain"] == "mail.example.test"
    assert f["msgid"] == "abc@mx.example.test"
    assert f["msgid_domain"] == "mx.example.test"
    assert f["list_id"] == "<list.example.test>"
    assert f["has_money"] and f["k_receipt"] and f["k_otp6"]
    assert f["to_addrs"] == ["me@x.test"]
    assert f["date_ts"] > 0


def test_extract_survives_garbage():
    f = features.extract(b"\xff\xfe not a header", b"")
    assert f["from_addr"] == ""
