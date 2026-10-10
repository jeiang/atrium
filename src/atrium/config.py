import os
from dataclasses import dataclass, field
from pathlib import Path

from .safety import mode_from_env

ICLOUD = "icloud"
GMAIL = "gmail"
IMAP_PORT = 993
BODY_CAP_BYTES = 65536
EMBED_DIM = 768
FETCH_BATCH = 40
EMBED_BATCH = 16
KNN_K = 7
KNN_MIN_SHARE = 0.9
RULE_MIN_N = 3
RULE_MIN_PURITY = 0.9
FOLDER_MIN_MESSAGES = 10
LLM_NEIGHBORS = 10
LLM_TOP_SENDERS = 3
LLM_BODY_CHARS = 1500
EMBED_BODY_CHARS = 1500
FTS_BODY_CHARS = 20000
IDLE_SECONDS = 29 * 60
IDLE_SETTLE_SECONDS = 2
BACKOFF_START = 5
BACKOFF_MAX = 300
GMAIL_CATEGORIES = ("social", "promotions", "updates", "forums")
GMAIL_ALL_MAIL = "[Gmail]/All Mail"
INBOX = "INBOX"


@dataclass(frozen=True)
class AccountSpec:
    name: str
    host: str
    user_env: str
    password_env: str
    uncertain_dest: str | None


ACCOUNTS = {
    ICLOUD: AccountSpec(ICLOUD, "imap.mail.me.com", "ICLOUD_MAIL_USERNAME", "ICLOUD_APP_PASSWORD", "Misc"),
    GMAIL: AccountSpec(GMAIL, "imap.gmail.com", "GMAIL_ADDRESS", "GMAIL_APP_PASSWORD", None),
}


@dataclass(frozen=True)
class Config:
    db_path: Path
    cache_dir: Path
    mode: str
    chat_url: str
    embed_url: str
    contacts_dir: Path | None
    jev_db: Path | None
    static_dir: Path | None = None
    credentials: dict = field(default_factory=dict, repr=False)

    def credential(self, account):
        spec = ACCOUNTS[account]
        user = self.credentials.get(spec.user_env, "")
        password = "".join(self.credentials.get(spec.password_env, "").split())
        if not user or not password:
            return None
        return user, password


def _path(value):
    return Path(value) if value else None


def load(environ=None):
    env = environ if environ is not None else os.environ
    state = env.get("STATE_DIRECTORY", "").split(":")[0]
    db = env.get("ATRIUM_DB") or (str(Path(state) / "atrium.db") if state else "")
    if not db:
        raise ValueError("ATRIUM_DB or STATE_DIRECTORY is required")
    cache = env.get("ATRIUM_CACHE_DIR") or (str(Path(state) / "cache") if state else "")
    if not cache:
        raise ValueError("ATRIUM_CACHE_DIR or STATE_DIRECTORY is required")
    wanted = [n for spec in ACCOUNTS.values() for n in (spec.user_env, spec.password_env)]
    return Config(
        db_path=Path(db),
        cache_dir=Path(cache),
        mode=mode_from_env(env),
        chat_url=env.get("ATRIUM_CHAT_URL", "").rstrip("/"),
        embed_url=env.get("ATRIUM_EMBED_URL", "").rstrip("/"),
        contacts_dir=_path(env.get("ATRIUM_CONTACTS_DIR")),
        jev_db=_path(env.get("ATRIUM_JEV_DB")),
        static_dir=_path(env.get("ATRIUM_STATIC_DIR")),
        credentials={k: env[k] for k in wanted if k in env},
    )
