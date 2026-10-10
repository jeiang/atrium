import argparse
import json
import logging
import signal
import sys

from . import config, db, embed, report, rules, run, watch
from .config import ACCOUNTS


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
    return p


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
    return 0


if __name__ == "__main__":
    sys.exit(main())
