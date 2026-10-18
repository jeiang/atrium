# GitHub App manifest flow, git over HTTPS and draft-PR permissions (ticket #77)

Researched 2026-10-17 against docs.github.com (REST API version `2026-03-10`, the version the docs now show). Atrium facts cite `path:line` at `origin/main` c221de8. External claims link to the owning doc. `[inference]` marks my own reading or something I could not confirm from a doc. **Nothing was executed against GitHub**: no app was created (the ticket forbids it), so every behaviour below is documented, not measured.

Builds on [#34](https://github.com/jeiang/atrium/blob/research/auth-notion-github-grafana-anthropic/research/auth-notion-github-grafana-anthropic.md) (section 3) and on the egress/git-gateway design in [research/egress-proxy](https://github.com/jeiang/atrium/blob/research/egress-proxy/research/egress-proxy.md) (section 3).

## 0. Answers in one table

| # | Question | Answer | Confidence |
|---|---|---|---|
| 1 | Manifest registration from the web UI? | **Yes, entirely from the UI**, with one unavoidable GitHub-side click ("Create GitHub App") and, later, one install click. atrium serves a form that `POST`s `manifest` JSON (+ `state`) to `github.com/settings/apps/new` (or `/organizations/ORG/settings/apps/new`); GitHub redirects the browser to `redirect_url?code=...&state=...`; atrium `POST`s `/app-manifests/{code}/conversions` (unauthenticated, one-time, within 1 h) and receives `id`, `slug`, `client_id`, `client_secret`, `webhook_secret`, `pem`. | documented ([manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest), [conversion API](https://docs.github.com/en/rest/apps/apps#create-a-github-app-from-a-manifest)) |
| 2 | Git over HTTPS with App tokens? | **Yes for both token kinds.** The doc says "request the Contents repository permission ... use the access token as the HTTP password": `https://x-access-token:TOKEN@github.com/owner/repo.git` and names "installation **or user** access token". This closes the #34 `[inference]` about `ghu_`. | documented ([choosing permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app), [installation auth](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation)) |
| 3 | Permissions? | Branch push: **Contents: write**. Draft PR: **Pull requests: write** + body `draft: true`. Issues: **Issues: write** (comments on PRs also need it or Pull requests). CI logs: **Actions: read** (+ **Checks: read** for check runs, **Commit statuses: read**). Workflow files: separate **Workflows: write**, which atrium should leave off. **Metadata: read** is mandatory/implicit. | documented ([permissions table](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps)) |
| 4 | Rulesets / protection vs App tokens? | Rules apply to Apps like any actor. **But Contents: write is enough to call the merge API, delete refs and PUT file contents**, so "default branch and merges stay operator actions" cannot rest on the permission alone: it needs a ruleset with an **empty bypass list** (backstop) plus a manager tool surface that has no merge/delete verbs. Draft PRs may be unavailable on private repos of Free/Pro personal accounts (section 5). | documented + inference |
| 5 | Device vs web flow, 8 h + refresh? | Web flow **with PKCE still needs `client_secret`** (atrium is a confidential server client, fine). Device flow needs an extra manual toggle that is not a manifest field, GitHub advises against enabling it unless headless, and its refresh needs no secret. Access token 8 h, refresh token 6 months, **refresh token is single-use** (rotates every refresh). | documented |

**Recommendation in one line:** register one private GitHub App via the manifest flow; do all repo work with **installation tokens atrium mints on demand (1 h, one repo, narrowed `permissions`)** and hands to the manager per call; make user-to-server tokens (web flow + PKCE) optional/off by default; protect default branches with an operator-owned ruleset (no bypass actors) in addition to the manager's typed tools.

## 1. Manifest flow, step by step (what atrium must implement)

Source: [Registering a GitHub App from a manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).

1. **Settings -> GitHub -> Create GitHub App** page in atrium. Fields: owner (personal account, or an org slug), app name. atrium renders a plain HTML `<form method=post action="https://github.com/settings/apps/new?state=<nonce>">` with a hidden `manifest` input holding the JSON. It must be a normal form submission from the user's browser (cross-origin POST to github.com); htmx must not intercept it (`hx-boost="false"`).
2. GitHub shows its own page where the user can edit the name and clicks **Create GitHub App**. If the manifest has no `name`, the user types one. Names must be unique on GitHub [inference: standard GitHub App naming rule; the doc example uses "Octoapp"]; atrium should suggest `atrium-<random suffix>`.
3. GitHub redirects the **browser** to `redirect_url?code=<temp>&state=<nonce>`. Because it is a top-level browser redirect, atrium behind an OIDC reverse proxy needs no proxy exemption: the user's existing session cookie rides along. The callback must validate `state` (the doc: "unguessable random string ... protect against CSRF"): bind it to the signed-in session, single use, short TTL.
4. atrium `POST https://api.github.com/app-manifests/{code}/conversions` with `Accept: application/vnd.github+json`. No auth header. Responses: **201** with the App object plus `client_id`, `client_secret`, `webhook_secret` (string or null), `pem`; **404** (unknown/used code); **422** "validation failed or the endpoint has been spammed". Rate limited. The three steps must all finish **within one hour**.
5. atrium persists in **one DB transaction** before rendering anything: `app_id`, `slug`, `client_id`, `client_secret`, `pem`, `webhook_secret`, `owner.login/id`, `html_url`. Then verifies by minting a JWT and calling `GET /app`.
6. Show **Install** button: `https://github.com/apps/<slug>/installations/new` (private apps install only on the owning account, see [installing your own app](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app)). Installing lets the owner pick **All repositories / Only select repositories**.

### Manifest parameters atrium would send

From the doc's parameter table (names verbatim): `name`, `url` (required), `hook_attributes{url (required if given), active}`, `redirect_url`, `callback_urls[]` (up to 10), `setup_url`, `description`, `public`, `default_events[]`, `default_permissions{}`, `request_oauth_on_install`, `setup_on_update`.

```json
{
  "name": "atrium-ab12cd",
  "url": "https://atrium.example.org",
  "description": "Personal secretary / coding harness",
  "public": false,
  "redirect_url": "https://atrium.example.org/settings/github/manifest/callback",
  "setup_url": "https://atrium.example.org/settings/github/installed",
  "callback_urls": ["https://atrium.example.org/settings/github/oauth/callback"],
  "hook_attributes": {"url": "https://atrium.example.org/github/webhook", "active": false},
  "default_permissions": {
    "metadata": "read", "contents": "write", "pull_requests": "write",
    "issues": "write", "actions": "read", "checks": "read", "statuses": "read"
  },
  "default_events": []
}
```

- Permission key names follow the REST permission names (`contents`, `pull_requests`, `issues`, `actions`, `checks`, `statuses`, `workflows`, `metadata`); the manifest doc points at the permissions page for "parameterized names". The installation-token body in [the REST doc](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app) uses the same names (`actions`, `administration`, ...).
- `public: false` => "Only on this account" ([making an app private](https://docs.github.com/en/apps/using-github-apps/installing-your-own-github-app)); matches "one instance, one owner".
- `callback_urls` only matters if user-to-server tokens are used ([callback URL doc](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url)). Omit it when going installation-token-only.
- **What the manifest cannot express** (not in the parameter table): *Enable Device Flow* and the *Expire user authorization tokens* setting. Expiry is on by default for newly created apps ([refresh doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens): "When you create your app, expiration of user access tokens is enabled unless you opt out"). Device flow needs the owner to tick *Enable Device Flow* in the App settings by hand [inference: absent from the manifest table; I could not test whether GitHub silently accepts an undocumented key]. Do not design around an undocumented key.
- `hook_attributes.active: false`: the doc table lists `active` (default true) but only `url` as required, so a disabled webhook should be expressible [inference; untested]. If atrium is VPN-only (the owner's current setup: `docs/runbook.md:122`, `ATRIUM_PUBLIC_URL` at `src/atrium/media/settings.py:50`), GitHub cannot deliver webhooks anyway. The conversion response still carries a `webhook_secret` (nullable), so store it if present.

### Failure modes to design for

- **Code is one-shot.** If atrium crashes between GitHub's redirect and the DB commit, the secrets are lost; the only recovery is to delete the App in GitHub's UI and re-run. Persist first, render second; show a clear "App created but not saved: delete `<slug>` at github.com/settings/apps and retry" error otherwise.
- **Base-URL changes are not API-editable** [inference: I found no REST endpoint for updating an existing App's callback/redirect URLs; the docs only describe the settings UI, [modifying a registration](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration)]. If the instance moves domain, user-flow callbacks break until the owner edits the App in GitHub. Another reason for installation-token-only.
- **Callback URL matching is exact for new apps**; wildcard matching is off by default for apps created after 2026-08-03 ([callback URL doc](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url)). Do not enable it.
- **Setup URL `installation_id` is untrusted** ([setup URL doc](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url) warns about spoofed IDs). atrium holds the JWT, so it can verify with `GET /app/installations/{id}` and check `account.login` equals the registered owner; no user token is needed for that.
- **Permission changes need installation re-approval.** Adding a permission later (e.g. enabling `workflows`) shows an approval prompt to each installation owner; until approved the installation keeps the old permissions ([choosing permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app), line "the owner of each account ... will be prompted"). Atrium should read the permissions the *installation* actually has (`GET /app/installations/{id}` returns `permissions`) and show a drift warning rather than assume the manifest was applied.
- Pinned alternative (matches the plan's pinning model): `github.app_id`, `github.client_id`, `github.client_secret_file`, `github.private_key_file`, `github.webhook_secret_file`, optional `github.installation_id`. The UI shows them read-only and hides the Create button.

## 2. Tokens and git over HTTPS

| | Installation token (IAT) | User access token (UAT) |
|---|---|---|
| Minted by | `POST /app/installations/{id}/access_tokens` with a JWT ([doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation)) | web flow or device flow ([doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)) |
| Needs | App private key (JWT `RS256`, `iat` = now-60 s, `exp` <= now+10 min, [doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)) | `client_id` + `client_secret` (web) or `client_id` only (device), plus the user to click |
| Lifetime | **1 h**, not refreshable: mint a new one | **8 h** (`expires_in=28800`); `refresh_token` valid **6 months** (`15897600` s), starts `ghr_`; access token starts `ghu_` |
| Prefix | `ghs_`; since 2026-04-27 rolling out a stateless `ghs_APPID_JWT` format, **not 40 chars** ([REST doc note](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app)) | `ghu_` |
| Narrowing | per request: `repositories`/`repository_ids` (<=500) and `permissions` (subset of the app's) | `repository_id` (one repo) at code exchange; otherwise intersection of app permissions/repos and the **user's** |
| Attribution | the app (`<slug>[bot]`) | the user plus the app badge ([on-behalf-of doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-with-a-github-app-on-behalf-of-a-user)) |
| Git over HTTPS | `https://x-access-token:TOKEN@github.com/o/r.git`, Contents permission | same, doc says "installation or user access token" |
| Revocation | `DELETE /installation/token`; leaks are auto-revoked if pushed to a public repo ([doc](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation)) | user revokes authorization; `github_app_authorization` webhook; credential-revocation API supports `ghu_`/`ghr_` |
| Idle expiry | n/a | refresh token 6 months; OAuth/PAT tokens unused for 1 year are revoked (doc above names OAuth token and PAT, not UAT: `[inference]` UAT follows the 8 h/6 month cycle) |

Details that matter for atrium:

- **Single-use refresh token.** "Once you use a refresh token, that refresh token and the old user access token will no longer work" ([refresh doc](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)). Two concurrent refreshes would strand the instance. Needs a single-flight lock plus atomic persistence of the new pair (same as #34 risk).
- **Idle instances.** Every successful refresh returns a new 6-month refresh token, so a weekly keep-alive refresh job removes the "forced re-auth after 6 months idle" risk from #34. Failure of refresh (`bad_refresh_token`) must surface as "Sign in with GitHub again".
- **Refresh without a client secret** is allowed only for tokens obtained through the device flow; web-flow tokens need `client_secret` at refresh too.
- **PKCE is additive, not a substitute for the secret**: the code-exchange table lists `client_secret` as **Required** and `code_verifier` as "strongly recommended". atrium is a server-side confidential client, so use web flow + `state` + PKCE ([web flow section](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app#using-the-web-application-flow-to-generate-a-user-access-token)).
- **Device flow trade-offs.** Poll `POST https://github.com/login/oauth/access_token` no faster than `interval` (default 5 s, `slow_down` adds 5 s); `user_code`/`device_code` expire after 900 s. Best-practices says device flow "does not require redirect URIs at all, which means that an attacker can use the device flow to remotely impersonate your app as part of a phishing attack ... do not enable the device flow ... unless you are using the app in a constrained environment (CLIs, IoT devices, or headless systems)" ([best practices](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app#dont-enable-device-flow-without-reason)). atrium has a browser and a stable callback, so web flow wins; keep device flow as a documented opt-in for instances whose public URL GitHub's redirect cannot use.
- Token cache: GitHub's guidance is to cache tokens and reuse until expiry ([best practices](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app#cache-tokens)). Keep IATs **in memory only** with a safety margin (e.g. re-mint at 50 min); never persist them.
- **User errors worth mapping to UI messages:** `unverified_user_email`, `redirect_uri_mismatch`, `device_flow_disabled`, `bad_refresh_token`, `incorrect_client_credentials` (all in the same doc's troubleshooting section). SAML-protected org repos need an active SAML session before authorizing.

### Git gateway usage (fits egress-proxy design B)

The manager-side smart-HTTP gateway needs only an HTTP header on upstream calls: `Authorization: Basic base64("x-access-token:" + IAT)` against `https://github.com/<owner>/<repo>.git/...`. The doc-stated minimum is the Contents permission. `[inference]` GitHub accepts the same credential for `git-upload-pack` (read) and `git-receive-pack` (write) with the per-permission split (contents:read vs write); the docs say read-only clones need read, push needs write, but I did not find a doc line that states the exact status code for a read-only token pushing (expected 403).

Mint one token per operation class so the credential in memory matches the verb:

| Operation | `permissions` in the token request | `repositories` |
|---|---|---|
| clone/fetch, `git_log`, file reads | `contents: read` | the env's repo |
| push branch, delete none | `contents: write` | the env's repo |
| open/update draft PR, comment | `pull_requests: write` (+ `issues: write` for labels/assignees on PRs) | the env's repo |
| list runs, fetch logs | `actions: read`, `checks: read`, `statuses: read` | the env's repo |
| issue read/write tools | `issues: read` or `write` | selected repos |

The token request cannot grant anything the App was not granted, so a read-only token minted from a write-capable App is a real narrowing, not a convention.

## 3. Permission matrix for atrium's GitHub module

Sources: [Permissions required for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps) (endpoint -> permission, token types), [pulls API](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request).

| Capability | Endpoint(s) | App permission | UAT/IAT |
|---|---|---|---|
| clone / fetch | git smart HTTP | Contents: read | both (doc line, section 2) |
| push (create/update branch) | git smart HTTP; `POST/PATCH /git/refs` | **Contents: write** (`POST/PATCH git/refs` also *additionally* needs Workflows if the commit touches `.github/workflows`) | both |
| draft PR | `POST /repos/{o}/{r}/pulls` with `draft: true` | **Pull requests: write** | both |
| update/close PR, request reviewers | `PATCH /pulls/{n}`, `.../requested_reviewers` | Pull requests: write | both |
| issue read/create/comment, labels | `/issues...`, `/issues/{n}/comments`, `/labels` | Issues: write (many of these accept Pull requests: write as the *additional permission*) | both |
| PR review comments | `/pulls/{n}/comments`, `/reviews` | Pull requests: write | both |
| CI run list/jobs/logs/artifacts | `GET /actions/runs`, `/actions/jobs/{id}/logs`, `/actions/runs/{id}/logs` | **Actions: read** | both |
| re-run a failed job / cancel | `POST /actions/runs/{id}/rerun-failed-jobs`, `/cancel` | Actions: write | both; optional extra, not needed for read-only CI triage |
| check runs / commit statuses | `GET /commits/{ref}/check-runs`, statuses | Checks: read / Commit statuses: read | both |
| edit `.github/workflows/**` | git push, `PUT /contents/{path}` | **Workflows: write** | both |
| list installed repos | `GET /installation/repositories` (IAT) or `GET /user/installations/{id}/repositories` (UAT) | none beyond Metadata | respective |

Notes:

- **Metadata: read** is always granted; it is the permission behind repo-list and search-style reads.
- **`workflows` is a separate gate for git pushes too.** A push that creates/updates a workflow file with an App token lacking Workflows is rejected by GitHub with `refusing to allow a GitHub App to create or update workflow '<path>' without 'workflows' permission` (error text as reported in [community #35410](https://github.com/orgs/community/discussions/35410); not in the official docs [inference: wording]). The rejection applies to any commit in the pushed range that touches that path, even incidentally ([community #27224](https://github.com/orgs/community/discussions/27224)). Recommendation: **omit Workflows**, and surface this specific error as "this branch changes CI files; ask the operator". Rationale: pushes with App tokens **do trigger Actions** (unlike `GITHUB_TOKEN`; [doc](https://docs.github.com/en/actions/concepts/security/github_token): events caused by `GITHUB_TOKEN` do not start runs, and it recommends an App installation token to get runs), so an agent that could edit a workflow could run arbitrary code with the repo's Actions secrets on its next push.
- A doc contradiction to flag: the intro of the [REST apps page](https://docs.github.com/en/rest/apps/apps) still says editing workflow files requires "an OAuth token that includes the `workflow` scope"; the newer permissions table lists the **Workflows** permission for both UAT and IAT, and [choosing permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app) says "request the Workflows repository permission". Prefer the permission pages; verify when the first implementation ticket first needs workflow edits (probably never).
- **Contents: write grants more than push.** The same table shows these under Contents/write: `PUT /pulls/{n}/merge`, `PUT .../merge-async`, `POST /merges`, `DELETE /git/refs/{ref}`, `POST/PATCH /git/refs`, `PUT/DELETE /contents/{path}`, `POST /releases`, `DELETE /releases/{id}`. See section 4 for the consequence.
- **Never request Administration.** It controls rulesets, branch protection, repo settings (`GET/POST/PUT /rulesets` are Administration-gated for orgs and repos) and, per the protected-branch doc, "people and apps with admin permissions ... are always able to push to a protected branch". An App holding Administration could edit the ruleset that restrains it.
- Draft PRs: `draft` is a documented boolean body parameter of `POST /repos/{owner}/{repo}/pulls`. See the plan-tier caveat in section 5.

## 4. Rulesets and protected branches vs an App token

Sources: [About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches), [Creating rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository), [Available rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets).

What GitHub documents:

- A ruleset can list **GitHub Apps** as bypass actors; an App that is not in the list is bound by the rules. Rules include *Restrict creations*, *Restrict updates* ("only users with bypass permissions can push"), *Restrict deletions*, *Require a pull request before merging*, *Block force pushes*, *Require signed commits*, *Require status checks*, *Restrict file paths* (could block `.github/workflows/**`).
- Targeting is `fnmatch` with include **and exclude** patterns, e.g. include default branch + `*`, exclude `atrium/*`. `*` does not cross `/` unless `**` ([fnmatch section](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository#using-fnmatch-syntax)).
- Classic branch protection: by default *not applied* to repository admins; "Restrict who can push" works only for repos in an **organization** (Free org public repos, Team/Enterprise); "Actors may only be added to bypass lists when the repository belongs to an organization" ([protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches)). "People and apps with admin permissions ... are always able to push".
- Bypass can be limited to "for pull requests only" for an actor ([rulesets doc](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository#granting-bypass-permissions-for-your-branch-or-tag-ruleset)).
- Required signed commits apply to "contributors and bots"; commits pushed via plain `git push` from the manager are unsigned unless atrium signs them. API-created commits are signed by GitHub for Apps [inference: not confirmed from a doc in this pass]. If an operator ruleset requires signatures, the manager's push path must either sign (key on the manager) or the repo must exempt `atrium/*`.

What this means for the plan's invariant "agent may push non-default branches and open draft PRs; default/protected branches, merges, deletes stay operator actions":

1. **Installation token = non-admin actor** as long as the App lacks Administration `[inference]`; so protected-branch rules bind it. **A user access token acts as the user**: if the owner is a repo admin, classic protection with default "admins bypass" does *not* bind a UAT `[inference: the UAT can do what the user can, per the on-behalf-of doc "a token has the same capabilities ... that the owner of the token has" intersected with app permissions]`. Rulesets without bypass actors bind admins too, so rulesets are the only reliable backstop in the UAT case. This is a real safety argument for IAT.
2. **PRs authored by `<app>[bot]`** let the owner approve them even under "require approval of someone other than the last pusher"; PRs authored via a UAT are authored by the owner, who then cannot self-approve [inference: GitHub's no-self-approval rule, not re-verified here]. Another IAT advantage for a solo operator.
3. **The permission cannot express "no merge, no delete"**: Contents: write includes merge and ref-delete endpoints (section 3). So the guarantee has three layers, each independently sufficient for one failure class:
    - **Tool surface** (manager): only typed verbs `git_push_branch`, `pr_open_draft`, ... no merge, no delete, no force push, no tag push, no generic `gh api`. The VM never holds a token (egress-proxy design), so a prompt-injected agent cannot call REST directly.
    - **Gateway policy** (manager): reject receive-pack commands that delete or force-update refs, or that target the default branch or a configured protected list ([egress-proxy sheet](https://github.com/jeiang/atrium/blob/research/egress-proxy/research/egress-proxy.md) section 3: the Anthropic proxy deliberately does *not* limit branches, "use branch protection rules or rulesets on GitHub", atrium should do both).
    - **GitHub ruleset** the operator owns (empty bypass list): branch rule on default branch `Restrict updates`, `Restrict deletions`, `Require a pull request`, `Block force pushes`; optionally a second ruleset targeting `~ALL` minus `atrium/**` with `Restrict creations` + `Restrict updates`, which confines the App to `atrium/*` branches without any gateway logic [inference: composition of documented include/exclude targeting]. atrium can *suggest* the ruleset JSON (importable per the rulesets doc) but should not create it: that needs Administration, which atrium must not hold.
4. **Personal-account repos** (the owner's current repos, e.g. jeiang/atrium) cannot use classic "Restrict who can push" and bypass lists, per the org-only wording above; rulesets are the portable mechanism [inference: ruleset availability on user-owned repos by plan tier not verified here; verify on one repo].
5. **PR creation itself** is gated by the same rules only at merge time; an App can open PRs against any branch it can see, including targeting `main`. That is intended.

## 5. Draft PRs: a plan-tier caveat

The pull-requests REST doc states: "Draft pull requests are available in public repositories with GitHub Free and GitHub Free for organizations, GitHub Pro, and legacy per-repository billing plans, and in public and private repositories with GitHub Team and GitHub Enterprise Cloud" ([pulls API](https://docs.github.com/en/rest/pulls/pulls#list-pull-requests)). Read literally, **private repos on a personal Free or Pro account cannot have draft PRs**. Do not rely on `draft: true` succeeding: the PR tool should catch a 422 and retry non-draft with a `[atrium draft]` title prefix and an `atrium` label, and the settings page should state the plan limit. `[inference: exact error body for `draft: true` on an ineligible plan not verified.]`

## 6. Atrium-side design implications

- **Where the private key lives.** The key signs JWTs that mint tokens for *every* installed repo ([best practices](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app): "grants access to every account that the app is installed on"). Options: (a) atrium holds it (encrypted DB, or `private_key_file` pinned) and **pushes 1 h scoped tokens to the manager per call**; (b) the manager holds it and atrium asks the manager for tokens. (a) keeps the long-lived secret off the beefier, more exposed sandbox host and lets scheduled runs mint tokens with no manager state; atrium (VPS) already holds the Anthropic key. Recommended: (a); manager holds nothing durable about GitHub. Per-sandbox request: `POST /v1/sandboxes/{id}/git/token {token, expires_at, repo, mode}` over the existing mTLS link; the manager keeps it in memory only. Cost: atrium must be reachable by the manager when a token is needed (acceptable; the manager is already driven by atrium).
- **Encrypted store prerequisite** (already ticket 11 in #34): `pem`, `client_secret`, `webhook_secret`, UAT/refresh pair all land in the DB; ship the encrypted-DB ticket first.
- **Rotation.** GitHub allows up to 25 private keys per app and recommends generating a second before deleting the first ([managing private keys](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps)); there is no documented API to rotate; UI shows a "paste new key" form. Fingerprint check: SHA-256 fingerprint compare is documented.
- **Revocation detection without webhooks**: on 401 from a token request, mark the App "key revoked / installation suspended" and stop scheduled runs that need GitHub (installations can be suspended/unsuspended, [endpoints](https://docs.github.com/en/rest/apps/apps#suspend-an-app-installation)).
- **Repo discovery**: `GET /installation/repositories` with an IAT lists exactly what the owner installed; the environment form can then offer a repo/branch picker without any user token.
- **Commit identity**: set `user.name`/`user.email` on the manager's scratch repo; either the owner's identity (config) or `<slug>[bot]` / `ID+slug[bot]@users.noreply.github.com` (per [community #35410 answer pattern](https://github.com/orgs/community/discussions/35410), not an official doc line). This is metadata only; authorization is by the token.
- **Fallback**: pasted fine-grained PAT remains a one-repo-owner option (#34); same gateway code, different token source.

## 7. Options and trade-offs

| Option | Pros | Cons |
|---|---|---|
| **A. IAT only (recommended)** | No user sign-in, no refresh state, works for scheduled runs, narrowest tokens (1 h, one repo, per-verb permissions), bot attribution, owner can approve agent PRs, no 6-month re-auth | Owner must install the App on each repo (or pick "All"); cannot call user-scoped endpoints (user-owned Projects, notifications); PRs attributed to the bot; private key is a high-value secret |
| B. UAT only | PRs/commits attributed to the owner; no private key; matches the plan's literal "per-user OAuth" | Token acts as an admin if the owner is one (can bypass classic protection); 8 h + single-use refresh rotation; 6-month idle expiry; needs callback URL or device flow; cannot work offline beyond refresh lifetime; owner cannot self-approve PRs |
| C. Both (#34's option C) | Identity-based features available | Two token types, two failure modes, callback URL maintenance, more code |
| D. PAT fallback | Simplest | Manual rotation, one owner, no org selection, no public-repo contributions |

Recommendation: **A as the default**, D as the documented fallback, B/C only if a concrete feature needs user identity (none in the current plan). This revises the plan's "GitHub: per-user OAuth (now: per-instance owner)" decision; the plan's goals (token never in VM, push non-default branches + draft PRs, default/protected branches stay operator) are all met by A.

## 8. Risks

- Documented-not-measured: git `x-access-token` basic auth with both token kinds, `draft: true` behaviour on private personal repos, ruleset behaviour for Apps on user-owned repos, and `hook_attributes.active:false` in a manifest are all unverified against live GitHub. The first implementation ticket must include one smoke test on a scratch repo.
- Stateless `ghs_APPID_JWT` tokens are large; do not assume length in URLs, headers, DB columns or log redaction patterns (redact by `ghs_`/`ghu_`/`ghr_` prefix, not by length).
- A token-injecting gateway that accepts "any repo the token can reach" widens exfiltration; always bind each sandbox to its attached repos by name (egress-proxy section 3.5).
- Wiring Actions: pushes by the App trigger workflows on that branch (secrets exposure only if workflow code is attacker-controlled; stays safe while Workflows is off and the owner reviews PRs before merge).

## 9. New questions / candidate tickets

1. **GitHub App register-from-manifest UI + conversion handler** (build, phase B, area github): manifest builder, `state` binding, one-shot conversion with atomic encrypted persist, install link, `setup_url` handler verifying `installation_id` via `GET /app/installations/{id}`, drift check of installation permissions vs expected, pinned-config read-only mode.
2. **GitHub token service: JWT signing and installation-token minting** (build, phase B, area github): per-verb permission sets, per-repo narrowing, in-memory cache with 50-minute re-mint, 401 handling (suspended/revoked installation), redaction by prefix, never logging tokens.
3. **Manager git token handoff API** (design, phase B, area sandbox/github): shape of the per-call token push over mTLS, in-memory-only storage, expiry, what happens when atrium is unreachable mid-push.
4. **Operator ruleset recipe and drift check** (docs+build, phase B, area github): importable ruleset JSON for default branch (no bypass actors) and `atrium/*` confinement; a read-only "repo rules audit" tool that warns if no ruleset protects the default branch (needs only Metadata/Contents read? verify: ruleset read may need Administration; if so, drop the check and document).
5. **Draft-PR fallback for ineligible plans** (design, phase B, area github): detect plan restriction, fallback label/title prefix, settings warning.
6. **Smoke-test GitHub against a scratch repo** (research, phase B, area github): measure `x-access-token` for IAT and UAT clone/push, `draft: true` on a private personal repo, 403 body for read-only push, whether a ruleset with no bypass actors binds an App token on a user-owned repo, and the `workflows` rejection text. Needs one throwaway App created by the owner.
7. **Signed commits from the manager** (design, phase B, area sandbox): whether atrium signs agent commits (SSH signing key per instance, "Verified" badge) so repos with signed-commit rules work; or documents the exemption.
8. **App re-registration/base-URL change procedure** (design, phase B, area github/settings): domain move requires editing GitHub-side callback/redirect URLs or re-registering; decide whether to drop `callback_urls` entirely (IAT-only) and document `redirect_url` as one-time.
9. **Plan wording change**: update the "GitHub: per-user OAuth" decision to "GitHub App, installation tokens (user tokens optional)" after the owner agrees (grilling, phase B, area github).
10. **Webhooks off vs polling** (design, phase B, area github): VPN-only instances cannot receive webhooks; define polling cadence for PR/CI status and what is lost (revocation signal, push events).

## Sources

- [Registering a GitHub App from a manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest) · [REST: apps (conversions, installation tokens, installations)](https://docs.github.com/en/rest/apps/apps)
- [Generating a user access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app) · [Refreshing user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens) · [On behalf of a user](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-with-a-github-app-on-behalf-of-a-user)
- [Authenticating as an installation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation) · [Generating a JWT](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app) · [Managing private keys](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps)
- [Choosing permissions](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app) · [Permissions required for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps) · [Create a pull request](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request)
- [Callback URL](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url) · [Setup URL](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-setup-url) · [Modifying a registration](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration) · [Registering an app](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app) · [Best practices](https://docs.github.com/en/apps/creating-github-apps/about-creating-github-apps/best-practices-for-creating-a-github-app) · [Token expiration and revocation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation)
- [Protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) · [Creating rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository) · [Available rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets) · [GITHUB_TOKEN and workflow triggering](https://docs.github.com/en/actions/concepts/security/github_token)
- Community reports for the workflow-push rejection text: [#35410](https://github.com/orgs/community/discussions/35410), [#27224](https://github.com/orgs/community/discussions/27224) (secondary, error wording only).
