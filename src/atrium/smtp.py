import email.utils
import re
import smtplib
from email.message import EmailMessage

from .config import ACCOUNTS, GMAIL, ICLOUD

SMTP_PORT = 587
SMTP_TIMEOUT = 60
SMTP_HOSTS = {ICLOUD: "smtp.mail.me.com", GMAIL: "smtp.gmail.com"}
ICLOUD_DOMAIN = "icloud.com"

SMTP_FACTORY = [smtplib.SMTP]


class SendError(RuntimeError):
    pass


def sender_address(account, user):
    if account == ICLOUD and "@" not in user:
        return f"{user}@{ICLOUD_DOMAIN}"
    return user


def split_addresses(text):
    text = re.sub(r"[\s,]*,[\s,]*", ", ", text or "").strip().strip(",")
    return [addr for _, addr in email.utils.getaddresses([text]) if "@" in addr and " " not in addr]


def build_message(sender, draft, now=None):
    msg = EmailMessage()
    msg["From"] = sender
    msg["To"] = draft["to_addrs"]
    if draft["cc_addrs"]:
        msg["Cc"] = draft["cc_addrs"]
    msg["Subject"] = draft["subject"]
    msg["Date"] = email.utils.formatdate(now, localtime=False)
    domain = sender.rsplit("@", 1)[-1]
    msg["Message-ID"] = email.utils.make_msgid(domain=domain)
    if draft["in_reply_to"]:
        msg["In-Reply-To"] = draft["in_reply_to"]
        msg["References"] = draft["refs"] or draft["in_reply_to"]
    msg.set_content(draft["body"])
    return msg


def send(cfg, account, msg):
    """STARTTLS submission. Returns recipients the server refused (empty on full success)."""
    creds = cfg.credential(account)
    if creds is None:
        raise SendError(f"{account} is not configured: {ACCOUNTS[account].user_env}/{ACCOUNTS[account].password_env} missing")
    user, password = creds
    recipients = [a for h in ("To", "Cc") for a in split_addresses(msg.get(h, ""))]
    if not recipients:
        raise SendError("no recipients")
    try:
        with SMTP_FACTORY[0](SMTP_HOSTS[account], SMTP_PORT, timeout=SMTP_TIMEOUT) as server:
            server.starttls()
            server.login(user, password)
            refused = server.send_message(msg, from_addr=sender_address(account, user), to_addrs=recipients)
    except (smtplib.SMTPException, OSError) as e:
        raise SendError(str(e)) from e
    return {addr: str(reply) for addr, reply in (refused or {}).items()}
