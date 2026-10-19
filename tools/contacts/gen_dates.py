"""Write internal/py/testdata/date_fromisoformat.json: date.fromisoformat's result (or null) for hand-picked and seeded random strings.

Run from the repo root: nix develop --impure -c python tools/contacts/gen_dates.py
"""

import datetime
import json
import random

OUT = "internal/py/testdata/date_fromisoformat.json"


def main():
    random.seed(1)
    cases = {
        "2026-10-16", "20261016", "2026-W42-5", "2026W425", "2026-W42", "2026W42", "2026-W53-1", "2020-W53-7",
        "2026-W53", "2026-W00-1", "2026-W01-0", "2026-W01-8", "2026-10-16x", "2026-10-1", "0000-01-01",
        "9999-W52-7", "9999-12-31", "2026-1016", "202610-16", "2026-W421", "2026W42-5", "0001-W01-1",
        "2026-02-29", "2024-02-29", "\uff12\uff10\uff12\uff16-10-16", "2026-10-16\0", "2026-10-16T", "",
        "2026-W42-5\u00e9", "\u00e92026-W4", "2026-10-1\u00e9",
    }
    for _ in range(3000):
        n = random.choice([7, 8, 10])
        cases.add("".join(random.choice("0123456789-W") if random.random() < 0.3 else random.choice("0123456789") for _ in range(n)))
    for y in (1, 2, 9998, 9999, 2020, 2026, 2015, 2004, 1900):
        for w in range(0, 55):
            for d in (0, 1, 7, 8):
                cases.add(f"{y:04d}-W{w:02d}-{d}")
                cases.add(f"{y:04d}W{w:02d}{d}")
    out = {}
    for c in sorted(cases):
        try:
            out[c] = datetime.date.fromisoformat(c).isoformat()
        except ValueError:
            out[c] = None
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=0, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()
