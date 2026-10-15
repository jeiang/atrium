"""One-time installed-app OAuth flow (loopback redirect, PKCE) that yields a Google refresh token."""

import base64
import hashlib
import http.server
import secrets
import urllib.parse

import httpx

from .calendar import SCOPE, TOKEN_URI, CalendarError

AUTH_URI = "https://accounts.google.com/o/oauth2/v2/auth"
TIMEOUT_SECONDS = 300
DONE_PAGE = b"<!doctype html><title>atrium</title><p>Authorised. You can close this tab."


def auth_url(client_id, redirect_uri, state, challenge):
    query = urllib.parse.urlencode(
        {
            "client_id": client_id,
            "redirect_uri": redirect_uri,
            "response_type": "code",
            "scope": SCOPE,
            "access_type": "offline",
            "prompt": "consent",
            "state": state,
            "code_challenge": challenge,
            "code_challenge_method": "S256",
        }
    )
    return f"{AUTH_URI}?{query}"


def _handler(result):
    class Catch(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            params = dict(urllib.parse.parse_qsl(urllib.parse.urlsplit(self.path).query))
            if "code" in params or "error" in params:
                result.update(params)
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.end_headers()
            self.wfile.write(DONE_PAGE)

        def log_message(self, *args):
            pass

    return Catch


def authorize(client_id, client_secret, *, port=0, out=print, timeout=TIMEOUT_SECONDS, post=httpx.post):
    """Print the consent URL, wait for the redirect on 127.0.0.1, exchange the code, return the refresh token."""
    if not client_id or not client_secret:
        raise CalendarError("GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required")
    result = {}
    server = http.server.HTTPServer(("127.0.0.1", port), _handler(result))
    server.timeout = 1
    try:
        redirect = f"http://127.0.0.1:{server.server_address[1]}"
        verifier = secrets.token_urlsafe(64)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
        state = secrets.token_urlsafe(16)
        out(f"Open this URL in a browser and approve access:\n\n{auth_url(client_id, redirect, state, challenge)}\n")
        waited = 0
        while not result and waited < timeout:
            server.handle_request()
            waited += 1
    finally:
        server.server_close()
    if not result:
        raise CalendarError("timed out waiting for the Google redirect")
    if result.get("error"):
        raise CalendarError(f"Google returned an error: {result['error']}")
    if result.get("state") != state:
        raise CalendarError("state mismatch; start again")
    reply = post(
        TOKEN_URI,
        data={
            "client_id": client_id,
            "client_secret": client_secret,
            "code": result["code"],
            "code_verifier": verifier,
            "grant_type": "authorization_code",
            "redirect_uri": redirect,
        },
        timeout=30,
    )
    if reply.status_code != 200:
        raise CalendarError(f"token exchange failed: {reply.status_code} {reply.text[:200]}")
    token = reply.json().get("refresh_token")
    if not token:
        raise CalendarError("Google returned no refresh token; revoke the app's access and retry")
    return token
