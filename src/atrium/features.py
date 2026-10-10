import email
import email.policy
import hashlib
import html
import re
from email.utils import getaddresses, parseaddr, parsedate_to_datetime

from .config import FTS_BODY_CHARS

MONTH = r"(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?"
CURRENCY = re.compile(
    r"(?:[$€£]\s?\d[\d,]*\.\d{2})|(?:\b(?:usd|ttd|tt\$|cad)\s?\d[\d,]*(?:\.\d{2})?)|(?:\b\d[\d,]*\.\d{2}\s?(?:usd|ttd|cad)\b)",
    re.I,
)
OTP = re.compile(r"(?<![\d.,$#-])\d{6}(?![\d.,-])")
VCODE = re.compile(r"verification code|security code|one[- ]time|passcode|your code|login code")
REPLY_PREFIX = re.compile(r"^\s*(re|fwd?|fw|aw)\s*:\s*", re.I)
ESP_EXACT = {
    "x-amazon-ses": "ses",
    "x-mg-id": "mailgun",
    "x-sendgrid-eid": "sendgrid",
    "x-mc-user": "mandrill",
    "x-pm-message-id": "postmark",
    "x-shopid": "shopify",
    "x-campaign": "mailchimp",
    "x-campaignid": "mailchimp",
    "x-mailchimp-id": "mailchimp",
    "x-mcid": "mailchimp",
    "x-github-reason": "github",
    "x-github-sender": "github",
}
ESP_PREFIX = (
    ("x-ses", "ses"),
    ("x-mailgun", "mailgun"),
    ("x-sg", "sendgrid"),
    ("x-mandrill", "mandrill"),
    ("x-postmark", "postmark"),
    ("x-sparkpost", "sparkpost"),
    ("x-msys", "sparkpost"),
    ("x-shopify", "shopify"),
)


def subject_template(subject):
    s = subject.strip()
    while True:
        n = REPLY_PREFIX.sub("", s)
        if n == s:
            break
        s = n
    s = s.lower()
    s = re.sub(
        r"(?:[$€£]|usd|ttd|tt\$|us\$|cad)\s?\d[\d,]*(?:\.\d+)?|\d[\d,]*(?:\.\d+)?\s?(?:usd|ttd|cad)",
        "<amt>",
        s,
    )
    s = re.sub(r"\b" + MONTH + r"\s+\d{1,2}(?:st|nd|rd|th)?(?:,?\s+\d{2,4})?", "<date>", s)
    s = re.sub(r"\b\d{1,2}(?:st|nd|rd|th)?\s+" + MONTH + r"(?:,?\s+\d{2,4})?", "<date>", s)
    s = re.sub(r"\b\d{1,4}[/-]\d{1,2}[/-]\d{1,4}\b", "<date>", s)
    s = re.sub(r"\b\w*\d\w*\b", "<n>", s)
    s = re.sub(r"#\s*<n>", "<n>", s)
    return re.sub(r"\s+", " ", s).strip()


def domain_of(addr):
    return addr.rsplit("@", 1)[-1].lower().strip("<>") if "@" in addr else ""


def strip_markup(text):
    text = re.sub(r"(?is)<(script|style).*?</\1>", " ", text)
    text = re.sub(r"<[^>]+>", " ", text)
    return re.sub(r"\s+", " ", html.unescape(text)).strip()


def header(msg, name):
    try:
        value = msg.get(name)
    except Exception:
        return ""
    return str(value) if value is not None else ""


def addresses(msg, *names):
    found = []
    for name in names:
        try:
            values = msg.get_all(name) or []
        except Exception:
            values = []
        found.extend(a.lower() for _, a in getaddresses([str(v) for v in values]) if a)
    return found


def filename_pattern(name):
    return re.sub(r"\d+", "#", name.lower())


def esp_of(msg):
    found = set()
    for key in msg.keys():
        k = key.lower()
        if k in ESP_EXACT:
            found.add(ESP_EXACT[k])
            continue
        for prefix, esp in ESP_PREFIX:
            if k.startswith(prefix):
                found.add(esp)
                break
        else:
            if "amazonses" in header(msg, key).lower()[:200]:
                found.add("ses")
    return sorted(found)


def body_text(msg):
    for kind, convert in (("plain", lambda t: re.sub(r"\s+", " ", t).strip()), ("html", strip_markup)):
        try:
            part = msg.get_body(preferencelist=(kind,))
            if part is not None:
                text = convert(part.get_content())
                if text:
                    return text
        except Exception:
            continue
    return ""


def normalize_msgid(value):
    return re.sub(r"[<>\s]", "", value)


def content_digest(header_bytes, text_bytes):
    h = hashlib.sha256()
    h.update(header_bytes)
    h.update(b"\x00")
    h.update(text_bytes)
    return h.hexdigest()


def extract(header_bytes, text_bytes, attachments=None, fallback_ts=0.0):
    msg = email.message_from_bytes(header_bytes + text_bytes, policy=email.policy.default)
    name, addr = parseaddr(header(msg, "From"))
    addr = addr.lower()
    try:
        date_ts = parsedate_to_datetime(header(msg, "Date")).timestamp()
    except Exception:
        date_ts = fallback_ts
    subject = header(msg, "Subject")
    body = body_text(msg)
    low = body[:4000].lower()
    reply_to = addresses(msg, "Reply-To")
    rpath = addresses(msg, "Return-Path")
    dkim = []
    for v in msg.get_all("DKIM-Signature") or []:
        m = re.search(r"\bd=([^;\s]+)", str(v))
        if m:
            dkim.append(m.group(1).lower())
    atts = attachments if attachments is not None else _attachments(msg)
    msgid = normalize_msgid(header(msg, "Message-ID"))
    return {
        "msgid": msgid,
        "msgid_domain": domain_of(msgid),
        "date_ts": date_ts,
        "from_addr": addr,
        "from_domain": domain_of(addr),
        "from_name": name,
        "to_addrs": sorted(set(addresses(msg, "To", "Delivered-To", "X-Original-To", "Cc"))),
        "reply_to": reply_to[0] if reply_to else "",
        "rpath_domain": domain_of(rpath[0]) if rpath else "",
        "list_id": header(msg, "List-Id").strip().lower(),
        "precedence": header(msg, "Precedence").strip().lower(),
        "subject": subject,
        "subject_tmpl": subject_template(subject),
        "dkim_d": sorted(set(dkim)),
        "esp": esp_of(msg),
        "att_types": sorted({t for t, _ in atts}),
        "att_names": sorted({filename_pattern(n) for _, n in atts if n})[:3],
        "has_money": bool(CURRENCY.search(body[:4000])),
        "k_statement": "statement" in low,
        "k_receipt": "receipt" in low,
        "k_order": "order" in low,
        "k_vcode": bool(VCODE.search(low)),
        "k_otp6": bool(OTP.search(body[:600])),
        "body_len": len(body),
        "body": body[:FTS_BODY_CHARS],
    }


def _attachments(msg):
    found = []
    try:
        for part in msg.iter_attachments():
            found.append((part.get_content_type(), part.get_filename() or ""))
    except Exception:
        pass
    return found
