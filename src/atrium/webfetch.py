import http.client
import ipaddress
import socket
import ssl
import time
import urllib.parse
from html.parser import HTMLParser

MAX_BYTES = 2 * 1024 * 1024
FETCH_TIMEOUT_SECONDS = 10
MAX_REDIRECTS = 5
READ_BLOCK = 65536
SCHEMES = ("http", "https")
REDIRECT_STATUSES = (301, 302, 303, 307, 308)
TEXT_TYPES = ("text/html", "application/xhtml+xml", "text/plain")
USER_AGENT = "atrium-webfetch/1"
PAGE_TEXT_CHARS = 60000
SKIP_TAGS = frozenset({"script", "style", "noscript", "svg", "nav", "header", "footer", "aside", "form", "template"})
BLOCK_TAGS = frozenset(
    {"p", "div", "br", "li", "ul", "ol", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "pre", "table"}
)
EMBEDDED_IPV4_NETWORKS = (
    ipaddress.ip_network("64:ff9b::/96"),
    ipaddress.ip_network("::/96"),
    ipaddress.ip_network("::ffff:0:0:0/96"),
)
IPV4_MASK = 0xFFFFFFFF


class Blocked(ValueError):
    pass


class FetchError(RuntimeError):
    pass


def default_allowed(ip):
    if ip.version == 6:
        if ip.is_site_local:
            return False
        if ip.ipv4_mapped is not None:
            ip = ip.ipv4_mapped
        elif any(ip in network for network in EMBEDDED_IPV4_NETWORKS):
            ip = ipaddress.IPv4Address(int(ip) & IPV4_MASK)
    return ip.is_global and not ip.is_multicast


def resolve(host, port, allowed):
    try:
        infos = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)
    except socket.gaierror as e:
        raise FetchError(f"dns: {e}") from e
    addresses = [i[4][0].split("%")[0] for i in infos]
    if not addresses:
        raise FetchError("dns: no address")
    for address in addresses:
        if not allowed(ipaddress.ip_address(address)):
            raise Blocked(f"{host} resolves to a non-public address")
    return addresses[0]


class PinnedHTTP(http.client.HTTPConnection):
    def __init__(self, host, port, address, timeout):
        super().__init__(host, port, timeout=timeout)
        self.address = address

    def connect(self):
        self.sock = socket.create_connection((self.address, self.port), self.timeout)


class PinnedHTTPS(http.client.HTTPSConnection):
    def __init__(self, host, port, address, timeout):
        super().__init__(host, port, timeout=timeout, context=ssl.create_default_context())
        self.address = address

    def connect(self):
        raw = socket.create_connection((self.address, self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)


def fetch_page(url, allowed=default_allowed):
    deadline = time.monotonic() + FETCH_TIMEOUT_SECONDS
    for _ in range(MAX_REDIRECTS + 1):
        parts = urllib.parse.urlsplit(url)
        if parts.scheme not in SCHEMES or not parts.hostname:
            raise Blocked(f"unsupported URL {url!r}")
        port = parts.port or (443 if parts.scheme == "https" else 80)
        address = resolve(parts.hostname, port, allowed)
        conn_class = PinnedHTTPS if parts.scheme == "https" else PinnedHTTP
        conn = conn_class(parts.hostname, port, address, max(deadline - time.monotonic(), 0.01))
        try:
            target = urllib.parse.urlunsplit(("", "", parts.path or "/", parts.query, ""))
            conn.request(
                "GET",
                target,
                headers={"user-agent": USER_AGENT, "accept": ", ".join(TEXT_TYPES), "accept-encoding": "identity"},
            )
            response = conn.getresponse()
            if response.status in REDIRECT_STATUSES and response.getheader("location"):
                url = urllib.parse.urljoin(url, response.getheader("location"))
                continue
            if response.status != 200:
                raise FetchError(f"status {response.status}")
            kind = (response.getheader("content-type") or "text/html").split(";")[0].strip().lower()
            if kind not in TEXT_TYPES:
                raise FetchError(f"content type {kind}")
            declared = response.getheader("content-length")
            if declared and declared.isdigit() and int(declared) > MAX_BYTES:
                raise FetchError("too large")
            return read_body(conn, response, deadline), response.headers.get_content_charset() or "utf-8", url
        except (OSError, http.client.HTTPException) as e:
            raise FetchError(str(e) or type(e).__name__) from e
        finally:
            conn.close()
    raise FetchError("too many redirects")


def read_body(conn, response, deadline):
    body = bytearray()
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise FetchError("timeout")
        block = response.read1(READ_BLOCK)
        if not block:
            return bytes(body)
        body += block
        if len(body) > MAX_BYTES:
            raise FetchError("too large")


class TextExtractor(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.title = ""
        self.parts = []
        self.skip = 0
        self.in_title = False

    def handle_starttag(self, tag, attrs):
        if tag == "title":
            self.in_title = True
        elif tag in SKIP_TAGS:
            self.skip += 1
        elif tag in BLOCK_TAGS:
            self.parts.append("\n")

    def handle_endtag(self, tag):
        if tag == "title":
            self.in_title = False
        elif tag in SKIP_TAGS:
            self.skip = max(self.skip - 1, 0)
        elif tag in BLOCK_TAGS:
            self.parts.append("\n")

    def handle_data(self, data):
        if self.in_title:
            self.title += data
        elif not self.skip:
            self.parts.append(data)


def plain_text(markup):
    parser = TextExtractor()
    parser.feed(markup)
    parser.close()
    lines = (" ".join(line.split()) for line in "".join(parser.parts).split("\n"))
    return " ".join(parser.title.split()), "\n".join(line for line in lines if line)


def extract_text(markup, url):
    title, fallback = plain_text(markup)
    try:
        import trafilatura

        text = trafilatura.extract(markup, url=url, include_comments=False, include_tables=False)
        meta = trafilatura.extract_metadata(markup, default_url=url)
        title = (getattr(meta, "title", None) or title) if meta else title
    except Exception:
        text = None
    return " ".join(title.split()), (text or fallback)[:PAGE_TEXT_CHARS]
