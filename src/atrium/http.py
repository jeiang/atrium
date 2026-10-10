import json
import urllib.error
import urllib.request

TIMEOUT_SECONDS = 600


class ServiceUnavailable(RuntimeError):
    pass


def post_json(url, payload):
    if not url.startswith(("http://", "https://")):
        raise ServiceUnavailable(f"no service URL configured for {url!r}")
    request = urllib.request.Request(
        url, json.dumps(payload).encode(), {"content-type": "application/json"}, method="POST"
    )
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            return json.load(response)
    except (urllib.error.URLError, ConnectionError, TimeoutError) as e:
        raise ServiceUnavailable(f"{url}: {e}") from e
