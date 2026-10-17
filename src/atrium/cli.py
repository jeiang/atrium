import argparse
import json
import logging
import os
import signal
import sys

import uvicorn

from . import (
    applier,
    backlog,
    calendar,
    config,
    db,
    deletions,
    embed,
    event_candidates,
    gmail_labels,
    google_auth,
    handlers,
    live,
    money,
    notion_api,
    notion_migrate,
    notion_search,
    notion_sync,
    openloops,
    proposals,
    rebalance,
    report,
    rules,
    run,
    scans,
    watch,
    watches,
)
from .agent import builtin_jobs, scheduler, service
from .agent.tools.base import CHAT, KINDS
from .config import ACCOUNTS
from .media import channels
from .web import auth

DEFAULT_HOST = "127.0.0.1"
NOTION_LOCK = "notion"


def parser():
    p = argparse.ArgumentParser(prog="atrium")
    sub = p.add_subparsers(dest="command", required=True)
    s = sub.add_parser("sync")
    s.add_argument("--account", choices=sorted(ACCOUNTS))
    w = sub.add_parser("watch")
    w.add_argument("--account", choices=sorted(ACCOUNTS))
    r = sub.add_parser("rules")
    rsub = r.add_subparsers(dest="rules_command", required=True)
    ri = rsub.add_parser("import")
    ri.add_argument("file")
    rsub.add_parser("export")
    sr = sub.add_parser("shadow-report")
    sr.add_argument("--since")
    sub.add_parser("reindex")
    rb = sub.add_parser("rebalance")
    rb.add_subparsers(dest="rebalance_command", required=True).add_parser("scan")
    dl = sub.add_parser("deletions")
    dl.add_subparsers(dest="deletions_command", required=True).add_parser("scan")
    gl = sub.add_parser("gmail-labels")
    gl.add_subparsers(dest="gmail_labels_command", required=True).add_parser("scan")
    lv = sub.add_parser("live")
    lsub = lv.add_subparsers(dest="live_command", required=True)
    for name in ("backlog", "rebaseline", "pause", "resume", "status", "deletions"):
        lsub.add_parser(name)
    la = lsub.add_parser("actions")
    la.add_argument("--limit", type=int, default=20)
    la.add_argument("--kind", choices=live.KINDS)
    lu = lsub.add_parser("undo")
    lu.add_argument("id", type=int)
    pr = sub.add_parser("proposals")
    psub = pr.add_subparsers(dest="proposals_command", required=True)
    psub.add_parser("scan-all")
    pl = psub.add_parser("list")
    pl.add_argument("--status", choices=proposals.STATUSES)
    pl.add_argument("--kind")
    for name in ("approve", "dismiss", "undo"):
        pp = psub.add_parser(name)
        pp.add_argument("id", type=int)
        if name == "approve":
            pp.add_argument("--selection", help="JSON selection document")
        if name == "dismiss":
            pp.add_argument("--forever", action="store_true")
    n = sub.add_parser("notion")
    nsub = n.add_subparsers(dest="notion_command", required=True)
    nsub.add_parser("sync")
    ns = nsub.add_parser("search")
    ns.add_argument("query")
    ns.add_argument("--private", action="store_true")
    ns.add_argument("--limit", type=int, default=10)
    nm = nsub.add_parser("migrate").add_subparsers(dest="migrate_command", required=True)
    nm.add_parser("ensure-schema")
    mi = nm.add_parser("inventory")
    mi.add_argument("--out")
    mp = nm.add_parser("plan")
    mp.add_argument("--inventory")
    mp.add_argument("--out")
    mp.add_argument("--skip-source", action="append", help="source id or title to skip; repeatable")
    for name in ("apply", "verify", "rollback"):
        mm = nm.add_parser(name)
        mm.add_argument("--manifest", required=True)
        if name != "verify":
            mm.add_argument("--limit", type=int)
    ag = sub.add_parser("agent")
    asub = ag.add_subparsers(dest="agent_command", required=True)
    asub.add_parser("scheduler")
    ar = asub.add_parser("run")
    ar.add_argument("--thread", type=int, help="existing thread id; omitted creates a new thread of --kind")
    ar.add_argument("--text", required=True)
    ar.add_argument("--kind", choices=KINDS, default=CHAT)
    cal = sub.add_parser("calendar")
    csub = cal.add_subparsers(dest="calendar_command", required=True)
    csub.add_parser("candidates")
    ga = csub.add_parser("google-auth")
    ga.add_argument("--port", type=int, default=0)
    lo = sub.add_parser("loops")
    lo.add_subparsers(dest="loops_command", required=True).add_parser("refresh")
    wt = sub.add_parser("watches")
    wt.add_subparsers(dest="watches_command", required=True).add_parser("refresh")
    mo = sub.add_parser("money")
    mr = mo.add_subparsers(dest="money_command", required=True).add_parser("refresh")
    mr.add_argument("--limit", type=int, default=money.CLI_SCAN_LIMIT, help="messages to extract in this run")
    md = sub.add_parser("media")
    msub = md.add_subparsers(dest="media_command", required=True)
    msub.add_parser("poll", help="poll EZTV for every channel now")
    msub.add_parser("sweep", help="select files in new media torrents and remove filed torrents qBittorrent stopped")
    sv = sub.add_parser("serve")
    sv.add_argument("--host", default=DEFAULT_HOST)
    sv.add_argument("--port", type=int, required=True)
    sv.add_argument("--auth", choices=auth.MODES, default=auth.PROXY)
    sv.add_argument("--allowed-host", action="append", default=[], help="accepted Host header; repeatable (proxy mode requires one, or ATRIUM_PUBLIC_HOST)")
    sub.add_parser("codemode-worker")
    return p


def calendar_command(cfg, args):
    if args.calendar_command == "google-auth":
        try:
            token = google_auth.authorize(
                os.environ.get("GOOGLE_OAUTH_CLIENT_ID"), os.environ.get("GOOGLE_OAUTH_CLIENT_SECRET"), port=args.port
            )
        except calendar.CalendarError as e:
            print(f"error: {e}", file=sys.stderr)
            return 1
        print(f"\nGOOGLE_REFRESH_TOKEN={token}")
        return 0
    handlers.register()
    conn = db.connect(cfg.db_path)
    out = event_candidates.draft_event_proposals(cfg, conn)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def loops_command(cfg):
    handlers.register()
    conn = db.connect(cfg.db_path)
    with db.exclusive(cfg.db_path):
        out = openloops.refresh(conn, cfg)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def watches_command(cfg):
    handlers.register()
    conn = db.connect(cfg.db_path)
    with db.exclusive(cfg.db_path):
        out = watches.refresh(conn, cfg)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def money_command(cfg, args):
    handlers.register()
    conn = db.connect(cfg.db_path)
    with db.exclusive(cfg.db_path):
        out = money.refresh(conn, cfg, limit=args.limit)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def media_command(cfg, args):
    conn = db.connect(cfg.db_path)
    if args.media_command == "poll":
        out = builtin_jobs.media_channels(cfg, conn)
    else:
        out = builtin_jobs.media_sweep(cfg, conn)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def agent_command(cfg, args):
    handlers.register()
    if args.agent_command == "scheduler":
        signal.signal(signal.SIGTERM, lambda *_: signal.raise_signal(signal.SIGINT))
        try:
            scheduler.run_worker(cfg)
        except KeyboardInterrupt:
            pass
        return 0
    conn = db.connect(cfg.db_path)
    thread_id = args.thread if args.thread is not None else service.create_thread(conn, args.kind)
    run_id = service.run_once(cfg, conn, thread_id, args.text, kind=args.kind)
    json.dump(service.run_view(conn, run_id), sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def proposals_command(cfg, args):
    conn = db.connect(cfg.db_path)
    handlers.register()
    if args.command == "rebalance":
        out = rebalance.scan(conn, cfg)
    elif args.command == "deletions":
        out = deletions.scan(conn, cfg)
    elif args.command == "gmail-labels":
        out = gmail_labels.scan(conn, cfg)
    elif args.proposals_command == "scan-all":
        out = scans.scan_all(conn, cfg)
    elif args.proposals_command == "list":
        out = [
            {k: p[k] for k in ("id", "kind", "account", "status", "title", "undoable")}
            for p in proposals.list(conn, args.status, args.kind)
        ]
    elif args.proposals_command == "approve":
        selection = json.loads(args.selection) if args.selection else None
        out = proposals.approve_and_apply(conn, cfg, args.id, selection)
    elif args.proposals_command == "dismiss":
        proposals.dismiss(conn, args.id, args.forever)
        out = {"id": args.id, "dismissed": True}
    else:
        out = proposals.undo(conn, cfg, args.id)
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def live_command(cfg, args):
    conn = db.connect(cfg.db_path)
    handlers.register()
    if args.live_command == "backlog":
        try:
            out = backlog.backlog(conn, cfg)
        except backlog.BacklogError as e:
            print(f"error: {e}", file=sys.stderr)
            return 2
    elif args.live_command == "rebaseline":
        with db.exclusive(cfg.db_path):
            run.sync_all(conn, cfg, run.open_client)
            baseline = live.rebaseline(conn)
            out = {"baseline": baseline, **backlog.refresh(conn, cfg)}
    elif args.live_command == "actions":
        out = [
            {k: a[k] for k in ("id", "ts", "account", "kind", "status", "src", "dest", "subject", "error", "undoable")}
            for a in live.list_actions(conn, kind=args.kind, limit=args.limit)
        ]
    elif args.live_command in ("pause", "resume"):
        live.set_paused(cfg, args.live_command == "pause")
        out = {"paused": live.paused(cfg)}
    elif args.live_command == "status":
        out = live.status(conn, cfg)
    elif args.live_command == "deletions":
        out = {}
        with db.exclusive(cfg.db_path):
            for account in run.select_accounts(cfg):
                done = applier.deletion_pass(conn, cfg, account, run.open_client, force=True)
                out[account] = {"ran": done.ran, "trashed": done.by_kind.get(live.TRASH, 0), "skipped": done.skipped, "failed": done.failed}
    else:
        try:
            out = live.undo(conn, cfg, args.id)
        except live.UndoError as e:
            print(f"error: {e}", file=sys.stderr)
            return 1
    json.dump(out, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


def notion_command(cfg, args):
    conn = db.connect(cfg.db_path)
    if args.notion_command == "sync":
        client = notion_api.NotionClient(notion_api.token_from_env())
        with db.exclusive(cfg.db_path.with_name(NOTION_LOCK)):
            stats = notion_sync.sync(conn, cfg, client)
        print(json.dumps({**vars(stats), **notion_sync.counts(conn)}))
    elif args.notion_command == "migrate":
        return notion_migrate.run_cli(cfg, args)
    else:
        result = notion_search.search(conn, cfg, args.query, include_private=args.private, limit=args.limit)
        for h in result["hits"]:
            print(f"{h['title']}\t{h['url']}")


def main(argv=None):
    args = parser().parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
    if args.command == "calendar" and args.calendar_command == "google-auth":
        return calendar_command(None, args)
    if args.command == "codemode-worker":
        from .agent import codemode_worker

        codemode_worker.main()
        return 0
    cfg = config.load()
    if args.command == "sync":
        run.run_sync(cfg, args.account)
    elif args.command == "watch":
        signal.signal(signal.SIGTERM, lambda *_: signal.raise_signal(signal.SIGINT))
        watch.watch(cfg, args.account)
    elif args.command == "rules":
        conn = db.connect(cfg.db_path)
        if args.rules_command == "import":
            try:
                if args.file == "-":
                    document = json.load(sys.stdin)
                else:
                    with open(args.file) as handle:
                        document = json.load(handle)
                count = rules.import_rules(conn, document)
            except rules.RuleError as e:
                print(f"error: {e}", file=sys.stderr)
                return 2
            print(f"imported {count} rules")
        else:
            json.dump(rules.export_rules(conn), sys.stdout, indent=2)
            sys.stdout.write("\n")
    elif args.command == "shadow-report":
        sys.stdout.write(report.build(db.connect(cfg.db_path), args.since))
    elif args.command == "reindex":
        print(f"embedded {embed.reindex(db.connect(cfg.db_path), cfg.embed_url)} messages")
    elif args.command in ("rebalance", "deletions", "gmail-labels", "proposals"):
        return proposals_command(cfg, args)
    elif args.command == "live":
        return live_command(cfg, args)
    elif args.command == "notion":
        return notion_command(cfg, args) or 0
    elif args.command == "calendar":
        return calendar_command(cfg, args)
    elif args.command == "agent":
        return agent_command(cfg, args)
    elif args.command == "loops":
        return loops_command(cfg)
    elif args.command == "watches":
        return watches_command(cfg)
    elif args.command == "money":
        return money_command(cfg, args)
    elif args.command == "media":
        return media_command(cfg, args)
    elif args.command == "serve":
        from .web.app import create_app

        hosts = args.allowed_host or [h for h in [os.environ.get("ATRIUM_PUBLIC_HOST", "").strip()] if h]
        if args.auth == auth.PROXY and not hosts:
            raise SystemExit("proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST")
        uvicorn.run(create_app(cfg, auth_mode=args.auth, allowed_hosts=hosts), host=args.host, port=args.port, log_level="info")
    return 0


if __name__ == "__main__":
    sys.exit(main())
