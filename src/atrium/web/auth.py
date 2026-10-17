import contextvars
import secrets
from http.cookies import SimpleCookie
from urllib.parse import parse_qs, urlsplit

PROXY = "proxy"
LOCAL = "local"
MODES = (PROXY, LOCAL)
GROUP = "atrium_access"
GROUPS_HEADER = b"remote-groups"
CSRF_COOKIE = "atrium_csrf"
CSRF_HEADER = b"x-csrf-token"
CSRF_FIELD = "csrf_token"
UNSAFE_METHODS = ("POST", "PUT", "PATCH", "DELETE")
LOCAL_HOSTS = frozenset({"localhost", "127.0.0.1", "[::1]"})
STATIC_PREFIX = "/static/"
FORM_TYPE = "application/x-www-form-urlencoded"
BODY_CAP = 1 << 20
CSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'"

csrf_token = contextvars.ContextVar("csrf_token", default="")


def header(scope, name):
    return next((v.decode("latin-1") for k, v in scope["headers"] if k == name), None)


def has_group(value):
    return GROUP in (part.strip() for part in (value or "").split(","))


def cookie_token(scope):
    raw = header(scope, b"cookie")
    if not raw:
        return None
    jar = SimpleCookie()
    try:
        jar.load(raw)
    except Exception:
        return None
    found = jar.get(CSRF_COOKIE)
    return found.value if found else None


def hostname(value):
    """Host header without its port; bracketed IPv6 literals stay bracketed."""
    value = (value or "").strip().lower()
    if value.startswith("["):
        return value[: value.find("]") + 1] if "]" in value else value
    return value.partition(":")[0]


def allowed_host(scope, hosts):
    return hostname(header(scope, b"host")) in hosts


def same_origin(scope):
    origin = header(scope, b"origin")
    if origin is None or origin == "null":
        return origin is None
    host = header(scope, b"host") or ""
    return urlsplit(origin).netloc.lower() == host.lower()


async def read_body(receive):
    chunks = []
    size = 0
    while True:
        message = await receive()
        chunk = message.get("body", b"")
        size += len(chunk)
        if size > BODY_CAP:
            return None
        chunks.append(chunk)
        if not message.get("more_body"):
            return b"".join(chunks)


def replay(body):
    sent = False

    async def receive():
        nonlocal sent
        if sent:
            return {"type": "http.disconnect"}
        sent = True
        return {"type": "http.request", "body": body, "more_body": False}

    return receive


async def deny(send, status, text):
    body = text.encode()
    await send(
        {
            "type": "http.response.start",
            "status": status,
            "headers": [(b"content-type", b"text/plain; charset=utf-8"), (b"content-length", str(len(body)).encode())],
        }
    )
    await send({"type": "http.response.body", "body": body})


class Guard:
    def __init__(self, app, auth=PROXY, allowed_hosts=None):
        self.app = app
        self.auth = auth
        # Rejecting unknown Host values defeats DNS rebinding. PROXY has no default: the public host must be configured.
        defaults = LOCAL_HOSTS if auth == LOCAL else frozenset()
        self.hosts = frozenset(hostname(h) for h in allowed_hosts) if allowed_hosts else defaults

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        if not allowed_host(scope, self.hosts):
            await deny(send, 400, "Bad Host header")
            return
        path = scope["path"]
        if self.auth == PROXY and not path.startswith(STATIC_PREFIX) and not has_group(header(scope, GROUPS_HEADER)):
            await deny(send, 403, "Forbidden")
            return
        token = cookie_token(scope)
        fresh = token is None
        if fresh:
            token = secrets.token_urlsafe(32)
        if scope["method"] in UNSAFE_METHODS:
            if fresh or not same_origin(scope):
                await deny(send, 403, "Forbidden: cross-site request")
                return
            supplied = header(scope, CSRF_HEADER)
            if supplied is None and (header(scope, b"content-type") or "").split(";")[0].strip().lower() == FORM_TYPE:
                body = await read_body(receive)
                if body is None:
                    await deny(send, 413, "Request too large")
                    return
                supplied = next(iter(parse_qs(body.decode("latin-1")).get(CSRF_FIELD, [])), None)
                receive = replay(body)
            if supplied is None or not secrets.compare_digest(supplied, token):
                await deny(send, 403, "Forbidden: missing or stale form token")
                return
        reset = csrf_token.set(token)
        secure = scope.get("scheme") == "https" or header(scope, b"x-forwarded-proto") == "https"

        async def send_with_headers(message):
            if message["type"] == "http.response.start":
                headers = list(message.get("headers", []))
                headers.append((b"content-security-policy", CSP.encode()))
                headers.append((b"x-content-type-options", b"nosniff"))
                headers.append((b"referrer-policy", b"same-origin"))
                if fresh:
                    cookie = f"{CSRF_COOKIE}={token}; Path=/; HttpOnly; SameSite=Strict" + ("; Secure" if secure else "")
                    headers.append((b"set-cookie", cookie.encode()))
                message = {**message, "headers": headers}
            await send(message)

        try:
            await self.app(scope, receive, send_with_headers)
        finally:
            csrf_token.reset(reset)
