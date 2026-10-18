# pi and Claude Code inside the sandbox (ticket #32)

Facts as of 2026-10-17. Versions seen: pi `@earendil-works/pi-coding-agent` **1.3.0** (2026-10-15, [changelog](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/CHANGELOG.md), [npm](https://registry.npmjs.org/@earendil-works/pi-coding-agent/latest)); Claude Code **2.1.302** ([npm](https://registry.npmjs.org/@anthropic-ai/claude-code/latest)). nixpkgs (26.11.20260923, checked locally with `nix search`) ships `claude-code` 2.1.280 (unfree) and `pi-coding-agent` 0.87.1: both lag upstream. `[inference]` marks my own reasoning; everything else is cited.

## 1. Answer in brief

1. **Both can be driven headlessly from a Go process as a subprocess with line-delimited JSON.** pi: `pi --mode rpc` (bidirectional, long-lived: `prompt`, `steer`, `follow_up`, `abort`, `get_session_stats`) or `pi --mode json` (one-shot). Claude Code: `claude -p ... --output-format stream-json --verbose --include-partial-messages` (optionally `--input-format stream-json`). Neither has a Go SDK: pi's SDK is TypeScript (Node/Bun) and Claude's Agent SDK is Python/TypeScript; for other languages Anthropic says to run the CLI with `-p` ([pi RPC](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/rpc.md), [pi SDK](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/sdk.md), [Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview)).
2. **Anthropic terms are the decisive constraint, and they split by program.**
    - *Claude Code (unmodified binary, run in a hosted sandbox, signed in by the end user with their own subscription)* is explicitly allowed ([legal](https://code.claude.com/docs/en/legal-and-compliance)). `claude -p` and `claude setup-token` are documented for CI/scripts, which is the "explicitly permit" exception to the Consumer Terms' ban on "automated or non-human means" ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms), [authentication](https://code.claude.com/docs/en/authentication#generate-a-long-lived-token)).
    - *pi with a Claude subscription is not allowed in a product*: OAuth "is intended exclusively for ... ordinary use of Claude Code and other native Anthropic applications"; third-party developers may not "route requests through Free, Pro, or Max plan credentials", and "may not collect, store, or intermediate Claude.ai credentials or session tokens" ([legal](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)). pi itself warns that Anthropic third-party usage on subscription auth "draws from extra usage and is billed per token" ([pi 0.66.0 changelog](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/CHANGELOG.md)). So pi should use an Anthropic **API key**, another provider, or a local model, never the subscription.
3. **Credentials: the plan's "token never in the VM" rule can be kept for API keys, not cleanly for a Claude subscription.** For API keys, set a placeholder in the VM (`ANTHROPIC_API_KEY=placeholder`) and let the manager's TLS-terminating egress proxy swap in the real key on `api.anthropic.com` requests (the pattern in ticket #28's findings and in pi's own Docker Sandboxes docs: [pi containerization](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/containerization.md)). A Claude Code *subscription* credential must be readable by the `claude` process; options in section 5. I recommend a documented carve-out to the rule for that one credential.
4. **Budgeting differs by credential.** API-key runs have dollar costs reported by both tools (client-side estimates) that atrium can write into its `usage` table and cap; subscription runs have no dollar cost and are limited by plan windows (5-hour and weekly) that atrium can only observe (`rate_limit_event`), and that the atrium monthly budget does not cover.
5. **Invocation shape:** one new tool in the atrium catalog (working name `code_agent`) modelled on the existing `research` subagent tool: creates a child thread, runs the agent in the thread's sandbox through the manager's exec API, maps events into atrium's progress stream, returns an untrusted digest plus cost and session id.

## 2. pi

### 2.1 What it is now

- Upstream moved from `badlogic/pi-mono` to `earendil-works/pi` in May 2026; npm scope is now `@earendil-works/*`, old `@mariozechner/*` packages deprecated ([announcement](https://pi.dev/changelog/2026/5/7/pi-has-a-new-home)). MIT licensed ([LICENSE](https://raw.githubusercontent.com/earendil-works/pi/main/LICENSE)). Install used in its own Dockerfile: `npm install -g --ignore-scripts @earendil-works/pi-coding-agent` on `node:24` ([containerization](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/containerization.md)).
- Default tools `read`, `bash`, `edit`, `write` (plus `grep`, `find`, `ls` opt-in). Built-in extensions load by default: `mcp` (stdio and streamable HTTP MCP client), `llama.cpp`, `codemode` (QuickJS scripts calling tools, off by default), `tool_search` ([CLI](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/cli.md), [settings](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/settings.md), [MCP](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/mcp.md)). Older write-ups saying pi has no MCP are out of date.
- No approval prompts: "it does not ask for approval before every tool call"; the security doc says safety comes from isolation (VM/container) and keeping credentials outside ([security](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/security.md)). That matches the sandbox design.

### 2.2 Invocation modes

| Mode | Command | Lifetime | Use |
|---|---|---|---|
| print | `pi -p "..."` | one shot, final text | not enough for streaming |
| JSON | `pi --mode json "..."` | one shot, JSONL events, then exits | simple streamed delegation; cannot be steered |
| RPC | `pi --mode rpc [--no-session]` | long-lived, JSONL commands in, responses+events out | **chosen** if atrium needs stop/steer/follow-up |
| SDK | `createAgentSession()` | in-process Node/Bun | only if a Node helper runs in the VM; not for a Go caller |

([CLI integration](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/cli-integration.md), [RPC](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/rpc.md))

RPC facts the integration depends on ([rpc.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/rpc.md), [rpc-commands.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/rpc-commands.md), [json.md](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/json.md)):

- Strict JSONL, LF-framed (not Unicode line separators); stdout is protocol only, logs on stderr; the reader must drain stdout continuously or pi stalls (backpressure).
- `prompt` returns immediately with `data.disposition` (`started` / `handled`); completion is **`agent_settled`** (not `agent_end`, which precedes retries, compaction, queued work). `agent_settled.aborted` says whether it was aborted.
- While a run is streaming a new `prompt` must say `streamingBehavior: steer | followUp`.
- Events: `message_update` (delta-only text/thinking/toolcall, with cumulative `usage` including `cost.total`), `message_end` (authoritative), `tool_execution_start/update/end`, `turn_*`, `compaction_*`, `auto_retry_*`, `queue_update`.
- `get_session_stats` returns tokens and `cost` for the whole session, including compaction/summary calls.
- Extension dialogs (`select`, `confirm`, `input`) become `extension_ui_request` records the host must answer; unanswered ones time out to a default. Atrium ships no extensions, so none expected, but repo-supplied extensions could issue them (see risks).
- Orderly stop: close stdin. Abort: `abort` command.
- **No cost/turn cap flag** exists in pi's CLI/settings (I found only `thinkingBudgets`, which is a token budget for reasoning, in [settings](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/settings.md)). Atrium must enforce a spend cap itself: watch `message_update.usage.cost.total` and send `abort`.

### 2.3 Provider auth

- Browser/device OAuth (`/login`, stored in `~/.pi/agent/auth.json`) or API key; env var per provider (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, ~35 providers listed), plus ambient Bedrock/Vertex and Anthropic workload identity federation ([providers](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/providers.md)).
- Any provider can be pointed at a proxy by setting only its `baseUrl` in `models.json`, and OpenAI-compatible local endpoints (llama.cpp, Ollama) are first-class with a dummy key ([models](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/models.md)). `[inference]` Atrium already requires an OpenAI-compatible local LLM for mail (plan), so pi can run against it at zero marginal cost for light tasks; quality for coding is a separate question (ticket candidate 7).
- `--api-key` is a non-persistent override; `auth.json` keys may be `!command` (resolved at first use) ([providers](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/providers.md)). Docker Sandboxes' proxy pattern: sandbox gets `ANTHROPIC_OAUTH_TOKEN` as an OAuth-shaped placeholder (`sk-ant-oat01-{rand}`), the proxy swaps it for requests to `api.anthropic.com`; "Do not run `/login` inside the sandbox because that writes a real credential into it" ([containerization](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/containerization.md)).
- Pi can still log in with an Anthropic subscription, but it is the prohibited route for atrium (section 4).

### 2.4 Isolation topologies (pi docs)

pi documents four: plain Docker, Docker Sandboxes (proxy-held credentials), OpenShell, and **Gondolin** (pi stays on the host, an extension routes `read/write/edit/bash/grep/find/ls` into a local micro-VM). Gondolin is not a credential boundary as shipped because commands inherit the host environment, and other extension tools still run on the host ([containerization](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/containerization.md)).

### 2.5 Project trust and repo-supplied content

`.pi/settings.json`, `.pi/mcp.json`, `.pi/extensions`, skills, `SYSTEM.md` and similar load only after project trust. Print/JSON/RPC cannot prompt: with no override, `defaultProjectTrust: "always"` loads them; `ask`/`never` skips. `AGENTS.md`/`CLAUDE.md` load regardless of trust ([security](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/security.md)). Atrium should pass `--no-approve` for untrusted repos.

## 3. Claude Code

### 3.1 Headless interface

- `claude -p "prompt"`; exit code 0 on success, non-zero on failure; failures inside the run (e.g. missing auth) are printed as the result on stdout ([headless](https://code.claude.com/docs/en/headless)).
- Output: `text`, `json` (one object with `result`, `session_id`, `total_cost_usd`, per-model usage), `stream-json` (NDJSON). For live streaming: `--output-format stream-json --verbose --include-partial-messages`; last line is a `result` message with final text, cost, session metadata ([headless](https://code.claude.com/docs/en/headless#stream-responses)). `--json-schema` yields `structured_output`.
- Event types relevant to atrium: `system/init` (model, tools, MCP server status, `capabilities`), `stream_event` text deltas, `assistant`/`user` messages (subagent messages carry `parent_tool_use_id`; text of subagents only with `--forward-subagent-text`), `system/api_retry` (with `error` category), `permission_denied`, `rate_limit_event` (`status`, `resetsAt`, `utilization`, `errorCode: "credits_required"`), terminal `result` with `subtype` such as `error_max_turns`, `error_max_budget_usd` ([headless](https://code.claude.com/docs/en/headless), [TS reference](https://code.claude.com/docs/en/agent-sdk/typescript)).
- Input: prompt argument/stdin (10 MB cap), or `--input-format stream-json` for a long-lived multi-turn process with interrupt and queued messages ([streaming vs single](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode)).
- Sessions: `--resume <id>`/`--continue`; transcripts persist on disk under `CLAUDE_CONFIG_DIR` (default `~/.claude`), so they survive VM suspend if that dir is on persistent disk; `--no-session-persistence` turns it off; `--session-id <uuid>` pins an id ([headless](https://code.claude.com/docs/en/headless#continue-conversations), [CLI reference](https://code.claude.com/docs/en/cli-reference)).
- Limits and control flags: `--max-turns`, `--max-budget-usd` (client-side estimate, "spend can pass the cap"), `--model`, `--effort low|medium|high|xhigh|max|ultracode`, `--fallback-model`, `--allowedTools/--disallowedTools/--tools`, `--permission-mode`, `--permission-prompts none`, `--mcp-config` (+ `--strict-mcp-config`), `--append-system-prompt[-file]`, `--settings` ([CLI reference](https://code.claude.com/docs/en/cli-reference)).
- Stopping: SIGTERM exits 143 and records no result for the in-flight turn; **SIGINT** (or SDK `interrupt()`) ends the turn cleanly ([headless](https://code.claude.com/docs/en/headless#stop-a-run-with-sigterm)).
- Background work: a `-p` run waits for background commands/subagents after the turn, with a 10-minute idle cap (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`) ([headless](https://code.claude.com/docs/en/headless#background-tasks-at-exit)). A dev server started by the agent keeps the process alive; account for it in timeouts.

### 3.2 Permission posture in the VM

`claude -p ... --dangerously-skip-permissions` is the documented mode for "fully unattended inside a container", requiring "a container, VM, or sandbox runtime", run as **non-root** on Linux/macOS; deny rules still apply in every mode ([permission modes](https://code.claude.com/docs/en/permission-modes)). Fits the microVM. Run as an unprivileged user in the toolset rootfs.

### 3.3 Repo-supplied configuration runs under `-p`

Without `--bare`, `-p` runs hooks from the project's `.claude/settings.json` and connects servers from `.mcp.json` "even in a folder you've never trusted"; no trust dialog is shown under `-p` ([headless](https://code.claude.com/docs/en/headless#start-faster-with-bare-mode)). `--bare` skips all of that (and auto memory, CLAUDE.md, plugins) and "will become the default for `-p` in a future release", **but does not read OAuth credentials or the keychain**: it needs `ANTHROPIC_API_KEY` or `apiKeyHelper` ([headless](https://code.claude.com/docs/en/headless#start-faster-with-bare-mode), [authentication](https://code.claude.com/docs/en/authentication#generate-a-long-lived-token)). Consequence: the API-key path can use `--bare` (safer against hostile repos, pass context explicitly); the subscription path cannot, so hostile-repo hooks run with a live credential in the environment.

### 3.4 Authentication in a headless VM

Precedence ([authentication](https://code.claude.com/docs/en/authentication#authentication-precedence)): cloud provider vars > `ANTHROPIC_AUTH_TOKEN` (bearer, for gateways) > `ANTHROPIC_API_KEY` (in `-p` always used when present) > `apiKeyHelper` > **`CLAUDE_CODE_OAUTH_TOKEN`** > profiles/WIF > stored `/login` credentials. Note `ANTHROPIC_API_KEY` outranks the subscription token, so an API key left in the environment silently switches billing.

Subscription options:

1. **`claude setup-token`**: run on a machine with a browser; prints a one-year OAuth token, does not store it; requires Pro/Max/Team/Enterprise; token "can only make model requests" (no Remote Control, no claude.ai connectors); set as `CLAUDE_CODE_OAUTH_TOKEN`; `--bare` ignores it ([authentication](https://code.claude.com/docs/en/authentication#generate-a-long-lived-token)). `setup-token` and `/install-github-app` enforce only `forceLoginMethod`, not `forceLoginOrgUUID` ([authentication](https://code.claude.com/docs/en/authentication#restrict-login-to-your-organization)).
2. **`claude auth login`** (interactive, prints a URL; in containers/SSH/WSL2 the browser shows a code to paste at the `Paste code here if prompted` prompt). Writes `~/.claude/.credentials.json` (0600; or keychain on macOS) under `CLAUDE_CONFIG_DIR`; a stored login expires and warns 3 days before; unattended sessions "can't recover until you sign in again" ([authentication](https://code.claude.com/docs/en/authentication)).
3. **`CLAUDE_CODE_OAUTH_REFRESH_TOKEN` + `CLAUDE_CODE_OAUTH_SCOPES`**: `claude auth login` exchanges the refresh token directly with no browser; documented as "provisioning authentication in automated environments" ([env vars](https://code.claude.com/docs/en/env-vars)).
4. **API key** (Console) or cloud-provider credentials: billed per token under commercial terms; the documented route for products built on the Agent SDK ([Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview), [legal](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)).

Gateway route: with `ANTHROPIC_BASE_URL` pointing at a gateway and a gateway credential variable set (`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`/`apiKeyHelper`), requests carry that credential, the subscription does not apply, and the gateway holds the real key; with only `ANTHROPIC_BASE_URL` set and a saved claude.ai login, the subscription still applies and the gateway must forward the OAuth capability in `anthropic-beta` or requests 401 ([LLM gateway](https://code.claude.com/docs/en/llm-gateway), [gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol#request-headers)).

### 3.5 Network and telemetry the VM must allow or block

- Needed: `api.anthropic.com`; for OAuth sign-in and refresh also `claude.ai` and `platform.claude.com` (API-key runs "can drop these two") ([sandbox environments](https://code.claude.com/docs/en/sandbox-environments)). Fast-mode availability and WebFetch domain checks call `api.anthropic.com` directly and ignore `ANTHROPIC_BASE_URL` ([gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol)).
- Quiet it: `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` (auto-update, telemetry, error reporting, feedback, release notes), `DISABLE_AUTOUPDATER=1`/`DISABLE_UPDATES=1`, `DISABLE_TELEMETRY=1` (also disables GrowthBook flag fetching; makes Remote Control unavailable) ([env vars](https://code.claude.com/docs/en/env-vars)).
- Pin the version via Nix: nixpkgs has `claude-code` (unfree, 2.1.280 vs upstream 2.1.302). Unfree redistribution matters for ticket #46/Nix rootfs: the manager must build with `allowUnfree` and cannot ship a prebuilt public image containing it `[inference from meta.license = unfree, redistributable = false]`.
- Anthropic's own legal page: don't modify the binary or "remove, disable, or restrict any authentication method built into it" ([legal](https://code.claude.com/docs/en/legal-and-compliance#can-customers-offer-claude-code-in-their-products)).

## 4. Terms of use (primary sources)

| Question | Source says | Reading for atrium |
|---|---|---|
| May a script drive `claude -p` on a subscription? | Consumer Terms forbid access "through automated or non-human means, whether through a bot, script, or otherwise" **except** "via an Anthropic API Key or where we otherwise explicitly permit it" ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms)). Claude Code docs explicitly describe `-p`, `setup-token`, "CI pipelines and scripts" ([authentication](https://code.claude.com/docs/en/authentication)). | Permitted via the documented route. `[inference]` Not a blanket license for unattended high-volume use: "Advertised usage limits for Pro and Max plans assume ordinary, individual usage of Claude Code and the Agent SDK" ([legal](https://code.claude.com/docs/en/legal-and-compliance#acceptable-use)). |
| May a platform host Claude Code in a sandbox? | Yes, under the Commercial Terms, binary unmodified, auth methods not restricted, and "Customers may not pay for, resell, or intermediate Claude usage on their end users' behalf. Each end user must authenticate with their own ... subscription plan credentials" ([legal](https://code.claude.com/docs/en/legal-and-compliance#can-customers-offer-claude-code-in-their-products)). "Nor does it prevent an end user from signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code." | OK when each atrium instance owner (= the one user, per the plan) uses their own plan. Atrium must not offer a shared/atrium-owned subscription across instances. |
| May atrium store or relay the user's subscription token? | "developers may not collect, store, or intermediate Claude.ai credentials or session tokens — sign-in to a Claude account must complete through Anthropic's own flow" ([legal](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)). | **Grey.** Having the *atrium web UI* ask for a pasted setup-token and keep it in atrium's DB reads as "collect/store". Letting the owner complete Anthropic's own flow inside their own sandbox is closest to explicitly allowed. A manager-side secret file the owner wrote on their own host (like any secret in their config) is `[inference]` defensible but unconfirmed; Anthropic points to contact-sales for edge cases. |
| Agent SDK on a subscription? | "Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK. Use the API key authentication methods" ([Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview)). Help-center update: SDK/`claude -p`/third-party usage "still draw from your subscription limits" for now; Max/Team plans got monthly API credits on 2026-10-07 that cover SDK/`claude -p` **with a Console API key** ([help center](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)). | Do not use the SDK-with-OAuth route; run the CLI. A Max/Team owner may get API credits that fund the API-key path. |
| pi (third-party) on a subscription? | Legal page: no routing through plan credentials; pi's changelog: such use is billed as extra usage per token ([pi changelog 0.66.0](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/CHANGELOG.md)). | Do not offer. pi uses API key / local model. |
| Branding | Agents built on the SDK may not be called "Claude Code" or mimic it; "runs Claude Code" in plain text is OK ([Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview), [legal](https://code.claude.com/docs/en/legal-and-compliance)). | UI label: "Claude Code (runs the Claude Code CLI)". |
| Enforcement | "Anthropic reserves the right to take measures to enforce these restrictions ... without prior notice." | Treat subscription use as revocable; do not make v2 depend on it. |

I did not find a primary Anthropic statement on running one subscription from a **remote VM with a different IP** or on concurrency limits; unverified (section 9).

## 5. Credential placement vs the "token never in the VM" rule

Context: ticket #28's findings and plan: GitHub token and any "inject" secrets live only in the manager/proxy (header-only, HTTPS-only, host-scoped, response-scrubbed); placeholder in VM ([cloud-sandboxes](https://github.com/jeiang/atrium/blob/research/cloud-sandboxes/research/cloud-sandboxes.md)).

| Credential | VM sees | Where real value lives | Notes |
|---|---|---|---|
| Atrium's Anthropic **API key** for pi or Claude Code | `ANTHROPIC_API_KEY=placeholder` | manager proxy, injected on `api.anthropic.com` | Satisfies the rule. Needs TLS termination for that host (ticket #25). Claude Code in `-p` always uses the key when present; pi supports it. Add a per-sandbox spend kill-switch in the proxy. |
| Claude subscription, **S1: placeholder + proxy swap** (`CLAUDE_CODE_OAUTH_TOKEN=placeholder`, proxy substitutes the real `setup-token`) | placeholder | manager secret file | Satisfies the rule technically. `[inference]` I could not verify that Claude Code accepts a non-token string and still sends the `anthropic-beta` OAuth capability; Docker's `sbx secret set-custom --env CLAUDE_CODE_OAUTH_TOKEN --placeholder` is a published precedent (docs.docker.com, seen only via search summary, not read directly). Legally this is "intermediating" a credential (section 4). |
| Claude subscription, **S2: owner signs in inside the sandbox** with Anthropic's flow (`claude auth login`, paste-code) | real credentials in `CLAUDE_CONFIG_DIR` | per-instance persistent volume on the manager, mounted only into Claude-Code-enabled sandboxes | Closest to the terms; token (incl. refresh token) is in the VM and readable by the model/injected repo code; reduce with egress allowlist (`api.anthropic.com`, `claude.ai`, `platform.claude.com` only) and a dedicated volume. Needs a PTY/relay in the manager API and a UI page. Credentials expire; unattended runs fail until re-login. |
| Claude subscription, **S3: `setup-token` as runtime env var** | real token (inference-only scope, 1 year) | manager secret ("runtime" class) | Simple; smallest-scope credential; the model can print it; setup-token cannot do Remote Control or connectors, so blast radius is "spend the owner's plan". Setup happens on the owner's own machine, outside atrium. |

Recommendation (tied to plan): **API key via proxy is the default for both agents** (so the rule holds). For a Claude subscription, offer **S3 as the opt-in** with a one-line, documented exception to the rule ("the Claude Code subscription token is the single credential that enters the VM, because the closed `claude` binary reads it itself") and S2 as the alternative where the owner prefers Anthropic's own sign-in flow; defer S1 until verified and cleared. Never store a subscription token in the atrium DB or echo it in the UI.

GitHub for coding agents (per plan): neither agent gets a GitHub token. In the VM `git` has no credentials; the manager's typed `git push`/`gh pr create` tools run outside; the atrium agent (or a post-run hook) pushes the branch the coding agent committed to. Both agents have `bash`, so raw `git push` will fail by design (no credential); tell the agent in the system prompt and check tool results for that failure mode. MCP is the way to hand harness tools to the agents: both read `--mcp-config`/`mcp.json` for stdio and streamable-HTTP servers ([Claude MCP flags](https://code.claude.com/docs/en/cli-reference), [pi MCP](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/mcp.md)); a manager-hosted MCP endpoint (reachable only inside the VM network) could expose `git_push_branch`, `open_draft_pr`, etc. so the coding agent can call typed tools without credentials (candidate 3).

## 6. How the atrium agent invokes them

### 6.1 Precedent in the Python app

- The `research` tool (a read-only subagent returning a digest) is the closest analogue: creates a child thread, runs `service.run_once(..., max_steps=CHILD_MAX_STEPS, max_cost_usd=CHILD_MAX_COST_USD)` under a slot semaphore, converts a stopped run to a `ToolError` with partial digest, returns `{"untrusted": True, ...}` ([`src/atrium/agent/tools/research.py:61-110`](../src/atrium/agent/tools/research.py), caps at `subagents.py:9-11`: $0.25, 12 steps, 2 concurrent).
- Progress events the UI renders: `run_start, thinking, text, tool_start, tool_done, user, plan, files, run_done` (`src/atrium/agent/progress.py:14-22`), 0.2 s flush (`:23`), stop check (`:102`).
- Tool kinds `chat/cron/nightly/sub/code` and per-tool `contexts` (`src/atrium/agent/tools/base.py:12-20,32-44`).
- Code mode today is a socket-activated systemd sandbox, "the security boundary" (`AGENTS.md:95-101`); the new tool replaces that.

### 6.2 Proposed tool

`code_agent(agent: "pi" | "claude_code", task, model?, effort?, max_cost_usd?, max_turns?, resume_session?)`, kinds `{chat}` only at first (not cron/nightly; see budgeting), effect `write` (it changes files in the VM) but with no direct external effect: pushes stay with typed manager tools. Flow:

1. Atrium creates a child thread (like `subagents.create_child`) bound to the parent thread's sandbox (plan: one sandbox per thread).
2. Atrium calls the manager exec API for a **bidirectional streamed process** (stdin, stdout, stderr, signal, exit code) with the command, env, and cwd. This is a requirement on ticket #41/#54 (candidate 2).
3. Atrium parses JSONL and maps events: pi `message_update.text_delta` / `tool_execution_*` and Claude `stream_event` / `assistant` tool_use + `user` tool_result → `text`, `tool_start`, `tool_done`; pi `agent_settled` / Claude `result` → `run_done`.
4. On finish, return `{untrusted: true, agent, session_id, text, cost_usd, turns, files_changed (manager `git diff --stat`), exit_state}`. Subagent output must be treated as data, as `research` does.
5. User "stop" → pi `abort`; Claude `SIGINT`, then wait for the `result`; after a grace period SIGTERM and mark the child run failed.

Process model: **pi over RPC** (steer/abort/stats, long-lived per thread) and **Claude Code one-shot `-p` per delegated task**, follow-ups via `--resume <session_id>` (crash-safe, definitive `result` message, cost in one place). `[inference]` Claude's long-lived stream-json input adds interrupt/queue but also a process that dies with the VM suspend; one-shot is simpler.

Suggested command lines:

```sh
# Claude Code, API-key path (placeholder key; proxy injects real key)
ANTHROPIC_API_KEY=placeholder DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
claude --bare -p "$TASK" --output-format stream-json --verbose --include-partial-messages \
  --permission-mode bypassPermissions --max-turns $N --max-budget-usd $CAP \
  --model $MODEL --effort $EFFORT --mcp-config /etc/atrium/mcp.json --strict-mcp-config \
  --append-system-prompt-file /etc/atrium/coding-system.md [--resume $SID]

# Claude Code, subscription path (no --bare; token env from manager or ~/.claude)
CLAUDE_CODE_OAUTH_TOKEN=... claude -p "$TASK" --output-format stream-json --verbose \
  --include-partial-messages --permission-mode bypassPermissions --max-turns $N

# pi
ANTHROPIC_API_KEY=placeholder PI_OFFLINE=1 pi --mode rpc --no-approve --model anthropic/$MODEL:$THINKING \
  --tools read,bash,edit,write,grep,find,ls --session-dir /workspace/.pi-sessions
```

(Flags from [CLI reference](https://code.claude.com/docs/en/cli-reference) and [pi CLI](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/cli.md); `PI_OFFLINE=1` per pi's `--offline` equivalence. The pi model-id format `provider/id:thinking` is documented there.) With `--bare` the agent loses CLAUDE.md and auto memory; the manager should pass the repo's `AGENTS.md`/`CLAUDE.md` from a trusted ref via `--append-system-prompt-file`.

## 7. Budgeting and limits

Atrium today: one monthly dollar budget in the `usage` table (`budget.py:3-6` default $75, plan raises to $150; `budget.check` at `budget.py:32-40`; unattended pauses at 80%, chat at 100%; per-run ceiling `loop.py:361-368`; costs computed from API usage by `pricing.cost_usd` in `usage.py:38-45`). A coding agent spends outside that loop, so it must be metered into the same table (ticket #55).

| Mode | Billing | What atrium can measure | What atrium can cap |
|---|---|---|---|
| API key, Claude Code | per token to atrium's key | `result.total_cost_usd` + `modelUsage` (client-side estimate, "not authoritative billing data"; subagents included in `total_cost_usd` but **not** in `usage`) ([cost tracking](https://code.claude.com/docs/en/agent-sdk/cost-tracking)); resumed sessions report the session total, so record the delta | `--max-budget-usd`, `--max-turns`, wall-clock kill; proxy kill-switch |
| API key, pi | per token | `message_update.usage.cost.total` live; `get_session_stats.cost` | none native: atrium aborts at the cap; proxy kill-switch |
| Subscription, Claude Code | plan windows (5-hour, weekly; Max weekly model limits); overage via "usage credits" at API rates if the owner enabled them ([help center](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan)) | `rate_limit_event.utilization/resetsAt/status`; `$` fields are estimates only and not what is charged | `--max-turns`, wall clock; stop on `status: rejected` (`credits_required` means plan exhausted) |
| Local LLM (pi) | none | token counts | wall clock |

Recommendations: record every coding-agent run as a `usage` row with `kind='code_agent'` (new kind), model, effort, and estimated cost; counts toward the monthly budget only for API-key runs; show subscription runs separately with plan utilization. Defaults: per-delegation cap like `CHILD_MAX_COST_USD` but larger (e.g. $2, `[inference]` tune after Sonnet 5.5/Opus 5.5 pricing from ticket #26), chat-only, never scheduled until the "ordinary individual use" question is settled. Unattended scheduled coding runs are exactly where the subscription's "ordinary, individual usage" language bites `[inference]`; if wanted, route them through the API key.

## 8. Options and trade-offs

| Decision | Options | Trade-offs | Recommendation |
|---|---|---|---|
| Which agents ship | pi only / Claude Code only / both | pi: MIT, any provider incl. local llama.cpp, no cost cap, no approvals, small npm package; Claude Code: closed binary, unfree in Nix, best fit with Anthropic models, subscription option, native `--max-budget-usd`/`--resume`/`--bare` | Both, behind one `code_agent` tool; Claude Code first (richer controls), pi second (local-model and non-Anthropic route) |
| Where pi's brain runs | inside VM (RPC over manager exec) / on manager host with tools delegated (Gondolin pattern) / SDK in a Node helper | host-side keeps key and `auth.json` out of VM but needs Node and an extension on the manager host, and extension tools run on the host; inside-VM is uniform with Claude Code and with the proxy-injection design | Inside VM. Reconsider host-side only if proxy injection proves hard |
| Claude subscription placement | S1 / S2 / S3 (section 5) | see table | S3 opt-in with documented exception; S2 as alternative; S1 later |
| Output transport | stdout JSONL over exec stream / a file tailed by manager / MCP callbacks | exec stream is simplest; file tailing survives atrium restarts and VM suspend better | Exec stream plus the agent's own on-disk session for resume |
| Hostile-repo handling | `--bare`/`--no-approve` always / trust per environment | bare breaks subscription auth; skipping repo `.claude`/`.pi` also removes useful skills | Default untrusted; per-environment "trust repo config" toggle |
| Scheduling | chat-only / allow cron | scheduled coding on a subscription is the riskiest terms reading | Chat-only in Phase B v1 |

## 9. Risks and unverified items

- **Terms ambiguity** on a manager-stored subscription token (section 4); Anthropic can change/enforce without notice. Provide an API-key path so nothing depends on the subscription.
- **Prompt injection + subscription token in VM**: the agent can `cat` the token; exfiltration is limited only by the egress list. Allowing `github.com` or a CDN is an exfil channel (as ticket #28 notes).
- **Repo-supplied code runs inside the agent**: `.claude/settings.json` hooks and `.mcp.json` servers under `-p` (unless `--bare`), pi extensions and `.pi/mcp.json` (unless not trusted), plus pi extension UI requests that block until answered. Under unattended runs a blocked dialog can hang a run; atrium must answer or time out.
- **Version churn**: both release near-daily (Claude Code gained `--permission-prompts` in 2.1.259, `--forward-subagent-text` 2.1.211, etc.) and nixpkgs lags (pi 0.87.1 vs 1.3.0). Pin and test event parsing against a recorded stream per version; treat unknown event types as ignorable (Claude's docs say to ignore unknown `capabilities`; pi's JSON event list is open).
- **Cost fields are estimates** on both; the authoritative number is Console usage (Usage and Cost API). Cap with margin.
- **Unfree redistribution** of Claude Code in a public Nix cache/release image `[inference]`.
- **Unverified**: (a) whether Claude Code accepts a placeholder `CLAUDE_CODE_OAUTH_TOKEN` through a header-swapping proxy; (b) a remote VM/different IP triggering anti-abuse on a subscription; (c) concurrency limits of a plan for parallel `claude -p` runs; (d) pi's behavior and cost on Sonnet/Opus 5.5 specifically (model catalog not read); (e) whether `claude auth login` paste-code flow works through a PTY relay from a web page (documented for terminals only); (f) the exact `rate_limit_event` cadence under `-p`; (g) I did not read pi's `rpc-commands.md` response for `get_state` in full, only prompt/stats parts.

## 10. Recommendation (summary)

1. Add a `code_agent` tool (chat-only) running pi (RPC) or Claude Code (`-p` one-shot) inside the thread sandbox through a streamed exec API on the manager; map events to atrium's progress stream; return an untrusted digest.
2. Default credential = atrium's Anthropic API key as a placeholder in the VM with proxy injection, so the plan's rule holds and costs are metered. pi never uses a subscription. Claude subscription is an opt-in, per-instance, owner-own-plan feature (S3, or S2), one documented exception to "token never in the VM", never in the atrium DB.
3. Budgets: write `kind='code_agent'` usage rows; enforce spend with `--max-budget-usd`/`--max-turns` (Claude) and abort-on-cost (pi), plus a proxy kill-switch; show subscription runs as plan utilization, not dollars.
4. Treat repos as hostile: `--bare` / `--no-approve`, non-root user, no GitHub token in the VM, harness tools via MCP.
5. Pin versions in the Nix toolset (`claude-code` unfree; pi from npm/overlay because nixpkgs lags).

## 11. Candidate new tickets

1. **Coding-agent tool contract (`code_agent`)**: design, B, assistant/sandbox. Args, kinds/contexts, digest shape, child thread binding, stop/resume semantics, cost fields; decide Claude-first vs both; feeds #46 and #42.
2. **Manager exec API needs bidirectional streaming + PTY**: design, B, sandbox. Stream stdin/stdout/stderr, signals (SIGINT vs SIGTERM semantics Claude needs), exit codes, reattach after atrium restart/VM suspend, 10 MB stdin cap, backpressure (pi stalls if stdout isn't drained), PTY for `claude auth login`. Input to #41/#54.
3. **MCP endpoint for harness tools inside the VM**: design, B, sandbox/assistant. Expose typed git/gh/notify/search tools to Claude Code and pi via a manager-hosted MCP server (no credentials in VM); auth between VM and MCP; which tools are allowed in untrusted-repo mode.
4. **Anthropic subscription credential policy**: decision/research, B, security+legal. Pick S1/S2/S3; whether to ask Anthropic (contact sales) about a manager-held `setup-token`; UI flow for sign-in inside the sandbox; expiry/renewal UX (3-day warning, one-year `setup-token`, refresh tokens); what happens to scheduled runs.
5. **Proxy-side Anthropic key injection and spend kill-switch**: design, B, sandbox/budget (extends #25 and #55). TLS-terminate `api.anthropic.com`, per-sandbox spend cap, usage accounting from response bodies as an independent check of the client-side estimates, handling of `x-api-key` vs `Authorization: Bearer` for pi's OAuth-shaped placeholder.
6. **Event-stream adapters and recorded fixtures**: build/test, B, assistant. Go parsers for pi JSONL and Claude stream-json with versioned golden streams, unknown-event tolerance; run in CI against pinned versions.
7. **pi on the local llama.cpp model**: research, B, assistant. Is a local model good enough for useful coding runs; `models.json` generation from atrium's settings; tool-calling reliability; context size.
8. **Hostile-repo posture for coding agents**: research/design, B, security. `--bare`/`--no-approve` defaults, per-environment trust toggle, handling pi extension UI requests and Claude hooks/MCP from repo, secret-in-env exposure.
9. **Toolset packaging for claude-code and pi**: build, B, sandbox/nix. Unfree handling for `claude-code`, pinning to the latest npm version instead of nixpkgs, pi package source, non-root user, `CLAUDE_CONFIG_DIR`/`PI_CODING_AGENT_DIR` on persistent volume, disabling updaters/telemetry.
10. **Usage model extension**: design, B, budget. New `kind='code_agent'` rows, session-total vs delta accounting for `--resume`, separating subscription utilization from dollars in the usage page, per-delegation cap setting.
11. **Instance-level model/provider setting for coding agents**: design, B, settings. Which provider/model pi uses (Anthropic key, local, other), which Claude model, effort mapping to `--effort`/`--thinking` (pi levels differ: `off..max`), tie into the new-chat page (#43).
