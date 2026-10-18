# Integration auth: Notion, GitHub, Grafana, Anthropic (ticket #34)

Researched 2026-10-17. Facts about atrium cite `path:line` in `origin/main` at c221de8; external facts link to the owning doc. `[inference]` marks my own reading.

## 0. Answer in one screen

| Integration | Today in atrium | Recommended v2 auth (web-UI performable) | Why |
|---|---|---|---|
| Notion | internal token in env `NOTION_TOKEN` | Keep **internal connection token** (paste in UI) as the default; add **personal access token (PAT)** as a second paste-in option; add **public OAuth** only as optional, deferred | The wiki mirror needs a bot that outlives the user and sees the whole shared tree; OAuth needs every self-hoster to register their own public connection |
| GitHub | none (only `x-github-*` mail-header matching, `src/atrium/features.py:29-30`) | **One GitHub App per instance, user-to-server tokens** (device flow or web flow, expiring 8h + refresh), registered from the web UI via the **App manifest flow**; fall back to a pasted fine-grained PAT | Gives repo selection at install time, 8h tokens, fine-grained permissions, and satisfies the plan's "per-user OAuth, token never enters the VM" |
| Grafana | env `GRAFANA_URL` + `GRAFANA_VIEWER_TOKEN` (`src/atrium/agent/tools/grafana.py:23-26`) | Paste a **service-account token with the Viewer role** + URL in UI; test button; optional expiry | Only supported API auth; Viewer already covers every call atrium makes |
| Anthropic | env `ANTHROPIC_API_KEY` (`src/atrium/agent/client.py:3-12`) | **Console API key** (personal or service-account key, workspace-scoped, with an expiry) pasted in UI; optional **Admin/usage key** for cost reconciliation; **no subscription OAuth for the assistant loop** | Anthropic's terms forbid routing subscription credentials through third-party products; the Agent loop must use API keys |

Every method above is performable in the web UI as "paste credential + Test" except GitHub (device/web flow + manifest flow) and optionally Notion OAuth (redirect flow). Nothing requires a CLI on the host.

## 1. Current atrium usage (facts from code)

- Notion: `NotionClient` sends `Authorization: Bearer <token>` and `Notion-Version: 2026-03-11` (`src/atrium/notion_api.py:13,83-87`); token read only from env `NOTION_TOKEN` (`notion_api.py:14,31-35`; callers `notion_proposals.py:71`, `agent/tools/notion_read.py:15`, `cli.py:283`). Endpoints used: `POST /v1/search`, `GET/PATCH /v1/pages/{id}`, `GET/PATCH /v1/pages/{id}/markdown`, `POST /v1/pages/{id}/move`, `POST /v1/pages`, `PATCH /v1/databases/{id}`, `GET/PATCH /v1/blocks/{id}/children`, page property fetch (`notion_api.py:154-187`). So it needs **read + update + insert content** capabilities; it uses no comment or user endpoints. Client-side pacing 3 req/s with 429/5xx retry (`notion_api.py:15-20`).
- Grafana: `GRAFANA_URL` + `GRAFANA_VIEWER_TOKEN`, `Bearer` auth (`agent/tools/grafana.py:22-33`); the tool refuses everything but GET and `POST /api/ds/query` (`grafana.py:36-37`); calls `/api/prometheus/grafana/api/v1/alerts` and dashboard search (`grafana.py:133-150`). Runbook says the token is "a Grafana service account with the Viewer role" (`docs/runbook.md:39-41`).
- Anthropic: `anthropic.Anthropic(api_key=ANTHROPIC_API_KEY)` (`agent/client.py:3-12`). Uses server tools (`web_search_20260318`, `code_execution_20260521`, Files API; `agent/registry.py:5`, `agent/files.py:43`) and local cost tracking by token rates (`agent/pricing.py`, `agent/usage.py`, `agent/budget.py`); no Admin API.
- GitHub: no integration. `AGENTS.md` says the UI "carries the IMAP, SMTP, Notion, Google and Anthropic credentials" (`AGENTS.md:112-113`), but `docs/runbook.md:28-48` and the code read all four from the environment file; I found no web code that writes them [inference: the AGENTS.md sentence is aspirational or stale]. v2's UI-editable credentials is therefore new work, not a port.

## 2. Notion

Sources: [Authorization](https://developers.notion.com/guides/get-started/authorization.md), [Internal connections](https://developers.notion.com/guides/get-started/internal-connections.md), [Public connections](https://developers.notion.com/guides/get-started/public-connections.md), [Personal access tokens](https://developers.notion.com/guides/get-started/personal-access-tokens.md), [Capabilities](https://developers.notion.com/reference/capabilities), [Create/refresh a token](https://developers.notion.com/reference/refresh-a-token).

Notion now names three credential types (the docs call them "connections"; "integration" is the older word):

| | Internal connection token | Personal access token (PAT) | Public connection (OAuth 2.0) |
|---|---|---|---|
| Identity | its own bot user | the creating user | the authorizing user (token tied to them) |
| Workspaces | exactly one | one (chosen at creation) | any / selected workspaces (set once at creation) |
| Content access | only pages **shared with the connection** (Add connections menu, or Content-access tab in the developer portal); parent share is inherited by children | everything the creator can access; no sharing step | pages picked in the **OAuth page picker**; parent grants children; user can edit later |
| Capabilities | configured on the connection (read / update / insert content, read+insert comments, user info levels) | two coarse capabilities: *Notion API* and *Workers* | configured on the connection (same list), shown on consent screen |
| Lifetime | static, no expiry | **7 d / 30 d / 90 d / 180 d / 1 y; default 1 y**; expired -> `unauthorized` | access token + **refresh token**; each refresh returns a **new pair** (`POST /v1/oauth/token`, Basic auth client_id:secret) |
| Who can create | Workspace Owner | per plan: Free/Business owners only by default, Plus all members, Enterprise owners+groups | any user who passes the consent screen |
| Survives creator leaving | yes (bot keeps access) | no (loses access with the user) | no (tied to user) |
| `GET /v1/users` (list users) | per capability | **not available** | per capability |
| Setup in atrium | paste token | paste token | atrium needs a registered public connection (client id/secret + redirect URI) |

What this means for atrium:

- The wiki/mirror/nightly writer (`notion_wiki`, `notion_sync`) is a server-owned automation that must keep working when the owner is offline: that is the internal-connection shape. It also gives a hard blast-radius limit (only shared pages), which fits the mail/Notion safety stance in `AGENTS.md`.
- A PAT is the easiest onboarding (no per-page sharing), but it acts with **all of the user's access** (including private pages unrelated to atrium) and has a max 1-year life. Offer it as "quick start", warn about scope, show `expires` (PATs expose it; internal tokens do not). [inference: atrium cannot read a PAT's expiry from the API; ask the user to enter it or detect `unauthorized`.]
- Public OAuth gives the nicest UX (page picker instead of manually sharing), but: every self-hosted instance must create its own public connection in the developer portal with its own redirect URI (installation scope "Selected workspaces only" avoids Marketplace review), store client secret + rotating refresh token, and handle refresh-pair races (two jobs refreshing concurrently invalidate each other [inference from "new access token and new refresh token"]). Worth it only if the owner wants "Connect Notion" without visiting the portal; defer.
- Web UI flow for internal token: Settings -> Notion -> paste token -> **Test** (`GET /v1/users/me`) -> show bot name/workspace -> show "root page" field (today `ATRIUM_NOTION_WIKI_ROOT`, `docs/runbook.md:35-38`) and an `Open in Notion -> Add connections` hint; after sharing, a **Verify access** button runs `POST /v1/search` and lists reachable top pages so misconfiguration ("shared nothing") is visible. A "capabilities" check is not exposed by the API [inference], so surface 403 `restricted_resource` errors with the exact missing capability.
- Notion Workers / agents / Meeting-notes endpoints exist in the API ([openapi tags in the token reference](https://developers.notion.com/reference/refresh-a-token)) but atrium uses none.

Recommendation (Notion): Keep internal-connection token as the primary; add PAT as an alternative; schedule OAuth as optional later. All stored encrypted in the (Phase B) encrypted DB; pinned by config `notion.token_file`.

## 3. GitHub

Sources: [Deciding when to build a GitHub App](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/deciding-when-to-build-a-github-app), [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app), [Generating an installation access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app), [Authenticating as an installation (git over HTTPS)](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation), [App manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), [Authorizing OAuth apps](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps), [OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps), [Managing PATs](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens).

| | OAuth app | GitHub App: user-to-server token | GitHub App: installation token | Fine-grained PAT | Classic PAT |
|---|---|---|---|---|---|
| Permission model | broad scopes (`repo`, `public_repo`, `workflow`, ...); `repo` = full read/write on **every** repo the user can reach, plus org projects/webhooks | fine-grained permissions set on the App; token = **intersection** of App permissions/repos and the user's | fine-grained, per installation; can be narrowed per request via `permissions` | fine-grained per token, single resource owner (one user or one org) | scopes; all repos of user + their orgs |
| Repo selection | none (all the user can access) | installation repo list, **plus** optional `repository_id` (one repo) when exchanging the code/device code | installation repo list, narrowed per request with `repositories` / `repository_ids` (<=500) | "All" or "Only select repositories" | none |
| Lifetime | never expires unless the app opts into expiring tokens (8h + 6-month refresh) | **8 h** access + **6 month** refresh (default; `offline_access` scope forces it) | **1 h** | user-chosen; infinite allowed unless org caps it | optional; auto-removed after 1 year unused |
| Needs private key | no (client secret) | no (client secret; PKCE supported) | **yes**: App private key signs a JWT to mint tokens | no | no |
| Device flow | yes, must be enabled in app settings | yes, must be enabled in app settings | n/a | n/a | n/a |
| Web flow | yes (PKCE supported) | yes (PKCE supported) | n/a | n/a | n/a |
| Org control | org owners approve/deny OAuth app access | org installs the App and picks repos | same | orgs can require approval and cap lifetime; blocked entirely if org disallows | orgs can restrict |
| Attribution | user | "app on behalf of user" | the app bot | user | user |
| Known gaps | check-runs API not available | none notable for git/PR | cannot call user-scoped endpoints | no write to public repos where you are not a member, no outside-collaborator access, one org at a time, no Packages/Checks, no user Projects; 50 token cap | none, but over-broad |

Other facts:
- Installation tokens work for git over HTTPS: `https://x-access-token:TOKEN@github.com/owner/repo.git`, needing the App's "Contents" permission (docs above). `[inference]` user access tokens work as the HTTP password the same way; I did not find a doc line saying so explicitly, so the first implementation ticket should verify it.
- Installation tokens: since 2026-04-27 GitHub is rolling out a stateless token format `ghs_APPID_JWT`; do not assume a 40-char length ([doc note](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)).
- Manifest flow: a `POST` to `github.com/settings/apps/new` (or the org equivalent) with a JSON manifest lets atrium register the App for the owner in one click and receive App ID, private key, webhook secret and client secret from a temporary code (must finish within one hour). The registrant owns the App. Needs atrium to be reachable at a redirect URL. [manifest doc]
- Branch protection / rulesets apply to Apps; an App is not a bypass actor unless added. Atrium's rule "agent never pushes default/protected branches" should be enforced twice: in the manager's typed git tool (refuse the ref) and by an operator-configured GitHub ruleset (defense in depth). [inference from the plan's rule; GitHub doc on rulesets not fetched.]
- Pushing changes to `.github/workflows/*` needs the "Workflows" App permission (App) or `workflow` scope (OAuth/classic). [inference: standard GitHub behaviour, not re-verified in these docs] Leave that permission off by default.

### 3.1 The trade-off requested for the B-G8 grilling: per-env installation token vs per-user OAuth token

Option A, **user OAuth token** (owner's current choice; implemented as GitHub App user-to-server token or classic OAuth app token):
- One human consent, one token per instance; works for every repo the owner can push, including new ones, without further setup. Commits/PRs are attributable to the owner ("app on behalf of user" for App tokens).
- Blast radius if the manager/proxy leaks it: **all repos the user can write**, for an OAuth app up to the lifetime of the token (forever unless expiring tokens are on); for an App user token it is bounded by the installed-repo list and 8 h.
- Requires atrium (not the VM) to refresh tokens and hand the manager a current one; the 8h/6-month cycle needs a stored refresh token and a re-auth path.
- Per-environment narrowing is possible **only** with App user tokens by passing `repository_id` at code exchange or device-flow poll (one repo per token), which means a separate authorization per repo or per environment, not transparent.

Option B, **GitHub App installation token per environment** (repo-bound environment => token minted for just that repo):
- Token is minted on demand by atrium (or the manager) from the App private key, scoped to `repositories: [that repo]` and the minimum `permissions` (e.g. `contents:write`, `pull_requests:write`, `metadata:read`), valid **1 hour**. A leak is a 1-hour, one-repo credential. This best matches "token never enters the VM" and "agent may push non-default branches and open draft PRs".
- Not tied to a user: commits/PRs appear as `<app>[bot]`; works while the owner is offline (scheduled runs). Needs the App private key held by atrium or the manager (a long-lived high-value secret, replacing the long-lived user token), and the owner must **install** the App on each repo/org (one-time per repo, can be "all repos" or a list).
- Cannot act on user-only things (e.g. user-owned Projects, notifications, `GET /user/repos` shows only installed repos [per "incomplete data" note in the App-vs-OAuth doc]).
- Repo-less contexts (browse/clone any repo the user mentions in chat, no environment) need either the user token or an installation covering the repo; with only B the agent can only touch installed repos.

Mixed option C (recommended below): one GitHub App; **user-to-server token** is used for identity, repo discovery (`GET /user/installations`) and read-only tools; **installation tokens** (1 h, one repo, minimal permissions) are minted for push/PR operations bound to an environment. Costs: one more secret (private key) and the install click. If B-G8 insists on only user OAuth, choose App user-to-server tokens over an OAuth app (expiring tokens, repo list, `repository_id` narrowing) and keep the private key out of scope.

Web UI flow (GitHub):
1. Settings -> GitHub -> **Create GitHub App** (manifest POST; atrium shows the pre-filled manifest: name, homepage, callback URL, device flow enabled, expiring tokens, permissions `contents:rw`, `pull_requests:rw`, `metadata:r`, optional `actions:r`, `issues:rw`).
2. GitHub redirects back with a code; atrium stores App ID, client id/secret, private key, webhook secret (encrypted).
3. **Install** link (`https://github.com/apps/<slug>/installations/new`); owner picks repos.
4. **Sign in with GitHub** (web flow with PKCE, or device flow button on headless/behind-proxy instances); atrium stores access + refresh token.
5. Pinned config alternative: `github.app_id`, `github.client_id`, `github.client_secret_file`, `github.private_key_file` pin the App; the UI then shows them read-only (matches the plan's pinning model).
6. Fallback "paste a fine-grained PAT" for owners who don't want an App; the UI warns about the gaps in section 3 (no public-repo contributions, one owner, expiry).

## 4. Grafana

Sources: [Service accounts](https://grafana.com/docs/grafana/latest/administration/service-accounts/), [Service account HTTP API](https://grafana.com/docs/grafana/latest/developers/http_api/serviceaccount/), [Roles and permissions](https://grafana.com/docs/grafana/latest/administration/roles-and-permissions/).

- Service accounts replace API keys as the supported way to authenticate to the Grafana HTTP API. Creating one needs org Admin or `fixed:roles:reader` + `fixed:serviceaccounts:creator`. Each SA gets an org role (None/Viewer/Editor/Admin); tokens are `Authorization: Bearer ...`.
- Token expiry: default **no expiration** (`secondsToLive` 0) but can be set at creation, and admins can cap it with `token_expiration_day_limit`.
- Roles: Viewer, Editor and Admin can all "query data sources directly" (roles table); only Admin edits data sources/users/teams/org. Atrium's calls (datasource list, `/api/ds/query`, alert list, dashboard search) are covered by **Viewer** [inference: datasource-list and alert-read permissions on Viewer follow Grafana defaults; not exhaustively checked per endpoint]. Enterprise/Cloud RBAC can narrow further (datasource-level permissions, custom roles).
- Grafana Cloud: use the stack's service accounts for the HTTP API; Cloud Access Policies are for sending/querying telemetry directly ([Grafana docs note](https://grafana.com/docs/grafana/latest/administration/service-accounts/)). Atrium goes through the Grafana API, so a service-account token is correct. Users of other auth (basic auth, OIDC) have no non-interactive equivalent worth supporting.
- Web UI flow: Settings -> Grafana -> URL + token -> **Test** (`GET /api/org`, `GET /api/datasources`) -> show role detected (`GET /api/user` returns the service-account identity [inference]) and warn if the token is Editor/Admin ("atrium only needs Viewer"). Include instructions: Administration -> Users and access -> Service accounts -> Add service account (role Viewer) -> Add token (set expiry).
- Note: the token has no org scoping beyond its org; multi-org Grafana needs one token per org (a future "multiple Grafana instances" ticket).

## 5. Anthropic

Sources: [Authentication](https://platform.claude.com/docs/en/manage-claude/authentication.md), [Admin API](https://platform.claude.com/docs/en/manage-claude/admin-api.md), [Usage and Cost API](https://platform.claude.com/docs/en/manage-claude/usage-cost-api.md), [Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance.md), [Claude Code authentication](https://code.claude.com/docs/en/authentication.md).

Credential types (all verified in the docs above):
- **API key** (`sk-ant-api...`): Console-created, chosen type: **personal key** (acts as you), **service-account key** (own identity), **workspace key** (legacy). Each can be bound to one workspace. Optional **expiration at creation** (3 h, 1 d, 7 d, 30 d, custom, or Never), not changeable later; Anthropic emails the creator before expiry. Keys can be disabled/deleted; the Admin API can read, rename and change status of keys but **cannot create them**.
- **Workload Identity Federation**: exchange an IdP token for a short-lived access token. Useful for cloud/CI workloads; atrium on a VPS with an OIDC identity could use it [inference], but it removes nothing the owner needs today.
- **Admin API key** (`sk-ant-admin...`), or an `org:admin` OAuth token, or an unscoped personal/service-account key: manages members, workspaces, keys, and the **Usage & Cost API** and rate-limit reports. Unavailable to individual accounts (needs an organization). Workspace keys do not work there.
- **Subscription OAuth** (Pro/Max/Team/Enterprise): `claude login` or `claude setup-token` (one-year token, env `CLAUDE_CODE_OAUTH_TOKEN`, model requests only). Anthropic's terms: OAuth is intended exclusively for the Claude apps; developers building products or using the Agent SDK must use API keys; third parties may not "offer Claude.ai login", "route requests through Free, Pro, or Max plan credentials on behalf of their users", and "may not collect, store, or intermediate Claude.ai credentials or session tokens". But "an end user [may] sign in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code" ([legal & compliance](https://code.claude.com/docs/en/legal-and-compliance.md)).

What each permits for atrium:
| Need | API key | Admin credential | Subscription OAuth |
|---|---|---|---|
| Agent loop (Messages API, web_search, Files, code_execution) | yes | no (admin endpoints only) | **no** (prohibited for non-Claude-Code software) |
| Spend visibility (server-side) | no | yes (Usage & Cost API; matches Console cost page) | no |
| Claude Code running unmodified in the sandbox | yes (`ANTHROPIC_API_KEY`) | no | yes, **only** the user's own sign-in to the real binary |

Recommendations (Anthropic):
1. Assistant loop: one Console key per instance, preferably a **service-account or workspace-bound key** in a dedicated workspace whose spend limit equals the instance budget (a server-side backstop for the in-app $150 budget). Set an expiry (e.g. 30-90 days) and surface `expires_at`-style warnings in the UI; because the Admin API cannot mint keys, rotation is a manual paste unless the owner supplies an admin credential (even then it cannot create keys). `[inference]` rotation therefore stays a paste flow.
2. Optional "Usage reconciliation" setting: an Admin API key lets atrium compare its local cost estimate (`agent/pricing.py`) with Anthropic's cost report and correct budget drift. This is a nice-to-have; admin keys are powerful (member/workspace/key management) so store separately, label "high privilege", and never expose it to tools or the VM.
3. Never store subscription tokens for the assistant. For Claude Code in the sandbox: the compliant path is the user performing the interactive sign-in **inside the sandbox's Claude Code** (browser/device flow handled by the binary, credentials stay in the VM profile and never pass through atrium), or an API key. A `claude setup-token` value pasted into atrium's DB conflicts with "may not collect, store, or intermediate Claude.ai credentials" [inference: ambiguous for a single-owner self-hosted instance; needs the owner's call and ideally Anthropic's confirmation]. Hand to ticket R32 (pi/Claude) and the sandbox-secrets design.
4. Web UI flow: Settings -> Anthropic -> paste key -> **Test** (a 1-token Messages call against Haiku, or `GET /v1/models`) -> display workspace/key type if the key is an Admin-visible type; budget fields (monthly budget, 80%/100% thresholds) beside it.

## 6. Cross-cutting design questions

1. Credential store: DB-stored, encrypted (Phase B key file), pinned by config. Phase A must keep env-only (`NOTION_TOKEN`, `GRAFANA_VIEWER_TOKEN`, `ANTHROPIC_API_KEY`) since the port is "as-is" (`PLAN`, Phase A).
2. A uniform "Test connection" result model for all integrations (ok / degraded / failed + reason + remediation text), reused for the module-enable gating (module disabled until its test passes).
3. Credential metadata: store `created_at`, optional `expires_at`, last-success, last-failure; surface expiring tokens (Notion PAT <= 1 y, Anthropic key expiry, Grafana SA expiry, GitHub refresh 6 months) on the dashboard and via ntfy.
4. Token refresh concurrency (GitHub 8 h access, Notion OAuth pairs): single-flight refresh with atomic persistence of the new pair.
5. Secrets never to the model or the VM: tool results must not echo tokens; the GitHub token is injected by the manager/proxy only.

## 7. Options and trade-offs (summary)

| Integration | Option | Pros | Cons |
|---|---|---|---|
| Notion | internal token | simple, bot identity, scoped by sharing, no expiry | manual share step; Workspace Owner needed; no per-user attribution |
| Notion | PAT | no sharing step | acts as user across entire workspace; <=1y; no user list; dies with user |
| Notion | OAuth | page picker UX | each instance needs its own public connection; refresh rotation; user-tied |
| GitHub | App (user token + installation tokens) | best scoping, short lifetimes, manifest registration in UI | more moving parts; private key to protect; install step |
| GitHub | OAuth app | simplest | `repo` scope = everything; non-expiring tokens by default |
| GitHub | fine-grained PAT | simplest paste flow, per-repo | manual rotation; single resource owner; gaps; no device flow |
| Grafana | SA token Viewer | least privilege | per-org; manual rotation |
| Anthropic | Console key | permitted, billable, workspace-limitable | static secret; cannot be minted via API |
| Anthropic | subscription OAuth | cheaper for heavy use | prohibited in third-party loop; credential storage restricted |

## 8. Risks

- Anthropic policy drift on subscription use (the policy text moved in Feb 2026 per the compliance page history [inference]); atrium should not depend on it.
- GitHub App model complexity may delay the first GitHub release; the PAT fallback bounds this.
- GitHub App user-token refresh token expiry (6 months) forces periodic re-auth for idle instances.
- Notion token drift: a revoked/renamed internal connection yields silent empty search results (no pages shared) rather than an error; detect via "reachable pages == 0".
- Storing an App private key and Admin key raises the value of the DB; makes the encrypted-DB ticket a prerequisite for shipping these.

## 9. Candidate new tickets

1. **GitHub App manifest registration flow in the web UI** (research+build, phase B, area github): can atrium register its own App from a manifest behind a reverse proxy with OIDC; what redirect URL/handling does GitHub require; how to store the credentials.
2. **GitHub token strategy decision: user-to-server vs per-environment installation tokens** (grilling, phase B, area github): choose among options A/B/C in section 3.1; decide who holds the private key (atrium vs manager) and how the manager receives tokens (push vs pull).
3. **Verify git-over-HTTPS with a GitHub App user access token and draft-PR creation permissions** (research, phase B, area github): `x-access-token` with `ghu_` tokens, required App permissions for push + draft PR, workflows permission, ruleset behaviour for Apps.
4. **Credential health and expiry model** (design, phase B, area core/settings): `expires_at` fields, expiry warnings via ntfy, uniform Test connection, pinned-setting display.
5. **Notion connection onboarding wizard** (build, phase B, area notion): token vs PAT choice, Verify access via search, "no pages shared" detection, root-page picker.
6. **Notion OAuth (public connection) support** (optional, phase B+, area notion): only if the owner wants one-click connect; includes refresh-pair single-flight.
7. **Anthropic usage reconciliation via Admin/Usage-Cost API** (research+build, phase B, area assistant): optional admin key, compare against `agent/pricing.py`, drift correction of the monthly budget.
8. **Anthropic workspace-per-instance guidance and key rotation docs** (docs, phase B, area assistant): workspace spend limit as a backstop, key expiry choices.
9. **Claude Code in sandbox: compliant subscription sign-in path** (research, phase B, area sandbox): in-VM interactive login vs stored `setup-token`; read against "collect/store/intermediate" wording; confirm with Anthropic if needed (coordinate with the pi/Claude research ticket).
10. **Grafana multi-instance / multi-org support** (grilling, phase B+, area grafana): single token per org limits; whether more than one Grafana is needed.
11. **Encrypted credential store prerequisite order** (planning, phase B, area core): the encrypted DB must land before GitHub App private key and Admin key storage ship.
12. **AGENTS.md invariants correction**: the sentence claiming the UI carries Notion/Anthropic credentials does not match code (`AGENTS.md:112-113` vs `docs/runbook.md:28-48`); fix in Phase A docs (chore, phase A, area docs).
