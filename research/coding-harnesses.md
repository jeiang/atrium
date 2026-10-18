# Coding harness survey (ticket #27)

Researched 2026-10-17 against primary sources (vendor docs, source repos, release tags). Every claim links to its source. `[inference]` marks my reasoning, not a sourced fact. Facts about atrium cite `path:line` in the repo at `origin/main` (c221de8).

Surveyed: Claude Code, Codex CLI, pi, Hermes Agent, opencode, OpenHands, Aider, Goose, Cursor agents, Amp. Versions seen: Claude Code v2.1.302, Codex rust-v0.163.0, pi v1.3.0 (published 2026-10-15), Hermes v2026.9.24, opencode v1.19.3 (a v2 line, v2.0.32, also exists), OpenHands v1.27.1 with Software Agent SDK v1.55.1, Aider v0.86.2 (PyPI, 2026-02-12), Goose v2.0 rc tags ([git tags](https://github.com/aaif-goose/goose)). Cursor and Amp are hosted products, so there are no tags.

Scope note: pi and Claude Code *inside the sandbox* is ticket #32, and the egress/credential proxy is #25/#58. Here they appear only where the survey needs them.

---

## 1. Answer in one page

1. **Every mature harness has the same skeleton.** There is a loop that calls the model, then runs tools, then appends results. Around it sit a tool registry, a permission layer, a context manager (clear old tool output, then summarize), subagents with their own context windows, markdown skills, an MCP client, lifecycle hooks, persistent sessions, and a headless/SDK surface. Atrium already has the loop, registry, run-kind policy, compaction, subagents and skills; it lacks MCP, a sandbox, permission *decisions* beyond a per-run-kind allowlist, and checkpoints.
2. **Nobody treats approval prompts or classifiers as the security boundary.** Claude Code says Bash deny rules are "not a security boundary around the program" ([permissions](https://code.claude.com/docs/en/permissions.md)). Cursor says Auto-review "is not a security boundary" ([run modes](https://cursor.com/docs/agent/security/run-modes.md)). Pi says watching the transcript and project trust "do not create a security boundary" ([security](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)). Goose's adversary reviewer fails open ([adversary mode](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/security/adversary-mode.md)). The boundary is an OS/VM sandbox plus a network proxy. That matches the plan (microVM plus egress proxy).
3. **Atrium's per-run-kind, code-enforced tool policy is stronger than anything surveyed for the secretary half.** The model never sees a tool its run kind cannot use (`src/atrium/agent/registry.py:9-11`). The closest equivalents are Claude Code's bare-name deny rules that remove a tool from context ([permissions](https://code.claude.com/docs/en/permissions.md)) and Codex's `prefix_rule` decisions. Keep it, and make it data-driven in Go.
4. **The coding half should be copied from the cloud-agent products (Cursor cloud agents, Amp orbs, Codex Cloud), not the terminal agents.** Those already solve what atrium's sandbox needs: env var vs redacted runtime secret vs build-only secret, setup phase with network and agent phase without, per-environment egress allowlist, hibernate when idle and delete later, a draft PR for human review, signed commits, and OIDC instead of long-lived keys. Details in section 4.
5. **Three harnesses keep a lightweight programmatic tool-calling layer separate from the shell** (pi `codemode`, Hermes `execute_code`, Goose Code Mode). Scripts reach tools only over RPC and only their output returns to the model. This is exactly the user's stated aim ("offload to harness tools so the agent does not waste tokens generating code"). The plan replaces Monty code mode with the sandbox. Whether to *also* keep a cheap in-process script layer is an open question (candidate ticket C1).
6. **Agent Skills (`SKILL.md`, [agentskills.io](https://agentskills.io/specification)) and `AGENTS.md` are the de-facto portable formats.** Codex, pi, Hermes, Amp, opencode, Claude Code and OpenHands all read them. Atrium stores skills in SQLite with a custom `/skill:name` syntax (`src/atrium/agent/skills.py:7-8`); import/export of `SKILL.md` is cheap and removes a lock-in.

---

## 2. Comparison matrix

| | Claude Code | Codex CLI | pi | Hermes | opencode | OpenHands | Aider | Goose | Cursor agents | Amp |
|---|---|---|---|---|---|---|---|---|---|---|
| Language / shape | Closed binary; Agent SDK wraps it | Rust, open | TypeScript, open, MIT | Python, open, MIT | TypeScript, open; client/server | Python SDK + agent server | Python, open | Rust, open, MIT | Hosted + desktop + CLI | Hosted + CLI |
| Default approval posture | Manual prompts; `auto` classifier is default in new interactive sessions | `workspace-write` sandbox + `on-request` | **No prompts** | `smart` (aux LLM) | Mostly `allow` | `AlwaysConfirm`/`NeverConfirm`/`ConfirmRisky` | Confirms each action; `--yes` | **Autonomous** default | Auto-review (sandbox + classifier) | **No approval by default** |
| OS sandbox for shell | Seatbelt / bubblewrap (opt-in) | Seatbelt / bubblewrap / Windows | None built in | Docker, Singularity, Modal, Daytona, SSH backends | None built in | Docker / remote / process | None | None built in | Sandbox for shell on desktop; VMs in cloud | Orbs (remote machines) |
| Subagents | Yes (md files, built-ins) | Yes (custom agents, TOML) | **No, by design** | `delegate_task` | Yes (`task`, depth 1 default) | `TaskToolSet` (sync, resumable) | No | Yes (recipes, ad hoc) | Yes (fg/bg) | Yes (Oracle, Librarian, Task…) |
| Skills (SKILL.md) | Yes | Yes | Yes | Yes (+ self-authored, curator) | Yes | Yes | No | Yes | Yes | Yes |
| MCP | Yes (stdio/HTTP, tool search) | Yes (TOML) | Yes (stdio/HTTP, OAuth) | Yes (+ tool search) | Yes (local/remote, OAuth) | Yes | **No** | Yes (core extension model) | Yes | Yes (bundled in skills) |
| Context compaction | Clear old tool output, then summarize | Auto-compact at configured token limit | Summary at window − 16k; branch summaries | Two engines, pluggable | Hidden `compaction` agent | `LLMSummarizingCondenser` | `/clear`, repo map | Auto at 80% or 225k tokens | Not documented in pages read | Not documented in pages read |
| Headless / SDK | `claude -p`, Agent SDK (Py/TS) | `codex exec --json`, TS SDK, app-server | print, JSONL, RPC, TS SDK | CLI `-q`, API server, ACP | `opencode serve` (OpenAPI 3.1) + JS SDK | Agent server REST+WS | `--message` | `goose serve`/ACP | `agent -p`, cloud API | `amp -x`, streaming JSON, SDKs |

Sections 3.x give the source for each cell.

---

## 3. Per-harness findings

### 3.1 Claude Code (Anthropic, closed source)

- **Architecture.** A three-phase loop (gather context, act, verify) driven by Claude models plus tools, with the "harness" being the layer that provides tools and manages context ([how it works](https://code.claude.com/docs/en/how-claude-code-works.md)). Same engine behind terminal, IDE, desktop, web, Slack, CI ([overview](https://code.claude.com/docs/en/overview.md)). Sessions are plaintext JSONL under `~/.claude/projects/` and can be resumed or forked; file edits are snapshotted for rewind ([how it works](https://code.claude.com/docs/en/how-claude-code-works.md)).
- **Tools.** Read, Edit, Write, Bash, Glob, Grep, WebFetch, WebSearch, NotebookEdit, LSP, Monitor, Agent, Skill, ToolSearch, plan/worktree tools, task-list tools, cron tools, `AskUserQuestion`, `Workflow` (script that orchestrates many subagents), `PushNotification`, `SendUserFile` ([tools reference](https://code.claude.com/docs/en/tools-reference.md)).
- **Permissions.** Rule syntax `Tool(specifier)`; evaluated deny, then ask, then allow, first match wins, specificity does not matter ([permissions](https://code.claude.com/docs/en/permissions.md)). A bare tool name in `deny` removes the tool from the model's context. Rules are "enforced by Claude Code, not by the model". Modes: `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`. `auto` runs a separate classifier model over actions; `dontAsk` auto-denies anything that would prompt ([permissions](https://code.claude.com/docs/en/permissions.md), [permission modes](https://code.claude.com/docs/en/permission-modes.md)). Bash matching strips a fixed wrapper list and splits compound commands, but `/bin/rm` or `bash -c 'rm …'` evade a `Bash(rm *)` rule, so rules are not a boundary ([permissions](https://code.claude.com/docs/en/permissions.md)). Protected paths (`.git`, `.claude`) stay write-protected except in `bypassPermissions`. A `cd` into another directory followed by `git` prompts because git runs that directory's hooks ([permissions](https://code.claude.com/docs/en/permissions.md)).
- **Sandbox.** Optional OS sandbox for shell commands only (Seatbelt on macOS, bubblewrap + socat on Linux): writes limited to the working directory, network only through a local proxy with an allowed-domain list that starts empty ([sandboxing](https://code.claude.com/docs/en/sandboxing.md)). A command that fails in the sandbox can be retried unsandboxed, which then goes through the normal permission flow. File tools, MCP servers and hooks run outside it ([sandboxing](https://code.claude.com/docs/en/sandboxing.md)). Built on [`@anthropic-ai/sandbox-runtime`](https://github.com/anthropics/sandbox-runtime).
- **Context.** Clears older tool outputs first, then summarizes; persistent rules belong in `CLAUDE.md`; a "Compact Instructions" section steers the summary; auto-compaction stops after repeated thrashing instead of looping ([how it works](https://code.claude.com/docs/en/how-claude-code-works.md)). MCP tool definitions are deferred and loaded on demand by tool search ([same](https://code.claude.com/docs/en/how-claude-code-works.md)). `PreCompact` hook exists ([hooks](https://code.claude.com/docs/en/hooks.md)).
- **Subagents.** Markdown files with YAML frontmatter (`tools`, `model`, `permissionMode`, `isolation: worktree`, `memory`, hooks…), own context window, return only a summary; built-ins Explore (read-only), Plan, general-purpose; subagent descriptions are capped by a 15,000-token startup warning ([sub-agents](https://code.claude.com/docs/en/sub-agents.md)). Plugin subagents cannot carry hooks, `mcpServers` or `permissionMode` for security ([same](https://code.claude.com/docs/en/sub-agents.md)). Separate features: background agents, agent teams, dynamic workflows ([overview](https://code.claude.com/docs/en/overview.md)).
- **Memory and skills.** `CLAUDE.md` (also reads `AGENTS.md`), auto memory (first 200 lines or 25KB of `MEMORY.md` loaded each session), skills load on demand ([how it works](https://code.claude.com/docs/en/how-claude-code-works.md)).
- **MCP.** stdio and HTTP; permission rules can match `mcp__server__tool` with globs; allow globs must be anchored to a literal server prefix ([permissions](https://code.claude.com/docs/en/permissions.md)).
- **Headless / SDK.** `claude -p` with `--output-format json`/stream; the Agent SDK (Python, TypeScript) "runs the Claude Code binary" and exposes tools, hooks, subagents, MCP, permissions, sessions ([Agent SDK](https://code.claude.com/docs/en/agent-sdk/overview.md), [headless](https://code.claude.com/docs/en/headless.md)). **Constraint:** third parties may not offer claude.ai login or its rate limits in their products without approval; use API keys ([same page](https://code.claude.com/docs/en/agent-sdk/overview.md)). Relevant to the plan's "Claude Code with subscription login in the sandbox" (ticket #32).

### 3.2 Codex CLI (OpenAI, Apache-2.0, Rust)

- **Architecture.** Rust workspace; the interactive TUI, `codex exec`, IDE and desktop app are clients of one core. The **app-server** is a JSON-RPC protocol for embedding Codex (threads, turns, approvals, history, model/config management) ([app-server README](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md), [SDK page](https://learn.chatgpt.com/docs/codex-sdk.md)). `codex mcp-server` has been removed in favor of app-server ([same](https://learn.chatgpt.com/docs/codex-sdk.md)).
- **Permissions: two orthogonal layers.** Sandbox mode (`read-only`, `workspace-write`, `danger-full-access`) says what a command can touch; approval policy (`on-request`, `never`, or a `granular` table) says when to stop and ask. `untrusted` was retired ([approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md)). Network is off by default; an optional `network_proxy` feature enforces domain `allow`/`deny` (deny wins, `*.x` vs `**.x`, loopback/private blocked, best-effort DNS-rebinding check) ([same](https://learn.chatgpt.com/docs/agent-approvals-security.md)). `.git` stays read-only inside writable roots ([same](https://learn.chatgpt.com/docs/agent-approvals-security.md)). Destructive MCP tool calls always need approval when the tool advertises a destructive annotation ([same](https://learn.chatgpt.com/docs/agent-approvals-security.md)).
- **Rules.** Starlark `prefix_rule(pattern, decision, justification, match, not_match)` in `rules/*.rules`; decisions `allow`/`prompt`/`forbidden`, the most restrictive wins; `match`/`not_match` are inline unit tests validated at load ([rules](https://learn.chatgpt.com/docs/agent-configuration/rules.md)).
- **Auto-review.** A reviewer agent replaces manual approval at the sandbox boundary only; the main agent still runs inside the same sandbox ([auto-review](https://learn.chatgpt.com/docs/sandboxing/auto-review.md)). `approvals_reviewer = auto_review` ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md)).
- **Cloud two-phase model.** Legacy Codex Cloud runs setup with network, then the agent phase offline by default; **secrets are available only during setup and removed before the agent phase** ([approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md)). This is the plan's "secrets marked setup-only or runtime" in a shipped product.
- **Context.** `model_auto_compact_token_limit`, `model_auto_compact_token_limit_scope`, `compact_prompt` override ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md)). Algorithm internals not read.
- **Subagents.** Spawned on request or by `AGENTS.md`/skill instruction; inherit the parent sandbox policy; custom agents set `model` and `model_reasoning_effort`; docs warn that parallel *writes* conflict and recommend parallel agents for read-heavy work ([subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents.md)).
- **Skills / memory / instructions.** Skills use progressive disclosure; the initial skill list is budgeted at 2% of context (or 8,000 chars); `agents/openai.yaml` sets `allow_implicit_invocation` and tool dependencies ([skills](https://learn.chatgpt.com/docs/build-skills.md)). `AGENTS.md` discovered global then root-to-cwd, one file per directory, `AGENTS.override.md`, 32 KiB combined cap ([AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md.md)). Memories exist ([memories](https://learn.chatgpt.com/docs/customization/memories.md)).
- **MCP and hooks.** `[mcp_servers.<id>]` TOML (`command`, `args`, `env`, `env_vars`, `cwd`, URL); `required = true` makes `codex exec` fail if the server cannot start; hooks on `PreToolUse`, `PermissionRequest`, `PreCompact`/`PostCompact`, `SubagentStart/Stop`… ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md), [non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md)).
- **Headless.** `codex exec` streams progress to stderr and prints only the final message to stdout; `--json` yields JSONL events (`thread.started`, `turn.*`, `item.*`); `--output-schema` forces a typed final answer; `exec resume`; read-only by default; `--ephemeral` ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md)). In CI, keep the API key out of any job env that runs repo-controlled code ([same](https://learn.chatgpt.com/docs/non-interactive-mode.md)).

### 3.3 pi (earendil-works/pi, formerly badlogic/pi-mono; MIT; v1.3.0)

- **Philosophy.** "Minimal, extensible agent harness"; ships "powerful defaults but skips features like sub-agents and plan mode" ([README](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md)).
- **Loop and sessions.** Turn = assistant response plus executed tool calls. *Steering* messages enter after the current assistant turn; *follow-up* messages after all pending work. Sessions are JSONL **trees**: continuing from an earlier entry creates a branch in the same file; compaction inserts a summary entry while originals stay in the tree ([how pi works](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/how-pi-works.md)).
- **Tools.** Defaults `read`, `bash`, `edit`, `write`; optional `grep`, `find`, `ls`; built-in extensions add `codemode` and `tool_search`; `--tools` allow-lists, `--exclude-tools` removes by pattern ([CLI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md)).
- **Codemode.** The model writes a JavaScript script run in a **QuickJS sandbox with no Node APIs, filesystem, network or timers**; it calls other tools via `tools.<name>(args)`, runs them in parallel, and only the script's output reaches the model; `store`/`load` persist small JSON across calls; `models.classify()` runs non-LLM classifier models; `searchTools()`/`describeTool()` find tools on demand ([codemode](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/codemode.md)).
- **Permissions: none by design.** "Does not ask for approval before every tool call"; tools run with the OS permissions of the Pi process; extensions can intercept `tool_call` events to gate; the supported answer is to run the whole thing in a container/VM ([security](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md), [containerization](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/containerization.md)). **Project trust** gates loading `.pi/` settings, MCP config, extensions, skills and system-prompt files from a folder; `AGENTS.md`/`CLAUDE.md` load regardless; headless modes default to skipping protected project resources ([security](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)). Containerization options include Docker Sandboxes where "provider credentials remain on the host and are substituted by the proxy" and a Gondolin micro-VM extension for tool execution ([containerization](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/containerization.md)).
- **Compaction.** Triggers when `contextTokens > contextWindow − reserveTokens` (default 16,384); keeps about 20k recent tokens; checked between turns after tool results; one compact-and-retry on provider overflow; separate branch summarization on `/tree` navigation; summary requests disable prompt-cache writes ([compaction](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md)).
- **MCP.** stdio and streamable HTTP; per-server `exposure` = `codemode` (default), `deferred`, `direct`, `hidden`, overridable per tool with globs; OAuth with dynamic client registration or CIMD and pre-registered clients; credentials stored per server name+URL; `mcp__server__tool` naming; results over 20 KB truncated in the middle with the full text saved to a temp file ([MCP](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)). Every MCP call passes through the normal tool pipeline so permission gates apply ([same](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)).
- **Skills.** Agent Skills spec; name+description in the prompt, body loaded on demand ([skills](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md)).
- **Headless.** Print mode, JSON event stream, **RPC mode** (JSONL commands on stdin, responses and events on stdout, extension UI records forwarded), in-process TypeScript SDK ([RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md), [SDK](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/sdk.md)).

### 3.4 Hermes Agent (Nous Research, MIT, Python; v2026.9.24)

Closest to atrium's "secretary + coding" aim: it is a personal agent with a messaging gateway, scheduler, memory and a coding toolset in one process ([README](https://github.com/NousResearch/hermes-agent)).

- **Architecture.** Entry points (CLI, gateway, ACP, API server, batch) share one `AIAgent`; prompt builder, provider resolution, tool dispatch, compression and caching, SQLite+FTS5 sessions, a tool registry of 70+ tools in 28 toolsets, seven terminal backends ([architecture](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/architecture.md)). The gateway reaches Telegram, Discord, Slack, WhatsApp, Signal, email, ntfy and more ([docs index](https://hermes-agent.nousresearch.com/docs/llms.txt)).
- **Approvals.** `approvals.mode`: `smart` (an auxiliary LLM auto-approves low-risk and auto-denies dangerous, escalating the unsure), `manual`, `off`. Separate **unattended modes** (`cron_mode`, `single_query_mode`, `unattended_mode`) default to `deny`, so a cron job that hits a dangerous command must find another path. API-server clients that can answer an approval card get it via `POST /v1/runs/{id}/approval`. Always-on hardline blocklist; user-defined `approvals.deny`; a permanent allowlist; `hermes approvals suggest` mines history for rules ([security](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/security.md)).
- **Eight security layers** listed in one page: user authorization (allowlists, DM pairing), command approval, file-write denylist plus optional write root, container isolation, MCP credential filtering, context-file injection scanning, cross-session isolation, input sanitization ([security](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/security.md)).
- **Egress credential-injection proxy.** In Docker mode the sandbox holds opaque proxy tokens; a host-side [iron-proxy](https://github.com/ironsh/iron-proxy) (Apache-2.0, Go) terminates TLS with a local CA and swaps tokens for real credentials, with an upstream host allowlist. Only the Docker backend is wired ([iron-proxy doc](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/egress/iron-proxy.md)). Directly relevant to tickets #25/#58.
- **Memory.** `MEMORY.md` (2,200 chars) and `USER.md` (1,375 chars), bounded; injected as a **frozen snapshot at session start** to preserve the prefix cache; the memory tool errors when full and the agent must consolidate; separate session search; external memory providers ([memory](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/memory.md)).
- **Context.** Pluggable `ContextEngine`; default compressor triggers at 50% of the window (configurable) with a gateway "hygiene" pass at 85%; uses the provider's last reported usage as the anchor; a failed summary arms a cooldown and falls back to a deterministic summary rather than dropping the session ([context compression](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/developer-guide/context-compression-and-caching.md)).
- **Subagents.** `delegate_task` spawns children with isolated context and their own terminals; only the final summary returns; up to 10 concurrent by default; background completion delivery with at-least-once retry; child background processes die with the child ([delegation](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/delegation.md)).
- **Programmatic tool calling.** `execute_code`: the model writes Python that imports a generated `hermes_tools` stub; calls travel over a Unix socket back to Hermes; only `print()` output reaches the model; usable tools are web, file, search, patch, foreground terminal ([code execution](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/code-execution.md)).
- **Other features worth noting.** Skills (agentskills.io) that the agent authors and a Curator maintains ([skills](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/skills.md)); opt-in shadow-git checkpoints and `/rollback` ([checkpoints](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/checkpoints-and-rollback.md)); `/goal` judge loop (a small model checks after each turn whether the goal is met, else continues) credited to Codex's `/goal` ([goals](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/goals.md)); tool search for large MCP/plugin tool sets ([tool search](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/tool-search.md)); Kanban multi-agent board and cron with delivery to any platform ([docs index](https://hermes-agent.nousresearch.com/docs/llms.txt)); managed scope for admin-pinned, user-immutable config ([docs index](https://hermes-agent.nousresearch.com/docs/llms.txt)).
- **ntfy as a channel.** Uses a topic as the identity: "anyone who knows the topic can talk to the agent"; secure with self-hosted ACLs, a token, or an unguessable topic ([ntfy page](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/messaging/ntfy.md)). Input for ticket #47.

### 3.5 opencode (anomalyco/opencode, open source; v1.19.3, v2 line exists)

- **Architecture.** TUI is a client of a local HTTP server; the server publishes an **OpenAPI 3.1** spec and the SDK is generated from it; `opencode serve` runs headless, optional HTTP basic auth via `OPENCODE_SERVER_PASSWORD`, SSE event stream, session CRUD, fork, revert, summarize, abort, **respond to a permission request** (`POST /session/:id/permissions/:permissionID`), `prompt_async`, dynamic MCP add ([server](https://opencode.ai/docs/server/)).
- **Permissions.** Each rule resolves to `allow`/`ask`/`deny`; object syntax with glob patterns per tool (bash matches parsed commands); **last matching rule wins** (opposite of Claude Code's deny-first); defaults mostly `allow`, with `external_directory` and `doom_loop` (same call repeated 3 times) defaulting to `ask`; `.env` reads denied by default; `--auto` approves everything not explicitly denied ([permissions](https://opencode.ai/docs/permissions/)).
- **Agents.** Primary agents Build (all tools) and Plan (edits and bash default to `ask`); subagents General, Explore (read-only), Scout (external docs/dependencies); hidden system agents compaction, title, summary; agents are markdown or JSON with their own model, prompt, `steps` cap and permissions; `subagent_depth` defaults to 1 ([agents](https://opencode.ai/docs/agents/), [config](https://opencode.ai/docs/config/)).
- **Context and undo.** Compaction runs as a hidden agent and "leaves enough window to avoid overflow during compaction"; snapshots use an internal git repo and can be disabled for large repos ([config](https://opencode.ai/docs/config/)).
- **Policy.** Managed settings in `/etc/opencode/` outrank user config ([config](https://opencode.ai/docs/config/)). MCP supports local and remote servers with OAuth (automatic dynamic registration or pre-registered) ([MCP servers](https://opencode.ai/docs/mcp-servers/)). Agent Skills, plugins, rules (`AGENTS.md`, `/init` generates it) ([intro](https://opencode.ai/docs/)).

### 3.6 OpenHands (All-Hands AI; Software Agent SDK v1.55.1)

- **Architecture.** A layered Python SDK (`openhands.sdk`, `.tools`, `.workspace`, `.agent_server`). **Agent Server** exposes conversations and workspaces over REST and WebSocket; clients (Agent Canvas, CLI, custom) sit on top. Sandbox providers: Docker (recommended), process (unsafe), remote ([SDK architecture](https://docs.openhands.dev/sdk/arch/overview.md), [sandboxes](https://docs.openhands.dev/openhands/usage/sandboxes/overview.md)). This is the nearest published analogue to "atrium + separate sandbox manager reachable remotely".
- **Tools.** Typed `Action`/`Observation` pairs with `ToolAnnotations` carrying MCP-style hints (read-only, destructive, idempotent, open-world); built-ins include `execute_bash`, `file_editor`, `glob`, `think`, `finish` ([tool system](https://docs.openhands.dev/sdk/arch/tool-system.md)). Parallel tool execution within one response ([guide](https://docs.openhands.dev/sdk/guides/parallel-tool-execution.md)).
- **Confirmation.** `ConfirmationPolicy` (`AlwaysConfirm`, `NeverConfirm`, `ConfirmRisky`) plus a `SecurityAnalyzer` that rates each action LOW/MEDIUM/HIGH; the conversation enters `WAITING_FOR_CONFIRMATION` and can be rejected programmatically ([security](https://docs.openhands.dev/sdk/guides/security.md)).
- **Secrets.** A secret registry detects secret references in bash commands, injects them as env vars only when needed, and **masks secret values in output**; sources can be callables for external vaults ([secrets](https://docs.openhands.dev/sdk/guides/secrets.md)).
- **Context.** `LLMSummarizingCondenser` keeps recent messages, preserves goals and key files, summarizes older content ([condenser](https://docs.openhands.dev/sdk/guides/context-condenser.md)).
- **Subagents / skills / memory.** `TaskToolSet`: synchronous, resumable by task ID, "designed for sequential blocking tasks" ([task tool set](https://docs.openhands.dev/sdk/guides/task-tool-set.md)). Skills: repo, keyword-triggered, and **path-triggered rules** injected deterministically when the agent touches matching files ([skills overview](https://docs.openhands.dev/overview/skills.md)). Persistent memory is opt-in markdown loaded into the system prompt ([memory](https://docs.openhands.dev/sdk/guides/persistent-memory.md)). Plugins bundle skills, hooks, MCP servers, agents, commands ([plugins](https://docs.openhands.dev/overview/plugins.md)).

### 3.7 Aider (Aider-AI; last PyPI release 0.86.2, 2026-02-12)

- **Architecture.** A chat that emits *edits as text* in a chosen format (`whole`, `diff`, `diff-fenced`, `udiff`, editor variants) rather than calling file tools ([edit formats](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/more/edit-formats.md)). The distinctive piece is the **repo map**: a concise map of files and key symbols ranked with a graph algorithm and sent with every request ([repo map](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/repomap.md)).
- **Modes.** `code`, `ask`, `architect` (architect model proposes, editor model applies), `help` ([chat modes](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/usage/modes.md)).
- **Safety = git.** Every edit is committed with a descriptive message, dirty files are committed first, `/undo` reverts ([git](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/git.md)). Scripting via `--message`/`--yes` ([scripting](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/scripting.md)). No sandbox, no subagents, no skills. The options reference contains no MCP option (0 matches in [options.md](https://github.com/Aider-AI/aider/blob/main/aider/website/docs/config/options.md)).
- **Relevance.** Cadence has slowed (last release Feb 2026). Worth copying: the repo map as a context-saving *harness* feature, and one-commit-per-edit undo. Not a model for the rest.

### 3.8 Goose (aaif-goose/goose, formerly block/goose; MIT, Rust)

- **Architecture.** Interface, agent, and **extensions that are MCP servers**; the interface can run several agents; goose can act as an **ACP (Agent Client Protocol) server** and can use ACP agents as providers ([architecture](https://github.com/aaif-goose/goose/blob/main/documentation/docs/goose-architecture/goose-architecture.md)). `goose serve` runs a remote ACP server with TLS and a shared secret key, plus `--enable-scheduler` ([remote server](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/remote-goose-server.md)).
- **Permissions.** Modes: Completely Autonomous (**default**), Manual, Smart (risk-based), Chat Only; per-tool permissions on top ([permission modes](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/managing-tools/goose-permissions.md)). **Adversary mode**: an independent reviewer sees the original task, recent messages and each tool call, returns ALLOW/BLOCK from a user-written rules file, and **fails open** on reviewer error ([adversary mode](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/security/adversary-mode.md)). Prompt-injection detection is a separate feature with a self-hostable classification API ([security](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/security/index.mdx)).
- **Code Mode.** An MCP extension that exposes three meta-tools (list functions, get details, run); the model writes JavaScript executed in a fresh Boa interpreter that chains tool calls; text results only ([code mode](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/managing-tools/code-mode.md)). The docs recommend it for 5+ extensions.
- **Context.** Auto-compaction at 80% of the window or 225,000 tokens, whichever is lower, with a customizable `compaction.md` template; a second tier of "context strategies" runs if the limit is still exceeded ([context management](https://goose-docs.ai/docs/guides/sessions/smart-context-management.md)).
- **Delegation and reuse.** Subagents (parallel or sequential, limited extension access), **recipes** (reusable task templates, with sub-recipes), skills, hooks per the Open Plugins spec, built-in Memory, Todo and Chat Recall extensions ([subagents](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/subagents.mdx), [hooks](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/hooks.md), [llms.txt](https://goose-docs.ai/llms.txt)).

### 3.9 Cursor agents (hosted)

- **Run modes (local).** Auto-review (allow-listed calls run; other shell commands run in a sandbox when possible; the rest go to a classifier on Cursor's backend, which can read files read-only to judge a call), Allowlist, Run Everything. "Auto-review is not a security boundary"; natural-language rules in `permissions.json` ([run modes](https://cursor.com/docs/agent/security/run-modes.md)).
- **Cloud agents.** Isolated VMs with cloned repos, installed dependencies, secrets and startup commands; many in parallel; multi-repo environments; started from desktop, web, mobile, Slack, GitHub/Bitbucket comments, Linear, API ([cloud agents](https://cursor.com/docs/cloud-agent.md)). Lifecycle: provision, run, persist, hand off by **pushing a branch and opening a draft PR for human review**, then hibernate and delete on timers ([security overview](https://cursor.com/docs/cloud-agent/security.md)). Access is inherited, never widened: an agent can only reach repos the triggering user could ([same](https://cursor.com/docs/cloud-agent/security.md)).
- **Secrets and network.** Secret types: *Environment Variable* (visible to the agent), *Runtime Secret* (loaded as env var but **redacted** from tool results, transcript, commits; still visible in the terminal), *Build Secret* (only available during Docker build, never in the running agent). Prefer short-lived OIDC JWTs over long-lived keys. All commits signed with an HSM-backed Ed25519 key. Network modes: allow all, default + allowlist, allowlist only, inherit; settable per user, team and saved environment; environment snapshots expire after 30 days idle ([secrets & network](https://cursor.com/docs/cloud-agent/security-network.md)). Docs concede that auto-running terminal commands "introduces data exfiltration risk" via prompt injection ([same](https://cursor.com/docs/cloud-agent/security-network.md)).
- **Other.** Subagents with their own context, foreground or background, custom prompts/tools/models ([subagents](https://cursor.com/docs/subagents.md)); hooks, rules, skills, MCP ([index](https://cursor.com/docs/llms.txt)); headless `agent -p` with `--force` to apply edits ([headless](https://cursor.com/docs/cli/headless.md)); a self-hosted worker pool option ([self-hosted](https://cursor.com/docs/cloud-agent/self-hosted.md)).

### 3.10 Amp (hosted; Sourcegraph spin-out [inference, not re-verified])

- **Modes, not models.** The Dial: `low`, `medium`, `high`, `ultra`, each a model + reasoning effort + system prompt + tools + oracle ([models & subagents](https://ampcode.com/docs/markdown/models-and-subagents), [the dial](https://ampcode.com/docs/markdown/the-dial)).
- **Subagents.** Search, **Oracle** (stronger second-opinion model exposed as a tool the main agent may call), Librarian (external code research), Task (delegated coding + verification), Read Thread; each has its own model and context ([models & subagents](https://ampcode.com/docs/markdown/models-and-subagents), [tools](https://ampcode.com/docs/markdown/tools)).
- **Permissions.** "By default, Amp does not ask for approval before running tools"; the answer for untrusted inputs is a custom **policy plugin** or an isolated environment; plugins hook `tool.call`, `tool.result`, agent lifecycle events and can call `amp.ai.ask/decide` ([tools](https://ampcode.com/docs/markdown/tools), [plugins](https://ampcode.com/docs/markdown/customize/plugins)).
- **Orbs** (the cloud half). Each orb thread gets a fresh isolated remote machine with your code and tools; it pauses a few minutes after idle and a sleeping orb costs nothing; spawn pacing (burst of 20, then one per five minutes) ([orbs](https://ampcode.com/docs/markdown/orbs)). Agents can start other agents in new orbs and exchange files and results ([agent to agent](https://ampcode.com/docs/markdown/orbs/agent-to-agent)). The "Ship" button sends a configurable prompt (default: commit, rebase, test, push to base; teams can pick other behaviors) ([shipping](https://ampcode.com/docs/markdown/orbs/shipping)).
- **Secrets.** Env var vs secret; precedence personal > project > workspace; secrets hidden after saving and **every occurrence masked in shell output** (short/common values warned against); OIDC identity preferred; agent asks for a known secret in a private dialog "so the value never appears in the thread"; change history without values ([handling secrets](https://ampcode.com/docs/markdown/orbs/handling-secrets)). GitHub App connection; pushes and `gh` run as the user; commits signed ([GitHub](https://ampcode.com/docs/markdown/github)).
- **MCP and skills.** Recommends bundling MCP servers inside skills (`mcp.json`) so tools load only when the skill is used ([MCP](https://ampcode.com/docs/markdown/customize/mcp)).
- **Headless.** `amp -x` execute mode (final message only), `-ox` to run in an orb, streaming JSON, TypeScript and Python SDKs ([execute mode](https://ampcode.com/docs/markdown/cli/execute-mode), [index](https://ampcode.com/llms.txt)).

---

## 4. Cross-cutting patterns (what the survey shows)

### 4.1 Permission model

| Pattern | Who | Notes for atrium |
|---|---|---|
| Per-tool `allow`/`ask`/`deny` with patterns | CC, opencode, Codex, Cursor, Goose | Precedence differs: CC deny, ask, allow; Codex most restrictive wins; opencode **last match wins** (easy to get wrong). Pick most-restrictive-wins. |
| Rule unit tests | Codex `match`/`not_match` | Cheap, catches rule typos at load. |
| Modes | CC (`plan`, `acceptEdits`, `auto`, `dontAsk`), Goose, Cursor, Aider (`ask`/`architect`), opencode (Plan agent) | A read-only "plan" mode is universal and nearly free in atrium (drop write tools from the run kind). |
| LLM approver/classifier | CC `auto`, Codex auto-review, Cursor Auto-review, Hermes `smart`, Goose smart/adversary, OpenHands `LLMSecurityAnalyzer` | All explicitly "not a boundary". Treat as noise reduction, not safety. |
| Unattended behavior | Hermes: `cron_mode`/`unattended_mode` default `deny`; CC `dontAsk`; Codex `exec` read-only | Atrium's scheduled runs "only read, notify and create proposals" (`AGENTS.md:93-95`), i.e. the same idea implemented as proposals rather than denials. Keep. |
| Doom-loop guard | opencode `doom_loop` ask after 3 identical calls; per-agent `steps` cap | Atrium has child step/cost caps (`subagents.py:8-10`); a chat-loop identical-call detector is new. |
| Protected paths | CC `.git`/`.claude`; Codex `.git` read-only | Matters because git hooks execute code ([CC](https://code.claude.com/docs/en/permissions.md)). |

**Reading of this table [inference].** Two different problems get conflated in these products. (a) *Containment*: what a rogue shell can reach. Solved only by OS/VM sandbox + network proxy. (b) *Authority*: which side effects (send mail, push to main, spend money) the agent may cause without a human. Solved by typed tools with explicit effect classes and a human step. Atrium should keep these separate: the microVM answers (a) for bash; the run-kind tool registry answers (b) for the secretary tools.

### 4.2 Context management

All harnesses converge on: (1) cheap first step (clear/stub old tool outputs), (2) LLM summary near a threshold, (3) a hard-overflow recovery path.

- Thresholds: pi window − 16,384 and ~20k recent kept; Goose 80% or 225k; Hermes 50% agent / 85% gateway; Atrium clear from 18.75%, server compaction at 87.5%, client preflight at 95% (`src/atrium/agent/context.py:14-33`), with stubs recalled by `recall(result_id, …)` (`context.py:11`).
- Failure handling worth copying: Hermes cooldown + deterministic fallback summary; Claude Code stops after repeated thrash; pi retries once after overflow.
- Prompt-cache hygiene: Hermes freezes memory at session start; pi appends new MCP-server notices to the conversation instead of changing tool declarations; CC defers MCP schemas. **Rule: never mutate the tool list or system prompt mid-thread** (applies directly to the new-chat "pick environment/model at start" design, #43).
- Per-run-kind summary prompts: Codex `compact_prompt`, Goose `compaction.md`, CC "Compact Instructions".

### 4.3 Subagents

- Universal contract: own context window, narrow tool allowlist, model per subagent, summary returned (CC, Codex, opencode, Hermes, Cursor, Amp, Goose, OpenHands). Atrium's is the same, with hard caps (`CHILD_MAX_COST_USD = 0.25`, `CHILD_MAX_STEPS = 12`, `MAX_CONCURRENT = 2`, `src/atrium/agent/subagents.py:8-10`) and a read-only pool (`registry.py:30-32`).
- Differences: depth limit (opencode default 1); sync vs background (OpenHands `TaskToolSet` sync; Hermes background-by-default with delivery); isolation for *writers* (CC `isolation: worktree`; Codex warns parallel writes conflict; Amp starts a new orb with its own working copy and does not share uncommitted files).
- pi deliberately has none; build it as a package. Amp's **Oracle** (strong second-opinion model as a tool) is a distinct cost-control pattern.

### 4.4 Skills, memory, instruction files

- `SKILL.md` + progressive disclosure is shared by Codex, pi, Hermes, Amp, Claude Code, OpenHands, opencode. Codex budgets the always-loaded index (2% of context). OpenHands adds path-triggered rules injected deterministically; CC subagent description budget 15k tokens.
- `AGENTS.md` is read by CC, Codex (hierarchical, 32 KiB cap), pi, opencode (`/init`), Goose, Amp, Hermes.
- Memory: Hermes bounded and curated (2,200 + 1,375 chars) and frozen per session; CC auto memory (200 lines/25KB); OpenHands opt-in markdown. Atrium already has a profile directory and a nightly run that writes the profile with undo (`AGENTS.md:93-95`) and a `recall` tool.

### 4.5 MCP

Everyone except Aider. Patterns to copy: deferred schemas with a search tool (CC `ToolSearch`, pi `tool_search`, Hermes tool search); per-server exposure modes `direct`/`deferred`/`codemode`/`hidden` (pi); anchored allow-globs and deny-by-pattern (CC); required servers fail loudly in headless mode (Codex `required = true`); credential filtering for MCP subprocess env (Hermes); OAuth with dynamic registration, CIMD, or a pre-registered client (pi, opencode); result truncation to a file (pi). Atrium has no MCP client today (`grep -ri mcp src nix docs AGENTS.md` returns nothing). The Grafana tool is a bespoke HTTP client (`src/atrium/agent/tools/grafana.py`).

### 4.6 Code mode / programmatic tool calling

| | Language / engine | Reaches | Persists | Notes |
|---|---|---|---|---|
| pi `codemode` | JS in QuickJS, no fs/network/timers | all pi tools incl. MCP and `bash`, classifier models | `store`/`load` JSON | Output-only to the model, parallel calls ([docs](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/codemode.md)) |
| Hermes `execute_code` | Python child process + UDS RPC | web, file, search, patch, terminal | no | On the agent host, approval flow applies to scripts ([docs](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/code-execution.md)) |
| Goose Code Mode | JS in Boa, fresh per request | any MCP extension tools via 3 meta-tools | no | Text results only ([docs](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/managing-tools/code-mode.md)) |
| Atrium today | Monty (Python subset) in socket-activated, no-network, read-only systemd unit | read-only tools | no | `AGENTS.md:97-100`; read-only (`AGENTS.md:95`) |

[inference] Atrium's Monty design is the same idea as these three. The plan removes it in favor of the microVM, but the microVM is a heavyweight place to run "call 5 harness tools and filter" (boot/resume latency, per-thread state, remote link). A Go-embedded interpreter (the plan says Go; pi/Goose show JS engines are what others use) calling the same registry would keep the "offload to harness tools" benefit for secretary tasks that never need a sandbox. See C1.

### 4.7 Sandbox environment model (what cloud-agent products give users)

| Concern | Cursor cloud | Amp orbs | Codex Cloud | Hermes | Plan's current words |
|---|---|---|---|---|---|
| Secret types | env var / runtime (redacted) / build-only | env var / secret (masked in output) | setup-only (removed before agent phase) | credential-injecting proxy tokens (Docker) | "setup-only or runtime" |
| Output redaction | `[REDACTED]` in results, transcript, commits | all occurrences masked in shell output | n/a | credential filtering | not specified (C3) |
| Short-lived creds | OIDC JWT via local socket | OIDC `amp orb id-token` | n/a | n/a | not specified (C4) |
| Network | allow all / default+allowlist / allowlist only / inherit; per env | n/a in pages read | off in agent phase unless enabled | iron-proxy host allowlist | "egress domain allowlist enforced by a proxy" |
| Idle lifecycle | hibernate then delete; snapshots 30 days | pause minutes after idle; free while asleep | n/a | Daytona/Modal hibernate | "suspended when idle, deleted with thread" |
| Git | draft PR; signed commits (HSM Ed25519); access = triggering user's | App connection; signed | n/a | n/a | "push non-default branches and open draft PRs; token never enters VM" |
| Precedence of settings | user / team / environment | personal > project > workspace | n/a | profiles | environment-level only (C5) |

The plan's design already matches this closely. The survey adds: output redaction, OIDC, signed commits and settings precedence, and one **warning**: Cursor's "runtime secret" is still an env var visible in the terminal and to any code the agent runs, so it is not a credential-injection model; the plan's "token never enters the VM" is strictly stronger and should not be softened to match.

### 4.8 Headless and embedding

| Harness | Drive it from outside | Notes |
|---|---|---|
| pi | `--mode rpc` (JSONL stdio), print, JSON stream, TS SDK | Language-independent, process-isolated ([RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md)) |
| Claude Code | `claude -p --output-format json/stream`, Agent SDK | Auth: API key only for third parties ([Agent SDK](https://code.claude.com/docs/en/agent-sdk/overview.md)) |
| Codex | `codex exec --json`, TS SDK, app-server JSON-RPC | `--output-schema` typed final answer ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md)) |
| opencode | `opencode serve` OpenAPI | Permission replies over HTTP ([server](https://opencode.ai/docs/server/)) |
| Goose | `goose serve` / `goose acp` (ACP) | TLS + shared secret ([remote server](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/remote-goose-server.md)) |
| Hermes | CLI `-q`, API server (`/v1/runs`), ACP adapter | Approvals over `POST /v1/runs/{id}/approval` |
| OpenHands | agent server REST + WebSocket | Remote workspaces |
| Amp / Cursor | `amp -x`, `agent -p` | Hosted |

[inference] pi RPC, Codex app-server, opencode HTTP, Goose/Hermes ACP and OpenHands agent server are all "agent as a subprocess/service with a JSON event stream and an approval callback". The sandbox manager's coding-agent adapter should expose one normalized event stream and approval callback so atrium is not coupled to one agent. See C6 and ticket #46.

### 4.9 Other recurring features

- **Checkpoints/undo:** CC file snapshots; opencode internal git snapshots (disable for large repos); Hermes opt-in shadow git; Aider commit-per-edit + `/undo`; Codex none seen in pages read.
- **Plan-then-build:** CC plan mode, opencode Plan agent, Aider `architect`/`ask`, Cursor plan mode.
- **Standing goal with a judge:** Codex `/goal` and Hermes `/goal`.
- **Steering while running:** pi steering vs follow-up queue; CC queued messages read after the current tool calls.
- **Hooks:** CC, Codex, Goose, Hermes, Cursor, Amp plugin events. Shell hooks execute local commands with the user's rights ([Goose warning](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/hooks.md)); plugin subagents in CC cannot declare hooks.
- **Managed/pinned config:** opencode managed settings, Hermes managed scope, Codex `requirements.toml`, CC managed settings. Equivalent to the plan's "env pins a setting, otherwise UI-editable".

---

## 5. What atrium should copy

Grouped by plan phase. "Plan fit" cites the decision it serves.

### 5.1 Decide before the Go port (cheap now, costly later)

1. **Make `Tool` richer than `effect`.** Today: `name, description, input_schema, handler, effect (read|write), contexts` (`src/atrium/agent/tools/base.py:32-39`). Add MCP-style annotations (read-only, destructive, idempotent, open-world, as in OpenHands [tool system](https://docs.openhands.dev/sdk/arch/tool-system.md)) and a `requires` tag for module/credential. The Go interface is where this is free. Supports #42 (tool catalog and run-kind policy), #39 (module toggles: a disabled module registers no tools).
2. **Run-kind policy as data, not code.** Keep `tools_for(kind)` semantics (`registry.py:9-11`), but express them as a table that can later hold `allow|ask|deny` and be unit-tested like Codex's `match`/`not_match`. Most-restrictive-wins.
3. **Keep the journal and thread model open to branching.** pi (tree), CC (`--fork-session`), opencode (`/fork`) all let the user branch from a message; atrium threads are linear today (`src/atrium/agent/journal.py`, `threads.py`). Don't bake a linear-only schema into the port if parity allows an optional `parent_message_id`.
4. **MCP-ready tool naming and exposure.** Reserve `mcp__<server>__<tool>` and an exposure enum (`direct|deferred|hidden`) in the Go registry even if MCP lands in Phase B.

### 5.2 Phase B: secretary side

5. **Unattended approvals become proposals** (already atrium's pattern). Hermes's `deny` default for cron validates it; keep scheduled runs unable to prompt.
6. **Read-only plan mode** in the new-chat page: a run kind that drops write tools (CC `plan`, opencode Plan, Aider `ask`). Near-zero cost (#43).
7. **Compaction failure cooldown and deterministic fallback** (Hermes) on top of the existing three-tier scheme (`context.py:14-33`); per-run-kind summary instructions.
8. **Frozen-per-thread system prompt and tool list** (Hermes, pi) so prompt caching survives environment/skill/memory changes; show "applies to next thread".
9. **Skills in `SKILL.md` format** with import/export and an index budget (Codex 2%/8,000 chars); optional path-triggered rules for repo-bound environments (OpenHands). Keep storage in SQLite (`AGENTS.md:62-63`).
10. **Oracle tool** (Amp): `consult(question)` calls a stronger model (Opus 5.5) from a Haiku/Sonnet chat under the same budget cap, rather than making the user pick the expensive model for the whole thread.
11. **Mode dial vs raw model** (Amp): new-chat offers effort presets that map to model + effort + context size (#43), with raw model as an advanced option.
12. **Interrupt/steer queue** in chat (pi, CC): messages typed mid-run are queued and delivered after the current tool calls.

### 5.3 Phase B: coding side (sandbox, GitHub)

13. **Boundary = microVM + proxy; approvals only for effects outside it.** Do not build per-command approval prompts for sandbox bash (CC, Cursor, Goose all say they are not boundaries). Approvals exist for: push to protected branches, merges, deletes, anything the manager runs outside the VM (matches plan GitHub decision).
14. **Environment = Cursor/Amp model.** Secret kinds: `env` (visible), `runtime` (injected by proxy for allowed hosts only; never in VM env), `setup-only` (present during setup scripts only, removed before the agent phase; Codex Cloud). Settings precedence user > environment > instance; per-environment egress mode (inherit / allowlist only). Mask secret values in all tool output and transcripts (Amp, Cursor, OpenHands).
15. **Run git and `gh` in the manager, never in the VM**, with `.git` and hook directories unreachable from agent shell writes (CC protected paths; Codex `.git` read-only; CC's note that `cd`+`git` executes hooks). Supports plan "typed git/gh harness tools run by the manager".
16. **Branch + draft PR hand-off** (Cursor, Amp "Ship"): agent pushes a branch, opens a draft PR; the human merges. Add signed commits from a manager-held key (Cursor HSM Ed25519; Amp signs) so branch-protection rules requiring signatures pass.
17. **Subagent isolation for writers:** read-only subagents are free; a subagent that can write gets its own worktree/branch (CC `isolation: worktree`) or its own sandbox (Amp). Codex's advice: parallel agents for read-heavy work.
18. **Normalized coding-agent adapter** inside the sandbox: pi RPC first (#32); define atrium's own event/approval schema so Codex app-server, Claude Code stream-json, or ACP agents can slot in later.
19. **Checkpoint per agent turn** in the sandbox working tree (commit to a scratch branch / shadow ref; opencode, Hermes, Aider all do git-based undo). Exposes "undo last turn" in the thread UI.
20. **Doom-loop detector** on identical repeated tool calls (opencode) and a step cap on the main loop (opencode `steps`), reusing the child caps (`subagents.py:8-10`).

### 5.4 What not to copy

- Shell hooks that execute on the atrium host (CC/Codex/Goose/Hermes/Amp). Single-tenant but remote-reachable; user-supplied hook scripts are a host-RCE surface. Use a typed in-process `ToolGate` interface and the sandbox for user code. [inference]
- LLM approver as a safety control (Goose adversary mode is fail-open). Fine as an advisory signal on proposals, not as a gate.
- opencode's last-match-wins rules, Goose's autonomous-by-default, Pi/Amp "no prompts" defaults. They assume the user is the only party whose files are at risk; atrium reads mail and holds credentials.
- Aider's text-edit protocol and repo map as a primary design. Good idea for context savings; a coding agent in the sandbox handles it.
- Cursor "runtime secret" semantics (env var visible in the terminal). Weaker than the plan's proxy injection.
- Treating `AGENTS.md`/`CLAUDE.md` content as trusted. pi loads these even when it declines project trust ([security](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)); atrium already treats uploaded and project files as untrusted data (`AGENTS.md:101-105`), keep it.

---

## 6. Options and trade-offs on the open design choices

### 6.1 How should atrium run coding agents: build own vs drive an external agent

| Option | For | Against |
|---|---|---|
| A. Atrium agent calls sandbox tools (bash/read/edit) directly through the manager | One loop, one budget, one transcript; fewest moving parts; no extra auth | Atrium reimplements what pi/Codex/Claude Code do well (edit tools, repo reading, retry, compaction for code) |
| B. Atrium agent delegates to pi (or others) via RPC as a "coding subagent" | Matches plan; pi is minimal, MIT, RPC is JSONL; atrium sees events and can budget | Two loops, two compaction systems; double model cost; need a normalized adapter |
| C. Both: direct simple tools for small edits, delegate for hard work | Best token economics [inference] | Needs a rule for when to delegate; two paths to test |

Recommendation: C, in that order of build (A first, since typed tools are required anyway for #30; B per #32/#46).

### 6.2 Programmatic tool-calling layer (C1)

| Option | For | Against |
|---|---|---|
| Sandbox only (plan) | One execution story | VM latency; secretary tools must be reachable from VM; ties secretary features to Linux/KVM host |
| In-process Go interpreter calling the tool registry | No VM, instant; same policy table applies; matches pi/Goose/Hermes | Another interpreter to secure; engine choice (goja/QuickJS) and its sandbox properties need a spike |
| Sandbox with RPC back to atrium tools (Hermes style, VM instead of host) | One engine | Needs authenticated callback path from VM; breaks "token never enters VM" unless the callback is scoped per thread |

### 6.3 Where permission decisions live

| Option | For | Against |
|---|---|---|
| Per-run-kind allowlist only (today) | Simple, verifiable | No `ask` outcome; no way for chat to request an operator-approved write other than "propose" |
| Allow/ask/deny table keyed by run kind and tool/args | Matches CC/Codex; `ask` becomes a proposal row with web+ntfy approval | Rule language needs scoping; risk of rule creep |
| LLM approver | Fewer prompts | Not a boundary; extra spend; fail-open pitfalls |

Recommendation: table, with `ask` implemented as the existing proposals mechanism; no LLM approver.

---

## 7. Risks

1. **Prompt injection through mail/Notion/web reaching a coding sandbox.** All vendors concede it; Cursor documents exfiltration risk for auto-running cloud agents ([source](https://cursor.com/docs/cloud-agent/security-network.md)). Atrium reads attacker-controllable mail. [inference] A thread that has read untrusted content should not also hold exfil-capable tools or runtime secrets; make "taint" a run-kind property (C7).
2. **Subscription auth for Claude Code.** Third parties may not offer claude.ai login ([Agent SDK](https://code.claude.com/docs/en/agent-sdk/overview.md)). Plan item "Claude Code in sandbox with subscription login" needs a legal read of that rule (#32).
3. **Moving targets.** pi moved orgs (badlogic to earendil-works); opencode has a v1 and v2 line; Goose moved to aaif-goose; Codex removed `mcp-server` and retired `untrusted`; Aider has not released since Feb 2026. Pin versions and keep the adapter narrow.
4. **Two compaction systems and two budgets** when delegating to pi/Codex. Budget accounting must include delegated agent usage (#55).
5. **MCP as an exfil and supply-chain surface.** pi reports MCP config is protected by project trust; CC plugin subagents cannot carry MCP servers; Hermes filters credentials for MCP subprocesses. If atrium adds MCP, every server is host-side code or a remote URL with credentials; policy and credential filtering must exist at day one.
6. **Cloud-style hibernate/delete lifecycle bugs.** Cursor snapshots expire after 30 days; Amp spawn pacing exists to protect capacity. The plan's "one sandbox per thread, suspended when idle" needs limits (count, disk, age) in manager config.
7. **Frozen prompts vs live data.** Freezing memory/skills at thread start (for cache hits) means edits do not apply mid-thread; UI must say so.

---

## 8. Candidate new tickets (not in the plan yet)

Types follow the map: `research`, `design`, `prototype`, `build`. Phase is when the answer is needed.

| ID | Title | Type | Phase | Area | Question |
|---|---|---|---|---|---|
| C1 | In-process programmatic tool calling after Monty | design (spike) | A/B boundary | agent | Keep a no-VM "script calls harness tools, only output returns" layer in Go (QuickJS/goja/other), or route everything through the sandbox? Compare latency, security model, parity with Monty and the plan's "Code mode replaced by sandbox". |
| C2 | Tool annotation and policy table schema | design | A (interface) / B (rules) | agent | What fields does the Go `Tool` need (effect, destructive, idempotent, open-world, module, credential tags) and how are allow/ask/deny rules per run kind expressed, tested (inline match/not_match) and surfaced in the UI? Overlaps #42; decide before the port freezes the interface. |
| C3 | Secret masking in tool output and transcripts | design | B | sandbox | Where is masking applied (manager, proxy, atrium), how are short/common values handled (Amp warns), and are masked values also kept out of journal, usage and logs? |
| C4 | Short-lived workload identity (OIDC) for sandboxes | research | B | sandbox | Should atrium/manager mint OIDC JWTs for the VM (Cursor, Amp do) so cloud roles need no long-lived keys? Needs the manager as an issuer and a JWKS endpoint. Relates to #25/#58. |
| C5 | Settings precedence model for environments | design | B | settings | User/instance/environment/thread precedence for env vars, secrets and egress (Amp: personal > project > workspace; Cursor: user/team/environment). Interacts with config pinning (#37). |
| C6 | Normalized coding-agent adapter schema | design | B | sandbox | One event stream + approval callback schema over pi RPC, Codex app-server, Claude Code stream-json, ACP. Is ACP the right neutral protocol ([agentclientprotocol.com](https://agentclientprotocol.com/))? |
| C7 | Taint-aware run kinds | research | B | security | Can a thread that has read untrusted mail/web content be denied exfil-capable tools or runtime secrets automatically? What does this do to the chat/coding UX? |
| C8 | Per-turn checkpoints and "undo last turn" | design | B | sandbox/chat | Git-based scratch ref vs VM snapshot; interaction with thread suspend and the manager API (#41). |
| C9 | Thread branching/forking in the chat UI | design | B | chat | Linear journal today; pi/CC/opencode all branch. Schema impact on the port (add nullable parent now?) and UI. |
| C10 | Oracle (consult) tool and effort-preset dial | design | B | chat/agent | Offer a bounded "ask a stronger model" tool and presets that map to model+effort+context (Amp's dial) instead of raw model choice. Feeds #43/#44. |
| C11 | MCP client and server stance | research | B | agent | Should atrium consume MCP servers (exposure modes, OAuth from the UI, credential filtering) and/or expose its tools as an MCP server so external agents (pi/Claude Code in the sandbox) reach harness tools? None exists today. |
| C12 | SKILL.md import/export and index budget | build | B | agent | Round-trip atrium DB skills with Agent Skills format; enforce an always-loaded index budget; optional path-triggered rules for environments. |
| C13 | Signed commits from the manager | design | B | github/sandbox | Manager-held signing key (Ed25519), registration on GitHub, interaction with branch protections and with the plan's token handling (#45). |
| C14 | Doom-loop and step-cap policy for the main loop | build | B | agent | Identical-call detector, per-run-kind step caps, behavior on trip (stop, notify, propose). |
| C15 | Goal loop with judge for long coding tasks | research | B | agent | Codex and Hermes `/goal` use a small judge model to continue until done. Does atrium want it, with what budget guard and what stop conditions? |
| C16 | AGENTS.md ingestion for repo-bound environments | build | B | github/sandbox | Hierarchical discovery with size cap (Codex 32 KiB) for the atrium agent when it works on a repo; keep content untrusted; surface in the system prompt without breaking cache. |

---

## 9. Source index

- Claude Code: [overview](https://code.claude.com/docs/en/overview.md), [how it works](https://code.claude.com/docs/en/how-claude-code-works.md), [permissions](https://code.claude.com/docs/en/permissions.md), [permission modes](https://code.claude.com/docs/en/permission-modes.md), [sandboxing](https://code.claude.com/docs/en/sandboxing.md), [sub-agents](https://code.claude.com/docs/en/sub-agents.md), [tools](https://code.claude.com/docs/en/tools-reference.md), [hooks](https://code.claude.com/docs/en/hooks.md), [headless](https://code.claude.com/docs/en/headless.md), [Agent SDK](https://code.claude.com/docs/en/agent-sdk/overview.md).
- Codex: [repo](https://github.com/openai/codex), [app-server](https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md), [approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md), [rules](https://learn.chatgpt.com/docs/agent-configuration/rules.md), [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents.md), [skills](https://learn.chatgpt.com/docs/build-skills.md), [AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md.md), [non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md), [SDK](https://learn.chatgpt.com/docs/codex-sdk.md), [config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md).
- pi: [repo](https://github.com/earendil-works/pi) and [docs dir](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/docs).
- Hermes: [repo](https://github.com/NousResearch/hermes-agent), [docs index](https://hermes-agent.nousresearch.com/docs/llms.txt), [website/docs](https://github.com/NousResearch/hermes-agent/tree/main/website/docs).
- opencode: [docs](https://opencode.ai/docs/), [permissions](https://opencode.ai/docs/permissions/), [agents](https://opencode.ai/docs/agents/), [server](https://opencode.ai/docs/server/), [config](https://opencode.ai/docs/config/), [MCP](https://opencode.ai/docs/mcp-servers/).
- OpenHands: [docs index](https://docs.openhands.dev/llms.txt), [SDK architecture](https://docs.openhands.dev/sdk/arch/overview.md), [security](https://docs.openhands.dev/sdk/guides/security.md), [condenser](https://docs.openhands.dev/sdk/guides/context-condenser.md), [secrets](https://docs.openhands.dev/sdk/guides/secrets.md), [sandboxes](https://docs.openhands.dev/openhands/usage/sandboxes/overview.md).
- Aider: [docs source](https://github.com/Aider-AI/aider/tree/main/aider/website/docs), [PyPI](https://pypi.org/project/aider-chat/).
- Goose: [repo](https://github.com/aaif-goose/goose), [docs](https://goose-docs.ai/llms.txt).
- Cursor: [docs index](https://cursor.com/docs/llms.txt), [run modes](https://cursor.com/docs/agent/security/run-modes.md), [cloud agents](https://cursor.com/docs/cloud-agent.md), [cloud security](https://cursor.com/docs/cloud-agent/security.md), [secrets & network](https://cursor.com/docs/cloud-agent/security-network.md), [subagents](https://cursor.com/docs/subagents.md).
- Amp: [docs index](https://ampcode.com/llms.txt), [tools](https://ampcode.com/docs/markdown/tools), [orbs](https://ampcode.com/docs/markdown/orbs), [secrets](https://ampcode.com/docs/markdown/orbs/handling-secrets), [GitHub](https://ampcode.com/docs/markdown/github).
- Atrium: `AGENTS.md:87-106`, `src/atrium/agent/registry.py:9-42`, `src/atrium/agent/tools/base.py:32-39`, `src/atrium/agent/subagents.py:8-10`, `src/atrium/agent/context.py:11-33`, `src/atrium/agent/skills.py:7-8`.

## 10. Not verified

- Codex compaction algorithm and Cursor/Amp compaction (config keys only; no algorithm pages read).
- Amp's corporate origin (stated as inference).
- Whether Anthropic's API offers a server-side programmatic tool-calling feature that would overlap C1; check in ticket #53/#46 research.
- Exact Goose release (only rc tags seen).
