# Codex CLI as a third coding agent (ticket #149)

Facts as of 2026-10-18. Codex = [openai/codex](https://github.com/openai/codex), latest stable tag `rust-v0.163.0` (2026-10-15), with `rust-v0.164.0-alpha.*` tags already out on 2026-10-16/17 ([releases](https://github.com/openai/codex/releases)); npm `@openai/codex` 0.163.0, Apache-2.0 ([npm](https://registry.npmjs.org/@openai/codex/latest)). Builds on [#32 pi and Claude Code](https://github.com/jeiang/atrium/blob/research/pi-claude-code-sandbox/research/pi-claude-code-sandbox.md), [#90 pi providers](https://github.com/jeiang/atrium/blob/research/pi-providers/research/pi-providers.md) and [#27 harness survey](https://github.com/jeiang/atrium/blob/research/coding-harnesses/research/coding-harnesses.md). `[inference]` marks my reasoning; `[unverified]` marks things I could not confirm from a primary source. Docs are OpenAI's first-party Markdown mirror at `learn.chatgpt.com/docs/*.md` (same content as developers.openai.com/codex).

## 1. Answer in brief

1. **Viable: yes. Needed for launch: no.** Codex can be driven headlessly from a Go process in two ways: `codex exec --json` (one-shot JSONL) and `codex app-server` (long-lived bidirectional JSON-RPC over stdio, with steer, interrupt, approvals, resume). Both fit the manager's "streamed exec" requirement from #32 candidate 2. It adds little that pi (with OpenAI models via the #90 gateway or SIWC plan login) plus Claude Code do not already cover (section 8).
2. **Headless and events.** `exec --json` emits `thread.started`, `turn.started`, `turn.completed{usage}`, `turn.failed`, `item.*`, `error`; items are agent messages, reasoning, command executions, file changes, MCP tool calls, web searches, plan updates ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md), [`exec_events.rs`](https://raw.githubusercontent.com/openai/codex/main/codex-rs/exec/src/exec_events.rs)). App-server adds `turn/steer`, `turn/interrupt`, `thread/resume`, `thread/tokenUsage/updated`, and server-initiated approval requests ([app-server](https://learn.chatgpt.com/docs/app-server.md)). Usage is **tokens only** (input, cached, cache-write, output, reasoning), never dollars, so atrium prices it with the #90 table.
3. **Auth.** Three usable paths: (a) **API key through the atrium gateway**: custom provider with `base_url`, `wire_api = "responses"`, `env_key` holding a per-sandbox placeholder; fits #25/#90 exactly and keeps the real key out of the VM. (b) **ChatGPT plan login in the VM with device code** (`codex login --device-auth`): OpenAI's recommended headless method, no paste of a redirect URL, credential in `$CODEX_HOME/auth.json` on the per-instance volume. (c) **Atrium-owned SIWC token handed to `codex app-server`** via `ACCESS_TOKEN` env: OpenAI documents this for apps, but it is the S-B shape #90 deferred. Codex's own ChatGPT login does not run through a gateway unless the gateway proxies `/backend-api/codex`.
4. **Sandbox and approvals in the microVM.** Codex's own OS sandbox is bubblewrap/Landlock on Linux and needs unprivileged user namespaces; inside atrium's microVM it is redundant. OpenAI's own guidance allows `--dangerously-bypass-approvals-and-sandbox` "inside a dedicated sandbox VM". Recommended: `sandbox_mode = "danger-full-access"`, `approval_policy = "never"`, `web_search = "disabled"`. Isolation comes from the VM and the egress proxy, same stance as pi.
5. **Licence/terms.** Code: Apache-2.0, redistributable in a Nix toolset. Plan-login terms: running Codex CLI signed in with the owner's own ChatGPT plan on the owner's own remote machine is Codex Local usage per OpenAI's help text; OpenAI's CI guidance for `auth.json` says trusted private runners only, not public/open-source repos. Whether atrium *orchestrating* that CLI on a schedule is fine is not stated `[unverified]`.
6. **Recommendation:** do not add Codex to the v2 launch set; keep "own small adapter, two implementations". Make the adapter shape (section 7) Codex-compatible and add Codex as an optional toolset (`codex`) after #46's two agents ship, if the owner wants OpenAI-plan coding or GPT-tuned behaviour. ACP is not a way in for Codex (section 6).

## 2. Headless modes and event stream

| Mode | Command | Lifetime | Use in atrium |
|---|---|---|---|
| exec | `codex exec --json [--ephemeral] [--output-schema f] -o last.txt "<task>"` | one shot; stdout JSONL, stderr human progress | delegated task, definitive final message, typed answer |
| exec resume | `codex exec resume <SESSION_ID> "<follow-up>"` | one shot | follow-ups, like Claude `--resume` |
| app-server | `codex app-server --listen stdio://` | long-lived; JSON-RPC 2.0 (without the `jsonrpc` field), newline-delimited | stop/steer/approval round trips, like pi RPC |
| SDK | TypeScript `@openai/codex-sdk` (Node 18+), Python `openai-codex` | in-process | not usable from Go; both are wrappers over the CLI/app-server |

Sources: [non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md), [app-server](https://learn.chatgpt.com/docs/app-server.md), [SDK](https://learn.chatgpt.com/docs/codex-sdk.md). The `codex mcp-server` command was removed in favour of app-server ([SDK page](https://learn.chatgpt.com/docs/codex-sdk.md)).

Facts the adapter depends on:

- `exec` streams progress to stderr and prints only the final message to stdout, unless `--json`, which makes stdout the JSONL stream. Read-only sandbox by default; `--full-auto` is no longer supported. Requires a git repo unless `--skip-git-repo-check`. `--output-schema` forces a typed final answer; `-o` writes the last message to a file ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md)).
- `turn.completed.usage` carries `input_tokens`, `cached_input_tokens`, `cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens` ([`exec_events.rs`](https://raw.githubusercontent.com/openai/codex/main/codex-rs/exec/src/exec_events.rs)).
- App-server stdio is the supported transport; WebSocket is "experimental and unsupported", non-loopback listeners are unauthenticated by default ([app-server](https://learn.chatgpt.com/docs/app-server.md#protocol)). Use stdio through the manager's exec API; never expose a listener.
- App-server ships a schema generator pinned to the binary version: `codex app-server generate-json-schema --out DIR` ([app-server](https://learn.chatgpt.com/docs/app-server.md#message-schema)). That is the right input for Go types and CI fixtures `[inference]`.
- Turn control: `turn/start`, `turn/steer` (needs `expectedTurnId`, no overrides), `turn/interrupt` (turn ends `interrupted`). Errors end a turn with `status: failed` and `codexErrorInfo` such as `ContextWindowExceeded`, `UsageLimitExceeded`, `HttpConnectionFailed`, `Unauthorized`, `SandboxError`. Success is only `turn.status == completed` ([app-server](https://learn.chatgpt.com/docs/app-server.md#errors)).
- Items: `agentMessage` (with `phase` commentary/partial/final), `reasoning`, `commandExecution`, `fileChange`, `mcpToolCall`, `plan`, `dynamicToolCall` (experimental). Deltas stream as `item/agentMessage/delta` etc.; `turn/diff/updated` and `turn/plan/updated` carry empty `items`, so use `item/*` as truth ([app-server](https://learn.chatgpt.com/docs/app-server.md#items)).
- **No spend cap flag**: I found no budget or max-turns option in the CLI reference or config reference (`grep` of both). There is an in-development `features.rollout_budget` (token limit, off by default) ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md)). Atrium must enforce its own cap by summing `thread/tokenUsage/updated` or `turn.completed.usage` and calling `turn/interrupt` / killing the process, as for pi (#32 section 2.2).
- Release cadence is several tags per week including alphas (releases list above). nixpkgs ships `codex` 0.156.1 (checked locally with `nix search nixpkgs codex`), seven stable minors behind. Pin from upstream release tarballs, as #32 recommends for pi and Claude Code.

## 3. Auth options

### 3.1 What Codex supports ([auth](https://learn.chatgpt.com/docs/auth.md))

| Method | How | Headless? | Credential location |
|---|---|---|---|
| ChatGPT sign-in, browser | `codex login`, loopback callback on `localhost:1455` | no (needs the browser on the same host or an SSH tunnel) | `$CODEX_HOME/auth.json` or OS keyring |
| ChatGPT sign-in, **device code** (beta) | `codex login --device-auth`; user opens `https://auth.openai.com/codex/device` anywhere and types a one-time code; also over app-server `account/login/start {type: chatgptDeviceCode}` returning `verificationUrl` and `userCode` | **yes, preferred by OpenAI for headless/remote** | same |
| API key | `printenv OPENAI_API_KEY \| codex login --with-api-key`, or `CODEX_API_KEY` env for `exec`/`review`/SDK | yes | same, or env only |
| Copy `auth.json` from a machine with a browser | scp into `~/.codex/` | yes | file; documented fallback |
| Enterprise access token | `codex login --with-access-token` / `CODEX_ACCESS_TOKEN` | yes | Enterprise workspaces only; not applicable to a personal instance `[inference]` |
| Custom provider | `[model_providers.<id>]` with `requires_openai_auth`, `env_key`, `env_http_headers` or a command-backed `auth` helper | yes | env or helper |
| Amazon Bedrock | provider type, API key or AWS chain | yes | out of scope |

`cli_auth_credentials_store = file | keyring | auto | ephemeral`; `file` is `CODEX_HOME/auth.json`, plaintext ("treat it like a password"). A NIC-less microVM has no keyring, so set `file` explicitly ([auth](https://learn.chatgpt.com/docs/auth.md#credential-storage)). `CODEX_HOME` must already exist ([env vars](https://learn.chatgpt.com/docs/config-file/environment-variables.md)), so the manager must create the volume directory first.

Token mechanics (from OpenAI's CI/CD guide): Codex refreshes `auth.json` itself when `last_refresh` is about 8 days old or on 401, and writes the new tokens back; "only one machine or serialized job stream" should use a given `auth.json`; not for public or open-source repos ([CI/CD auth](https://learn.chatgpt.com/docs/auth/ci-cd-auth.md)). Same concurrency rule as pi in #90 section 3.3: one subscription-mode sandbox at a time per instance.

### 3.2 API key through the atrium gateway

Codex requires the **Responses** wire format: `wire_api` accepts only `responses` ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md), `model_providers.<id>.wire_api`). So the #90 `/gw/openai/v1/responses` route serves Codex unchanged. OpenAI's own gateway checklist lists what the gateway must preserve: `POST /v1/responses`, incremental SSE with a terminal `response.completed`, replayed input across turns, `function_call` / `function_call_output` pairs, and a `models` array on the model list ([gateway compatibility](https://learn.chatgpt.com/docs/enterprise/gateway-compatibility.md)).

```toml
# $CODEX_HOME/config.toml generated by atrium; no secret in the file
model = "gpt-6.1-sol"
model_provider = "atrium"
web_search = "disabled"
cli_auth_credentials_store = "file"

[model_providers.atrium]
name = "atrium gateway"
base_url = "http://127.0.0.1:<shim-port>/gw/openai/v1"
wire_api = "responses"
env_key = "ATRIUM_GW_KEY"        # per-sandbox bearer placeholder
requires_openai_auth = false
```

Two caveats from the gateway docs: custom aliases and non-built-in providers do not get Codex's bundled model metadata adjustments, so context size and reasoning options may need a `model_catalog_json` ([gateway](https://learn.chatgpt.com/docs/enterprise/connect-to-a-gateway.md)); and the client's `/models` call expects a `models` array, not the OpenAI `/v1/models` shape ([SIWC through a gateway](https://learn.chatgpt.com/docs/enterprise/sign-in-with-chatgpt-through-a-gateway.md)). The #90 gateway sheet says pi never calls `/models`; Codex may, so the route needs either a catalog file or a fixed-response `/models` `[inference]`.

OpenAI also ships `codex-responses-api-proxy`: a strict proxy forwarding only `POST /v1/responses` to `api.openai.com` with the key read from stdin (mlock'd), optional `--upstream-url` and `--dump-dir` ([README](https://raw.githubusercontent.com/openai/codex/main/codex-rs/responses-api-proxy/README.md)). It is ~5 MB per Linux arch in the release assets. It confirms the pattern atrium already chose (placeholder in VM, key injected outside), but atrium's own gateway is still needed for metering and per-sandbox bearer, so it is a reference, not a dependency `[inference]`.

OpenAI's guidance for CI: do not set the API key as a job-wide env var where repo-controlled code runs; set `CODEX_API_KEY` only on the Codex process ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md#authenticate-in-automation)). The gateway placeholder satisfies this: the real key never exists in the VM.

### 3.3 ChatGPT plan (subscription)

- **Native login in the VM (S-A analogue).** Device-code login fits the NIC-less microVM better than pi's SIWC paste step: the terminal prints a URL and a code, the user finishes on any device, nothing is pasted back ([auth: headless](https://learn.chatgpt.com/docs/auth.md#login-on-headless-devices)). It must be enabled in the user's ChatGPT security settings first. Credential lives on the per-instance volume as `CODEX_HOME`. Egress needed `[inference, confirm with the proxy deny log]`: `auth.openai.com` plus the ChatGPT backend that Codex's `chatgpt_base_url` points at; I did not find an official domain list.
- **Atrium-owned SIWC token into app-server (S-B analogue).** OpenAI documents it: set `ACCESS_TOKEN` in the child env, start `codex app-server` with a Responses provider at `https://api.openai.com/v1`, `env_key = "ACCESS_TOKEN"`, `requires_openai_auth = false`, `supports_websockets = false`; the app renews the token and restarts app-server, resuming with `thread/resume`; `clientInfo.name` must match the app's `agent_name_hint` ([Codex app-server for SIWC](https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server.md)). The token is then in the VM's process env, which breaks "token never in the VM" in the same way S-A does, and keeps #90's unresolved terms question (general-purpose access, remote runtime) `[inference]`. It is the only route that needs no Codex login state in the VM. Deferred, same as #90.
- Do **not** use app-server's own `account/login/start {type: chatgpt}` inside atrium for a hosted service: OpenAI says "App-server authentication has never been permitted for commercial or hosted services" and points hosted use to Sign in with ChatGPT ([app-server auth](https://learn.chatgpt.com/docs/app-server.md#auth-endpoints)). Atrium is open source and self-hosted per owner, so the open-source/local carve-out ("you can continue using it") plausibly applies, but the line is untested `[inference]`.
- **Plan limits.** Plan usage follows the ChatGPT plan, shared with ChatGPT Work; remote machines do not add allowance ([help: Codex with your ChatGPT plan](https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan), via search summary; the page returned HTML to a plain fetch). App-server exposes `account/rateLimits/read`, `account/rateLimits/updated` and `account/usage/read`, which is more plan-observability than pi or Claude Code give ([app-server](https://learn.chatgpt.com/docs/app-server.md#6-rate-limits-chatgpt)). Atrium can show remaining plan allowance in the UI instead of only the last error.
- **Models.** Plan sign-in models: `gpt-6.1-sol` (Plus, Pro, Business, Enterprise, Edu), `gpt-6-luna` (Free, Go); GPT-5.5 retired from plan sign-in on 2026-10-14 ([models](https://learn.chatgpt.com/docs/models.md)). API-key pricing: #90 section 2.4 (gpt-6.1-sol 2/10 USD per 1M in/out short context).

## 4. Sandbox and approval behaviour inside the microVM

- **Defaults.** `exec` is read-only unless told otherwise. `sandbox_mode` is `read-only | workspace-write | danger-full-access`; `approval_policy` is `on-request | never | granular{...}` (`untrusted` retired, `on-failure` deprecated). Network is off by default under `workspace-write` ([approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md), [config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md)).
- **Linux enforcement** needs `bwrap`; if absent Codex uses a bundled helper that "requires support for unprivileged user namespace creation", and warns at startup when it cannot ([sandboxing](https://learn.chatgpt.com/docs/sandboxing.md)). Release assets include a static `bwrap` (~260 KB) ([releases](https://github.com/openai/codex/releases/latest)). Whether the atrium guest kernel allows user namespaces is a manager choice `[inference]`.
- **Inside the microVM prefer no nested sandbox.** OpenAI: avoid `--dangerously-bypass-approvals-and-sandbox` "unless you are inside a dedicated sandbox VM" ([commands](https://learn.chatgpt.com/docs/developer-commands.md)). Atrium's VM is that. Setting `danger-full-access` + `approval_policy = "never"` removes both a failure mode (userns missing) and every approval prompt, so the adapter needs no approval callback for Codex. A repo cannot make it stricter than atrium sets, because project-local `.codex/config.toml` is loaded only when the project is trusted, and provider, auth, notify, profile and `otel` keys in project config are ignored ([config reference](https://learn.chatgpt.com/docs/config-file/config-reference.md)). Do not mark any project trusted.
- **Gotcha: web search flips to live.** `web_search` defaults to `cached`, but "if you use `--yolo` or another full access sandbox setting, it defaults to `live`" (config reference). Set `web_search = "disabled"` (or `cached`) explicitly. Hosted web search runs on OpenAI's side, so the VM egress allowlist does not govern it ([approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md#traffic-outside-the-command-network-proxy)); disabling it keeps the allowlist meaningful.
- **Network inside the VM.** Codex has its own `network_proxy` domain policy, but it only constrains commands, and only when network is already enabled; it does not filter model/auth requests, MCP, or web search (same page). Atrium's egress proxy (#25) stays the single enforcement point; leave Codex's feature off.
- **Environment leakage.** `shell_environment_policy` controls which env vars commands inherit (`inherit = core|none`, `filters`, `set`; default keeps names with KEY/SECRET/TOKEN unless `ignore_default_excludes = false`) (config reference). Use `inherit = "core"` and drop `ATRIUM_GW_KEY` from child commands so a model-run `env` does not print the gateway placeholder (low value, but free) `[inference]`.
- **Telemetry and updates to switch off:** `analytics.enabled = false` (also disables the default `statsig` metrics exporter), `otel.*` none, `check_for_update_on_startup = false`, `feedback.enabled = false` (config reference). Without these Codex may call OpenAI endpoints the egress policy would otherwise deny.
- **Repo-supplied content.** Codex reads `AGENTS.md` (size-capped by `project_doc_max_bytes`), skills, hooks (`features.hooks`; command hooks run shell commands) and MCP servers from config. In the hostile-repo posture (#32 section 3.3 for Claude Code, candidate 8) add: untrusted project, `--ignore-rules` for execpolicy `.rules` files, no repo-supplied hooks/MCP ([non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md#permissions-and-safety)).
- **Protected paths.** Codex keeps `.git` read-only inside writable roots under its own sandbox ([#27 section 3.2](https://github.com/jeiang/atrium/blob/research/coding-harnesses/research/coding-harnesses.md)); with `danger-full-access` that protection is gone, so `.git`/hooks protection must come from the manager (git runs in the manager per #27 recommendation 15) `[inference]`.
- **Safety monitoring:** GPT-6 Astra-class models can pause a task asynchronously; in Codex CLI "the task ends" without findings/resume ([approvals & security](https://learn.chatgpt.com/docs/agent-approvals-security.md#safety-monitoring-and-paused-tasks)). The adapter needs a distinct end state for "paused by OpenAI" `[inference]`.

## 5. Licence and terms

| Item | Finding | Source |
|---|---|---|
| Code licence | Apache-2.0; includes patent grant | [LICENSE](https://raw.githubusercontent.com/openai/codex/main/LICENSE), [license.md](https://raw.githubusercontent.com/openai/codex/main/docs/license.md) |
| Contributing | CLA required for contributions to openai/codex | [CLA.md in docs/](https://github.com/openai/codex/blob/main/docs/CLA.md) |
| Redistribution in a Nix toolset | allowed by Apache-2.0; nixpkgs package exists (0.156.1), not unfree-flagged `[unverified: license meta not read]` | local `nix search` |
| API key use | standard API pricing and API org data policy; "use API key authentication for programmatic Codex CLI workflows, such as CI/CD jobs. Don't expose Codex execution in untrusted or public environments." | [auth](https://learn.chatgpt.com/docs/auth.md) |
| Plan login on own remote host | Codex Local usage, no separate allowance | help article above (search summary) |
| Plan login in automation | "API keys are the right default"; `auth.json` flow for trusted private runners only, "Do not use this workflow for public or open-source repositories" | [non-interactive](https://learn.chatgpt.com/docs/non-interactive-mode.md#authenticate-in-automation), [CI/CD auth](https://learn.chatgpt.com/docs/auth/ci-cd-auth.md) |
| App-server auth in hosted services | not permitted; use Sign in with ChatGPT | [app-server](https://learn.chatgpt.com/docs/app-server.md#auth-endpoints) |
| SIWC terms (if atrium holds the token) | as in #90 section 3.5 | [#90 sheet](https://github.com/jeiang/atrium/blob/research/pi-providers/research/pi-providers.md) |

Compared with Claude Code (#32 section 4), Codex's first-party client with a plan login on the owner's host carries less ToS tension than pi with a Claude subscription, and is comparable to Claude Code's own supported route. The "public/open-source repos" warning is about repository code reading `auth.json`; atrium runs agent work on repos of the owner's choosing, so a public repo checked out into a subscription-mode sandbox would be outside OpenAI's stated guidance `[inference]`. A per-environment rule "plan login only for private repos" would honour it.

## 6. Interaction with the #46 decision (own adapter, ACP later)

- `zed-industries/codex-acp` (the ACP bridge for Codex) is **archived** (`archived: true`, last push 2026-07-22, last release v0.16.0 2026-06-08, licence NOASSERTION) per the [GitHub API](https://api.github.com/repos/zed-industries/codex-acp). The Codex docs I read do not mention ACP at all (grep of the mirrored pages). So "revisit ACP if a third agent appears" does not get cheaper with Codex: the native route is app-server, and that is what the adapter should target.
- Codex app-server is not ACP-shaped, but the three interactions the adapter needs map one-to-one: prompt (`turn/start` | exec argv), stream (`item/*` | JSONL), stop (`turn/interrupt` | SIGINT), plus optional steer and approval callbacks. #27 already reached this conclusion ("pi RPC, Codex app-server, opencode HTTP... all agent as a subprocess with a JSON event stream and an approval callback").

## 7. Adapter shape that keeps Codex a cheap add-on

`[inference]` Define the atrium-side interface by capability, not by agent:

| Adapter capability | pi | Claude Code | Codex |
|---|---|---|---|
| start(task, model, effort, cwd, env) | `--mode rpc` + `prompt` | `claude -p --output-format stream-json` | `codex exec --json` or app-server `thread/start` + `turn/start` |
| events → `text`, `tool_start`, `tool_done`, `run_done` | `message_update`, `tool_execution_*`, `agent_settled` | `stream_event`, `assistant`/`user`, `result` | `item/agentMessage/delta`, `item/started`/`item/completed`, `turn/completed` |
| stop | `abort` / close stdin | SIGINT then wait `result` | `turn/interrupt` / SIGINT |
| steer / follow-up | `steer`, `follow_up` | restart with `--resume` | `turn/steer` (app-server), `exec resume` |
| resume | session file | `--resume <id>` | `thread/resume`, `exec resume <id>` |
| usage | `usage.cost.total` (dollars, estimated) | `total_cost_usd` | tokens only; priced by atrium |
| spend cap | atrium abort-on-cost | `--max-budget-usd`, `--max-turns` | atrium-only (interrupt) |
| approvals | none | `-p` + permission mode / allowlist | `approval_policy = never` in the VM, so none |
| auth in VM | gateway placeholder (API) or SIWC login | gateway placeholder (API) or `setup-token` / login | gateway placeholder (API), device-code login, or SIWC token env |
| typed final answer | no | `--json-schema` | `--output-schema` |

Everything in the Codex column fits the two-implementation adapter; the only addition is "cost reported in tokens, not dollars", which the #90 table and an adapter field `cost_usd | null` plus `tokens{}` already cover (pi on a plan route has the same property).

## 8. Does it add anything pi plus Claude Code do not?

| Dimension | Verdict |
|---|---|
| OpenAI models | **pi already does this**: `openai` provider, API key through the gateway or SIWC login (#90). Codex adds nothing in reach. |
| Model-tuned harness | `[inference]` GPT models are trained against Codex's tool shapes (`apply_patch`, shell) and Claude against Claude Code's; pi's generic read/bash/edit/write is a lowest common denominator. Plausible quality gain for OpenAI models; unmeasured. |
| Plan-based billing for a second vendor | **Yes**: a ChatGPT plan is a distinct allowance from the Claude plan, with an official first-party CLI, documented headless login (device code), plan limits readable over app-server. Closest to a "clean" subscription path. |
| Provider breadth | **No**: Codex speaks only the Responses wire format. No Anthropic, no local llama.cpp chat completions; `--oss` targets local providers (LM Studio crate present) but I did not verify llama.cpp compatibility `[unverified]`. pi covers local models (#32 candidate 7). |
| Built-ins worth having | `exec --output-schema` typed result, `review/start` reviewer, subagents ("custom agents"), `/goal`, skills in SKILL.md format, MCP client with `required = true` ([#27 section 3.2](https://github.com/jeiang/atrium/blob/research/coding-harnesses/research/coding-harnesses.md)). Claude Code covers most; pi lacks subagents by design. |
| Protocol quality | Best of the three for embedding: schema generator, steer, interrupt, resume, rate-limit and token-usage notifications, version-pinned types. |
| Footprint | ~112 MB x86_64 musl tarball (`codex`), ~87 MB app-server-only; release cadence several tags a week; nixpkgs seven minors behind ([releases](https://github.com/openai/codex/releases/latest)). Heavier than pi (npm package plus Node) in disk, similar in churn. |
| Cost to atrium | A third event parser with recorded fixtures (#32 candidate 6), a third credential story and settings page entry, a third pinned toolset, a third hostile-repo checklist, and a gateway `/models` shim. |

Net: Codex is the right answer to "I want a ChatGPT plan to do coding work" and "I want GPT models inside their native harness". Neither is in the plan's stated aim; the owner's stated aim is token savings via harness tools, which all three agents get through the same MCP/harness endpoint (#32 candidate 3).

## 9. Options and trade-offs

| Option | Pros | Cons |
|---|---|---|
| **A. Skip Codex in v2; keep pi + Claude Code; keep adapter agent-agnostic** | Least work; both agents already researched; OpenAI models reachable via pi | No ChatGPT-plan coding path with a first-party client; revisit later |
| **B. Add Codex as optional toolset after #46's two ship (exec first, app-server later)** | Cheap if the adapter is capability-based (section 7); gives plan and GPT-native harness; no approval callbacks needed in the VM | One more parser/credential/pinning surface; gateway `/models` shim; no dollar cost; plan-login-on-public-repo caveat |
| C. Codex as a first-class launch agent (app-server) | Best protocol, richest controls | Largest scope; pi plus Claude Code already satisfy the plan's launch requirements; heavy binary; weekly churn |
| D. Reach Codex through ACP | Neutral protocol | The only bridge is archived; no ACP in Codex docs; not viable |

## 10. Recommendation (tied to the plan)

1. **Option B, deferred.** Phase B ships pi + Claude Code per #46. Reserve a `codex` toolset slot and a `code_agent` `agent` enum value, but do not build it in the first wave. Trigger to build: the owner wants ChatGPT-plan coding or OpenAI-model coding that pi underdelivers on.
2. **When built:** drive `codex exec --json` for one-shot delegated tasks first (parity with Claude `-p`), add app-server only if steer/interrupt/rate-limit display justify the second code path. Config generated by atrium (section 3.2 and 4), `CODEX_HOME` on the per-instance volume, one plan-mode sandbox at a time, `danger-full-access` + `never`, `web_search = "disabled"`, telemetry and updater off, no trusted projects.
3. **Credentials:** API key via the existing `/gw/openai` route as the default (meterable, key never in VM); plan login via device code as the opt-in (a documented carve-out like Claude's subscription, per #46 review addition). Defer atrium-held SIWC token with #90's S-B.
4. **Keep the adapter interface capability-based** (section 7) so the third implementation is parser + launch config, not new tool semantics. Make `cost_usd` nullable and add `tokens{}` now, since pi-on-plan and Codex both need it.
5. **Do not add ACP** on Codex's account.

## 11. Risks and unverified items

1. **Gateway `/models` and catalog behaviour.** I did not run Codex against a stub Responses endpoint. Unknown whether Codex refuses to start or degrades without a `models` array, and whether `gpt-6.1-sol` via a custom provider gets the right context window `[unverified]`.
2. **Required egress domains** for plan login and for Codex's other background calls (analytics, update check, plugin marketplace, remote catalog) are not published as a list. Must be measured with the proxy's deny log `[unverified]`.
3. **Terms for orchestrated plan use**: OpenAI describes interactive local/remote use and CI on private runners; it does not say whether an owner's self-hosted agent harness may start the CLI on a schedule. Needs a written answer from OpenAI or a conservative rule (chat-initiated only) `[unverified]`.
4. **User-namespace availability** in the guest kernel; irrelevant under `danger-full-access`, but relevant if the owner wants Codex's own sandbox as defence in depth.
5. **Preview/beta surfaces**: device-code login is "beta"; app-server WebSocket is "experimental and unsupported"; `features.rollout_budget` is under development; SIWC open-source sharing is preview (#90). Pin versions and gate by a fixture CI job.
6. **Safety monitoring pause** can end a task with no resume in the CLI; adapter needs an explicit end state.
7. **Weekly churn**: `codex mcp-server` was removed, `untrusted` retired, `--full-auto` dropped. Treat the generated JSON schema as the contract and run golden-stream tests against the pinned version.
8. **`danger-full-access` removes `.git` read-only protection**; protection must live in the manager.
9. **Default `web_search` becomes `live`** under full access; easy to miss.
10. I did not read OpenAI's consumer Terms of Use for any "automated or non-human means" language (the Claude equivalent is the deciding clause in #32) `[unverified]`.

## 12. Candidate new tickets

1. **Codex toolset and `code_agent` adapter implementation**: build, phase B (after #46's two), area sandbox/assistant. Question: implement the third adapter (exec first) behind the capability interface; generated `config.toml`; nullable `cost_usd` + `tokens{}`; fixtures from `generate-json-schema`.
2. **Codex gateway compatibility spike**: research/spike, phase B, area sandbox/budget. Question: run Codex 0.16x against a stub `/gw/openai/v1` (`responses` + `models` shape) and record `/models` need, catalog metadata for `gpt-6.1-sol`, SSE and usage parsing, and every outbound domain Codex contacts with analytics/updates/feedback off.
3. **OpenAI plan-login credential policy for coding agents (Codex device code, pi SIWC, atrium SIWC)**: decision, phase B, area security/legal. Question: which routes are offered, private-repos-only rule, chat-initiated-only consent, one-plan-sandbox-at-a-time, and whether to ask OpenAI about orchestrated use; merges with #90's S-A/S-B decision.
4. **Plan-allowance display from app-server**: design, phase B, area UI/budget. Question: show `account/rateLimits/read` and `account/usage/read` for plan-mode Codex runs and decide whether atrium may gate scheduled runs on remaining allowance.
5. **Manager-side `.git` and hook protection when agents run unsandboxed**: design, phase B, area sandbox/security. Question: with pi and Codex in full-access mode, how does the manager keep `.git`, hooks and config directories write-protected (mount flags, overlay, post-run diff of `.git/hooks`)?
6. **Codex-specific hostile-repo posture**: design, phase B, area security. Question: trust-level handling, `--ignore-rules`, repo hooks/skills/AGENTS.md size cap, project config precedence; merges into #32 candidate 8.
7. **Agent selection policy on the new-chat page**: design, phase B, area UI/assistant. Question: how the user picks agent vs model vs credential (API vs plan) when OpenAI and Anthropic models are both reachable through two or three agents; defaults per environment.
8. **Toolset packaging for codex**: build, phase B, area sandbox/nix. Question: pin upstream release tarball (not nixpkgs 0.156.1), `CODEX_HOME` on persistent volume, bundled `bwrap` or not, updater/telemetry off, size budget (~112 MB) in the rootfs.
