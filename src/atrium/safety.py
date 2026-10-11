import contextlib
import contextvars
import os

LIVE = "live"
SHADOW = "shadow"
MODES = (SHADOW, LIVE)

_approved = contextvars.ContextVar("atrium_approved_proposal", default=None)


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


def approved_proposal():
    return _approved.get()


def write_gate(action, environ=None):
    if _approved.get() is not None:
        return
    if mode_from_env(environ) != LIVE:
        raise WriteRefused(f"refusing mailbox write {action!r}: ATRIUM_MODE is not {LIVE}")
