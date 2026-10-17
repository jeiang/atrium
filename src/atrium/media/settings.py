from dataclasses import dataclass
from pathlib import Path

from ..config import Config

IMPORT = "import"
CACHE_SUBDIR = "media"
LINK_KEY_FILE = "media-link.key"
LINK_TTL_SECONDS = 12 * 3600
DOWNLOADS_PAGE = "/media/downloads"


class NotConfigured(Exception):
    pass


@dataclass(frozen=True)
class Settings:
    root: Path | None
    qbit_url: str
    public_url: str
    movies_url: str
    tv_url: str
    ntfy_topic: str
    tmdb_key: str
    link_key: str
    cache: Path
    key_path: Path

    def need_root(self):
        if self.root is None:
            raise NotConfigured("ATRIUM_MEDIA_ROOT is not set")
        return self.root

    def need_qbit(self):
        if not self.qbit_url:
            raise NotConfigured("QBITTORRENT_URL is not set")
        return self.qbit_url


def _clean(env, name):
    return (env.get(name) or "").strip().rstrip("/")


def load(cfg: Config, env):
    root = _clean(env, "ATRIUM_MEDIA_ROOT")
    return Settings(
        root=Path(root) if root else None,
        qbit_url=_clean(env, "QBITTORRENT_URL"),
        public_url=_clean(env, "ATRIUM_PUBLIC_URL"),
        movies_url=_clean(env, "ATRIUM_MEDIA_MOVIES_URL"),
        tv_url=_clean(env, "ATRIUM_MEDIA_TV_URL"),
        ntfy_topic=(env.get("NTFY_MEDIA_TOPIC") or "").strip(),
        tmdb_key=(env.get("TMDB_API_KEY") or "").strip(),
        link_key=(env.get("ATRIUM_MEDIA_LINK_KEY") or "").strip(),
        cache=cfg.cache_dir / CACHE_SUBDIR,
        key_path=cfg.db_path.with_name(LINK_KEY_FILE),
    )
