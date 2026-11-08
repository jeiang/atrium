# Integrations

Every connection is configured on the Integrations settings page and tested
there. A secret is stored write-only in an encrypted database; the host may
pin it instead through `secretFiles`. The matrix is the support summary; the
sections below cover the console work atrium cannot do for you.

## Support matrix

| Connection | Registry name | Auth | What it permits | Limits |
|---|---|---|---|---|
| Anthropic | `anthropic` | Console API key | Assistant model calls | Key is workspace-bound; use one workspace per instance with a monthly spend limit |
| Grafana | `grafana` | Service account token | Read-only dashboard and datasource queries | Viewer role is enough; optional token expiry |
| Mail chat endpoint | `mail_chat` | Optional Bearer key | Chat and routing calls to an OpenAI-compatible endpoint | A hosted endpoint sends mail content off this host |
| Mail embed endpoint | `mail_embed` | Optional Bearer key | Embeddings for mail search and routing | Same as the chat endpoint |
| Notion | `notion` | Internal integration token or personal access token | Wiki read, update and insert | Share the pages with the integration; a personal token acts as the user and lasts at most a year |
| ntfy | `ntfy` | Optional token | Push notifications | Full text goes only to topics that refuse an anonymous read |
| qBittorrent | `qbittorrent` | API key | Media torrent and library operations | API keys need qBittorrent 5.2 or newer |
| TMDB | `tmdb` | API read access token | Media metadata | Read-only; media pages show the attribution |
| Google Calendar and Gmail | — | OAuth web client | Calendar events and Gmail over IMAP and SMTP | See [Google](#google) |
| iCloud | — | App-specific password | IMAP, SMTP, CalDAV and CardDAV | See [iCloud](#icloud) |
| EZTV | — | None | Media channel episode lists | Public API; only torrents tagged media are written |

## Google

Google Calendar and Gmail use a Web-application OAuth client that each
instance registers itself. The wizard on the Integrations page stores the
client ID and secret, performs one consent per capability, and keeps the
refresh token as a secret that can be pinned with its `*_file` variable.

| Account | Gmail IMAP and SMTP | Calendar | Notes |
|---|---|---|---|
| Personal | App password; XOAUTH2 planned | OAuth | An app password needs 2-step verification and Google does not recommend it |
| Workspace | XOAUTH2, not implemented yet | OAuth | App passwords are not available |
| Advanced Protection | XOAUTH2, not implemented yet | OAuth | App passwords are refused |

The Gmail consent in the wizard stores a refresh token for the XOAUTH2 mail
transport. Until that transport lands, a Gmail mail account uses an app
password, which personal accounts can create and Workspace and Advanced
Protection accounts cannot.

### Setup

1. Open [Google Cloud credentials](https://console.cloud.google.com/apis/credentials)
  and create a project, or pick one.
2. Configure the [OAuth consent screen](https://console.cloud.google.com/apis/credentials/consent)
  as External, then add your own address as a test user.
3. Enable the [Google Calendar API](https://console.cloud.google.com/apis/library/calendar-json.googleapis.com).
  Gmail over IMAP and SMTP uses the `https://mail.google.com/` scope and
  needs no Gmail API.
4. Create credentials of type **Web application** and add the redirect URI
  `https://<atrium-host>/settings/integrations/google/callback`.
5. Paste the client ID and secret into the Google wizard, then connect
  Calendar and Gmail separately.

If Google rejects the redirect URI, register the same path on a host you
control, or use the paste-back box with any registered URI.

### Testing mode

An External project in **Testing** issues refresh tokens that expire after
7 days. Publish the app to **Production**; personal use under 100 users does
not need verification or CASA, and the unverified screen is expected. A
token also dies on revocation, after 6 months unused, on a password change
for the Gmail scope, or when an administrator policy revokes it. Unused
OAuth clients are deleted after 6 months.

Calendar uses `https://www.googleapis.com/auth/calendar.events`; the narrower
`calendar.events.owned` does not suffice because the calendar lists existing
events. Gmail uses `https://mail.google.com/`. Each instance brings its own
client; do not share one.

## iCloud

iCloud has no third-party developer registration. The only method for IMAP,
SMTP, CalDAV and CardDAV is an app-specific password:

- Create it at [account.apple.com](https://account.apple.com); it needs
  2-step verification.
- One password covers all four protocols. At most 25 exist at once.
- It is revoked when the Apple Account password changes.
- Atrium can accept, test and store it, but cannot create it.

Mail accounts come from `mailAccounts`; calendar and contacts reuse an iCloud
mail account for their CalDAV and CardDAV login.

## Notion

Atrium uses read, update and insert content, and no comments or users.

- **Internal integration token** (the default): static, no expiry, and the
  integration sees only the pages shared with it through **Add connections**.
- **Personal access token**: acts as the user, needs no sharing step, lasts
  at most a year, and cannot list users.
- **Public OAuth**: user-tied with a page picker and rotating refresh pairs;
  every self-hoster must register their own public connection. Atrium does
  not implement this yet.

Create the internal integration at
[notion.so/my-integrations](https://www.notion.so/my-integrations), share the
wiki pages with it, then paste its token.

## Grafana

Create a service account in Grafana and paste its token. The Viewer role is
enough: it can query datasources. A token has no expiry by default; set a
TTL if your policy requires one. The Test button searches dashboards and
reports a token that may not search as needing the Viewer role.

## Anthropic

Use a Console API key from a workspace dedicated to this instance, with a
monthly spend limit, so a leaked key cannot spend beyond it. A key can be a
personal, service-account or legacy workspace key; it is workspace-bound and
may carry an expiry.

- Rotate the key in the Console and paste the new one on the Integrations
  page; the old key stops working.
- The key is stored encrypted and can be pinned with `secretFiles`; a pinned
  key cannot be changed from the UI.
- An Admin API key can list members, workspaces and usage cost, but cannot
  mint keys. Atrium does not use one yet.
- Subscription OAuth is not allowed for third-party products; only the
  unmodified Claude Code binary signs in to a subscription itself.

## Mail endpoints

The chat and embed endpoints are OpenAI-compatible base URLs ending in `/v1`,
with an optional Bearer key. The probe lists models and, for embeddings, the
dimension. A hosted endpoint warns that mail content leaves this host.

## qBittorrent

Create an API key in qBittorrent 5.2 or newer and paste it. Without a key,
requests go unauthenticated. Test fails on a server older than 5.2, which has
no API keys.

## ntfy

Point the URL at your ntfy server; the token is optional. Notifications use
three channels (assistant, media and system), each with its own topic and
optional token. Full text goes only to topics that refuse an anonymous read.

## TMDB

Create an API read access token in your TMDB account settings and paste it.
Every request carries it as a Bearer token, never as a query parameter.
Media pages that show TMDB data carry the attribution.

## EZTV

EZTV's public API supplies the media channel episode lists. It needs no
credential. Media writes touch only torrents tagged media.

## OIDC presets

The Sign-in settings page offers these presets and renders their steps with
the redirect URI and group names to copy.

| Preset | Name | Issuer | Scopes | Groups claim | Notes |
|---|---|---|---|---|---|
| Pocket ID | `pocket_id` | Issuer URL | openid profile email groups | groups | Enable PKCE; allow the access and admin groups |
| Authelia | `authelia` | Issuer URL | openid profile email groups | groups | PKCE with S256 |
| Authentik | `authentik` | Issuer URL ending in a slash | openid profile email | groups | Use the provider's OpenID configuration issuer |
| Keycloak | `keycloak` | Realm URL | openid profile email | groups | Add a Group Membership mapper named groups |
| Google | `google` | `https://accounts.google.com` | openid email profile | none | Access through verified email or domain allowlists per role |
| Microsoft Entra | `entra` | Tenant ID | openid profile email | groups | Allowlist group object IDs |
| Generic OIDC | `generic` | Issuer URL | openid profile email groups | groups | Confidential client with PKCE S256 |
