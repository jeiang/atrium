from atrium.imap import MailboxInfo, parse_labels


def raw(subject, sender="a@x.test", msgid=None, body="hello"):
    header = (
        f"From: {sender}\r\nTo: me@x.test\r\nSubject: {subject}\r\nMessage-ID: <{msgid or subject}@x.test>\r\n"
        "Date: Tue, 06 Oct 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n"
    ).encode()
    return header, body.encode()


class FakeBox:
    def __init__(self, validity=1):
        self.validity = validity
        self.modseq = 1
        self.messages = {}
        self.next_uid = 1

    def add(self, header, text, flags=(), labels=None):
        uid = self.next_uid
        self.next_uid += 1
        self.modseq += 1
        self.messages[uid] = {"header": header, "text": text, "flags": list(flags), "labels": labels, "modseq": self.modseq}
        return uid

    def set_flags(self, uid, flags=None, labels=None):
        self.modseq += 1
        message = self.messages[uid]
        if flags is not None:
            message["flags"] = list(flags)
        if labels is not None:
            message["labels"] = list(labels)
        message["modseq"] = self.modseq

    def remove(self, uid):
        self.modseq += 1
        del self.messages[uid]


class FakeClient:
    def __init__(self, boxes, flags=None, capabilities=("IDLE", "CONDSTORE")):
        self.boxes = boxes
        self.box_flags = flags or {}
        self.capabilities = frozenset(capabilities)
        self.selected = None
        self.searches = []
        self.fetches = []
        self.category_uids = {}

    def list_mailboxes(self):
        return [MailboxInfo(n, n, tuple(self.box_flags.get(n, ())), "/") for n in self.boxes]

    def status(self, name):
        box = self.boxes[name]
        return {"uidvalidity": box.validity, "uidnext": box.next_uid, "highestmodseq": box.modseq, "messages": len(box.messages)}

    def select(self, name):
        self.selected = name

    def uid_search(self, *criteria):
        self.searches.append(criteria)
        if criteria[0] == "X-GM-RAW":
            return sorted(self.category_uids.get(criteria[1].strip('"').split(":")[1], []))
        return sorted(self.boxes[self.selected].messages)

    def uid_fetch(self, uids, items, changedsince=None):
        box = self.boxes[self.selected]
        self.fetches.append((list(uids), items, changedsince))
        wanted = sorted(box.messages) if uids == ["1:*"] else sorted(set(uids) & set(box.messages))
        full = "BODY.PEEK[HEADER]" in items
        out = []
        for uid in wanted:
            m = box.messages[uid]
            if changedsince is not None and m["modseq"] <= changedsince:
                continue
            out.append(
                {
                    "uid": uid,
                    "flags": list(m["flags"]),
                    "labels": list(m["labels"]) if m["labels"] is not None else None,
                    "modseq": m["modseq"],
                    "size": len(m["header"]) + len(m["text"]) if full else 0,
                    "gm_msgid": f"g{uid}" if m["labels"] is not None else None,
                    "gm_thrid": f"t{uid}" if m["labels"] is not None else None,
                    "internaldate": "06-Oct-2026 10:00:00 +0000" if full else "",
                    "attachments": [] if full else None,
                    "header": m["header"] if full else b"",
                    "text": m["text"] if full else b"",
                }
            )
        return out


__all__ = ["FakeBox", "FakeClient", "raw", "parse_labels"]
