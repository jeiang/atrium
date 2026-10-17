"""Links to library files: a public signed URL (HMAC over path and expiry, 12 h) that bypasses tinyauth, and the
VPN direct URL served by darkhttpd on artemis."""

import hashlib
import hmac
import os
import secrets
import time
from urllib.parse import parse_qs, quote

from . import parse
from .settings import LINK_TTL_SECONDS

FILE_PREFIX = "/media/file/"
KEY_BYTES = 32


def key(settings):
    """HMAC key from ATRIUM_MEDIA_LINK_KEY, else one created once and kept in the state dir."""
    if settings.link_key:
        return settings.link_key.encode()
    path = settings.key_path
    try:
        return path.read_bytes().strip()
    except FileNotFoundError:
        pass
    fresh = secrets.token_hex(KEY_BYTES).encode()
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:  # the other atrium process won the race
        return path.read_bytes().strip()
    with os.fdopen(fd, "wb") as handle:
        handle.write(fresh)
    return fresh


def sign(secret, rel, exp):
    return hmac.new(secret, f"{rel}\n{int(exp)}".encode(), hashlib.sha256).hexdigest()


def verify(secret, rel, exp, sig, now=None):
    try:
        exp = int(exp)
    except (TypeError, ValueError):
        return False
    if exp <= (time.time() if now is None else now):
        return False
    return hmac.compare_digest(sign(secret, rel, exp), (sig or "").strip().lower())


def public_url(settings, secret, rel, now=None, ttl=LINK_TTL_SECONDS):
    exp = int((time.time() if now is None else now) + ttl)
    return f"{settings.public_url}{FILE_PREFIX}{quote(rel)}?exp={exp}&sig={sign(secret, rel, exp)}"


def vpn_url(settings, rel):
    """darkhttpd roots at movies/ and tv/, so the first path element picks the host."""
    top, _, rest = rel.partition("/")
    base = settings.movies_url if top == parse.MOVIES else settings.tv_url if top == parse.TV else ""
    return f"{base}/{quote(rest)}" if base else ""


def verifier(get_key):
    """For the web Guard: True when an ASGI scope is a validly signed /media/file/ request. get_key() is called lazily
    so the key file is only created when a public link is first checked or made."""

    def check(scope):
        path = scope.get("path") or ""
        if not path.startswith(FILE_PREFIX):
            return False
        query = parse_qs(scope.get("query_string", b"").decode("latin-1"))
        exp, sig = next(iter(query.get("exp", [])), None), next(iter(query.get("sig", [])), None)
        return verify(get_key(), path[len(FILE_PREFIX):], exp, sig)

    return check
