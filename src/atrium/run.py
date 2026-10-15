import logging

from . import db, embed, event_candidates, flags, live, router, safety
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


def sync_all(conn, cfg, opener):
    for account in select_accounts(cfg):
        sync_one(conn, cfg, account, opener)


def process(conn, cfg, accounts, opener=open_client):
    from . import applier

    if cfg.mode == safety.LIVE:
        live.ensure_golive(conn, lambda: sync_all(conn, cfg, opener))
    try:
        embed.embed_pending(conn, cfg.embed_url)
    except ServiceUnavailable as e:
        log.warning("embedding unavailable: %s", e)
    for account in accounts:
        routed = router.route_account(conn, cfg, account)
        flagged = flags.flag_account(conn, cfg, account)
        log.info("%s: %d routed, %d flag judgments", account, routed, flagged)
        try:
            invites, candidates = event_candidates.scan_account(conn, cfg, account)
            log.info("%s: %d invitation proposals, %d event candidates", account, invites, candidates)
        except Exception:
            log.exception("%s: event scan failed", account)
        try:
            applier.apply_all(conn, cfg, account, opener)
        except Exception:
            log.exception("%s: live pass failed", account)


def run_sync(cfg, requested=None, opener=open_client):
    conn = db.connect(cfg.db_path)
    accounts = select_accounts(cfg, requested)
    with db.exclusive(cfg.db_path):
        for account in accounts:
            sync_one(conn, cfg, account, opener)
        process(conn, cfg, accounts, opener)
    return accounts
