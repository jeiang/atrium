# ntfy programmatic provisioning (ticket #31)

Question: can atrium create ntfy users, ACLs and tokens via API so a user picks ntfy, atrium generates a topic, the user enters a password and atrium provisions access? Self-hosted vs ntfy.sh.

Sources: ntfy source at `9fe512a` (main, 2026-10-17; latest release v2.31.1, published 2026-10-17: <https://github.com/binwiederhier/ntfy/releases/latest>), ntfy docs ([config](https://docs.ntfy.sh/config/), [publish](https://docs.ntfy.sh/publish/), [phone](https://docs.ntfy.sh/subscribe/phone/)), ntfy-android v1.25.2, ntfy-ios v1.4.1, live `https://ntfy.sh/v1/config` and `/v1/tiers` (fetched 2026-10-17). Source paths below are in <https://github.com/binwiederhier/ntfy>. Atrium facts cite `path:line` at origin/main `c221de8`.

## 1. Short answer

- **Self-hosted ntfy: yes, with an admin credential.** Since v2.5.0 (2023-05-18) ntfy has an HTTP admin API: `GET/POST/PUT/DELETE /v1/users` and `POST/PUT/DELETE /v1/users/access` (`server/server.go:670-681`, handlers in `server/server_admin.go`). The release notes say it is "intentionally undocumented as of now" ([docs/releases.md, v2.5.0](https://github.com/binwiederhier/ntfy/blob/main/docs/releases.md), tracking issue [#722](https://github.com/binwiederhier/ntfy/issues/722)); the public docs describe only CLI (`ntfy user|access|token`) and `server.yml` (`auth-users`, `auth-access`, `auth-tokens`) provisioning ([config.md, Access control](https://docs.ntfy.sh/config/#access-control)). Every route is wrapped in `ensureAdmin` (`server/server_middleware.go:90-97`): caller must be an `admin`-role user.
- **ntfy.sh: no admin API; only self-service.** ntfy.sh has signup, login and reservations enabled (`GET https://ntfy.sh/v1/config`: `enable_signup:true, enable_login:true, enable_reservations:true`), but the admin API is closed to everyone but ntfy's operator, and reservations need a paid tier (free tier `reservations:0`; Supporter $6/mo = 3; Pro $12/mo = 10; `GET https://ntfy.sh/v1/tiers`). On ntfy.sh, atrium cannot create the user; the user must have an account (signed up themselves, possibly via `POST /v1/account` while anonymous, rate limited by `AccountActionAllowed`, `server/server_account.go:25-36`).
- The requested flow ("atrium generates a topic, user enters a password, atrium creates the ntfy user + ACL") works exactly as stated on self-hosted ntfy **if** atrium holds an admin token for that server. It cannot work on ntfy.sh. Apps log in with username+password only (see section 5), so a password is genuinely needed for phone subscription, not only for atrium.

## 2. API surface (what atrium can call)

All calls need HTTPS in practice (Basic auth is plain base64, [publish.md](https://docs.ntfy.sh/publish/#authentication)). Auth is `Authorization: Bearer tk_…` or Basic.

| Purpose | Call | Caller must be | Notes |
|---|---|---|---|
| Probe server features | `GET /v1/config` (anonymous) | anyone | `enable_login`, `enable_signup`, `enable_reservations`, `require_login`, `disallowed_topics` (live ntfy.sh example above; `server/server_web.go:80-87`) |
| Probe admin token | `GET /v1/version` | admin | returns 401 for non-admins (`server/server.go:662-663`); `GET /v1/account` returns the caller's role (anonymous allowed, `server.go:684`) |
| Create user | `POST /v1/users` `{username, password \| hash, tier?}` | admin | 409 if exists; `role` is deliberately not accepted, only regular users are created (`server/types.go:169-175`, `server_admin.go:handleUsersAdd`). `hash` accepts a bcrypt hash, so atrium can send a hash and never transmit the plaintext. Username regex `^[-_.+@a-zA-Z0-9]+$` (`user/util.go:20`) |
| Upsert / change password / tier | `PUT /v1/users` | admin | refuses admin targets (403) and **provisioned** users (409, `ErrProvisionedUserChange`) |
| Delete user | `DELETE /v1/users` `{username}` | admin | regular users only; also kills open subscriptions; provisioned users refused |
| Grant ACL | `POST|PUT /v1/users/access` `{username, topic, permission}` | admin | topic may be a `*` pattern; permissions `read-write`, `read-only`, `write-only`, `deny` |
| Revoke ACL | `DELETE /v1/users/access` `{username, topic}` | admin | kills matching subscribers |
| List users + grants | `GET /v1/users` | admin | returns username, role, tier, grants; no tokens |
| Mint token | `POST /v1/account/token` `{label?, expires?}` | **the user themself** | acts on `v.User()` (`server_account.go:290-323`); an admin cannot mint a token for another user via the API. Default expiry is 72 h; `expires:0` stores `expires=0`, which the token query treats as never expiring (`user/manager_sqlite.go:55`: `expires = 0 OR expires >= ?`). Max 60 tokens per user (`user/manager.go:34`) |
| Delete token | `DELETE /v1/account/token` (X-Token header) | the user | |
| Change own password | `POST /v1/account/password` | the user | |
| Topic reservation | `POST /v1/account/reservation` `{topic, everyone}`; `DELETE /v1/account/reservation/{topic}` | the user; regular users need a tier (`server_account.go:528`) | gives the owner exclusive claim and sets an `everyone` permission (e.g. `deny-all`); admin may reserve without a tier |
| Signup | `POST /v1/account` `{username,password,email?}` | anonymous only when `enable-signup`; **admin always allowed** (`server_account.go:26-36`) | alternative way to create a user, works with an admin token even if signup is off |

Facts that constrain the design:

1. **Tokens are not scoped.** "Access tokens grant users full access to the user account… Granular access tokens are on the roadmap" ([config.md, Access tokens](https://docs.ntfy.sh/config/#access-tokens)). An **admin** token therefore has full control of the whole ntfy server: it can read and write every topic of every user (`admin` role: "can read/write to all topics", [config.md, Users and roles](https://docs.ntfy.sh/config/#users-and-roles)).
2. **Provisioned users are immutable over the API.** Users in `auth-users` are "provisioned" and cannot be changed or deleted via API; removing them from config deletes them at restart ([config.md](https://docs.ntfy.sh/config/#users-via-the-config)). Atrium-created users are ordinary and persist.
3. **Default access matters.** ntfy's default is `auth-default-access: read-write`: everyone can read/write every topic (ntfy.sh works this way; "the topic is essentially a password", [publish.md](https://docs.ntfy.sh/publish/)). Adding an ACL entry for user X does not stop anonymous access. A private setup needs `auth-default-access: deny-all` (config.md, [Access control](https://docs.ntfy.sh/config/#access-control)) or, on a server with reservations, a reservation with `everyone: deny-all`.
4. **Declarative bootstrap exists for the admin.** The admin atrium needs can be created with `auth-users` (`user:bcrypt:admin`) and a fixed token via `auth-tokens` (`user:tk_<29 chars>:label`) in `server.yml`, or the same via `NTFY_AUTH_USERS` / `NTFY_AUTH_TOKENS` ([config.md](https://docs.ntfy.sh/config/#tokens-via-the-config)); `ntfy token generate` creates a value. These are the only documented ways to get an admin; the API cannot create one.
5. **Topic names**: `^[-_A-Za-z0-9]{1,64}$` (`server/server.go:91`). 128 random bits as 26 base32 chars plus a prefix fits. Names in `disallowed_topics` (live: `about, admin, docs, v1, …`) are rejected.
6. **JSON publish** goes to the root URL: `POST https://host/` with `{"topic","title","message","click",…}` ([publish.md](https://docs.ntfy.sh/publish/#publish-as-json)). Needs write permission on that topic (Bearer or Basic).
7. **iOS instant delivery for self-hosted servers** needs `upstream-base-url: https://ntfy.sh`; only the message id and a SHA-256 of the topic URL go upstream, the payload stays on the self-hosted server ([config.md, iOS](https://docs.ntfy.sh/config/#ios-instant-notifications)). Without it delivery can take 20-30+ minutes or hours.
8. **Retention**: messages are cached 12 h by default, in memory unless `cache-file`/`database-url` is set ([config.md, Message cache](https://docs.ntfy.sh/config/#message-cache)); atrium message bodies persist that long on the ntfy host.

## 3. How atrium sends notifications today

- One function, `deliver()` at `src/atrium/agent/tools/notify.py:55-72`: reads `NTFY_URL`, `NTFY_TOPIC` (or the `topic` override) and optional `NTFY_TOKEN` from the process env, POSTs JSON `{topic,title,message[,click]}` to `NTFY_URL` (JSON-publish root) with `Authorization: Bearer <NTFY_TOKEN>`, 10 s timeout. Failure returns `(False, "ntfy unreachable…" | "ntfy <status>")`; it is recorded, never raised (`notify.py:81-87`).
- Every notification is first stored in the `notifications` table (`notify.py:19-33`, `send()` at `75-87`) with `delivered`/`error`, shown on the Notifications page (`web/notifications.py`). So ntfy is a best-effort push mirror of an in-app inbox.
- Two topics: the assistant topic `NTFY_TOPIC` (agent `notify` tool, `notify.py:94-104`, caps 3 per run / 1 nightly; scheduler "Skipped: …" notice `agent/scheduler.py:552`) and a media topic `NTFY_MEDIA_TOPIC` (`media/settings.py:53`, used in `media/channels.py:174` with a `click` link to the downloads page). `NTFY_TOKEN` is a single shared token for both.
- Config source today: NixOS options `ntfy.url`, `ntfy.topic`, `ntfy.mediaTopic` (`nix/module.nix:76-78,267-271`); `NTFY_TOKEN` is a secret from the runbook's secret file list (`docs/runbook.md:41`). Nothing creates or checks anything on the ntfy side; the operator does all setup by hand.
- Not used today: priority, tags, actions, attachments, email/phone fan-out, subscribing, or reading from ntfy.

## 4. Options

| | A. Admin-provisioned (the flow as asked) | B. Bring-your-own account, atrium mints a token | C. Token/topic only (no user provisioning) | D. Manual (today) |
|---|---|---|---|---|
| Atrium needs | admin token for the ntfy server | the user's username+password once (then discards it) | a publish-capable token, pasted | URL, topic, token pasted |
| What atrium does | `POST /v1/users` (with bcrypt `hash`), `POST /v1/users/access` (rw on topic for user, write-only for a separate publisher user optional), generates topic | `POST /v1/account/token` as the user (`expires:0`, label `atrium`), stores token, generates topic; optionally reserves it | stores what it is given | stores what it is given |
| Works on ntfy.sh | **No** | Yes (signup first); reservation only if paid | Yes | Yes |
| Self-hosted | Yes, needs `auth-users` admin and `deny-all` default | Yes, but the user must exist: create via CLI/`auth-users`/web signup, or admin-created by hand | Yes | Yes |
| Phone setup | user adds `server/topic` and logs in with the chosen username+password (atrium shows a `ntfy://host/topic` link; links carry no credentials, [phone.md](https://docs.ntfy.sh/subscribe/phone/#ntfy-links)) | same | user logs in with whatever account owns the token | same |
| Blast radius of atrium's stored secret | **High**: a leaked atrium DB or config gives full read/write of the entire ntfy server | Low: one user's account token (still unscoped but only that user's topics) | Low | Low |
| Reliance on undocumented API | Yes (`/v1/users*`, in the wild since 2023) | No: `/v1/account/*` is what the official web app uses | No | No |

Additional variant A2: atrium-managed user plus **separate write-only publisher user** for atrium (`ntfy access atrium-pub <topic> wo`). Its token cannot read the topic, so a leaked atrium token cannot read the history, but the token can still do everything its (narrow) account can, and only an admin can create the user, then atrium must log in as it to mint the token (password is generated by atrium and kept only long enough).

## 5. Phone clients

- Android: a protected topic triggers a "Login required" dialog (username + password); "Manage users" stores per-server users (ntfy-android `strings.xml`: `add_dialog_login_title`, `settings_general_users_title`). Credentials are username/password; no token entry field in those strings [inference: tokens cannot be used for subscribing from the Android app; the web app and CLI accept tokens].
- iOS: `ApiService.swift` takes a `BasicUser?` (username/password) for poll and `checkAuth`; no token path ([ntfy-ios](https://github.com/binwiederhier/ntfy-ios/blob/main/ntfy/Utils/ApiService.swift)).
- So the **user must be able to know the subscriber password**. In option A the user chooses it (atrium sends only a bcrypt hash and keeps no copy); in B it is their existing account.
- `ntfy://<host>/<topic>?secure=false|display=` deep links are Android-only ([phone.md](https://docs.ntfy.sh/subscribe/phone/#ntfy-links)).

## 6. Recommendation (fits plan: settings UI, pinnable config, per-instance single owner)

1. **Make B the default and portable path; A an optional "managed ntfy" mode for self-hosted.**
  - Settings > Notifications: server URL (pinnable), and a mode selector. Core steps: `GET /v1/config` to show what the server allows, ask for username+password, log in via Basic auth, mint a never-expiring labelled token, store only the token (encrypted at rest with the DB, #40), generate topic(s) with 128-bit entropy and display `ntfy://` and `https://host/topic` for the phone. Send a test notification and show the delivery result.
  - Works on ntfy.sh and self-hosted. On ntfy.sh warn that unprotected topics are secret-by-name only, that messages sit unencrypted on a third party for up to the retention window, and that reservations (the only way to lock a topic) need a paid plan.
2. **Mode A: only when the operator pins an admin token in deploy config** (`ntfy.admin_token_file`), never typed into the UI by default. Flow: generate topic, user enters password in the UI, atrium computes a bcrypt hash and calls `POST /v1/users {username, hash}` then `POST /v1/users/access {username, topic, "read-write"}`, then logs in as the user to mint its publish token (needed because an admin cannot mint another user's token), and then discards the password. Require and verify `auth-default-access` ≠ read-write by an anonymous probe publish to a throwaway topic (expect 401/403) before declaring the topic private.
3. Keep the one `notifications` table and `delivered/error` columns; send failure remains non-fatal (current behaviour).
4. Replace the two global topics with named **channels** (assistant, media, and later e.g. mail, budget alerts) each with its own topic and per-channel on/off, which is what #47 should decide. One user token can publish to all (unscoped), or use per-channel ACLs in A.
5. Phase placement: phase B as planned. For Phase A (as-is port) keep `NTFY_URL/TOPIC/MEDIA_TOPIC/TOKEN` env exactly and port `deliver()` unchanged (the JSON-root publish contract is what ntfy documents).

## 7. Risks

- **Admin token = whole server.** Tokens are unscoped, so storing one in atrium (even encrypted) makes atrium a full-control credential for the ntfy host, including other apps' topics. Mitigation: dedicated ntfy instance for atrium, or option B.
- **Undocumented admin API** can change without notice ("intentionally undocumented", v2.5.0 notes); pin tested ntfy versions, probe `GET /v1/version` and fail soft to B.
- **Provisioned users cannot be changed** by the API; if a deployment declares the user in `auth-users`, atrium must not offer to manage it (handle HTTP 409).
- **Default-open servers** make "private topic" a false claim; atrium must verify, not assume.
- **Password reuse and transit**: sending plaintext passwords over plain HTTP exposes them; require https except for loopback.
- **Token expiry**: default 72 h unless `expires:0` is passed; a lapsed token silently turns notifications into 401 (today's code reports `ntfy 401` per message). Need a visible "notifications broken" state.
- **Message content leaves the host**: agent-written bodies mentioning mail or calendar items are stored by the ntfy server (12 h default) and, on iOS with `upstream-base-url`, only ids reach ntfy.sh. Consider a "private titles" mode that sends "New message" with a click link only.
- **Rate limits**: ntfy.sh free tier is 250 messages / 12 h per IP basis (`/v1/tiers`); self-hosted default visitor burst 60 requests, 1 per 5 s ([config.md, rate limiting](https://docs.ntfy.sh/config/#rate-limiting)). The nightly/scheduled caps already keep atrium far below.
- **Account lifecycle**: deleting a thread/instance/channel in atrium should revoke the token and (mode A) the user + ACL, otherwise orphan users accumulate on the ntfy host.
- **Programmatic signup on ntfy.sh** is subject to its rate limits and terms [inference: not verified against ntfy.sh ToS]; do not auto-create ntfy.sh accounts, ask the user.

## 8. Candidate new tickets

1. **ntfy notification channels model** (design, phase B, area: notifications): replace `NTFY_TOPIC`/`NTFY_MEDIA_TOPIC` with named channels, per-channel topic and enable flag, per-module defaults; feeds #47.
2. **ntfy connect flow in Settings** (design, B, notifications/UI): modes B and A above, `GET /v1/config` capability probe, test-send, health badge; pinning rules for URL/token (#37).
3. **Notification health and token-expiry handling** (design, B): detect 401/403/unreachable, show state on the Notifications page, retry policy, re-authenticate prompt.
4. **Private-topic verification probe** (research or small design, B): anonymous publish/subscribe probe to prove `deny-all`; wording of warnings for ntfy.sh.
5. **Notification content policy** (design, B, privacy): what the agent may put in push bodies, "private titles" mode, retention note; relates to mail safety posture.
6. **Optional two-way ntfy (actions / reply)** (research, B/post): ntfy action buttons (HTTP actions with atrium endpoints) for approve/dismiss proposals; needs secure callback design and the OIDC change (#38). New feature the plan does not mention.
7. **NixOS ntfy-sh module wiring** (build, B, shipping): document/ship `auth-users`/`auth-tokens` + `deny-all` + `upstream-base-url` recipe in docs for self-hosters; secret handling via env file.
8. **Multi-channel alternatives** (research, post-B): ntfy also does email/phone calls; Web Push from atrium itself (no ntfy) as a fallback for browsers [inference: not researched here].

## 9. Open questions

- Will the owner's ntfy be dedicated to atrium (then A is acceptable) or shared (then B only)?
- Should atrium store an admin token at all, or accept one only for the duration of the provisioning request (typed in the UI, used once, never persisted)? One-shot use removes the standing blast radius while keeping the flow; recommended if A is wanted in the UI.
- Is the ntfy server public-facing with OIDC-incompatible Basic auth acceptable beside atrium's OIDC-only access (#38)? ntfy has its own user database, so there will be two identities per person.
