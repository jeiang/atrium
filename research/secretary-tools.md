# Direct tools for a personal secretary (ticket #29)

Question: which actions (mail, calendar, contacts, notes, tasks, files, web, notifications, money, media) should be typed harness tools so the agent spends no tokens generating code; survey of assistant products and MCP servers.

Date of research: 2026-10-17. Atrium facts are from `origin/main` (c221de8), cited as path:line. Product claims link to the owner's docs where I could reach them; where I only had a search-engine digest of a primary page it is marked `[digest]`. Inference is marked `[inference]`.

## 1. Answer in short

1. Atrium already has the right architecture: a typed registry with four effect classes and per-run-kind allowlists (`src/atrium/agent/tools/base.py:8-13,22-35`; `registry.py:9-11`), an undo log for direct writes (`agent/writes.py`), proposals for approval, and a read-only Monty `run_code` batch tool (`tools/run_code.py`). 66 tools plus Anthropic's server-side `web_search` (`registry.py:5`).
2. The coverage is lopsided. Read tools are good for mail, calendar, Notion, money, media, loops. There are **no** tools for: tasks/reminders (a todo list the agent owns), mail triage actions (archive/label/snooze/mark), contact writes, calendar updates/RSVP/free-busy/find-slot, file reads beyond attachments, a "send" path that goes through approval, push-with-actions/scheduled reminders, memory search beyond `recall` of tool-result stubs, and a typed quick-capture inbox.
3. Every comparable product converges on the same split: reads free, drafts free, anything that leaves the house (send, invite, forward) behind an approval, and everything else reversible. Gmail's official MCP has **no** send tool at all (drafts + labels only) [Gmail MCP](https://developers.google.com/workspace/gmail/api/reference/mcp); Claude's Gmail connector sends only after per-action approval by default [Claude support](https://support.claude.com/en/articles/10166901-use-google-workspace-connectors) `[digest]`; Lindy has "Ask for Confirmation" on side-effect actions [Lindy](https://www.lindy.ai/academy-lessons/action-configuration) `[digest]`. Atrium's "no tool sends mail, operator's click sends" (AGENTS.md "Decisions") is the strictest of the set and should stay.
4. Recommendation: keep the four classes, split `write` into `write_undo` (reversible, logged) and add `operator_only` as a documented non-tool class; add a machine-readable risk triple per tool (Rule-of-Two flags) and MCP-style annotations for future export; build ~30 new typed tools in three waves (section 6). Prefer workflow-level tools over API-shaped tools (Anthropic's guidance).

## 2. What atrium has today

All tools live in `src/atrium/agent/tools/*.py`; `all_tools()` aggregates (`tools/__init__.py:15-21`). A tool is `Tool(name, description, input_schema, handler, effect, contexts)` (`base.py:34-41`). Run kinds: chat, cron, nightly, sub, code (`base.py:15-22`). Default effect is READ and default contexts are chat/cron/nightly.

| Domain | Tools (effect; contexts) |
|---|---|
| Mail | `mail_search`, `mail_read` (read; chat/cron/nightly/sub/code) `tools/mail.py:92-124`; `draft_email_create`, `draft_email_update` (write; chat only; never send) `tools/drafts.py:50-89` |
| Mail intelligence | `open_loops`, `commitments` (read) `tools/openloops.py:59-86`; `packages`, `upcoming_birthdays` (read) `tools/watches.py:52-73` |
| Calendar | `calendar_list` (read, all kinds), `calendar_create`, `calendar_delete` (write, chat only, undoable, only atrium-created events, no attendees) `tools/calendar.py:136-185`; `event_propose` (propose; chat/cron) `:187-202` |
| Contacts | `contacts_lookup` (read) `tools/contacts.py:19-31`. No writes. |
| Notes/knowledge | `search_knowledge`, `fetch_knowledge` (read) `tools/notion_read.py:121-148`; `notion_create_page/update_page/move_page/archive_page/add_alias` (write; chat+nightly; logged with undo) `tools/notion_write.py:360-420`; `propose` (notion_edit, notion_link; chat/cron) `tools/propose.py:48-60` |
| Tasks | none. Only `plan` (per-thread checklist, `tools/plan.py`) and `schedule_*` (create/update/cancel/list scheduled agent runs, chat only, `tools/schedule.py:101-145`). Commitments can create a `reminder` proposal (`openloops.py:33`). |
| Files | none as tools. Uploaded/project files are attached to the model context (`agent/files.py`), untrusted, deleted with the thread (AGENTS.md). |
| Web | server-side `web_search` (chat/cron, max 5 uses, `registry.py:5-6`); `web_fetch` restricted to URLs the operator typed or a search returned (`tools/web_fetch.py:28-35`) |
| Notifications | `notify` (NOTIFY; 3 per run, 1 nightly; title 120, body 1000 chars; link only to `/chat/<id>`) `tools/notify.py:10-12,95-127`; transport is an ntfy-style JSON POST with optional bearer (`:61-78`) |
| Money | `spending`, `transactions`, `bills`, `payees` (read); `payee_rename/merge/alias_add/set_category`, `transaction_merge/split/set_account` (write, undoable, chat only) `tools/money.py:156-258` |
| Media | `media_downloads`, `media_library_search`, `media_channels` (read); `media_add_magnet`, `media_channel_add/remove` (write, chat only) `tools/media.py:202-267` |
| Ops | `grafana_*` read tools (5) `tools/grafana.py`; `threads_today` (nightly only, `nightly.py:73`) |
| Agent-internal | `profile_get/set` (write, undoable), `recall` (page a stored tool result), `context_status/pin/drop/fold`, `load_skill`, `research` (subagent), `run_code` (read-only batch via Monty) |

Policy shape worth keeping: policy is in the registry, not the prompt (AGENTS.md); scheduled runs only read, notify and propose; `run_code` exposes only READ tools in CODE context (`codemode.py:44`); subagents may use only READ tools in SUB (`registry.py:30-32`).

Gaps visible in the code:

- Effect class is a single string with no notion of reversibility or data flow. `write` mixes "append a draft" (harmless), "delete a Notion page" (undoable) and "add a torrent magnet" (external download, partially undoable). The only per-tool risk information is prose in the description.
- Chat-only writes mean a cron/nightly run can never tidy anything except via proposals. That is the right default but there is no "approve in one tap from the notification" path.
- `notify` carries no actions, priority, or scheduling (ntfy supports all three; section 4.7).
- Tools are mostly resource-shaped (`payee_rename`, `payee_merge`, `payee_alias_add`, ...). Anthropic recommends consolidating chained operations into workflow-level tools [Anthropic: Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) `[digest]`.

## 3. Survey: what other assistants and servers expose

### 3.1 Assistant products

| Product | Mail | Calendar | Tasks / reminders | Other | Approval model |
|---|---|---|---|---|---|
| Claude connectors (Gmail, Calendar, Drive) | search, read, draft, list drafts, labels, send/reply/forward with approval; attachment metadata only | view, create, update, delete, find mutual availability, manage attendees, RSVP, recurrence | none native | Drive search/read | Approval before send/reply/forward by default; admins can relax ([support](https://support.claude.com/en/articles/10166901-use-google-workspace-connectors) `[digest]`) |
| Lindy | Gmail send, search, reply/draft, Save As Draft | create, quick add, update, delete, add attendees, check availability, find available times, create from past event | via integrations | thousands of actions, triggers | per-action "Ask for Confirmation" on side-effect actions ([Lindy](https://www.lindy.ai/academy-lessons/action-configuration) `[digest]`) |
| Martin | search, read, draft, send, reply, forward, label, mark read/unread, archive, delete; daily auto-drafts; "CC to Schedule" | view, create, reschedule, recurring, availability, propose times by email | built-in to-dos with labels, priorities, due dates; reminders by push/email/SMS/phone call; wake-up calls | notes, Drive, Slack, SMS, calls on your behalf | drafts reviewed before send ([Martin](https://www.trymartin.com/blog/gmail-slack-drive) `[digest]`) |
| Shortwave AI | "Organize my inbox": proposes archive/label/delete/todo on groups, user confirms each batch; writes, improves drafts; AI filters; search | manage calendar | todos | CRM integration, MCP client | batch suggestions, user stays in control ([Shortwave](https://shortwave.com/docs/guides/ai-assistant/)) |
| Hermes Agent (Nous) | via plugins/MCP | via plugins/MCP | `todo_list`, `cronjob_manage` (create/list/update/pause/resume/run/remove) | web, file, terminal, browser, vision, image/TTS, `memory`, `session_search`, `delegate_task`, `execute_code`, `clarify`; Home Assistant `ha_*` plugin; delivery is a gateway/CLI feature, not an agent tool ([tools](https://hermes-agent.nousresearch.com/docs/user-guide/features/tools), [registry](https://hermes-agent.nousresearch.com/docs/reference/tools-reference/)) | tool gating by toolset per platform; `clarify` tool asks the user |
| Apple (Siri + App Intents) | Mail schema domain | Calendar | Reminders schema domain: createReminder, updateReminder, deleteReminders, lists, sections, location triggers ([Apple](https://developer.apple.com/documentation/appintents/app-schema-domain-reminders)) | on-screen awareness, Spotlight personal index | system-mediated intents |
| ChatGPT connectors / Google assistants | not verified; `[inference]` same pattern as Claude's | | | | |

Takeaways: (a) every mail assistant that sends does so behind a confirmation or a draft; (b) tasks/reminders and a typed "remind me" are table stakes (Martin, Hermes, Apple) and atrium has no equivalent; (c) mail triage verbs (archive, label, mark read, snooze) are the main time-saver in Shortwave/Martin and atrium deliberately keeps them out of agent hands (AGENTS.md: "Mail routing and flagging run in code, never as a tool an agent chooses"); (d) calendar scheduling is an availability problem, so find-slot and RSVP matter more than raw create.

### 3.2 MCP servers (primary tool lists)

| Server | Tools |
|---|---|
| Google Gmail MCP (preview) | `create_draft, get_message, get_thread, label_message, label_thread, list_drafts, list_labels, search_threads, unlabel_message, unlabel_thread`. No send ([ref](https://developers.google.com/workspace/gmail/api/reference/mcp)) |
| Google Calendar MCP | `list_events, get_event, list_calendars, suggest_time, create_event, update_event, delete_event, respond_to_event, search_events` ([ref](https://developers.google.com/workspace/calendar/api/v3/reference/mcp)) |
| Google Drive MCP | `copy_file, create_file, download_file_content, get_file_metadata, get_file_permissions, list_recent_files, read_file_content, search_files` ([ref](https://developers.google.com/workspace/drive/api/reference/mcp)) |
| Notion hosted MCP | `notion-search, notion-fetch, notion-create-pages, notion-update-page, notion-move-pages, notion-duplicate-page, notion-create-comment, notion-get-comments, notion-query-data-sources, notion-create-database, notion-create-view, notion-get-users, notion-create-file-upload ...` ([docs](https://developers.notion.com/guides/mcp/mcp-supported-tools)) |
| Todoist MCP (Doist) | `add-tasks, find-tasks, find-tasks-by-date, complete-tasks, update-tasks, find-projects` plus more in `src/tools` ([repo](https://github.com/Doist/todoist-ai)); note the plural, batch-shaped verbs |
| Actual Budget MCP | `get-transactions, create-transaction, update-transaction`, balance history, accounts, categories, payees, rules ([repo](https://github.com/s-stefanov/actual-mcp)) |
| Home Assistant MCP | Assist API: `HassTurnOn, HassTurnOff, HassToggle, HassGetState` plus per-domain tools, restricted to entities the user exposed ([docs](https://www.home-assistant.io/integrations/mcp_server/)) |
| iCloud/CalDAV MCPs (community) | list/create/update/delete events; `create_todo/update_todo` on VTODO; `create_contact/update_contact` (CardDAV). Caveat: upgraded iCloud Reminders lists no longer sync over CalDAV ([dav-mcp](https://github.com/Hugomaulwurf76/dav-mcp), [apple-caldav-mcp](https://github.com/dragoshont/apple-caldav-mcp)) `[digest]` |
| ntfy (not MCP, publish API) | message, title, priority 1-5, tags, `click`, up to 3 `actions` (view/http/broadcast/copy), `attach`, `delay`/`at`/`in` (10 s to 3 days default), `sequence_id` to update or cancel a scheduled message ([docs](https://docs.ntfy.sh/publish/)) |

### 3.3 Design guidance that applies

- MCP `ToolAnnotations`: `readOnlyHint`, `destructiveHint` (default true), `idempotentHint`, `openWorldHint` (default true). They are hints, not security guarantees, and must not replace server-side enforcement ([MCP blog](https://blog.modelcontextprotocol.io/posts/2026-03-16-tool-annotations/)). Atrium enforces policy server-side already; annotations are only useful as an export format if atrium ever serves MCP.
- Anthropic: expose workflow-level tools (`schedule_event`) over API-shaped chains (`list_users`+`list_events`+`create_event`), return needs-clarification/alternatives instead of raw lists, avoid opaque ids ([Anthropic](https://www.anthropic.com/engineering/writing-tools-for-agents) `[digest]`).
- Rule of Two / lethal trifecta: within one autonomous session do not combine untrusted input, private data access, and external effects ([Willison](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/), [Meta](https://ai.meta.com/blog/practical-ai-agent-security/)). Atrium's reads of mail are untrusted + private; therefore any tool with an external effect in a run that has read mail must go through approval. This is already what `notify` caps, `web_fetch` allowlist and "no send" do; it should become an explicit per-tool property rather than convention.

## 4. Recommended catalog, by domain

Effect classes (section 5 explains them):

- `R` read, `P` propose (creates a pending item the operator approves), `W` write-with-undo (applied now, logged in `agent_writes`, one-click undo), `N` notify (reaches the operator only), `O` operator-only (never a tool; UI click or CLI).

Flags: `U` returns untrusted third-party text; `X` has an external effect outside atrium's stores.

Kinds: C chat, S cron (scheduled), Ni nightly, B sub, K code (K implies R and non-U-sensitive, as today).

### 4.1 Mail (iCloud + Gmail)

| Tool | Class | Kinds | Status | Notes |
|---|---|---|---|---|
| `mail_search`, `mail_read` | R, U | C S Ni B K | exists | add `mail_thread` (read whole thread, deduped quotes) |
| `mail_attachment_read` | R, U | C B | new | text/PDF extraction of an attachment into the file store with the same caps as uploads (`agent/files.py` caps); Claude's Gmail connector exposes metadata only, so this is a differentiator |
| `draft_email_create/update` | W | C | exists | keep; no send |
| `draft_send_request` | P | C S | new | marks a draft "ready" and notifies; the click on `/drafts` still sends. Gives scheduled runs a way to queue a reply |
| `mail_triage_propose` | P | C S Ni | new | batch of {archive, label, mark-read, snooze, unsubscribe-candidate} per message, shown as one proposal with per-item checkboxes (Shortwave "organize my inbox" model). Applies only via the mail core in `live` mode and its safety gates (`safety.py`). The agent never calls IMAP |
| `snooze_set` | W | C | new | atrium-local resurface (no mailbox write) tied to `open_loops` |
| `open_loops`, `commitments` | R | exists | add `loop_dismiss`/`loop_snooze` as W (local state only) |
| `mail_send` | O | none | n/a | stays operator-only |

### 4.2 Calendar

| Tool | Class | Status | Notes |
|---|---|---|---|
| `calendar_list` | R | exists | add `calendar_get` and `calendar_search` (by text) |
| `calendar_free_busy` / `calendar_find_slot` | R | new | availability over both calendars with working hours and travel buffers; returns candidate slots (the Anthropic `schedule_event` pattern, minus the create step) |
| `calendar_create`, `calendar_delete` | W | exists | `calendar_update` (move/retitle events atrium created) new W |
| `event_propose` | P | exists | extend to updates and to RSVPs |
| `event_rsvp` | P | new | accept/decline/tentative; external effect (organizer notified), so propose until proven |
| attendees/invitations | O | n/a | today invitations are never sent (`calendar.py:160-162`); keep, make a proposal-only path later |

### 4.3 Contacts

| Tool | Class | Status | Notes |
|---|---|---|---|
| `contacts_lookup` | R | exists | add `contact_get` (full card, last interaction from mail index, related loops) |
| `contact_propose_update` | P | new | merge/new-contact/dedupe from mail signatures; CardDAV write only after approval. iCloud contacts are write-capable over CardDAV (community servers expose `create_contact/update_contact`) |
| `contact_note_set` | W | new | atrium-local notes/tags on a contact (no CardDAV write; undoable) |

### 4.4 Notes and knowledge

Exists and mostly right (`notion_*`, `search_knowledge`, `fetch_knowledge`, `propose`, profile). Additions:

- `note_capture` (W): quick capture into a configured inbox page/database, so "remember this" costs one call and no page-id lookup.
- `memory_search` (R): search past threads and nightly summaries (Hermes `session_search`, `memory`); today only `recall` pages stub results.
- `knowledge_link_propose` already covered by `propose`.
- Notion's hosted MCP also exposes comments, databases, views, file uploads; none of these are needed before Phase A parity [inference].

### 4.5 Tasks and reminders (largest gap)

| Tool | Class | Notes |
|---|---|---|
| `task_list`, `task_get` | R | atrium-owned tasks table first (due, priority, labels, source message/event, status); a CalDAV VTODO or Notion database backend can be added later. Apple Reminders upgraded lists do not sync over CalDAV, so do not make iCloud Reminders the source of truth (`[digest]` of apple-caldav-mcp README) |
| `task_add`, `task_update`, `task_complete` | W | batch-shaped like Todoist (`add-tasks`, `complete-tasks`); undoable |
| `remind_me` | W+N | create a time-based reminder that fires a notification (and optionally a scheduled agent run); backs the existing `schedule_*` and ntfy delayed delivery |
| `schedule_*` | W | exists; chat only |
| `task_from_message_propose` | P | extract tasks from `commitments`/`open_loops`; today `reminder` proposals exist (`openloops.py:33`) |

### 4.6 Files and documents

- `file_read` (R, U): read an uploaded/project file by name with page/offset (today files are injected wholesale into context; large PDFs cost tokens every turn).
- `file_search` (R): full-text/semantic over uploaded + project files.
- `file_write` (W): produce an artifact (report, CSV, ICS) in the thread's `generated` files; no overwrite of uploads.
- Drive/Dropbox/iCloud Drive tools: not recommended before the sandbox lands; see #30. Real code/data work belongs in the sandbox (decision in the plan: Monty code mode is replaced by the sandbox).

### 4.7 Web

- `web_search` stays a server tool (Anthropic) for chat/cron.
- `web_fetch` stays allowlisted. Add `web_extract` options (readability vs raw, `selector`, `pdf`) rather than loosening the allowlist. Add `link_check`-style fetch for packages (carrier tracking pages) as R tools with fixed domains instead of generic fetch.
- Browser automation (Hermes `browser_*`) is out of scope until the sandbox has egress control (#25).

### 4.8 Notifications

| Tool | Class | Notes |
|---|---|---|
| `notify` | N | exists. Add: `priority` (1-5, capped to 3 for cron/nightly without explicit rule), `actions` (view links back into atrium; `http` actions to approve/deny a proposal through a signed, single-use, expiring URL), `delay`/`at` (ntfy supports up to 3 days; longer needs atrium's own scheduler) |
| `notify_cancel` | N | cancel by `sequence_id` for scheduled notifications |
| Escalation policy (quiet hours, digest batching, dedupe) | harness, not agent | belongs in `notify.send`, driven by settings (#22), not the model |

The per-run caps (3 / 1) stay as defense against injection-driven spam; `notify` stays NOTIFY because it only reaches the operator.

### 4.9 Money

Read tools are good. The write set (`payee_*`, `transaction_*`) is resource-shaped and could be folded:

- `payee_edit` (W): `{payee_id, name?, category?, add_aliases?, merge_into?}` in one call, one undo entry.
- `transaction_fix` (W): `{ids, account?, merge|split}`.
- `bill_watch`/`budget_check` (R/N): "tell me when category X passes Y" as a harness rule driven by the scheduler, not an agent loop.
- Anything that moves money, pays a bill, or touches a bank: O, never a tool. The Actual Budget MCP exposes `create-transaction`/`update-transaction`; atrium's ledger is mail-derived, so correction tools are the right level.

### 4.10 Media

Read tools and `media_add_magnet`, `media_channel_*` exist. `media_add_magnet` has an external effect (download, VPN) and is chat-only W; fine. Add `media_search_release` (R, EZTV/TMDB lookup) so the agent does not fabricate magnets, and `media_status_notify` as a harness event (download complete) rather than a tool.

### 4.11 Home, health, travel, location (not in the ticket's list; inventory only)

- Home Assistant has a first-party MCP Assist API with entity exposure; a module would be `home_state` (R) and `home_set` (W with undo by inverse action) behind an exposed-entities allowlist [HA docs](https://www.home-assistant.io/integrations/mcp_server/). Candidate ticket, not Phase A.
- Package/shipping, birthdays, bills already exist as read tools.

### 4.12 Agent-internal tools (keep)

`plan`, `recall`, `context_*`, `load_skill`, `research`, `profile_*`, plus new `ask_operator` (a `clarify`-style structured question that pauses the run and surfaces in chat/notify; Hermes has `clarify`). `run_code` is replaced by the sandbox in v2; until then keep as is.

## 5. Effect classes and enforcement

Recommended taxonomy (all enforced by the registry, as today):

| Class | Meaning | Applied | Undo | Allowed kinds (default) |
|---|---|---|---|---|
| `read` | no change in atrium or the world | now | n/a | all |
| `propose` | creates a pending item, nothing changes until approved | on approval | proposal discard | chat, cron |
| `write_undo` | changes atrium's own stores or a reversible external object; logged in `agent_writes` with before/after and an undoer | now | one click (`writes.undo`) | chat, nightly |
| `notify` | reaches the operator only; capped per run | now | none | chat, cron, nightly |
| `operator_only` | not exposed as a tool (send mail, merge, delete, payment, push to protected branch, restart) | UI/CLI | n/a | none |

Notes on the split:

- Today's single `write` becomes `write_undo` plus a lint: a `write_undo` tool must register an undoer (`writes.register_undoer`) and call `writes.record` (`writes.py:20-36`). A test can enforce it for every tool with that class; this is how `media_add_magnet` or anything partly irreversible is forced to be `propose` or documented as best-effort.
- External effects that cannot be undone (send invitations, RSVP, forward) are `propose` until the operator opts a specific tool to `write_undo` in settings (Lindy/Claude let the user relax per action).
- Add per-tool flags `untrusted_output`, `private_data`, `external_effect` (the Rule-of-Two triple). Enforce: a run that has seen `untrusted_output` from a `private_data` tool loses `external_effect` tools unless the effect is `notify` or `propose`. This formalizes the current `web_fetch` allowlist and "no send" rule and covers new tools (file read of an untrusted upload, attachment text) without prompt wording.
- Export mapping if atrium ever serves MCP: `read` -> `readOnlyHint=true`; `propose`/`write_undo` -> `readOnlyHint=false, destructiveHint=false`; `openWorldHint` = `external_effect`.
- Per-kind policy table lives in settings (#22) as defaults that an operator can narrow, never widen past the registry maximum.

## 6. Build order (candidate tickets)

Wave 0 (Phase A, port as is): none; same 66 tools, same effects. Rename `write` -> `write_undo` internally only if it costs nothing; behavior parity first.

Wave 1 (Phase B, high value, low risk, all local state):
1. `task_*` + `remind_me` + task proposals.
2. `notify` priority/actions/delay + `notify_cancel`.
3. `calendar_free_busy`/`find_slot`, `calendar_get/search/update`.
4. `memory_search`, `note_capture`, `ask_operator`.
5. Effect-class lint and risk flags.

Wave 2 (needs mail-core and approvals work):
6. `mail_thread`, `mail_attachment_read`, `mail_triage_propose`, `draft_send_request`, `snooze_set`.
7. `event_rsvp` (propose), `contact_propose_update`, `contact_note_set`.
8. Consolidate money write tools.

Wave 3 (depends on sandbox/modules):
9. `file_read/search/write`, GitHub tools, Home Assistant module, browser tools.

## 7. Options and trade-offs

| Option | Pros | Cons |
|---|---|---|
| A. Keep API-shaped tools, add the missing ones | smallest change; matches current style | more tool-selection errors and chained calls; long descriptions |
| B. Workflow-level tools (recommended) | fewer tokens, atomic, fewer partial states; fits "offload to harness" aim | harder to compose; needs good needs-clarification results |
| C. Expose atrium tools via MCP and consume third-party MCP servers | ecosystem reuse | breaks the "policy in registry" guarantee for third-party tools; untrusted tool descriptions are an injection vector; annotation hints are not security |
| D. Let the sandbox do everything (code) | maximal flexibility | the opposite of the owner's aim (token spend) and a larger blast radius |

Recommendation: B for new tools, A where a tool already exists and works, C only as an optional later module with a default-deny allowlist and every third-party tool forced into `propose`/`read` classes, D only for coding.

## 8. Risks

- Approval fatigue: too many proposals (mail triage, RSVPs, tasks) degrade the safety. Mitigate with batch proposals and a "trust this tool" setting per tool, off by default.
- Notification abuse via injection: keep caps and the link allowlist; `http` actions must be signed single-use and scoped to one proposal.
- Tasks as a new store: choose atrium-owned tables now; two-way sync with VTODO or Notion later is a separate decision. iCloud Reminders over CalDAV is unreliable for upgraded lists (`[digest]`).
- Attachment/file reads enlarge the untrusted-text surface; reuse the uploads' "never add to allowlist or expand skills" rule (AGENTS.md).
- `calendar_*` on Google: Google's own MCP scopes show a narrower default (`calendar.events.readonly`, freebusy) [config guide](https://developers.google.com/workspace/guides/configure-mcp-servers); atrium's OAuth scopes should be reviewed per tool class (feeds #33/#35).

## 9. New questions / candidate tickets

1. **Tasks and reminders model** (design, Phase B, area: assistant). Atrium-owned task store vs CalDAV VTODO vs Notion DB; reminder delivery through notifications and scheduler; undo semantics.
2. **Risk-triple enforcement and effect-class migration** (design, Phase B, area: assistant/security). Add `write_undo`, `operator_only`, `untrusted_output/private_data/external_effect`; per-run policy; registry lint. Blocks #42.
3. **Mail triage as proposals** (design, Phase B, area: mail). Batch archive/label/snooze/unsubscribe proposals under the existing shadow/live gates; how it coexists with "routing runs in code, never as a tool".
4. **Operator approval inbox** (feature, Phase B, area: UI). One-tap approve/deny from a notification action (signed URL), batch approvals, per-tool trust levels.
5. **Notification capabilities** (research follow-up with #31). Priority, actions, delay, sequence ids, quiet hours, digest, escalation ladder (push then email/SMS), per-topic ACL.
6. **Calendar scheduling workflow** (feature, Phase B). Free/busy over both calendars, find-slot, update, RSVP, attendee invitations as an approved action only.
7. **Contact write path** (design, Phase B). CardDAV create/update through proposals; dedupe; local notes.
8. **File tools** (feature, Phase B/C). `file_read/search/write` for uploads/projects, extraction caps, interaction with the sandbox filesystem.
9. **Memory/recall search** (research, Phase B). `memory_search` over threads and nightly summaries; retention and encryption (links to #23).
10. **Home Assistant / other-domain modules** (research, Phase C). Exposed-entity allowlist, inverse-action undo.
11. **Third-party MCP consumption policy** (design, Phase C). Default-deny, class forcing, description pinning; relevant to Hermes-style plugin catalogs.
12. **Voice / phone / SMS channels** (research, fog). Martin's call and SMS channels show demand; not in plan; needs a scope decision.
13. **`ask_operator` / clarify tool** (feature, Phase B). Structured pause that surfaces via chat or notification.
14. **Tool catalog docs generator** (chore). Emit docs/tools.md from the registry (name, class, kinds, flags).

## 10. Sources

- Atrium code at c221de8: `src/atrium/agent/tools/*.py`, `registry.py`, `writes.py`, `codemode.py`, `AGENTS.md`.
- Gmail, Calendar, Drive MCP references on developers.google.com (linked above).
- Claude Google Workspace connectors: https://support.claude.com/en/articles/10166901-use-google-workspace-connectors (via search digest).
- Lindy action configuration: https://www.lindy.ai/academy-lessons/action-configuration (digest).
- Martin: https://www.trymartin.com/blog/gmail-slack-drive (digest).
- Shortwave AI Assistant docs: https://shortwave.com/docs/guides/ai-assistant/.
- Hermes Agent docs: https://hermes-agent.nousresearch.com/docs/user-guide/features/tools and `/docs/reference/tools-reference/`.
- Apple Reminders schema domain: https://developer.apple.com/documentation/appintents/app-schema-domain-reminders.
- Notion MCP: https://developers.notion.com/guides/mcp/mcp-supported-tools.
- Todoist MCP: https://github.com/Doist/todoist-ai; Actual MCP: https://github.com/s-stefanov/actual-mcp.
- Home Assistant MCP: https://www.home-assistant.io/integrations/mcp_server/.
- ntfy publish: https://docs.ntfy.sh/publish/.
- MCP tool annotations: https://blog.modelcontextprotocol.io/posts/2026-03-16-tool-annotations/.
- Anthropic, Writing effective tools for agents: https://www.anthropic.com/engineering/writing-tools-for-agents.
- Willison lethal trifecta: https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/; Meta Agents Rule of Two: https://ai.meta.com/blog/practical-ai-agent-security/.
- Not verified against primary pages: ChatGPT connectors, Google/Apple consumer assistants beyond the Apple Reminders schema; Superhuman AI (no source reached).
