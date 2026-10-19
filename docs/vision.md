# atrium: what it is for

This page says what atrium is for and where v2 is heading. The wayfinder map
([issue #15](https://github.com/jeiang/atrium/issues/15)) holds the detail
and the decisions. Owner answers are recorded in
[Owner answers on the vision](https://github.com/jeiang/atrium/issues/89).

## Goal

atrium is a private assistant service that one person runs on their own
host. It keeps a local index of that person's mail, calendar, contacts,
Notion wiki, spending and media library. It does routine work on that data
in plain code, with no model in the loop. A Claude-based agent sits on top
for anything that needs judgment. v2 adds a coding harness: an isolated
microVM per chat thread, where the agent or a coding agent it calls can run
scripts and change a repository.

Others running their own instance is a v2 goal. At least two other people
intend to use and extend it. That is part of why there is a Go port and a
separate repo. Nothing in v2 may assume the owner's country, bank or
hardware.

## Who it serves

One operator per instance: the person whose mail, calendar and money it
reads. More people means more instances, not more users. The operator is
also the admin. In v2 those roles split into the `atrium_access` and
`atrium_admin` OIDC groups.

## Jobs

- Mail secretary. Sync iCloud and Gmail, decide a folder per message in
  code (rules, embedding neighbors, a model judgment, fallback), verify
  every write, log it with an undo. Shadow mode records decisions without
  writing.
- Follow-up tracking: open loops, commitments, packages, birthdays and
  event candidates from mail.
- Drafting, never sending. Only the operator's Send click sends.
- Knowledge keeping: a Notion wiki, a nightly run that records stable
  preferences, weekly reviews and daily nudges.
- Calendar, money and media, with undoable agent edits.
- Read-only Grafana access.
- A chat workspace with threads, plans, files, projects, skills and
  research subagents.
- v2 adds a sandbox per thread, environments, GitHub, pi and Claude Code as
  callable coding agents, model and effort choice per thread, settings
  editable in the UI, OIDC login, module toggles and an encrypted database.

## Principles

- Policy lives in code, not in the prompt. The tool registry decides what
  each run kind may use.
- A ladder of autonomy by effect: read freely, propose when the operator's
  judgment is needed, write directly only with a logged undo, and leave
  irreversible or outward-facing acts (send, invite, merge, delete) to a
  click. v2 writes this ladder down as the tool contract, so a new tool's
  place follows from its effect.
- Mail routing is never an agent decision.
- Everything the model reads is untrusted data.
- Data stays home by default, and what leaves is named.
- Spend is bounded and visible.
- Boundaries are real: a microVM with an egress proxy, not an interpreter.
- Journaled, resumable, idempotent.
- Degrade, do not crash: a missing credential disables only what needs it.
- Deterministic jobs stay model-free.
- No personal literal in the repo.

## Decisions from the owner

- Memory precedence is profile, then nightly, then wiki. The profile is the
  source of truth. The nightly run updates the wiki, weighted by confidence.
  If the wiki says the operator usually uses tool X and the nightly saw them
  use Y once, the wiki keeps X and gains a note that they sometimes use Y.
  The mechanism is a ticket: Memory model.
- Money is not specific to one country. Currencies are configurable, with
  USD as the base. Every other currency has a conversion rate that the
  operator sets as a fixed value. There is no rate fetcher. Changing a rate
  never changes older transactions, because each transaction stores the rate
  it used.
- Mail does not need a local model. Classification and embeddings use any
  OpenAI-compatible endpoint, local or hosted. The operator chooses, and the
  privacy consequence of a hosted endpoint is shown where it is chosen.
- Scripts in the sandbox may call atrium's own read tools. This replaces
  the tool calling that `run_code` does today. Write-class tools are not
  callable that way.
- There is no fixed cost target yet. Measurements come first.

## Direction accepted

- A per-job token and cost table (weekly review, nudge, nightly, a typical
  chat answer), plus a replay harness that runs journaled threads against a
  new build or model. It also serves as parity evidence for the port.
- One attention view across open loops, commitments, proposals, plans,
  event candidates, unassigned transactions and drafts, used by the operator
  and by the daily nudge.
- A data-driven money module: currencies, rates and bank profiles as
  settings and data, not code constants.
- A stated memory model for profile, wiki, skills, projects, nightly
  capture and thread folds: what goes where, what the system prompt loads
  and how stale facts expire.

## What success looks like

- The inbox holds nothing that has a rule or a confident decision, and
  nothing is misfiled that the operator did not correct once.
- Weekly reviews and daily nudges replace scanning mail for deadlines,
  bills, birthdays and packages, and each item links to its source.
- Questions about the operator's own data are answered from tools, with ids
  the operator can open, inside the budget.
- The agent never sends, invites, merges or deletes. Every automatic write
  shows on `/activity` with a working Undo.
- Monthly spend stays under budget, and the budget page says where tokens
  went.
- v2: the operator opens a thread bound to a repo, asks for a change, and
  gets a draft PR without a terminal. The token never enters the VM.
- v2: a second person stands up their own instance from the NixOS module and
  the docs, and connects each integration from the web UI.
- The Go port passed the parity inventory against the Python app it replaced.

## Still open

- Whether a branch push or draft PR counts as undoable or needs a proposal
  on the first push per repo. It stays open in the GitHub module and tool
  interface tickets.
- Whether and how the operator reaches atrium from a phone: ntfy action
  buttons, a PWA with web push, or a chat bridge, and what it may do
  (approve or dismiss a proposal, send a short message). Ticket: Inbound
  phone channel.

## Non-goals

- The agent never sends mail, invites attendees, pays, merges or deletes.
- No multiple users in one instance, no untrusted tenants, no shared
  sandbox manager.
- No general shell on the atrium host for the model. Code runs only in the
  sandbox.
- Not an email client, a Notion replacement, a budgeting app or a torrent
  client. The operator keeps using the originals.
- Not an IDE. Scripts, small changes and draft PRs; long refactors go to pi
  or Claude Code in the sandbox.
- No CDN, no inline script, no third-party frontend at runtime.
- No Nix inside the sandbox, no macOS sandbox host, no OCI image.
