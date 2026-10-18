# Integration auth: everything else (ticket #35)

Scope: every external service in `src/atrium` and `nix/module.nix` that is NOT covered by #33 (Apple, Google), #34 (Notion, GitHub, Grafana, Anthropic). For each: auth method(s), what each method permits, and the web-UI flow. Research date 2026-10-17. Facts about atrium cite `path:line` at origin/main `c221de8`; external facts link their source. `[inference]` marks my reasoning.

## 1. Inventory of what the code talks to

Found by grepping every URL, `environ`/`env.get` and `nix/module.nix` env wiring.

| Service | Where (code) | Today's auth | Config knobs today |
|---|---|---|---|
| qBittorrent Web API v2 | `src/atrium/media/qbit.py:114-130` (client built per request, no cookie jar, no auth header) | **None**: docstring says "the loopback whitelist on the host socket proxy makes login unnecessary" (`qbit.py:1`) | `QBITTORRENT_URL` (`media/settings.py:49`, `nix/module.nix:83,288`) |
| EZTV JSON API | `media/eztv.py:5,30` (hard-coded `https://eztvx.to/api/get-torrents`) | None | none (host hard-coded) |
| TMDB v3 | `media/tmdb.py:8-9,34` (`api_key` query param) | v3 API key in the query string | `TMDB_API_KEY` (`media/settings.py:54`), optional |
| ntfy | `agent/tools/notify.py:55-69` (`POST {NTFY_URL}` JSON body with `topic`) | Optional `Authorization: Bearer <NTFY_TOKEN>` | `NTFY_URL`, `NTFY_TOPIC`, `NTFY_TOKEN`, `NTFY_MEDIA_TOPIC` (`nix/module.nix:76-78,268`) |
| Local LLM chat (OpenAI-compatible) | `llm.py:71-86` (`POST {url}/chat/completions`, model name hard-coded `qwen`, `llm.py:6`) | None (`http.py:12-22` sends only `content-type`) | `ATRIUM_CHAT_URL` (`config.py:95`) |
| Local LLM embeddings | `embed.py:23` (`POST {url}/embeddings`, model `embeddinggemma`, dim 768 `config.py:11`) | None | `ATRIUM_EMBED_URL` (`config.py:96`) |
| CBTT FX rate | `money.py:74,430` (`get_text("https://www.central-bank.org.tt/")` then HTML table scrape) | None (public page) | none |
| Web fetch (agent tool) | `webfetch.py` (pinned-IP, public-IP-only, 2 MiB cap, UA `atrium-webfetch/1`, line 16) | None | none |
| Web search | `agent/registry.py:5` Anthropic server tool `web_search_20260318`, `max_uses: 5` | Rides the Anthropic API key (#34) | none |
| Reverse-proxy identity (inbound "auth") | `web/auth.py:7-10,44`: trusts header `Remote-Groups` containing `atrium_access` + a Host allow-list (`--auth proxy`) | Header from a trusted proxy | `web.auth`, `web.allowedHost` (`nix/module.nix:244-256`) |
| Signed media links (inbound) | `media/links.py`, `media/settings.py:9` (HMAC, 12 h) | HMAC-SHA256 over `path\nexp` | `ATRIUM_MEDIA_LINK_KEY` |

Not in this ticket (other tickets own them): iCloud IMAP/SMTP/CalDAV/CardDAV + vdirsyncer and Google OAuth (#33), Notion, GitHub, Grafana (`agent/tools/grafana.py:22-33`, Bearer service-account token), Anthropic (#34). Sandbox-manager mTLS is #41; ntfy provisioning mechanics are #31/#47.

No other outbound hosts exist: the URL grep over `src/atrium` returned only caldav.icloud.com, Google OAuth/Calendar, eztvx.to, api.themoviedb.org, image.tmdb.org, central-bank.org.tt, api.notion.com.

## 2. qBittorrent WebUI

**Methods (current upstream, latest release `release-5.2.4`, 2026-09-28 ([GitHub API](https://api.github.com/repos/qbittorrent/qBittorrent/releases/latest))):**

1. **Cookie session**: `POST /api/v2/auth/login` with `username`/`password` returns `Set-Cookie: SID=…`; every later call must carry the cookie; set `Referer`/`Origin` equal to the Host ([WebUI API wiki](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-5.0))). Full account: no scopes.
2. **API key (new, qBittorrent ≥ 5.2.0 / WebAPI 2.14.1)**: `Authorization: Bearer qbt_…` (also an `X-API-Key` header is honoured, see `apiKeyHeader`/`BEARER_AUTH` in [`webapplication.cpp`](https://github.com/qbittorrent/qBittorrent/blob/master/src/webui/webapplication.cpp)). Format `qbt_` + 28 alphanumerics; **only one key**; rotation invalidates the old one; keys cannot fetch the WebUI static assets or call `auth/*`; generated in Preferences → WebUI → API Key ([API-key wiki](https://github.com/qbittorrent/qBittorrent/wiki/API-Key-Authentication-%28%E2%89%A5v5.2.0%29)). Endpoints `app/rotateAPIKey` (2.14.1, #23212) and `app/deleteAPIKey` (#23388) ([WebAPI changelog](https://github.com/qbittorrent/qBittorrent/blob/master/WebAPI_Changelog.md)). Because the key is one stateless header, it fits atrium's per-request httpx client (`qbit.py:118`) with a one-line change.
3. **Basic auth**: "WebAPI credentials can now be supplied via Basic auth" is listed under WebAPI 2.15.0 (#23564) in the same changelog. The release carrying 2.15.0 is newer than 5.2.0 `[inference]`: do not depend on it.
4. **Bypass**: `bypass_local_auth` (127.0.0.1) and `bypass_auth_subnet_whitelist` preferences ([wiki preferences table](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-5.0))). This is today's deployment (host systemd socket-proxyd to 127.0.0.1, `cornn-flaek` `modules/qbittorrent/default.nix:170`).

**Permissions:** every method is all-or-nothing on the whole API (delete torrents + files, change preferences incl. `autorun_program` which runs an arbitrary program on completion, `web_ui_password`). atrium only needs `torrents/info|files|add|addTags|filePrio|start|delete` (`qbit.py:128-173`). There is no read-only or scoped credential `[inference from the wiki: no scope concept documented]`. Consequence: the stored key is a full-control secret for the qBittorrent host, and a compromised atrium can set `autorun_program`.

**Why it matters for v2:** the plan puts atrium on a VPS and allows remote sandbox hosts; a loopback-whitelist qBittorrent is then unreachable or must be exposed unauthenticated. Upstream API keys remove the reason to bypass auth.

**Web UI flow (recommended):** Settings → Media → qBittorrent: fields `URL`, `Auth` select (`None / API key / Username+password`), secret field (write-only, `_file` pinned allowed). **Test connection** button calls `GET /api/v2/app/webapiVersion` (version ≥ 2.14.1 enables the API-key option, else disable it with a hint) and `GET /api/v2/torrents/info?limit=1` to prove auth works. Cannot generate the key for the user from atrium without already having auth (`app/rotateAPIKey` needs an authenticated session), so the guided path is "open qBittorrent → Preferences → WebUI → API Key → paste" with a deep-link to `<url>/`. A username+password login flow (session cookie, re-login on 403) is only needed for qBittorrent < 5.2.0. Keep None as an explicit option for the loopback-whitelist setup.

**Gotchas:** TLS/self-signed qBittorrent behind a reverse proxy needs a CA/skip-verify option; `Referer`/`Origin` must match Host for cookie logins (not for API keys, `webapplication.cpp:702` skips CSRF checks when using an API key); `Host header validation` (`web_ui_domain_list`) rejects a base URL whose host is not whitelisted.

## 3. EZTV

- API is "in beta mode and does not require an API Login or API key" ([eztvx.to/api](https://eztvx.to/api/)); `get-torrents` accepts `limit` (1-100), `page`, and `imdb_id`. Live check on 2026-10-17: `GET https://eztvx.to/api/get-torrents?imdb_id=0944947&limit=1` → HTTP 200.
- Auth: none. Permissions: read-only public data. Nothing to connect in the UI; the only configurable thing is the **mirror host**. The code hard-codes `eztvx.to` (`media/eztv.py:5`), the site has moved domains repeatedly (the page itself points at `eztvstatus.org` for status) `[inference: eztvstatus.org is the status page linked from the site header]`. Settings flow: a "Base URL" text field (default `https://eztvx.to`) with a **Test** button running the same `imdb_id` query.
- Risk: legal/ToS posture of a torrent index and an unauthenticated third-party that can change schema or vanish; the code already degrades (`EztvError`). Treat as an optional, replaceable "release source" provider.

## 4. TMDB

- Two credentials, same account: **v3 API key** (query `?api_key=`) and **API Read Access Token** (`Authorization: Bearer`, works for v3 and v4) ([TMDB auth docs](https://developer.themoviedb.org/docs/authentication-application)). Both are read-only application auth; user-scoped writes (lists, ratings) need a separate session flow that atrium does not use.
- atrium sends the v3 key in the query (`media/tmdb.py:34`), so the key lands in any proxy/access log. Recommend accepting either token, sending Bearer.
- Rate limit: legacy 40/10s limit disabled but upper bounds exist ([rate limiting](https://developer.themoviedb.org/docs/rate-limiting)). Terms require TMDB logo attribution in the app ([API terms](https://www.themoviedb.org/api-terms-of-use)): needed in the UI when metadata/posters are shown. Free for non-commercial use `[inference from terms; verify before any commercial distribution]`.
- UI flow: Settings → Media → TMDB: paste key or read token (auto-detect: the Bearer token is a long JWT, the v3 key is 32 hex), **Test** = `GET /3/configuration`. Link to `themoviedb.org/settings/api`. Entire feature is optional today (`tmdb.py:2`).

## 5. ntfy (consumer side; provisioning is #31/#47)

- Server auth for publishing ([ntfy publish docs](https://github.com/binwiederhier/ntfy/blob/main/docs/publish.md)): Basic (user:password), Bearer access token, or either via `?auth=` query. Access tokens `tk_…` are created with `ntfy token`, config `auth-tokens`, or the account API; **"access tokens grant users full access to the user account … Granular access tokens are on the roadmap"** ([config docs, access tokens](https://github.com/binwiederhier/ntfy/blob/main/docs/config.md)). Topic-level permissions (read-write/read-only/write-only/deny) are on the **user** via ACLs; default policy set by `auth-default-access` (default `read-write`, so anonymous publish works on an open server).
- atrium today: Bearer if `NTFY_TOKEN`, none otherwise; posts JSON to the **server root** with `topic` in the body (`notify.py:55-69`). Delivery failure is recorded but never raised.
- Methods atrium should support: none (public/open server), Basic, Bearer token. Permission needed: write to exactly one or two topics. Best practice: a dedicated ntfy user with write-only ACL on the atrium topics, then use that user's password or a token of that user.
- UI flow: Settings → Notifications: URL, auth (`None / Token / Username+password`), topics (main, media), **Send test notification** that surfaces the HTTP status (401/403 distinguishes bad credential from ACL).
- #31's idea (atrium provisions the user via ntfy's API) needs an **admin** credential to ntfy; that admin secret must be single-use (entered, used, discarded) `[inference]` and is out of scope here.
- Open design point: the user subscribes with their own ntfy account/phone app; atrium's token is for publishing only. Topic name is the capability on an open server, so generated topics must be unguessable (≥ 128 bits).

## 6. OIDC providers (instance login; ticket #38 owns the design)

Atrium is a relying party (RP). Use authorization code flow + **PKCE S256 even for a confidential client** (OAuth 2.1 direction), `nonce`, state, discovery, JWKS validation. Go: `github.com/coreos/go-oidc/v3` (pkg.go.dev shows v3.21.0) + `golang.org/x/oauth2` `[inference: selection is #17's call]`.

| | Pocket ID (v2.18.0) | Authelia (v4.39.28) | Authentik | Keycloak |
|---|---|---|---|---|
| Issuer / discovery | `APP_URL`; `/.well-known/openid-configuration`; token `/api/oidc/token`, userinfo `/api/oidc/userinfo`, end-session `/api/oidc/end-session` ([connect an app](https://github.com/pocket-id/website/blob/main/src/content/docs/docs/setup/connect-an-app.mdx)) | base URL; `/.well-known/openid-configuration` | **per application**: `…/application/o/<slug>/.well-known/openid-configuration` (discovery lives under the app slug, not the root issuer, [docs](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/)) | **per realm**: `…/realms/<realm>/.well-known/openid-configuration` ([server admin](https://www.keycloak.org/docs/latest/server_admin/index.html)) |
| PKCE | Required for public clients; optional (`PkceEnabled`) for confidential ([source](https://github.com/pocket-id/pocket-id/blob/main/backend/internal/service/oidc_service.go)); discovery `code_challenge_methods_supported: [plain,S256]` ([well_known_controller.go](https://github.com/pocket-id/pocket-id/blob/main/backend/internal/controller/well_known_controller.go)) | `require_pkce`, `pkce_challenge_method` per client, global `enforce_pkce` default `public_clients_only` ([client config](https://www.authelia.com/configuration/identity-providers/openid-connect/clients/), [provider config](https://www.authelia.com/configuration/identity-providers/openid-connect/provider/)) | supported (docs list PKCE for the code flow) | client setting "Proof Key for Code Exchange Challenge Method" (blank = honoured only if the client sends it; or force `S256`) ([server admin](https://www.keycloak.org/docs/latest/server_admin/index.html)) |
| Token endpoint client auth | `client_secret_basic`, `client_secret_post`, `none`; federated JWT assertions ([guide](https://pocket-id.org/docs/guides/oidc-client-authentication)) | per-client `token_endpoint_auth_method`, `public` clients need blank secret | client secret / public | client secret, JWT, mTLS |
| Groups claim | scope `groups` → claim `groups` (group **name**, not friendly name); custom claims via `profile`; in ID token **and** userinfo ([scopes & claims](https://github.com/pocket-id/website/blob/main/src/content/docs/docs/guides/scopes-and-claims.md)) | scope `groups` → `groups`, **default location: UserInfo, not the ID token**; to put it in the ID token a `claims_policy` is required ([claims](https://www.authelia.com/integration/openid-connect/openid-connect-1.0-claims/)) | `groups` is a list of group names emitted by the default `profile` scope mapping ([blueprint](https://github.com/goauthentik/authentik/blob/main/blueprints/system/providers-oauth2.yaml)) | **Not emitted by default**: needs a Group Membership mapper (`oidc-group-membership-mapper`) on a client scope; option `full.path` decides `admins` vs `/org/admins` ([source](https://github.com/keycloak/keycloak/blob/main/services/src/main/java/org/keycloak/protocol/oidc/mappers/GroupMembershipMapper.java)); note the built-in `microprofile-jwt` scope's `groups` claim is realm *roles*, not groups |
| Who may sign in (IdP side) | per-client "Allowed user groups", default none | `authorization_policy` | application/group bindings | client/role access |
| Revocation propagation | checks user + group at every refresh; ID token 1 h, access 60 min, refresh 30 d idle | n/a measured | n/a measured | n/a measured |
| Break-glass for the IdP itself | account recovery doc | n/a | n/a | n/a |

Required atrium behaviours that follow:
1. **Read groups from ID token then fall back to userinfo** (Authelia default is userinfo-only). Make the claim name configurable (`groups` default) and accept both array-of-strings and a single string. For Keycloak accept both `admins` and `/admins` (normalise leading `/`).
2. Request scopes `openid profile email groups` (+`offline_access` only if atrium wants refresh; Pocket ID issues refresh tokens regardless).
3. Group names `atrium_access` / `atrium_admin` are configurable strings, not literals (plan Settings decision).
4. **Session lifetime = revocation latency.** IdP-side removal from a group is only seen at the next refresh/re-login ([Pocket ID tokens](https://github.com/pocket-id/website/blob/main/src/content/docs/docs/guides/scopes-and-claims.md)). If atrium keeps its own 30-day session, a removed user keeps access. Recommend short atrium sessions (≤ 1 h access, silent re-auth via refresh token or `prompt=none`) `[inference]`.
5. Validate `iss` exactly (Authentik/Keycloak issuer carries a path), `aud` = client id, `exp`, `nonce`; use `issuer` from the discovery doc, never from the user.
6. Redirect URI is `https://<public host>/auth/callback` `[inference: path is #38's]`; it must be registered **exactly** at the IdP (Pocket ID supports wildcards, [docs](https://github.com/pocket-id/website/blob/main/src/content/docs/docs/advanced/callback-url-wildcards.md), but exact is safer).

**Web UI flow (cannot be the only path, chicken-and-egg):** first boot has no IdP, so bootstrap comes from TOML/env pins or the host CLI (matches plan: emergency recovery via CLI). In Settings → Authentication (admin only): (a) Issuer URL → atrium fetches discovery and shows endpoints, `scopes_supported`, `claims_supported`, `code_challenge_methods_supported` and warns when `S256` or `groups` is absent; (b) show the exact redirect URI to copy into the IdP; (c) client id + client secret (secret field, or public client with PKCE only); (d) group claim, access group, admin group; (e) **Test sign-in** runs a full login in a new tab with a claims preview (like Pocket ID's "OIDC Data Preview"), and **refuses to enable OIDC until the tester's own account passes the access-group check**, so an admin cannot lock themselves out `[inference, standard safe-rollout pattern]`. Keep a pre-OIDC fallback (CLI-issued one-time login link) enabled until the first successful OIDC login.

Optional later: Client ID Metadata Documents and federated client credentials exist in Pocket ID ([CIMD](https://github.com/pocket-id/website/blob/main/src/content/docs/docs/guides/client-id-metadata-documents.md)) but are not needed for a self-hosted RP.

## 7. OpenAI-compatible local LLM endpoints

Current code sends **no auth header at all** (`http.py:12-22`), uses stdlib `urllib` (no TLS options), fixed model names (`llm.py:6`, `embed.py:11`) and llama.cpp-only extras in the body: `cache_prompt` and `chat_template_kwargs.enable_thinking` (`llm.py:76-83`) and `response_format: json_schema, strict` (`llm.py:80`). So "OpenAI-compatible" is only partly true today.

Auth reality per server:
- **llama.cpp `llama-server`**: `--api-key KEY` (comma list) or `--api-key-file FNAME` (env `LLAMA_API_KEY`, `LLAMA_ARG_API_KEY_FILE`); clients send `Authorization: Bearer`; `/health` is public ([server README](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)). Example clients in the README use dummy keys like `sk-no-key-required`, i.e. unauthenticated by default.
- **Ollama / LM Studio / vLLM**: OpenAI-compat endpoints accept a key `[inference: not individually verified; Ollama's OpenAI doc says the key is required by clients but ignored, LM Studio has an "Authentication" developer page I could not extract text from]`. Design for "Bearer key optional".
- **Hosted OpenAI-compatible (OpenRouter, etc.)**: same Bearer header; out of the plan's "local LLM" framing but costs nothing to allow.

Permissions: a key grants inference only; it does not distinguish chat vs embed, so two endpoints may carry two keys.

UI flow: Settings → Mail → Local models: for chat and embeddings separately: Base URL, optional API key (secret/`_file`), **model name** (dropdown filled from `GET {url}/models`), **Test** = one tiny `chat/completions` with the real JSON-schema `response_format` (this catches servers that ignore structured output, which mail safety needs) and one `embeddings` call that **checks the returned dimension against the stored vectors** (`EMBED_DIM = 768`, `config.py:11`; `embed.py:24` raises on mismatch). Changing the embed model/dimension requires a re-embed of the whole mail index: surface that as an explicit action, not a silent setting.

Plan link: the mail module "needs OpenAI-compatible local LLM chat+embed URLs". Strict-schema output and thinking-off are capability requirements, so Settings should have capability flags (`supports_json_schema`, `supports_enable_thinking`) or the code should degrade (parse JSON, retry) rather than hard-fail.

## 8. Money data sources

- Only external data source: CBTT USD **selling** rate scraped from the homepage HTML (`money.py:74,402-436`, `get_text` with UA `atrium/1 (ledger rate fetch)` at `http.py:25-29`). No auth. Brittle: breaks silently on markup change (`parse_rate_table` raises `ValueError`).
- Everything else is data derived from mail (bank alert emails) via the local LLM: there is no bank API, aggregator, Plaid-like or CSV import in `src/atrium`. "Money data sources" therefore means (a) mail-derived (auth = IMAP, #33), (b) the FX rate.
- **Frankfurter v2** ([frankfurter.dev](https://frankfurter.dev/), no API key, free for commercial use of its blended rates; queried live 2026-10-17): lists 165 currencies including `TTD` and `BBD`, and exposes **provider `CBTT`** ("Central Bank of Trinidad and Tobago", rate type **weighted average**, daily) with `GET https://api.frankfurter.dev/v2/rates?base=USD&quotes=TTD,BBD&providers=CBTT` → `USD/TTD 6.748`, `USD/BBD 1.9136` ([providers](https://api.frankfurter.dev/v2/providers)). Direct provider use is "subject to each provider's terms" (CBTT copyright notice linked there).
- **Semantic mismatch:** atrium uses CBTT's *selling* rate to convert a TTD account's foreign charge (`money.py:458-475`); Frankfurter's CBTT series is a *weighted average*. Not interchangeable for reconciling against card statements. Do not swap silently `[inference from the provider metadata]`.
- Recommendation: keep CBTT scrape as default provider (it matches how banks convert), put the fetcher behind a `rate provider` interface (`cbtt-html`, `frankfurter`, `manual`), show last-fetched/as-of in Settings, and alert (notification) when the scrape fails N days.
- Auth/UI: none for any provider. Settings → Money: provider select, "Fetch now" button, staleness display. No credentials. (Contrast: if a paid FX API such as exchangerate-api were chosen, an API key field is needed; `open.er-api.com` returns `USD/TTD 6.76`, `BBD 2`, but its terms require attribution [live response], and it does not give CBTT's selling rate.)

## 9. Web search / fetch

- Search today = Anthropic server tool `web_search_20260318`, `max_uses 5` (`agent/registry.py:5`), enabled for chat and cron runs only (`registry.py:4`). Priced at **$10 per 1,000 searches** plus tokens ([Claude docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)); no extra credential.
- **Finding that touches the plan:** from `web_search_20260209` on, `allowed_callers` defaults to `["code_execution_20260120"]`; the API then provisions code execution automatically for dynamic filtering; to call search directly set `allowed_callers: ["direct"]` (same page). atrium's `WEB_SEARCH` dict sets neither (`registry.py:5`), so today every search can run through Anthropic's hosted code execution, which contradicts the plan decision "Anthropic code_execution replaced by the sandbox" `[inference: the API default applies unless overridden; no atrium override found by grep]`. Also relevant to Zero Data Retention eligibility (docs point to the "Server tools" page).
- Alternatives needed only if the model/provider changes or the sandbox/pi agent needs search without Anthropic: **Brave Search API** (`X-Subscription-Token` header, $20 free monthly credit then prepaid, [auth docs](https://api-dashboard.search.brave.com/documentation/guides/authentication), [pricing](https://brave.com/search/api/)), **Tavily** (`Authorization: Bearer`, $5 per 1,000 requests with $5 free monthly, [API reference](https://docs.tavily.com/documentation/api-reference/endpoint/search)), **SearXNG** self-hosted (no key; `format=json` must be enabled in `settings.yml` or it returns 403, [search API](https://docs.searxng.org/dev/search_api.html)). Pricing figures are from the vendor pages at fetch time and change.
- UI flow: Settings → Assistant → Web: toggle search (Anthropic server tool default), optional provider + key, "Test" runs one query. `web_fetch` needs no credential; its safety (public IPs only, redirect cap, allowlist of operator-typed or search-returned URLs, `agent/tools/web_fetch.py:32`) must be ported as is.

## 10. Other things found

- **Inbound reverse-proxy trust** (`web/auth.py`): `Remote-Groups`, Host allow-list. Replaced by OIDC (plan). Port note: the signed `/media/file/*` links bypass the group check (`web/media.py:396`, runbook lines 412-418); with native OIDC the proxy no longer needs a bypass for them but atrium itself must keep that single unauthenticated route (HMAC key from `ATRIUM_MEDIA_LINK_KEY`/`media-link.key`, rotation by deleting the key). The key should move into the settings/secret model.
- **Contacts/calendar via vdirsyncer** and CalDAV: owned by #33.
- **Host-side wiring**: the NixOS module renders every integration into env vars and `EnvironmentFile=` (`nix/module.nix:75-86,113`); personal literals (llama model unit names, ntfy topics) live in `cornn-flaek` `modules/atrium/default.nix:127-136`. Every integration above needs a TOML key + env + `*_file` for its secret, and a UI field with "pinned" state (plan: Settings).
- **No webhook receivers exist** in atrium (grep for `webhook` over `src/atrium` returned no integration endpoint), so none of these integrations need inbound public endpoints except OIDC callback and signed media links.

## 11. Cross-cutting design implications

1. **One "connection" abstraction** per integration: `{kind, base_url, auth_method, secret_ref, status, last_test, pinned}`, each with a `Test` action returning typed errors (unreachable, TLS, 401, 403, wrong version, wrong dimension). Status badge on the integrations page; module toggles (plan) depend on it.
2. **Secrets**: all secrets are write-only in the UI (shown as set/unset), stored encrypted in the DB (DB-encryption ticket #40), pinnable via `*_file`. Never echoed in logs; note TMDB's key-in-URL pattern is a log-leak risk.
3. **TLS options per outbound connection**: custom CA, skip-verify (explicit, loud), since qBittorrent, ntfy and local LLMs commonly use self-signed/internal CAs. Python's `urllib` path (`http.py`) has none; the Go port should use a shared `http.Client` factory with SSRF-aware dialer only for agent-driven fetches (not for operator-configured internal URLs, which are usually private IPs).
4. **Least privilege is mostly impossible** for qBittorrent (single full-access key) and ntfy tokens (full account); mitigate by dedicated accounts (ntfy) and by network scope (qBittorrent bound to an interface only atrium reaches).
5. **Optional integrations fail soft**: TMDB, ntfy, EZTV already do; keep this contract (module "enabled but unhealthy" ≠ crash).

## 12. Options and recommendation

| Area | Options | Recommendation (tied to plan) |
|---|---|---|
| qBittorrent auth | (a) keep loopback bypass; (b) API key; (c) cookie login | Support **None + API key (+ optional Basic if version reports ≥ 2.15.0)**; default guidance = API key. Cookie login only if a user needs qBittorrent < 5.2.0. |
| EZTV | hard-coded; configurable base URL; replaceable provider | Configurable base URL + Test; model as a "release source" provider list. |
| TMDB | v3 key; Bearer token | Accept both, send Bearer, attribute in UI. |
| ntfy | none/basic/token | Support all three + test button; dedicated write-only ntfy user. |
| OIDC | single generic OIDC with configurable claim names | Generic OIDC, S256 PKCE always, groups from ID token with userinfo fallback, claim/group names configurable, lockout-safe Test sign-in. Provider presets (Pocket ID / Authelia / Authentik / Keycloak) only prefill hints (discovery URL pattern, scope list, claim notes). |
| Local LLM | no auth; optional Bearer | Optional Bearer key, model dropdown from `/models`, capability checks, embed-dimension guard. |
| FX | CBTT scrape; Frankfurter(CBTT); manual | Provider interface; CBTT scrape default (selling rate); Frankfurter as optional fallback labelled "weighted average". |
| Web search | Anthropic tool; Brave; Tavily; SearXNG | Keep Anthropic tool; set `allowed_callers: ["direct"]` in Phase B when code_execution is retired; provider interface deferred until a non-Anthropic model path exists. |

## 13. Risks

- Embedding model/dimension changes silently break vector search (`embed.py:24`) unless the settings UI blocks it.
- OIDC lockout on misconfiguration; IdP quirks (Authelia userinfo-only groups; Keycloak no groups by default and path-style names) cause "authenticated but no access" support load.
- qBittorrent key is root-equivalent on the torrent host (autorun program); remote exposure raises stakes.
- Third-party shape changes: EZTV domain churn, CBTT HTML markup, Frankfurter provider semantics.
- Web search through hosted code execution contradicts the "sandbox replaces code_execution" decision until `allowed_callers` is set.

## 14. Candidate new tickets

1. **qBittorrent connection: API key support and version detection** (build, Phase B, media). Question: detect `webapiVersion`, prefer Bearer key, keep `None`, add Test; how do we handle qBittorrent < 5.2.0?
2. **OIDC claim handling matrix and Test sign-in** (design+build, Phase B, auth; feeds #38). Question: groups from ID token vs userinfo, claim name normalisation (`/path`), session lifetime vs revocation latency, lockout-safe enable flow, per-IdP presets/docs (Pocket ID, Authelia, Authentik, Keycloak).
3. **Outbound HTTP client policy** (design, Phase B, core). Question: per-connection custom CA/skip-verify, timeouts, proxy support, SSRF dialer only for agent-driven fetches; replaces Python `urllib` `post_json`.
4. **Local LLM endpoint config and capability probing** (design, Phase B, mail). Question: optional Bearer, model discovery, structured-output and thinking-off capability flags, embed dimension guard and re-embed workflow, generic OpenAI-compat vs llama.cpp-only fields (`cache_prompt`, `chat_template_kwargs`).
5. **FX rate provider interface** (build, Phase B, money). Question: CBTT-HTML vs Frankfurter(CBTT, weighted average) semantics, staleness alert, manual rate entry.
6. **Retire hosted code execution under web_search** (fix, Phase B, assistant). Question: set `allowed_callers: ["direct"]` (or accept dynamic filtering), and its ZDR/cost effects (`registry.py:5`).
7. **Integrations health page** (build, Phase B, UI; feeds #48). Question: uniform `connection` record, Test action contract, status badges, pinned/read-only display, alert on prolonged unhealthy state.
8. **Release-source provider abstraction (EZTV and alternatives)** (design, Phase B, media). Question: configurable base URL now; do we want other sources or RSS indexers later?
9. **TMDB credential handling and attribution** (fix, Phase B, media). Question: accept read token, move out of query string, add required attribution.
10. **Signed media links under native OIDC** (design, Phase B, media/auth). Question: keep the unauthenticated HMAC route, where the key lives (settings vs state dir), rotation UX.
