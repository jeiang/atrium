import logging

from . import db, embed, flags, router
from .config import ACCOUNTS
from .http import ServiceUnavailable
from .imap import ImapClient
from .sync import sync_account

log = logging.getLogger("atrium.run")


def open_client(cfg, account):
    creds = cfg.credential(account)
    if creds is None:
        raise RuntimeError(f"missing credentials for {account}")
    return ImapClient(ACCOUNTS[account].host, *creds).connect()


def select_accounts(cfg, requested=None):
    if requested:
        if cfg.credential(requested) is None:
            raise RuntimeError(f"missing credentials for {requested}")
        return [requested]
    return [a for a in ACCOUNTS if cfg.credential(a) is not None]


def sync_one(conn, cfg, account, opener=open_client):
    client = opener(cfg, account)
    try:
        stats = sync_account(conn, cfg, account, client)
    finally:
        client.close()
    log.info(
        "%s: %d new, %d gone, %d flag changes", account, stats.new, len(stats.gone_ids), stats.flag_changes
    )
    return stats


def process(conn, cfg, accounts):
    try:
        embed.embed_pending(conn, cfg.embed_url)
    except ServiceUnavailable as e:
        log.warning("embedding unavailable: %s", e)
    for account in accounts:
        routed = router.route_account(conn, cfg, account)
        flagged = flags.flag_account(conn, cfg, account)
        log.info("%s: %d routed, %d flag judgments", account, routed, flagged)


def run_sync(cfg, requested=None, opener=open_client):
    conn = db.connect(cfg.db_path)
    accounts = select_accounts(cfg, requested)
    with db.exclusive(cfg.db_path):
        for account in accounts:
            sync_one(conn, cfg, account, opener)
        process(conn, cfg, accounts)
    return accounts
