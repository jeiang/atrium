import logging
import threading

from . import db
from .config import (
    BACKOFF_MAX,
    BACKOFF_START,
    IDLE_SECONDS,
    IDLE_SETTLE_SECONDS,
    INBOX,
)
from .run import open_client, process, select_accounts, sync_one

log = logging.getLogger("atrium.watch")


def next_backoff(current):
    return min(current * 2, BACKOFF_MAX) if current else BACKOFF_START


def wait_for_change(client, stop):
    if "IDLE" not in client.capabilities:
        stop.wait(IDLE_SECONDS / 30)
        client.noop()
        return True
    client.select(INBOX)
    return client.idle_changes(IDLE_SECONDS)


def watch_account(cfg, account, stop, opener=open_client, sleep=None):
    sleep = sleep or stop.wait
    backoff = 0
    while not stop.is_set():
        conn = None
        client = None
        try:
            conn = db.connect(cfg.db_path)
            sync_one(conn, cfg, account, opener)
            process(conn, cfg, [account])
            client = opener(cfg, account)
            backoff = 0
            while not stop.is_set():
                changed = wait_for_change(client, stop)
                if stop.is_set():
                    break
                if changed:
                    sleep(IDLE_SETTLE_SECONDS)
                sync_one(conn, cfg, account, opener)
                process(conn, cfg, [account])
        except Exception:
            backoff = next_backoff(backoff)
            log.exception("%s watch failed, retrying in %ds", account, backoff)
            sleep(backoff)
        finally:
            if client is not None:
                client.close()
            if conn is not None:
                conn.close()


def watch(cfg, requested=None):
    stop = threading.Event()
    accounts = select_accounts(cfg, requested)
    threads = [
        threading.Thread(target=watch_account, args=(cfg, a, stop), name=f"watch-{a}", daemon=True)
        for a in accounts
    ]
    for t in threads:
        t.start()
    try:
        while any(t.is_alive() for t in threads):
            stop.wait(1)
    except KeyboardInterrupt:
        pass
    finally:
        stop.set()
    for t in threads:
        t.join(timeout=30)
