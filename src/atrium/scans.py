from . import deletions, gmail_labels, proposals, rebalance

SCANNERS = (
    ("rebalance", rebalance.scan),
    ("deletions", deletions.scan),
    ("gmail_labels", gmail_labels.scan),
)


def init_schemas(conn):
    proposals.init_schema(conn)
    rebalance.init_schema(conn)


def scan_all(conn, cfg):
    init_schemas(conn)
    return {name: scan(conn, cfg) for name, scan in SCANNERS}
