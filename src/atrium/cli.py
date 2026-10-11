import argparse
import json
import logging
import signal
import sys

import uvicorn

from . import (
    config,
    db,
    deletions,
    embed,
    executors,
    gmail_labels,
    notion_api,
    notion_proposals,
    notion_search,
    notion_sync,
    proposals,
    rebalance,
    report,
    rules,
    run,
    scans,
    watch,
)
from .config import ACCOUNTS
from .web.app import create_app

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
    sv = sub.add_parser("serve")
    sv.add_argument("--host", default=DEFAULT_HOST)
    sv.add_argument("--port", type=int, required=True)
    return p


def proposals_command(cfg, args):
    conn = db.connect(cfg.db_path)
    scans.init_schemas(conn)
    executors.register()
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


def notion_command(cfg, args):
    conn = db.connect(cfg.db_path)
    notion_sync.init_schema(conn)
    if args.notion_command == "sync":
        client = notion_api.NotionClient(notion_api.token_from_env())
        with db.exclusive(cfg.db_path.with_name(NOTION_LOCK)):
            stats = notion_sync.sync(conn, cfg, client)
        print(json.dumps({**vars(stats), **notion_sync.counts(conn)}))
    else:
        result = notion_search.search(conn, cfg, args.query, include_private=args.private, limit=args.limit)
        for h in result["hits"]:
            print(f"{h['title']}\t{h['url']}")


def main(argv=None):
    args = parser().parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
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
        sys.stdout.write(report.build(db.connect(cfg.db_path), cfg, args.since))
    elif args.command == "reindex":
        print(f"embedded {embed.reindex(db.connect(cfg.db_path), cfg.embed_url)} messages")
    elif args.command in ("rebalance", "deletions", "gmail-labels", "proposals"):
        return proposals_command(cfg, args)
    elif args.command == "notion":
        notion_command(cfg, args)
    elif args.command == "serve":
        uvicorn.run(create_app(cfg), host=args.host, port=args.port, log_level="info")
    return 0


if __name__ == "__main__":
    sys.exit(main())
