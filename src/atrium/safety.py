import contextlib
import contextvars
import os
from pathlib import Path

LIVE = "live"
SHADOW = "shadow"
MODES = (SHADOW, LIVE)
APPEND_ACTION = "IMAP APPEND"

_approved = contextvars.ContextVar("atrium_approved_proposal", default=None)
_operator_send = contextvars.ContextVar("atrium_operator_send_mailbox", default=None)
_automatic = contextvars.ContextVar("atrium_automatic_pause_path", default=None)


class WriteRefused(RuntimeError):
    pass


def mode_from_env(environ=None):
    value = (environ if environ is not None else os.environ).get("ATRIUM_MODE", SHADOW)
    if value not in MODES:
        raise ValueError(f"ATRIUM_MODE must be one of {MODES}, got {value!r}")
    return value


@contextlib.contextmanager
def approved_writes(proposal_id):
    token = _approved.set(proposal_id)
    try:
        yield
    finally:
        _approved.reset(token)


@contextlib.contextmanager
def operator_send(sent_mailbox):
    """Allow IMAP APPEND of the sent copy to exactly this mailbox; no other mailbox write."""
    token = _operator_send.set(sent_mailbox)
    try:
        yield
    finally:
        _operator_send.reset(token)


@contextlib.contextmanager
def automatic_writes(pause_path):
    token = _automatic.set(Path(pause_path))
    try:
        yield
    finally:
        _automatic.reset(token)


def approved_proposal():
    return _approved.get()


def is_paused(pause_path):
    return Path(pause_path).exists()


def set_paused(pause_path, paused):
    path = Path(pause_path)
    if paused:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.touch()
    else:
        path.unlink(missing_ok=True)


def write_gate(action, environ=None, target=None):
    pause_path = _automatic.get()
    if pause_path is not None and is_paused(pause_path):
        raise WriteRefused(f"refusing automatic mailbox write {action!r}: atrium is paused")
    if _approved.get() is not None:
        return
    sent = _operator_send.get()
    if sent is not None and action == APPEND_ACTION and target == sent:
        return
    if mode_from_env(environ) != LIVE:
        raise WriteRefused(f"refusing mailbox write {action!r}: ATRIUM_MODE is not {LIVE}")
