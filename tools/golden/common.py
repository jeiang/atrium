import json
import sys
from pathlib import Path

FREEZE_COMMIT = "cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40"
ROOT = Path(__file__).resolve().parents[2]
GOLDEN = ROOT / "testdata" / "golden"


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
