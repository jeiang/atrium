import hashlib
import json
import os
import stat
import sys
from pathlib import Path

FREEZE_COMMIT = "cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40"
ROOT = Path(__file__).resolve().parents[2]
GOLDEN = ROOT / "testdata" / "golden"


def _tree_id(directory):
    entries = []
    for entry in os.scandir(directory):
        if entry.name == "__pycache__" or entry.name.endswith(".pyc"):
            continue
        info = entry.stat(follow_symlinks=False)
        if entry.is_dir(follow_symlinks=False):
            mode, oid = b"40000", _tree_id(entry.path)
        else:
            if stat.S_ISLNK(info.st_mode):
                mode, data = b"120000", os.readlink(entry.path).encode()
            else:
                mode, data = (b"100755" if info.st_mode & 0o100 else b"100644"), Path(entry.path).read_bytes()
            oid = hashlib.sha1(b"blob %d\0" % len(data) + data).digest()
        sort_name = entry.name.encode() + (b"/" if mode == b"40000" else b"")
        entries.append((sort_name, mode + b" " + entry.name.encode() + b"\0" + oid))
    body = b"".join(raw for _, raw in sorted(entries))
    return hashlib.sha1(b"tree %d\0" % len(body) + body).digest()


def generating_tree():
    return _tree_id(ROOT / "src" / "atrium").hex()


GENERATED_FROM = generating_tree()


def use_source_tree():
    for path in (ROOT / "src", ROOT / "tests"):
        if str(path) not in sys.path:
            sys.path.insert(0, str(path))


def dumps(value):
    return json.dumps(value, sort_keys=True, indent=2, ensure_ascii=False) + "\n"


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(dumps(value), encoding="utf-8")


def out_dir(slice_name):
    """The slice's output directory: argv[1] when given (a scratch copy for the determinism check), else testdata/golden/<slice>."""
    return Path(sys.argv[1]) if len(sys.argv) > 1 else GOLDEN / slice_name
