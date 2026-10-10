import os

LIVE = "live"
SHADOW = "shadow"
MODES = (SHADOW, LIVE)


class WriteRefused(RuntimeError):
    pass


def mode_from_env(environ=None):
    value = (environ if environ is not None else os.environ).get("ATRIUM_MODE", SHADOW)
    if value not in MODES:
        raise ValueError(f"ATRIUM_MODE must be one of {MODES}, got {value!r}")
    return value


def write_gate(action, environ=None):
    if mode_from_env(environ) != LIVE:
        raise WriteRefused(f"refusing mailbox write {action!r}: ATRIUM_MODE is not {LIVE}")
