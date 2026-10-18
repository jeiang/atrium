import base64
import importlib.metadata
import json
import os
import sys
import time
import zoneinfo
from pathlib import Path

# Python reads naive times (a Date header without a zone) in the process zone; pin it so the host does not leak in.
# libc gets the zone file by path: the Linux build sandbox has no /usr/share/zoneinfo.
PROCESS_TZ = "America/Port_of_Spain"
os.environ["TZ"] = next((f":{p}" for p in (Path(base) / PROCESS_TZ for base in zoneinfo.TZPATH) if p.is_file()), PROCESS_TZ)
time.tzset()


ROOT = Path(__file__).resolve().parents[3]
OUT = ROOT / "testdata" / "corpora"
FREEZE_COMMIT = "cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40"


def use_source_tree():
    src = str(ROOT / "src")
    if src not in sys.path:
        sys.path.insert(0, src)


def b64(data):
    return base64.b64encode(data).decode("ascii")


def error(e):
    return {"type": type(e).__name__, "message": str(e)}


def versions(*distributions):
    return {d: importlib.metadata.version(d) for d in distributions}


def line(record):
    return json.dumps(record, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n"


def emit(name, records, manifest, fixtures=None):
    """Writes testdata/corpora/<name>.jsonl, <name>_manifest.json and the fixture files under <name>/;
    with --check, compares a fresh generation against the committed files instead."""
    fixtures = fixtures or {}
    manifest = {
        "corpus": name,
        "freeze_commit": FREEZE_COMMIT,
        "python": f"{sys.version_info.major}.{sys.version_info.minor}",
        "process_tz": PROCESS_TZ,
        "records": len(records),
        "fixtures": sorted(f"{name}/{p}" for p in fixtures),
        **manifest,
    }
    wanted = {
        OUT / f"{name}.jsonl": "".join(line(r) for r in records).encode(),
        OUT / f"{name}_manifest.json": (json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode(),
        **{OUT / name / p: data for p, data in fixtures.items()},
    }
    fixture_dir = OUT / name
    present = {p for p in fixture_dir.rglob("*") if p.is_file()} if fixture_dir.is_dir() else set()
    stale = present - set(wanted)
    if sys.argv[1:] == ["--check"]:
        errors = [f"{p.relative_to(ROOT)} differs from a fresh generation" for p, data in wanted.items() if not p.is_file() or p.read_bytes() != data]
        errors += [f"{p.relative_to(ROOT)} is not generated" for p in sorted(stale)]
        if errors:
            print("\n".join(errors), file=sys.stderr)
            print(f"regenerate: python tools/golden/corpora/gen_{name}.py", file=sys.stderr)
            raise SystemExit(1)
        print(f"ok: {name}: {len(records)} records, {len(fixtures)} fixtures")
        return
    if sys.argv[1:]:
        raise SystemExit(f"usage: gen_{name}.py [--check]")
    for p in stale:
        p.unlink()
    for p, data in wanted.items():
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(data)
