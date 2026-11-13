# Integrations

Every connection is configured on the Integrations settings page or the
Connections page (`/settings/connections`) and tested there. A provider
credential is stored write-only in an encrypted database or supplied by the
connection's `credential_files` entry in the host configuration; there is no
environment pin for a secret. The only environment secret inputs left are the
database key (`ATRIUM_DB_KEY`/`ATRIUM_DB_KEY_FILE`) and the two app key files
(`app_keys.action_link_key_file` and `app_keys.link_key_file`), which the
NixOS module's `secretFiles` option renders. The matrix is the support
summary; the sections below cover the console work atrium cannot do for you.

## Support matrix

| Connection | Registry name | Auth | What it permits | Limits |
|---|---|---|---|---|
| Anthropic | `anthropic` | Console API key | Assistant model calls | Key is workspace-bound; use one workspace per instance with a monthly spend limit |
| Grafana | `grafana` | Service account token | Read-only dashboard and datasource queries | Viewer role is enough; optional token expiry |
| Embedding endpoint | `service/embedding` | Optional host-file bearer | Embeddings for mail search and routing | A hosted endpoint sends mail content off this host |
| Notion | `service/notion` | Internal integration token or personal access token | Wiki read, update and insert; knowledge export | A connection on `/settings/connections`; share the pages with the integration; a personal token acts as the user and lasts at most a year |
| qBittorrent | `service/qbittorrent` | API key | Media torrent and library operations | A connection on `/settings/connections`; API keys need qBittorrent 5.2 or newer |
| TMDB | `service/tmdb` | API read access token | Media metadata | A connection on `/settings/connections`; read-only; media pages show the attribution |
| Open-Meteo | `open_meteo` | None | Weather forecasts and place lookup | Public API; no key. The configured coordinates are sent to Open-Meteo |
| Ship24 | `ship24` | Bearer API key | Live package tracking | Free plan registers 10 shipments a month; polling, no webhooks |
| Google Calendar and Gmail | — | OAuth web client | Calendar events and Gmail over IMAP and SMTP | See [Google](#google) |
| iCloud | — | App-specific password | IMAP, SMTP, CalDAV and CardDAV | See [iCloud](#icloud) |
| EZTV | `service/eztv` | None | Media channel episode lists | A connection on `/settings/connections`; public API; only torrents tagged media are written |

## Google

Google Calendar and Gmail use a Web-application OAuth client that each
instance registers itself. The wizard on the Connections page stores the
client ID and secret, performs one consent per capability, and keeps the
refresh token as a write-only secret in the database.

| Account | Gmail IMAP and SMTP | Calendar | Notes |
|---|---|---|---|
| Personal | App password or XOAUTH2 | OAuth | An app password needs 2-step verification and Google does not recommend it |
| Workspace | XOAUTH2 | OAuth | App passwords are not available |
| Advanced Protection | XOAUTH2 | OAuth | App passwords are refused |

The Gmail consent in the wizard stores a refresh token for the XOAUTH2 mail
transport. Add the Gmail account on the Mail settings page and choose
XOAUTH2; a personal account may use an app password instead.

### Setup

1. Open [Google Cloud credentials](https://console.cloud.google.com/apis/credentials)
  and create a project, or pick one.
2. Configure the [OAuth consent screen](https://console.cloud.google.com/apis/credentials/consent)
  as External, then add your own address as a test user.
3. Enable the [Google Calendar API](https://console.cloud.google.com/apis/library/calendar-json.googleapis.com).
  Gmail over IMAP and SMTP uses the `https://mail.google.com/` scope and
  needs no Gmail API.
4. Create credentials of type **Web application** and add the redirect URI
  `https://<atrium-host>/settings/connections/google/callback`.
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
mail account for their CalDAV and CardDAV login. The Calendar section on the
Calendar settings page (`/settings/calendar`) edits `calendar_account`, `icloud_apple_id`, `icloud_calendar`,
`default_event_minutes`, `work_hours` and `work_days`.

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
wiki pages with it, then paste its token on the Notion connection on
`/settings/connections`. The connection is `service/notion`: the host declares
it under `connections."service/notion"` with `config.api` (default
`https://api.notion.com`), optional `transport.ca_file` and `transport.proxy`,
and the `token` credential slot, which may be a host credential file. The
Notion settings page (`/settings/notion`) edits `wiki_root`,
`sensitivity_property`, `notion_private_value` and `wiki_log_title`.

The knowledge base is projected into Notion by the `knowledge-export` job; the
wiki pages above are not touched. The **Export knowledge to Notion** section on
the Export settings page (`/settings/export`) sets the export: paste the parent
page's URL or id (`export_notion_parent`, stored as the normalized page id),
choose what to include (`export_notion_kinds`) and turn on Include private
records to store the same destination in `export_notion_private_to`. The
export overwrites its own pages on every run and trashes the page of a record
that is retracted or leaves the export set; edits made in Notion are lost. A
write with an unknown outcome blocks the export until it is resolved. The
**Obsidian export** section writes a vault folder instead (`export_obsidian_dir`,
normally declared by the host); its private switch stores the folder path in
`export_obsidian_private_to`.

## Grafana

Create a service account in Grafana and paste its token. The Viewer role is
enough: it can query datasources. A token has no expiry by default; set a
TTL if your policy requires one. The Test button searches dashboards and
reports a token that may not search as needing the Viewer role. The Grafana
record edits `grafana_metrics_uid`, `grafana_logs_uid` and `grafana_logs_type`.

## Anthropic

Use a Console API key from a workspace dedicated to this instance, with a
monthly spend limit, so a leaked key cannot spend beyond it. A key can be a
personal, service-account or legacy workspace key; it is workspace-bound and
may carry an expiry.

- Rotate the key in the Console and paste the new one on the Integrations
  page; the old key stops working.
- The key is stored encrypted in the `service/anthropic` connection, or the
  host supplies it through `credential_files.api_key`; a key from the host
  file cannot be changed from the UI.
- An Admin API key can list members, workspaces and usage cost, but cannot
  mint keys. Atrium does not use one yet.
- Subscription OAuth is not allowed for third-party products; only the
  unmodified Claude Code binary signs in to a subscription itself.

## Mail endpoints

The embedding endpoint is the `service/embedding` connection: an
OpenAI-compatible base URL ending in `/v1`, with an optional host-file bearer.
Its Test lists models and measures the dimension. A hosted endpoint warns that
mail content leaves this host. Mail classification has no chat endpoint; it
uses fixed-schema Haiku requests through the `service/anthropic` connection.

## qBittorrent

Create an API key in qBittorrent 5.2 or newer and paste it. Without a key,
requests go unauthenticated. Test fails on a server older than 5.2, which has
no API keys.

## TMDB

Create an API read access token in your TMDB account settings and save it on
the `/settings/connections` TMDB record. Every request carries it as a Bearer
token, never as a query parameter, and it is never sent to the image host.
Test reads `/configuration` and then checks the image host without the token.
The API and image URLs come from `connections."service/tmdb".config` in the
host configuration. Media pages that show TMDB data carry the attribution.
`media_movies_url` and `media_tv_url` remain host settings until the media
file servers are retired.

## Open-Meteo

Weather forecasts and place lookup need no credential. Set the location
(`weather_latitude`, `weather_longitude` and optionally
`weather_location_name`) on the Open-Meteo record on the Connections page;
the `weather` tool reads it, or geocodes a place named in the call. Latitude
must be between -90 and 90 and longitude between -180 and 180, and a value
the host file declares is shown read-only. The tool fetches only
the configured Open-Meteo hosts, and the request carries no operator data
beyond the coordinates.

## Ship24

Ship24 supplies live package tracking for packages the operator registers
with the `track_package` tool. Create an API key in the Ship24 dashboard and
paste it on the Connections page, or declare `credential_files.api_key` on
the `service/ship24` connection in the host configuration. The free plan registers 10 shipments a month; the
`mail_ship24_monthly_quota` setting (default 10) refuses further
registrations once the month's count is exhausted. Atrium polls the active
trackers hourly and stops polling delivered packages. There is no webhook
route.

## EZTV

EZTV's public API supplies the media channel episode lists. It needs no
credential. Test fetches one bounded indexer response (`limit=1`) and checks
its shape without adding a torrent. Media writes touch only torrents tagged
media.

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
