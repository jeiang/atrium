"""Write internal/contacts/testdata/expected.json from atrium.contacts over internal/contacts/testdata/book.

Run from the repo root: PYTHONPATH=src nix develop --impure -c python tools/contacts/gen_expected.py
"""

import json

from atrium import contacts

BOOK = "internal/contacts/testdata/book"
OUT = "internal/contacts/testdata/expected.json"
QUERIES = ["", "strasse", "STRASSE jürgen", "example.test", "+49", "nord", "ärzte", "ÄRZTE praxis", "renée", "nobody"]


def main():
    cards = contacts.load_contacts(BOOK)
    search = [{"query": q, "names": [c["name"] for c in contacts.filter_contacts(cards, q)]} for q in QUERIES]
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump({"cards": cards, "search": search}, f, ensure_ascii=False, indent=2)
        f.write("\n")


if __name__ == "__main__":
    main()
