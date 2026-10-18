# Integration auth: Apple (iCloud) and Google

Ticket: [#33](https://github.com/jeiang/atrium/issues/33). Researched 2026-10-17. Facts about atrium cite `path:line` on `origin/main` (c221de8). External claims cite a primary source; `[inference]` marks my reasoning.

## 1. Answer in brief

- **iCloud has exactly one third-party auth method for IMAP, SMTP, CalDAV and CardDAV: an app-specific password (ASP).** One ASP works for all four protocols. It needs 2FA on the Apple Account, is generated only at account.apple.com, is revoked automatically when the Apple Account password changes, and is capped at 25 active. Apple documents a newer "authorize the app with your Apple Account" flow, but only for "supported third-party apps". I found no public developer registration, scopes or endpoints, so atrium cannot use it today.
- **The web UI can accept, test and store an ASP, but it cannot create one.** Creation is a manual step at account.apple.com; the UI can only guide the user there.
- **Gmail IMAP/SMTP has two methods: app password or XOAUTH2.**
  - App password: personal accounts with 2-Step Verification; Google says "not recommended". Unavailable for Advanced Protection accounts, for work/school accounts, and for 2SV set up with security keys only.
  - XOAUTH2: needs the restricted scope `https://mail.google.com/`. Atrium cannot narrow it, because IMAP and SMTP require this scope.
- **Google OAuth for a self-hosted, single-owner app does not need verification or CASA.** Google exempts "personal use" apps (fewer than 100 users). They show an "unverified app" screen the user clicks through. CASA (annual security assessment) applies only if we ship one verified shared client, which we should not do.
- **The one real trap is publishing status.** An external-user-type project left in "Testing" issues refresh tokens that expire after 7 days. The project must be set to "In production" (unverified is fine). `docs/runbook.md:262-264` already says this.
- **Google Calendar API and People API are the right Google surfaces.** Google's CalDAV and CardDAV endpoints also require OAuth 2.0 (no passwords), have gaps, and add nothing atrium needs.
- **Google OAuth from the web UI is feasible.**
  - Use a Web-application client with a callback on atrium's public HTTPS URL, or a Desktop client with a paste-back loopback.
  - The device flow is not available for Calendar or Gmail scopes.
  - Today's flow is CLI-only (`atrium calendar google-auth`) and prints a token for the env file.

## 2. What atrium does today (Phase A baseline)

| Concern | Code | Behavior |
|---|---|---|
| iCloud IMAP | `src/atrium/config.py:48`, `imap.py:301-305` | `imap.mail.me.com:993`, `IMAP4_SSL`, `login(ICLOUD_MAIL_USERNAME, ICLOUD_APP_PASSWORD)`. The username is the short name. |
| iCloud SMTP | `smtp.py:7-10,56-58`, `smtp.py:19-22` | `smtp.mail.me.com:587`, STARTTLS, `login`. The From and login user become `<short>@icloud.com` if no `@`. |
| iCloud CalDAV write | `calendar.py:22,357-376` | `caldav.DAVClient(url="https://caldav.icloud.com/", username=ICLOUD_APPLE_ID, password=ICLOUD_APP_PASSWORD)`. The Apple ID differs from the IMAP short name (comment at `calendar.py:358`). |
| iCloud CalDAV/CardDAV read | `nix/module.nix:20-57` | vdirsyncer mirrors `https://caldav.icloud.com/` (VEVENT only) and `https://contacts.icloud.com/` (`read_only = true`). The password comes via `printenv ICLOUD_APP_PASSWORD`. `contacts.py` parses the `.vcf` mirror. |
| Gmail IMAP | `config.py:49`, `imap.py:301-305` | `imap.gmail.com:993`, `login(GMAIL_ADDRESS, GMAIL_APP_PASSWORD)`. Whitespace in the app password is stripped (`docs/runbook.md:33-34`). It uses `X-GM-LABELS`, `X-GM-MSGID`, `X-GM-THRID` (`imap.py:259-263`) and `X-GM-RAW` (`sync.py:263`). |
| Gmail SMTP | `smtp.py:9,56-58` | `smtp.gmail.com:587`, STARTTLS, app password. |
| Google Calendar | `calendar.py:23-24,271-287,410-432` | Scope `calendar.events` only. It uses the Google API client with `refresh_token`, `client_id`, `client_secret` from env, and calls `calendarId="primary"` insert/delete/list with `sendUpdates="none"`. No attendees are ever written. |
| Google OAuth bootstrap | `google_auth.py:1-94`, `cli.py:135-143` | CLI-only installed-app flow. It binds `127.0.0.1:<port>`, prints a consent URL (PKCE S256, `access_type=offline`, `prompt=consent`), and prints `GOOGLE_REFRESH_TOKEN=...` to paste into `atrium.env`. |
| Refresh failure handling | `calendar.py` (grep `invalid_grant\|RefreshError`: no match) | A revoked or expired token surfaces as a generic exception. There is no "needs re-auth" state. |
| Google contacts | none | Contacts come only from the iCloud CardDAV mirror. |
| UI credential entry | `src/atrium/web/settings.py` (51 lines) | Only `/settings/assistant` and `/settings/usage` exist. No credential pages. `AGENTS.md:112` says the UI "carries the IMAP, SMTP, Notion, Google and Anthropic credentials". That is inaccurate today (credentials live in `atrium.env`). Candidate docs fix. |

One secret (`ICLOUD_APP_PASSWORD`) already feeds four protocols across three code paths: Python IMAP, Python SMTP, python-caldav and vdirsyncer. Atrium uses two usernames for the same Apple Account (`ICLOUD_MAIL_USERNAME`, `ICLOUD_APPLE_ID`).

## 3. iCloud (Apple)

### 3.1 App-specific password (the only usable method)

| Fact | Source |
|---|---|
| Generate at account.apple.com, Sign-In and Security, App-Specific Passwords. | [Apple 102654](https://support.apple.com/en-us/102654) |
| The Apple Account must have two-factor authentication. | same |
| Up to 25 active ASPs. Revoke individually or all at once. | same |
| Changing or resetting the Apple Account password revokes every ASP automatically. | same |
| iCloud Mail: IMAP `imap.mail.me.com:993` (SSL), SMTP `smtp.mail.me.com:587` (STARTTLS), no POP. | [Apple 102525](https://support.apple.com/en-us/102525) |
| IMAP username is usually the part before `@`, but "if your client can't connect using just the name, try the full address". | same |
| SMTP username is the full address; SMTP authentication is required; the same ASP is used. | same |
| Mail, Contacts and Calendars are not end-to-end encrypted, even under Advanced Data Protection (CalDAV/CardDAV have no E2EE support). So ADP does not block these protocols. | [Apple 102651](https://support.apple.com/en-us/102651) |
| Reminders synced over CalDAV are also not E2EE. | same |
| vdirsyncer cannot do 2FA against iCloud DAV, so an ASP is required. Calendars should be created from an Apple client, not vdirsyncer. iCloud enforces a minimum collection-name length. | [vdirsyncer iCloud tutorial](https://vdirsyncer.pimutils.org/en/stable/tutorials/icloud.html) |
| iCloud DAV endpoints in use: `https://caldav.icloud.com/` and `https://contacts.icloud.com/`. | same |

What an ASP permits: full mailbox access (read, move, delete, send), full calendar and contacts DAV access for that account. There is no scope or per-service limit. [inference: nothing in the Apple docs offers a per-protocol ASP.] The mail safety gates (`AGENTS.md:64-75`, `safety.py`) are the only write limit, and they are application-level.

Limits and risks:
- The ASP is silently revoked on any Apple Account password change, so every iCloud feature breaks at once (mail sync, Send, calendar writes, vdirsyncer).
- The Apple ID and the IMAP short name differ in atrium's model, which is an easy user error. The UI should ask for one thing, the Apple Account email, and try short name then full address. [inference, from the "try the full address" guidance]
- Revocation is invisible to atrium until the next login fails. The current code has no stored "auth failed" state.

Web UI end-to-end: **partial.**
- Generating the ASP happens at account.apple.com and cannot be done from atrium. This is inherent: Apple provides no API for it.
- The UI can do the rest: paste the ASP, derive the usernames, run a connection test across IMAP login, SMTP login (EHLO, STARTTLS, AUTH, no send), CalDAV principal discovery and CardDAV principal discovery, and store it.
- The test gives immediate, precise feedback ("IMAP ok, CalDAV 401").

### 3.2 "Authorize with your Apple Account" (OAuth-like)

- Apple documents that in "supported third-party apps" the user enters their Apple Account email, clicks Allow, and can later revoke under Account Data Sharing. Apple says an ASP is the fallback if the app does not support this. [Apple 121539](https://support.apple.com/en-us/121539)
- I found no public developer registration, client credentials, scopes, token endpoint or token-to-IMAP/CalDAV mechanism. Apple's developer docs list "Account & Organizational Data Sharing" for other Apple services, and Sign in with Apple only authenticates the user. [inference: neither grants iCloud mailbox or DAV access; a web-search summary and the absence of any such Apple doc support this, not a primary Apple statement.]
- Conclusion: not usable by atrium today. Watch item: if Apple publishes a developer flow, revisit.

### 3.3 Other iCloud methods
- Normal Apple Account password: not accepted by third-party clients when 2FA is on (the ASP exists for this reason, per Apple 102654).
- POP: not supported ([Apple 102525](https://support.apple.com/en-us/102525)).
- Sign in with Apple: not a mailbox credential (see 3.2).

## 4. Google

### 4.1 Gmail IMAP and SMTP: app password

| Fact | Source |
|---|---|
| App passwords are "not recommended and unnecessary in most cases", and Google points to "Sign in with Google". | [Google 185833](https://support.google.com/accounts/answer/185833) |
| A 16-digit passcode, requires 2-Step Verification. | same |
| Unavailable if 2SV is security-keys-only, if the account is a work/school/organization account, or if the account has Advanced Protection. | same |
| App passwords are revoked when the Google Account password changes. Each is shown once. | same |
| Plain username/password ("less secure apps") is no longer supported for Workspace accounts from January 2025. | [Google 6010255](https://support.google.com/accounts/answer/6010255) |
| Gmail IMAP bandwidth: download 2500 MB/day, upload 500 MB/day. Hitting it suspends sign-in for about 1 hour, up to 24. The page is Workspace-titled; it says "all Google Workspace accounts". | [Workspace Gmail bandwidth limits](https://knowledge.workspace.google.com/admin/gmail/gmail-bandwidth-limits) |

What it permits: full mailbox (IMAP and SMTP) for that account. It is simple: no Cloud project, no consent screen.

Limits: personal accounts only, realistically. Google discourages it and could remove it. [inference, from the "not recommended" banner and the Workspace wind-down.]

Web UI end-to-end: **partial.** The user generates the password at myaccount.google.com/apppasswords. The UI accepts, tests and stores it, same shape as iCloud.

### 4.2 Gmail IMAP and SMTP: XOAUTH2

| Fact | Source |
|---|---|
| SASL XOAUTH2 works for IMAP `AUTHENTICATE`, POP and SMTP `AUTH`. Initial response is `base64("user=" user "^Aauth=Bearer " token "^A^A")`. The server advertises `AUTH=XOAUTH2` and `SASL-IR`. | [Gmail XOAUTH2](https://developers.google.com/workspace/gmail/imap/xoauth2-protocol) |
| The required scope is `https://mail.google.com/`. "To be approved, your app must show full utilization" of it. | same |
| `https://mail.google.com/` is a **restricted** scope. Restricted-scope data stored on servers triggers the security assessment. | [Gmail API scopes](https://developers.google.com/workspace/gmail/api/auth/scopes) |
| Google's alternative for narrower access is the Gmail API (`gmail.modify`, `gmail.readonly`, ...), which are also restricted. The only sensitive Gmail scope is `gmail.send`. | same |

Atrium consequence: atrium's mail core is IMAP-native (UIDs, `X-GM-*`, IDLE, label add/remove, `APPEND` semantics in `imap.py`, `sync.py`). Replacing IMAP with the Gmail API is a rewrite of the mail core for no verification benefit, because those scopes are restricted too. XOAUTH2 is a drop-in at the auth step.

Web UI end-to-end: **yes, after one-time setup outside the UI** (create the Cloud project, consent screen and client; see 4.4).

### 4.3 Verification, CASA and refresh-token expiry (what applies to atrium)

| Fact | Source |
|---|---|
| "Personal use apps (fewer than 100 users)" need not complete verification; users click through the "unverified app" screen. | [Google 13464323](https://support.google.com/cloud/answer/13464323) |
| Internal Workspace-only apps skip verification and the 100-user cap. | same |
| Apps in "development/testing/staging" are also exempt, but the 100-user cap and unverified screen still apply until verified. | same |
| Sensitive/restricted verification needs: homepage on a verified domain, privacy policy, demo video, scope justification. Restricted scopes also need the annual security assessment. | [Google 13464321](https://support.google.com/cloud/answer/13464321) |
| Assessment is annual, uses the App Defense Alliance CASA framework, and ends with a Letter of Validation. Trust and Safety contacts the developer when it is time. | [Google 13465431](https://support.google.com/cloud/answer/13465431) |
| An External project with publishing status "Testing" gets refresh tokens that expire in **7 days**, unless scopes are only openid/email/profile. | [Google OAuth overview, refresh token expiration](https://developers.google.com/identity/protocols/oauth2#expiration) |
| Other refresh-token killers: user revocation; 6 months unused; password change when the token has Gmail scopes; more than 100 live tokens per account per client (oldest silently dies); admin policy `admin_policy_enforced`; time-based access. | same |
| OAuth clients unused for 6 months are auto-deleted (30-day warning email); any token request or console edit resets the clock. | [Google 15549257](https://support.google.com/cloud/answer/15549257) |

CASA cost: a vendor summary puts Tier 2 / AL1 at roughly $675-855 per year at one assessor (TAC Security), and Google's broader range at about $500-4,500. This comes from search-summary secondary sources, not Google's pages (Google's own pages do not list prices). Treat it as indicative only. It does not apply unless atrium ships one shared verified Google client, which this plan does not call for.

Implications:
- **Per-instance "bring your own Google Cloud client" is the only workable model for an open-source self-hosted app.**
  - Verification is per client, per developer-owned project.
  - A shared verified client would require atrium (the project) to pass restricted-scope verification and CASA, and every instance's mail would pass through instance operators' servers (`[inference]`, per the Google policy language above).
  - vdirsyncer states it is "against Google's Terms of Service to hardcode [client id/secret] into opensource software", citing the Google ToS "Confidential Matters" ([vdirsyncer docs](https://vdirsyncer.pimutils.org/en/stable/config.html)).
- Each instance owner creates a Cloud project, sets the consent screen to External, adds scopes, **publishes to "In production"** (skipping verification), and creates a client. Do not leave it in Testing.
- The 100-user cap is irrelevant (single owner). The unverified screen is a one-time click-through.
- Google can change policy ("Upcoming Policy Enforcement Notice" in [Google 7454865](https://support.google.com/cloud/answer/7454865)): scopes can be reclassified and previously exempt apps become unverified with a grace period. This is low risk for owner-only use.

### 4.4 Google OAuth flows and what the web UI can do

| Option | Works for atrium? | Source / notes |
|---|---|---|
| **Desktop client + loopback redirect** (today) | Only if the browser is on the same host as atrium. On a VPS, atrium's loopback listener is unreachable. | [Google desktop OAuth](https://developers.google.com/identity/protocols/oauth2/native-app): loopback `http://127.0.0.1:port`, PKCE recommended. |
| **Desktop client + "paste-back"** | Yes, from the UI. The user's browser is sent to `http://127.0.0.1:<port>/?code=...`, which fails to load; the user copies the URL into atrium, and atrium exchanges the code with PKCE. [inference: loopback with any port is allowed for Desktop clients per the same doc; the paste-back UX is the common pattern but not documented by Google.] | Needs no public URL. |
| **Web application client + HTTPS callback** | Yes, the cleanest UI flow: "Connect Google" redirects to Google and back to `https://<atrium>/…/callback`. Redirect URIs must be HTTPS except localhost, hosts cannot be raw IP addresses, TLD must be on the public suffix list, no wildcards. Changes can take 5 minutes to hours. Needs a client secret. | [Google 15549257](https://support.google.com/cloud/answer/15549257). Fits the planned OIDC deployment (a public HTTPS host). |
| **Device flow (TV/limited-input)** | **No.** Allowed scopes are only `email`, `openid`, `profile`, `drive.appdata`, `drive.file`, `youtube`, `youtube.readonly`. | [Google device flow](https://developers.google.com/identity/protocols/oauth2/limited-input-device) |
| **Service account** | Not for consumer Gmail/Calendar. Workspace domain-wide delegation is possible (and `gmail.imap_admin` exists for it) but is out of scope. | [Gmail XOAUTH2](https://developers.google.com/workspace/gmail/imap/xoauth2-protocol), [Google OAuth overview](https://developers.google.com/identity/protocols/oauth2) |

Setup that must happen outside atrium either way: Cloud project, enable APIs, consent screen, publish, create the client, copy the id and secret. The UI should accept `client_id` and `client_secret` (or `*_file` pins), show the exact redirect URI to register, and drive the consent from there.

Workspace (work/school) accounts: app passwords are not available (Google 185833). A Workspace admin may block the OAuth client (`admin_policy_enforced`), and Internal-type consent only works inside the same organization ([Google 13464323](https://support.google.com/cloud/answer/13464323)). OAuth-only support for such accounts is possible but admin-dependent.

### 4.5 Calendar: Calendar API vs CalDAV

| | Calendar API (current) | Google CalDAV |
|---|---|---|
| Auth | OAuth 2.0 | OAuth 2.0 only; Basic auth gets 401. [CalDAV guide](https://developers.google.com/workspace/calendar/caldav/v2/guide) |
| Scopes | Many, including `calendar.events`, `calendar.events.owned` ("see, create, change, and delete events on Google calendars you own"), `calendar.app.created`. [Calendar scopes](https://developers.google.com/workspace/calendar/api/auth) | The guide requires a client id and secret and an OAuth token. It does not state a scope list on the fetched page. |
| Missing | n/a | No `MKCALENDAR`, `LOCK`, `COPY`, `MOVE`, no VTODO/VJOURNAL, `free-busy-query` missing, no `calendar-proxy` under iOS UA, and an empty scheduling inbox. Same quota as the Calendar API. [CalDAV guide](https://developers.google.com/workspace/calendar/caldav/v2/guide) |
| Verification | `calendar.events` is a sensitive scope: verification only needed for a public app. [Google sensitive-scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/sensitive-scope-verification) names calendar-event reading as sensitive (general class; per-scope classification is shown in the Cloud Console). | same |

Atrium only inserts, deletes and lists events on `primary`, with no attendees and `sendUpdates="none"` (`calendar.py:410-432`). Calendar API is simpler, typed (Go: `google.golang.org/api/calendar/v3`), and exposes an explicit client-chosen event id. Atrium already relies on this: it catches a 409 on duplicate id (`calendar.py:415`). **Keep the API; do not move Google to CalDAV.**

Possible narrowing: `calendar.events.owned` instead of `calendar.events`. `[inference]` Atrium only writes to `primary`, which is owned, so it likely suffices. Verify the behavior against a real account before changing. It is a small blast-radius reduction, not a prerequisite.

### 4.6 Contacts: People API vs CardDAV

| | People API | Google CardDAV |
|---|---|---|
| Auth | OAuth 2.0; `contacts.readonly` or `contacts`. [searchContacts](https://developers.google.com/people/api/rest/v1/people/searchContacts) | OAuth 2.0 only, "Google does not support any other authentication method"; vCard 3.0; not all CardDAV reports. [CardDAV guide](https://developers.google.com/people/carddav) |
| Notes | Warm-up request before search; `pageSize` capped at 30; `readMask` selects fields (includes `birthdays`, `events`). | Sync tokens valid 29 days; polling `getctag` throttles; "data safety" warnings from vdirsyncer. [vdirsyncer config](https://vdirsyncer.pimutils.org/en/stable/config.html) notes BDAY with a partial date (no year) is not synced and group labels are not synced. |
| Sensitivity | "Storing a new contact in Google Contacts" is named as sensitive. Reading contacts is likely sensitive too `[inference]`. | same |

Atrium reads contacts only to find addresses and birthdays/anniversaries (`contacts.py`). If Google contacts are wanted, read-only People API is the better fit (birthdays are a first-class field and partial dates survive). CardDAV would hit the BDAY gap that matters here. This is a new feature, not Phase A.

### 4.7 Gmail IMAP behaviour that matters for a long-running sync
- Gmail exposes labels as IMAP folders and the extensions `X-GM-LABELS`, `X-GM-MSGID`, `X-GM-THRID`. Atrium depends on them. [Gmail IMAP extensions](https://developers.google.com/workspace/gmail/imap/imap-extensions)
- The initial backfill and re-embeds can approach the 2500 MB/day IMAP download cap. The cap suspends sign-in for 1 to 24 hours (see 4.1). `[inference]` The cap is documented for Workspace but is likely the same for consumer Gmail. The sync engine needs a backoff for this suspension, not a generic retry loop.

## 5. Go implementation notes (for the build tickets)
- **IMAP:** `emersion/go-imap/v2` `Client.Authenticate(sasl.Client)` runs any SASL mechanism and uses SASL-IR when the server advertises it ([source](https://raw.githubusercontent.com/emersion/go-imap/v2/imapclient/authenticate.go)).
- **SASL:** `emersion/go-sasl` ships anonymous, external, login, oauthbearer and plain, but **no XOAUTH2** (directory listing). Google's docs define only XOAUTH2, so we hand-write a ~20-line `sasl.Client` (and an `smtp.Auth` equivalent for submission). `[inference]` OAUTHBEARER is not documented for Gmail IMAP.
- **Token refresh before every connect.** Access tokens are short-lived (the doc example shows `expires_in: 3920`). `golang.org/x/oauth2` `TokenSource` handles this. An open IDLE session keeps its authentication until it drops.
- **CalDAV/CardDAV for iCloud:** `emersion/go-webdav` has `caldav` and `carddav` packages. The vdirsyncer replacement is a separate build question (see candidates).
- **Google Calendar:** `google.golang.org/api/calendar/v3` with a `oauth2.TokenSource` built from the stored refresh token ([Google OAuth library list](https://developers.google.com/identity/protocols/oauth2)).

## 6. Options, trade-offs, recommendation

### 6.1 iCloud
| Option | Trade-off |
|---|---|
| A. ASP pasted into UI, tested, stored (recommended) | The only available method. Needs a shared-secret guide, a connection test, a "revoked" state. |
| B. Apple "authorize with Apple Account" | Not available to us. |

**Recommendation:** A. Model "iCloud account" as one connection (Apple Account email plus ASP) that mail, calendar and contacts all reference. Ask for the Apple Account email only. Derive the IMAP short name and fall back to the full address on login failure. Keep Phase A env vars as is (`ICLOUD_MAIL_USERNAME`, `ICLOUD_APPLE_ID`, `ICLOUD_APP_PASSWORD`, matching the host module).

### 6.2 Gmail mail
| Option | Trade-off |
|---|---|
| A. App password (status quo) | Zero Cloud setup; personal accounts only; Google discourages it. |
| B. XOAUTH2 with `mail.google.com` | Works for Workspace and Advanced-Protection accounts; per-app revocable; needs BYO Cloud client and an in-production consent screen; same blast radius as an app password. |
| C. Gmail API (`gmail.modify`) | Narrower than `mail.google.com` (no permanent delete), but restricted too, and rewrites the IMAP-native mail core. |

**Recommendation:** support A (default) and B (opt-in "Connect Google" under mail, implemented after the Phase A port). Reject C. Phase A stays app-password only, as today.

### 6.3 Google Calendar and People
**Recommendation:** Calendar API (keep). Add People API read-only for Google contacts only if the owner wants Google contacts (new feature, Phase B). Do not use Google CalDAV or CardDAV.

### 6.4 Google OAuth in the UI
**Recommendation:** per-instance BYO client; Web-application client with a callback on atrium's public URL as the primary flow; Desktop-client paste-back as a fallback for instances without a public HTTPS URL. Store `client_id`, `client_secret` (pinnable via `*_file`) and the refresh token in settings. Request one consent per capability (calendar, mail, contacts) using the same client, so the user can grant and revoke each separately. Use incremental authorization, which Google recommends ([Google OAuth overview](https://developers.google.com/identity/protocols/oauth2)).

## 7. Risks
- **Silent credential death.**
  - ASPs die on Apple password change.
  - Google refresh tokens die on password change (Gmail scopes), after 7 days in Testing, after 6 months unused, and on revocation.
  - Unused Cloud clients are deleted at 6 months.
  - Today there is no "needs re-auth" state, so scheduled runs fail with generic errors.
- **Testing-mode trap.** A new user who follows the console defaults gets 7-day tokens. The setup guide and the UI must check the publishing status in the instructions, since atrium cannot detect it directly. `[inference]`
- **Apple's Advanced Data Protection** does not break these protocols, but nothing in atrium can use a future Apple E2EE variant of Mail/Contacts/Calendar.
- **Google policy drift:** scope reclassification and enforcement are possible (Google 7454865).
- **Single secret, many protocols:** one iCloud ASP compromise gives access to mail, calendar and contacts.
- **Blast radius** of `mail.google.com` is the same as an app password.

## 8. Candidate new tickets

1. **Connections model for provider accounts.** design, B, settings. Does an iCloud account (one ASP, four protocols) or a Google account (one client, several grants) become a first-class "connection" that modules reference? How are they pinned and shown read-only?
2. **Credential health and re-auth UX.** design, B, core. Define the states (ok, auth failed, needs reauthorization, throttled), banner, ntfy alert, and per-module behavior when the connection is dead (`invalid_grant`, ASP revoked, `admin_policy_enforced`, Gmail bandwidth suspension).
3. **Google OAuth consent from the web UI.** build, B, settings/google. Web-application client callback plus Desktop paste-back fallback; state and PKCE; the exact redirect URI to display; a public-URL setting.
4. **Gmail XOAUTH2 for IMAP and SMTP in the Go mail core.** build, B, mail. Custom SASL client, token refresh on connect, migration from the app password, and Workspace account behavior.
5. **Google setup wizard and docs.** docs, B. Cloud project, consent screen type, "In production" (not Testing), scopes, test connection, plus an explicit "Testing means tokens die in 7 days" warning.
6. **Replace vdirsyncer with native Go CalDAV/CardDAV.** design+build, B, calendar/contacts. Depends on the Go library research. Covers read mirror, VEVENT-only, read-only contacts, iCloud collection quirks, and the 15-minute timer.
7. **Google contacts via People API (read-only).** feature, B, contacts. Is a Google contact source (birthdays, emails) wanted, and which scope (`contacts.readonly`)?
8. **Narrow Google Calendar scope.** spike, B, calendar. Does `calendar.events.owned` cover `primary` insert/delete/list? What does the consent screen show?
9. **Gmail IMAP bandwidth and backfill throttling.** design, A or B, mail. Initial sync size against the 2500 MB/day cap; a resumable backfill with a budget; backoff when sign-in is suspended.
10. **Account support matrix.** decision, B. Which account types are supported: personal Gmail with app password, personal with OAuth, Workspace (OAuth only, admin-dependent), Advanced Protection (OAuth only), iCloud with ASP.
11. **Fix `AGENTS.md:112`.** docs, A. It says the UI carries credentials; today it does not (`src/atrium/web/settings.py`).
12. **Watch: Apple "Account Data Sharing" for third-party apps.** monitor, low. Revisit if Apple publishes developer docs.
