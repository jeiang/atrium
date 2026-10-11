from atrium.imap import MailboxInfo, utf7_decode
from atrium.safety import write_gate
from fake_imap import FakeBox, FakeClient


class WriteClient(FakeClient):
    def __init__(self, boxes, flags=None, capabilities=("IDLE", "CONDSTORE", "UIDPLUS"), gmail=False):
        super().__init__(boxes, flags, capabilities)
        self.gmail = gmail
        self.writes = []
        self.created = []
        self.deleted = []
        self.rw = None

    def list_mailboxes(self):
        return [MailboxInfo(utf7_decode(n), n, tuple(self.box_flags.get(n, ())), "/") for n in self.boxes]

    def label_count(self, name):
        return sum(
            1
            for box in self.boxes.values()
            for m in box.messages.values()
            if m["labels"] is not None and name in m["labels"]
        )

    def status(self, name):
        if name not in self.boxes and self.gmail:
            return {"uidvalidity": 1, "uidnext": 1, "highestmodseq": 1, "messages": self.label_count(utf7_decode(name))}
        return super().status(name)

    def uid_search(self, *criteria):
        if criteria[0] == "UID":
            wanted = {int(u) for u in criteria[1].split(",")}
            return sorted(wanted & set(self.boxes[self.selected].messages))
        if criteria[0] == "HEADER":
            needle = criteria[2].encode()
            return sorted(u for u, m in self.boxes[self.selected].messages.items() if needle in m["header"])
        return super().uid_search(*criteria)

    def select_rw(self, name):
        write_gate("IMAP SELECT read-write")
        self.selected = name
        self.rw = name

    def mutate(self, verb, *args):
        write_gate(f"IMAP {verb}")
        self.writes.append((verb, self.selected, *args))
        box = self.boxes[self.selected]
        uids = [int(u) for u in args[0].split(",")]
        if verb == "COPY":
            dest = self.boxes[args[1].strip('"')]
            mapping = {}
            for uid in uids:
                m = box.messages[uid]
                mapping[uid] = dest.add(m["header"], m["text"], m["flags"], m["labels"])
            src = ",".join(str(u) for u in mapping)
            dst = ",".join(str(v) for v in mapping.values())
            return [f"[COPYUID {dest.validity} {src} {dst}] Done".encode()]
        if verb == "STORE":
            mode, value = args[1], args[2]
            names = [n.strip('"') for n in value.strip("()").split()]
            for uid in uids:
                m = box.messages[uid]
                if "X-GM-LABELS" in mode:
                    target = list(m["labels"] or [])
                else:
                    target = list(m["flags"])
                for n in names:
                    if mode.startswith("+") and n not in target:
                        target.append(n)
                    if mode.startswith("-") and n in target:
                        target.remove(n)
                if "X-GM-LABELS" in mode:
                    box.set_flags(uid, labels=target)
                else:
                    box.set_flags(uid, flags=target)
            return []
        if verb == "EXPUNGE":
            for uid in uids:
                if "\\Deleted" in box.messages[uid]["flags"]:
                    box.remove(uid)
            return []
        raise AssertionError(verb)

    def mutate_mailbox(self, verb, name):
        write_gate(f"IMAP {verb}")
        self.writes.append((verb, name))
        if verb == "CREATE":
            self.boxes[name] = FakeBox(validity=7)
            self.created.append(name)
        elif verb == "DELETE":
            self.deleted.append(name)
            self.boxes.pop(name, None)
        return []

    def close(self):
        pass
