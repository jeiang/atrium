#!/usr/bin/env python3
import base64
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, use_source_tree  # noqa: E402

use_source_tree()

from atrium import features  # noqa: E402

SUBJECTS = [
    "Re: FWD: Order #12345 shipped",
    "Payment of $1,234.50 received",
    "Statement for March 5, 2026",
    "Due 2026-03-05",
    "Your code ab12cd is ready",
    "",
    "   ",
    "RE: re: Re : Fw: AW: fwd:Hello",
    "Re:Re:Re: nested",
    "Fwd : spaced prefix",
    "Reply: not a prefix",
    "Regarding: also not",
    "  Re:  leading space",
    "Invoice TT$1,000.00 due",
    "Invoice tt$ 50 due",
    "US$20 credit",
    "Refund of €5.00 and £3",
    "Total 12.50 CAD",
    "Total 12.50cad today",
    "USD 1,000 transfer",
    "Paid 3 usd",
    "Meeting on 5th March",
    "Meeting on 21st Sept., 2026",
    "Dinner 3 Jan 26",
    "Due Dec 31",
    "due december 31st 2026",
    "Report 03/05/26",
    "Report 2026/3/5",
    "Report 1-2-3",
    "Order # 42 confirmed",
    "Order #A1B2 confirmed",
    "Ticket #  77 updated",
    "Build 1.2.3 released",
    "v2 vs v10",
    "Room 101 at 3pm",
    "COVID-19 update",
    "Ünïcode 4ème édition",
    "日本語 123 テスト",
    "Price 1,2,3,4 total",
    "Your OTP is 482913",
    "Mayday may 5",
    "Augustus aug 2026",
    "Re: [list] Weekly digest #208",
    "Fwd: Re: Fw: chain",
    "re:",
    "Re: $",
    "Sale: 50% off for 48 hours",
    "Tabs\tand\nnewlines\r\nhere 9",
]

LONG_BODY = " ".join(f"word{i % 97} receipt order statement" for i in range(1500))


def crlf(text):
    return text.replace("\n", "\r\n")


def qp(text):
    import quopri

    return quopri.encodestring(text.encode("utf-8")).decode("ascii")


def b64lines(data):
    encoded = base64.b64encode(data).decode("ascii")
    return "\n".join(encoded[i : i + 76] for i in range(0, len(encoded), 76))


# (case, header bytes, text bytes, attachments passed as from BODYSTRUCTURE or None, fallback_ts)
MESSAGES = [
    (
        "basic",
        b"From: Some One <Some.One@Mail.Example.test>\r\nTo: me@x.test\r\nSubject: Receipt $12.00\r\n"
        b"Message-ID: <abc@mx.example.test>\r\nList-Id: <list.example.test>\r\n"
        b"Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n",
        b"Your receipt total $12.00. Code 123456",
        None,
        0.0,
    ),
    ("garbage", b"\xff\xfe not a header", b"", None, 0.0),
    ("empty", b"", b"", None, 1767225600.5),
    (
        "multipart_alternative_encoded_headers",
        crlf(
            "From: =?UTF-8?B?w4lsb2RpZSBNYXJ0w61u?= <Elodie@Shop.Example.test>\n"
            "To: \"Me, Myself\" <me@example.test>, other@example.test\n"
            "Cc: =?ISO-8859-1?Q?Andr=E9?= <andre@example.test>\n"
            "Delivered-To: ME@example.test\n"
            "X-Original-To: alias@example.test\n"
            "Reply-To: Support <Support@Shop.Example.test>\n"
            "Return-Path: <bounce+abc=example.test@Bounces.Shop.Example.test>\n"
            "Subject: =?UTF-8?Q?Votre_commande_n=C2=B0_4521_est_exp=C3=A9di=C3=A9e?=\n"
            "Date: Mon, 5 Oct 2026 08:15:30 -0400 (EDT)\n"
            "Message-ID: < order-4521@mail.shop.example.test >\n"
            "In-Reply-To: <prev-1@shop.example.test> garbage <prev-2@shop.example.test>\n"
            "References: <root@shop.example.test>\n <prev-1@shop.example.test>\n\t<prev-2@shop.example.test>\n"
            "List-Id: Shop News <News.Shop.Example.test>\n"
            "Precedence: Bulk\n"
            "DKIM-Signature: v=1; a=rsa-sha256; d=Shop.Example.test; s=s1;\n h=from:to; b=abc\n"
            "DKIM-Signature: v=1; a=rsa-sha256; d=esp.example.test; s=s2; b=def\n"
            "DKIM-Signature: v=1; a=rsa-sha256; d=shop.example.test; s=s3; b=ghi\n"
            "MIME-Version: 1.0\n"
            "Content-Type: multipart/alternative; boundary=\"b1\"\n\n"
        ).encode(),
        crlf(
            "--b1\n"
            "Content-Type: text/plain; charset=utf-8\n"
            "Content-Transfer-Encoding: quoted-printable\n\n"
            + qp("Bonjour,\n\nVotre commande n° 4521 est expédiée.  Total : 45,00 €.\nMontant: €45.00\n")
            + "\n--b1\n"
            "Content-Type: text/html; charset=utf-8\n\n"
            "<html><body><p>HTML version should not win</p></body></html>\n"
            "--b1--\n"
        ).encode(),
        None,
        0.0,
    ),
    (
        "html_only_base64_latin1",
        crlf(
            "From: News <news@letters.example.test>\n"
            "To: me@example.test\n"
            "Subject: Weekly letter\n"
            "Date: Wed, 07 Oct 2026 23:59:59 GMT\n"
            "Message-ID: <letter-208@letters.example.test>\n"
            "MIME-Version: 1.0\n"
            "Content-Type: text/html; charset=iso-8859-1\n"
            "Content-Transfer-Encoding: base64\n\n"
        ).encode(),
        b64lines(
            "<html><head><style>p{color:red}</style><SCRIPT type='x'>var a='<p>';</SCRIPT></head>"
            "<body><h1>Caf\u00e9 &amp; cr\u00e8me</h1><p>Your&nbsp;verification code is <b>739201</b>.</p>"
            "<!-- comment --><div>Total&#58; USD 19.99</div></body></html>".encode("latin-1")
        ).encode()
        + b"\r\n",
        None,
        0.0,
    ),
    (
        "html_only_unclosed_tags",
        b"From: a@b.example.test\nSubject: odd html\nContent-Type: text/html\n\n",
        b"<p>one<p>two<br>three &lt;not a tag&gt; <a href='x'>link</a> 5 > 3 < 4 <unclosed",
        None,
        0.0,
    ),
    (
        "bad_date",
        b"From: x@example.test\r\nDate: not a date\r\nSubject: Bad date\r\n\r\n",
        b"body",
        None,
        1760000000.25,
    ),
    (
        "date_two_digit_year_obsolete_zone",
        b"From: x@example.test\r\nDate: 6 Oct 26 10:00 EST\r\nSubject: Obsolete\r\n\r\n",
        b"body",
        None,
        0.0,
    ),
    (
        "date_no_zone",
        b"From: x@example.test\r\nDate: Tue, 06 Oct 2026 10:00:00\r\nSubject: No zone\r\n\r\n",
        b"body",
        None,
        0.0,
    ),
    (
        "date_minus_zero",
        b"From: x@example.test\r\nDate: Tue, 06 Oct 2026 10:00:00 -0000\r\nSubject: Minus zero\r\n\r\n",
        b"body",
        None,
        0.0,
    ),
    (
        "date_missing",
        b"From: x@example.test\r\nSubject: No date\r\n\r\n",
        b"body",
        None,
        1234.0,
    ),
    (
        "esp_headers",
        crlf(
            "From: \"Store\" <orders@store.example.test>\n"
            "To: me@example.test\n"
            "Subject: Order 1001 shipped\n"
            "X-SES-Outgoing: 2026.10.06-1.2.3.4\n"
            "X-Mailgun-Sid: abc\n"
            "X-SG-EID: def\n"
            "X-Mandrill-User: md_1\n"
            "X-PM-Message-Id: 1-2-3\n"
            "X-SparkPost-Id: x\n"
            "X-MSYS-API: {}\n"
            "X-Shopify-Shop: s\n"
            "X-ShopId: 99\n"
            "X-Campaign: c\n"
            "X-CampaignID: c\n"
            "X-MC-User: u\n"
            "X-GitHub-Reason: mention\n"
            "X-Unrelated: nothing\n"
            "Feedback-ID: 1.us-east-1.abc:AmazonSES\n"
            "Content-Type: text/plain; charset=utf-8\n\n"
        ).encode(),
        b"Your order has shipped. Order total TT$1,250.00\r\n",
        None,
        0.0,
    ),
    (
        "esp_value_only",
        b"From: a@b.example.test\r\nMessage-ID: <0100abc@email.amazonses.com>\r\nX-Github-Sender: someone\r\nSubject: s\r\n\r\n",
        b"plain",
        None,
        0.0,
    ),
    (
        "multipart_mixed_attachments",
        crlf(
            "From: billing@utility.example.test\n"
            "To: me@example.test\n"
            "Subject: Your statement for September 2026\n"
            "Date: Thu, 01 Oct 2026 06:00:00 -0400\n"
            "MIME-Version: 1.0\n"
            "Content-Type: multipart/mixed; boundary=\"outer\"\n\n"
        ).encode(),
        crlf(
            "--outer\n"
            "Content-Type: multipart/related; boundary=\"inner\"\n\n"
            "--inner\n"
            "Content-Type: text/html; charset=utf-8\n\n"
            "<p>Your statement is attached. Amount due: $88.10</p><img src=\"cid:logo\">\n"
            "--inner\n"
            "Content-Type: image/png\n"
            "Content-ID: <logo>\n"
            "Content-Disposition: inline; filename=\"logo.png\"\n"
            "Content-Transfer-Encoding: base64\n\n"
            "iVBORw0KGgo=\n"
            "--inner--\n"
            "--outer\n"
            "Content-Type: application/pdf; name=\"Statement-2026-09.pdf\"\n"
            "Content-Disposition: attachment; filename=\"Statement-2026-09.pdf\"\n"
            "Content-Transfer-Encoding: base64\n\n"
            "JVBERi0xLjQK\n"
            "--outer\n"
            "Content-Type: text/csv\n"
            "Content-Disposition: attachment; filename*=UTF-8''r%C3%A9sum%C3%A9%2012.csv\n\n"
            "a,b\n1,2\n"
            "--outer\n"
            "Content-Type: application/octet-stream\n"
            "Content-Disposition: attachment\n\n"
            "raw\n"
            "--outer--\n"
        ).encode(),
        None,
        0.0,
    ),
    (
        "attachments_from_bodystructure",
        b"From: a@b.example.test\r\nSubject: with atts\r\nContent-Type: text/plain\r\n\r\n",
        b"see attached",
        [["application/pdf", "Invoice-2026-03.pdf"], ["image/png", ""], ["application/pdf", "Statement 12 of 12.PDF"], ["text/csv", "a1.csv"], ["text/csv", "b22.csv"]],
        0.0,
    ),
    (
        "otp_and_currency_edges",
        b"From: verify@auth.example.test\r\nSubject: Your login code\r\nContent-Type: text/plain\r\n\r\n",
        b"Your one-time passcode: 123456\r\nNot codes: $654321 #111111 1234567 222222.5 -333333 4.444444\r\nAmounts: 12.50 usd, cad 5, 7.25\r\n",
        None,
        0.0,
    ),
    (
        "otp_beyond_600_chars",
        b"From: a@b.example.test\r\nSubject: late code\r\n\r\n",
        b"x" * 650 + b" 987654",
        None,
        0.0,
    ),
    (
        "windows1252_8bit",
        b"From: \xe9mile <emile@example.test>\r\nSubject: Caf\xe9\r\nContent-Type: text/plain; charset=windows-1252\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
        b"\x93Quoted\x94 \x96 dash \x80 5.00 \xe9t\xe9\r\n",
        None,
        0.0,
    ),
    (
        "eight_bit_no_charset",
        b"From: a@b.example.test\r\nSubject: raw 8bit\r\n\r\n",
        b"caf\xc3\xa9 and caf\xe9\r\n",
        None,
        0.0,
    ),
    (
        "unknown_charset",
        b"From: a@b.example.test\r\nSubject: unknown\r\nContent-Type: text/plain; charset=x-unknown-42\r\n\r\n",
        b"text in unknown charset\r\n",
        None,
        0.0,
    ),
    (
        "lf_line_endings_folded_subject",
        b"From: Folks <folks@example.test>\nSubject: A very long subject line that\n is folded over two lines #9\nTo: undisclosed-recipients:;\nMessage-ID: no-brackets@example.test\n\n",
        b"Body with\n\nseveral   spaces\tand tabs\n",
        None,
        0.0,
    ),
    (
        "group_and_odd_addresses",
        crlf(
            "From: \"Last, First\" <First.Last@Example.TEST>\n"
            "To: Team: a@example.test, B <b@example.test>;, c@example.test\n"
            "Cc: <>\n"
            "Cc: d@example.test (comment)\n"
            "Reply-To: \n"
            "Return-Path: <>\n"
            "Subject: groups\n\n"
        ).encode(),
        b"hi",
        None,
        0.0,
    ),
    (
        "from_without_address",
        b"From: Just A Name\r\nSubject: no addr\r\n\r\n",
        b"hi",
        None,
        0.0,
    ),
    (
        "duplicate_from",
        b"From: first@example.test\r\nFrom: second@example.test\r\nSubject: two froms\r\n\r\n",
        b"hi",
        None,
        0.0,
    ),
    (
        "calendar_only",
        b"From: cal@example.test\r\nSubject: Invitation\r\nContent-Type: text/calendar; method=REQUEST\r\n\r\n",
        b"BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n",
        None,
        0.0,
    ),
    (
        "forwarded_rfc822",
        crlf(
            "From: me@example.test\n"
            "Subject: Fwd: Receipt\n"
            "MIME-Version: 1.0\n"
            "Content-Type: multipart/mixed; boundary=\"f\"\n\n"
        ).encode(),
        crlf(
            "--f\n"
            "Content-Type: text/plain\n\n"
            "See below.\n"
            "--f\n"
            "Content-Type: message/rfc822\n\n"
            "From: shop@example.test\n"
            "Subject: Receipt\n\n"
            "Receipt total $9.99\n"
            "--f--\n"
        ).encode(),
        None,
        0.0,
    ),
    (
        "broken_multipart",
        b"From: a@b.example.test\r\nSubject: broken\r\nContent-Type: multipart/alternative; boundary=\"missing\"\r\n\r\n",
        b"no boundary lines here\r\n",
        None,
        0.0,
    ),
    (
        "empty_plain_falls_back_to_html",
        crlf(
            "From: a@b.example.test\n"
            "Subject: empty plain\n"
            "Content-Type: multipart/alternative; boundary=\"e\"\n\n"
        ).encode(),
        crlf(
            "--e\n"
            "Content-Type: text/plain\n\n"
            "   \n"
            "--e\n"
            "Content-Type: text/html\n\n"
            "<p>From the <i>HTML</i> part</p>\n"
            "--e--\n"
        ).encode(),
        None,
        0.0,
    ),
    (
        "long_body",
        b"From: a@b.example.test\r\nSubject: long\r\n\r\n",
        LONG_BODY.encode(),
        None,
        0.0,
    ),
    (
        "header_only_no_separator",
        b"From: a@b.example.test\r\nSubject: header only",
        b"",
        None,
        0.0,
    ),
    (
        "nul_and_controls",
        b"From: a@b.example.test\r\nSubject: ctl\x07 bell\r\n\r\n",
        b"nul\x00byte and \x1b escape\r\n",
        None,
        0.0,
    ),
]


def build():
    records = [{"fn": "subject_template", "input": [s], "output": features.subject_template(s)} for s in SUBJECTS]
    for name in ("Invoice-2026-03.pdf", "IMG_0042.JPG", "", "report.v2.10.docx", "１２.pdf"):
        records.append({"fn": "filename_pattern", "input": [name], "output": features.filename_pattern(name)})
    for value in ("", "<a@b>", " < a@b > ", "<a@b> <c@d>", "<a@b><c@d>", "a@b", "<a b@c>", "<<x@y>>"):
        records.append({"fn": "msgid_list", "input": [value], "output": features.msgid_list(value)})
        records.append({"fn": "normalize_msgid", "input": [value], "output": features.normalize_msgid(value)})
    for addr in ("A@B.Example", "<x@y.test>", "no-at-sign", "a@b@c.test", "", "x@<y.test>"):
        records.append({"fn": "domain_of", "input": [addr], "output": features.domain_of(addr)})
    fixtures = {}
    for case, header_bytes, text_bytes, attachments, fallback_ts in MESSAGES:
        path = f"{case}.eml"
        fixtures[path] = header_bytes + text_bytes
        atts = None if attachments is None else [tuple(a) for a in attachments]
        records.append(
            {
                "fn": "extract",
                "case": case,
                "file": f"features/{path}",
                "header_len": len(header_bytes),
                "attachments": attachments,
                "fallback_ts": fallback_ts,
                "output": features.extract(header_bytes, text_bytes, atts, fallback_ts=fallback_ts),
                "digest": features.content_digest(header_bytes, text_bytes),
            }
        )
    return records, fixtures


if __name__ == "__main__":
    records, fixtures = build()
    emit(
        "features",
        records,
        {
            "source": "src/atrium/features.py",
            "functions": ["subject_template", "filename_pattern", "msgid_list", "normalize_msgid", "domain_of", "extract", "content_digest"],
            "fts_body_chars": features.FTS_BODY_CHARS,
        },
        fixtures,
    )
