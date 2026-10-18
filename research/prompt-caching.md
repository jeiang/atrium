# Prompt cache breakpoints across agent flows (ticket #64, slug `prompt-caching`)

Researched 2026-10-17 from Anthropic's current docs (platform.claude.com `.md` mirrors), Claude Code docs, pi docs and the Go SDK source. Model and price facts come from the closed fact sheet `research/claude-models` (ticket #26) and are not repeated; only the cache-specific rows are. `[inference]` marks my reading, not a doc statement. atrium facts cite `path:line` on `origin/main` (c221de8).

## 1. Answer in brief

- **Where the money is.** Over a run, 80-98% of prompt tokens are re-reads of history. Cache reads cost 0.05x base on Opus 5.5 and Sonnet 5.5, 0.1x on Haiku 5.5 ([pricing](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#pricing)). My model (section 5) gives 65-93% savings against an uncached loop, depending on the flow. The tools and system prefix is only ~10-15k tokens `[inference]`, worth $0.002-$0.05 per cold start. So **the history breakpoint and the TTL decide the bill; the exact placement of the prefix breakpoints barely does.**
- **atrium already caches correctly in the common case.** Four breakpoints (tools, SYSTEM, last stable system block, last user message) in the right order, 5m or 1h TTL for the prefix, per-message date stamps so history never changes (`loop.py:61-85,100-113,138-143`). Metering reads the 5m/1h split (`usage.py:20-31`).
- **Five real defects/risks** in the current design (section 3): (1) `cache_ttl_chat=1h` does not reach the history breakpoint, which is the one that matters; (2) the profile and skills index sit in `system`, so any `profile_set` (including the nightly run's) invalidates every open thread and strips its thinking; (3) the client-side preflight compaction at 95% is a cold, uncached call over up to ~950K tokens; (4) mid-thread effort, project, sandbox switch and module toggles each rewrite the whole cache; (5) server-side tool clearing and the 20-block lookback can silently turn warm turns into full rewrites.
- **Recommendation (section 4).** Freeze tools, system, effort, beta set and model per thread at creation; deliver later changes as appended messages (mid-conversation system messages, per-message effort, `tool_addition`); re-snapshot only at cache-cold boundaries. Use **1h TTL on every breakpoint for chat and any run that waits on slow tools** (sandbox, subagents), **5m for cron, nightly and subagent threads**. Layout: 2 static prefix breakpoints + 1 moving "previous tail" + 1 automatic tail = 4. Compact with the *same prefix* so the summarizer reads the cache. Never change model, thinking, effort, `tool_choice`, speed, or the beta-header set mid-thread without telling the operator what it costs.
- **Operator exposure (section 8).** Expose TTL policy per flow, cache-aware idle/cold behaviour, hit-rate and savings display, and a cache-diagnostics debug toggle. Do not expose breakpoint count, automatic vs explicit, or beta headers.

## 2. Mechanics that matter (primary sources)

| Fact | Source |
|---|---|
| Prefix order is `tools` -> `system` -> `messages`; the hash is cumulative, so any change at or before a breakpoint changes it. Writes happen only at breakpoints; reads look back up to 20 positions for an entry an earlier request wrote. Consecutive `tool_use` blocks count as one position, as do consecutive `tool_result` blocks. | [how automatic prefix checking works](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#how-automatic-prefix-checking-works) |
| Max 4 breakpoints. Automatic caching (top-level `cache_control`) uses one slot, moves to the last cacheable block, and is a 400 if 4 explicit ones already exist or if the last block has a different TTL. | [automatic caching edge cases](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#edge-cases) |
| Breakpoints are free; you pay for writes (5m 1.25x, 1h 2x base), reads (0.1x; 0.05x on Opus/Sonnet 5.5; 0.025x on Fable 5.1) and uncached input. | [cost](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#understanding-cache-breakpoint-costs), [pricing](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#pricing) |
| TTL is measured from the **start** of the request that wrote or read the entry; each read refreshes it free. 1h entries must come before 5m ones in a request. A write by a request becomes readable only after its response begins, so parallel siblings both write. | [how it works](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#how-prompt-caching-works), [mixing TTLs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#mixing-different-ttls), [limitations](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#cache-limitations) |
| Minimum cacheable prompt: 512 tokens on Fable 5.1, Opus 5.5, Sonnet 5.5, Haiku 5.5 (4,096 on Haiku 4.5/Opus 4.5-4.6). Shorter prompts cache nothing and return no error. | [cache limitations](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#cache-limitations) |
| Invalidation matrix: tool definitions -> everything; web-search or citations toggle -> system + messages; `tool_choice`, images present/absent -> messages; thinking config and `output_config.effort` -> messages always, tools/system on models that render the config ahead of them; speed -> system + messages. Setting effort to the model default equals omitting it. | [what invalidates](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#what-invalidates-the-cache), [thinking and caching](https://platform.claude.com/docs/en/build-with-claude/thinking#thinking-and-prompt-caching) |
| **Per-message effort** (beta `mid-conversation-output-config-2026-07-01`): an effort change carried in a `role: system` message leaves the prefix intact. **Mid-conversation system messages** (no beta header; Fable 5.1, Opus 5.5, Sonnet 5.5, Haiku 5.5; not Sonnet 5) add instructions without touching `system`. **`tool_addition`/`tool_removal`** (beta `inline-tools-2026-09-15`) change tools without touching `tools`. | [effort row](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#what-invalidates-the-cache), [mid-conversation messages](https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages) |
| Thinking blocks of earlier turns are kept by default on Opus 4.5+, Sonnet 4.6+ and Haiku 5.5, stay in the cached prefix and **count as input tokens when read**. A dropped block (replayed to a model that cannot read it) changes the prefix from that position on. | [caching with thinking blocks](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#caching-with-thinking-blocks) |
| Server tools (web search, fetch, code execution): the API auto-places a **5m** breakpoint on each server-tool result inside the request, so `ephemeral_5m` writes appear even when all your markers are 1h. Toggling web search/fetch invalidates system + messages. | [tool use with caching](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching#server-tool-results-are-cached-automatically) |
| `defer_loading` (tool search): deferred tools are not in the prefix; discovered ones append as `tool_reference` blocks, so the cache survives. | [defer_loading](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching#defer_loading-and-cache-preservation) |
| Pre-warm: `max_tokens: 0` writes the cache and returns an empty response with full `usage`; put the breakpoint on the shared prefix and match thinking/effort; rejected with `stream: true`, `thinking.type: enabled`, structured outputs, forced `tool_choice`, and inside Batches. | [pre-warming](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#pre-warming-the-cache) |
| **Cache diagnostics**: pass `diagnostics: {previous_message_id}`; the response says where the prefix diverged (`model_changed`, `system_changed`, `tools_changed`, `messages_changed`, `previous_message_not_found`, `unavailable`) with `cache_missed_input_tokens`. Differences in `tool_choice`, `thinking`, `context_management`, `output_config`, or the active `anthropic-beta` set report `unavailable`, which tells us those are prompt-affecting. GA, no beta header needed. | [cache diagnostics](https://platform.claude.com/docs/en/build-with-claude/cache-diagnostics) |
| Context editing: **tool-result clearing invalidates the cached prefix** at the point of clearing (use `clear_at_least` so it is worth it); clearing thinking blocks invalidates from that point; keeping them preserves the cache. | [context editing](https://platform.claude.com/docs/en/build-with-claude/context-editing) |
| Compaction: usage must be summed over `usage.iterations` (the compaction iteration is not in top-level `input_tokens`/`output_tokens`; each iteration carries its own cache statistics). Put a breakpoint on the system prompt so the summary is the only new write; `cache_control` may sit on the compaction block. On-demand compaction (`compact-2026-09-04`) cannot share a request with `context_management`. | [threshold usage](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold#understanding-usage), [cache section](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold#prompt-caching), [on-demand limits](https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand#how-it-fits-with-the-rest-of-the-api) |
| Caches are isolated per workspace (and per organization); never shared across them. | [cache storage](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#cache-storage-and-sharing) |
| JSON key order must be stable (Go and Swift randomize map key order in some paths). | [troubleshooting](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#troubleshooting-common-issues) |
| Cache reads do not count toward ITPM rate limits; writes do. | fact sheet `claude-models` section 4 ([rate limits](https://platform.claude.com/docs/en/api/rate-limits)) |

Go SDK v1.82.0 (2026-10-15) has every field needed: `BetaMessageNewParams.CacheControl` (top-level automatic), `.Compaction`, `.Diagnostics`, `BetaCacheControlEphemeralParam{TTL}` on system text, tools and content blocks, and `Usage.Iterations` (`betamessage.go:25738,25748,25757`, [source](https://github.com/anthropics/anthropic-sdk-go/blob/main/betamessage.go)). The Go SDK sorts struct fields and map keys when encoding (`research/anthropic-go-sdk` note 2), so request bytes are deterministic run to run. `[inference]` the API keys on content, not on key order, so this does not break caching; it only matters to the extent tool `input_schema` maps are built with a randomizing path, so keep schemas as `json.RawMessage`.

## 3. What atrium does today, and what is wrong with it

Request builder (all `src/atrium/agent/loop.py`):

- `CACHE_CONTROL`, `CACHE_TTL_KEY="cache_ttl_chat"`, `CACHE_TTLS=("5m","1h")` (`:24-26`). `prefix_cache_control` returns 5m unless `kind == CHAT` and the setting is `1h` (`:61-63`).
- Breakpoint 1: last tool in `tools` (the server tool `web_search` is appended last for CHAT/CRON, so it carries the marker) (`:100-113`, `registry.py:server_tools_for`). Breakpoint 2: the SYSTEM text block. Breakpoint 3: the last stable system block (profile, skills index, role addendum). The project block and sandbox prompt follow **uncached** (`:75-85`). Breakpoint 4: the last block of the last user message, always `{"type":"ephemeral"}` = 5m (`:138-143`).
- Dates are appended to each operator message from that run's start, so earlier turns never change (`:117-135`).
- Context management: `clear_tool_uses_20250919` triggered at 18.75% of the thread limit, keep 3, `clear_at_least` 5%, `recall` excluded; `compact_20260112` at 87.5% with `pause_after_compaction` (`context.py:147-165`). Client preflight at 95%: `client_compact` (`context.py:596-620`).
- `usage` rows keep `cache_write_5m`, `cache_write_1h`, `cache_read` and cost per call (`usage.py:20-56`); the dashboard shows `cache_read_share` (`usage.py:85-95`). Runbook cache section: `docs/runbook.md:238-242`.

Flow shapes that matter for caching:

- Chat: one thread, many runs; each human turn is a tool loop of up to `MAX_STEPS=24` requests seconds apart (`loop.py:18`). Tool results over `STUB_TOKENS=2000` are stubbed (`context.py:9,124-130`), so growth per step is mostly <=2.5k tokens.
- Cron and nightly: **a new thread per run** (`scheduler.py:479-495`); only the tools+system prefix can be shared across runs, and only within the same `kind` because each kind has its own tool list (`tools_for(kind)` filters by `contexts`, `registry.py:12-14`). Nightly is `daily 02:30` (`scheduler.py:26`); at most 20 active cron jobs (`:23`).
- Subagents: own thread per call, tool allowlist plus `DIGEST_RULES` as system extra, `CHILD_MAX_COST_USD=0.25`, 12 steps, 2 concurrent (`subagents.py:9-11,27-33`). The parent waits for the children inside one tool call.

Defects and risks, in order of dollar impact:

1. **The TTL setting covers the wrong breakpoint.** With `cache_ttl_chat=1h` the tools/system/profile breakpoints get 1h but the history breakpoint stays 5m (`loop.py:142`). After a 6-minute pause the 1h prefix (~12k) hits and the history (often 50-400k) rewrites in full. The setting gives almost none of the benefit it was added for. Valid today (1h before 5m), just ineffective.
2. **Profile/skills edits invalidate every thread.** `system_blocks` renders `profile_block` and `skills.index_block` from live DB rows on every run (`loop.py:75-85`, `tools/profile.py:20-28`, `skills.py:72-79`). `profile_set` is a chat and nightly write tool, so one write changes the system prefix of every open thread: the next turn rewrites the whole history, **and** `active_messages(..., system_sig)` strips thinking from every older assistant turn because the signature changed (`context.py:264-287`, `loop.py:89-93`). The second effect is a further prefix change and loses reasoning. The same applies to subagent edits (`research.with_agents` renders enabled roles into the `research` tool description, `tools/research.py:40`): editing a role changes the tools prefix of CHAT/CRON/NIGHTLY.
3. **Client preflight compaction is a cold call.** `client_compact` sends `SUMMARY_SYSTEM`, no tools, tool blocks flattened by `shrink`, `effort=low`, no `cache_control` (`context.py:63,562,596-611`). Nothing shares a prefix with the thread, so up to ~95% of the limit is billed as plain input: ~950K tokens = $3.80 on Opus 5.5, $1.90 on Sonnet 5.5, ~$0.48 on Haiku 5.5 (>100K tier, $0.50/MTok). Claude Code does the opposite: same system, tools and history plus an appended instruction, so a warm cache is read ([compacting](https://code.claude.com/docs/en/prompt-caching#compacting-the-conversation)). Today it only fires if the server compaction at 87.5% did not (e.g. attachments), so it is rare, but the 1M window makes each occurrence expensive.
4. **Mid-thread setting changes rewrite the cache.** `chat_options` lets effort change on an existing thread (`web/chat.py:167-170`, `threads.set_options`), which per the matrix invalidates messages. Toggling `code_sandbox` adds the `code_execution` server tool and a system note (`loop.py:100-113`, `files.py:43-47`), which invalidates tools -> everything. Branch/fork copies effort and sandbox flag (`service.py:387`).
5. **Context-edit and lookback hazards.** (a) `clear_tool_uses` invalidates the cache from the first cleared block on every batch (`context-editing` doc). At Sonnet/Opus cache-read prices it never pays back in dollars (section 5.3); it exists for quality and for Haiku's >100K tier. (b) A single web search response can contain many `server_tool_use`, `web_search_tool_result` and citation-split `text` blocks. If one assistant turn adds more than ~19 positions after the previous request's breakpoint, the 20-position lookback cannot find the previous write and the whole history is rewritten `[inference: block counts of search responses not measured]`. The API's own 5m server-tool breakpoint may rescue some of these cases, but nothing documents that it does.
6. **Tool churn.** `run_code.with_stubs` renders every other tool's stub into the `run_code` description per kind (`registry.py:11`), so any tool change also changes that definition. Sorted-by-name tool order (`tools/__init__.py:26`) means toggling a module removes tools from the middle of the array and invalidates from there on. Server-tool auto-breakpoints always write 5m even with a 1h setting; harmless but visible in the usage split.
7. **Metering gap (cross-ref `claude-models` gap 1).** Compaction iterations are not summed from `usage.iterations` (`grep iterations src/atrium/agent` has no match), so server compaction cost is invisible. Rates are hard-wired to Haiku (`pricing.py:7-10`).
8. **Baseline is unknown.** I did not have the production `usage` table. `usage.summary` already computes `cache_read_share` per month and `by_kind` exists (`usage.py:98`), so the operator can read the current share per kind off the Usage page; no number in this sheet is a measured atrium number.

## 4. Per-flow breakpoint plan

### 4.1 Layout (all flows)

Request order stays `tools` -> `system` -> `messages`. Four slots:

| Slot | Position | Purpose | TTL |
|---|---|---|---|
| BP1 | end of **static core**: last tool + `SYSTEM` core text (identical for every thread of one kind and module set) | Cross-thread/cross-run sharing of ~12k tokens; survives profile and project changes | flow TTL |
| BP2 | end of the **frozen per-thread system snapshot** (profile, skills index, role addendum, project block, sandbox/environment notes) | Compaction and branch rewrites keep this entry; matches the [compaction guidance](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold#maximizing-cache-hits-with-system-prompts) | flow TTL |
| BP3 | **previous request's tail** (the block that carried BP4 on the last request of this thread) | Robust against the 20-position lookback: the lookback from BP3 finds an entry that the last request wrote exactly there, regardless of how many blocks the last turn added | flow TTL |
| BP4 | **tail**: automatic top-level `cache_control` (last cacheable block) | Writes the new entry every step | flow TTL |

Notes:

- Slots 1-2 are a deliberate downgrade from today's "tools bp + SYSTEM bp + profile bp": tools and SYSTEM are one static prefix, and tools never change independently of the system text once frozen. That frees a slot for BP3. If the system text becomes small (<512 tokens, i.e. only a few tools), BP1 is silently ignored by the API, which is harmless `[inference: the minimum applies to the cumulative prefix]`.
- Mixed-TTL rule: BP1, BP2 and the tail may share one TTL, or BP1-3 may be 1h and BP4 5m, never the reverse. Use a single TTL per thread to avoid both the 400 and the billing split.
- Automatic vs explicit: use automatic for the tail so compaction blocks, `pause_turn` continuations and empty-text edge cases are handled by the API; use explicit markers for BP1-3. Caveat from the docs: automatic caching lands on the *last block*; atrium's last block is stable once sent (date stamp is appended once and never rewritten), so it is safe. Any per-request varying block (e.g. a queue-injected hint) must go **before** the tail or into a mid-conversation system message, never after it.
- Keep `thinking`, `output_config`, `tool_choice` (never set; Opus/Sonnet 5.5 reject forced values anyway, `claude-models` section 3), `context_management`, the beta-header set and speed constant for the thread's life. `[inference]` the header set and `context_management` change the hash even though not listed in the invalidation table: the diagnostics page lists them as prompt-affecting parameters. Verify with diagnostics before relying on it.

### 4.2 Interactive chat

- **TTL: 1h on BP1-BP4** by default (operator-editable). Break-even (section 5.2) is 5-17% of turns with a 5-60 minute gap; a personal assistant that the operator reads and answers clears that bar for any thread >30k tokens `[inference on usage pattern]`. Below ~15k tokens everything costs cents either way.
- Tool loop steps are seconds apart, so reads refresh the entry; the cache stays warm for the whole run.
- Freeze per thread: model, effort, thinking mode, context size, project, sandbox environment and repo/branch binding, tool list (module set), beta set, profile/skills snapshot. Changes go in as appended messages (section 6).
- Show a "cache warm for N more minutes" hint (last request start + TTL, known client-side) and the estimated cost of a model switch (`prompt_tokens x (new write rate - ...)`), as Claude Code asks for confirmation while the cache is warm ([model switching](https://code.claude.com/docs/en/prompt-caching#switching-models)).
- Expected hit rate (token-weighted `cache_read / prompt`, section 5.1): ~97% on a warm 60k Sonnet run, ~82% on a cold-start run (the first step of a returning thread pays the full write), ~98% / ~87% on Opus at 150k. Typical blended chat month `[inference]`: 88-95%.

### 4.3 Scheduled (cron) runs

- New thread per run; **TTL 5m**; BP1 + BP2 + BP3 + tail all work within the run. Cross-run sharing exists only for the 10-14k prefix (same kind, same module set, same profile snapshot) and only if another run of that kind started in the last 5 minutes.
- Why not 1h: cron jobs fire at fixed times, mostly >1h apart, so a 1h write (2x) is never read by the next run; within a run the steps are seconds apart. Exception: a job whose tool calls routinely block >5 minutes (sandbox, subagent) uses 1h (section 4.5).
- Do not engineer cross-job warming. A shared prefix saves ~$0.0013 per Haiku run and ~$0.03 per Sonnet run. If several jobs share a due minute, run them sequentially (the scheduler claims all due jobs at once, `scheduler.py:~430-450`; the first call of each concurrently-started run writes separately) only if it falls out of the Go scheduler design for free.
- Expected hit rate: ~79% cold start, ~90% when the prefix is warm, on a 6-step Haiku run (prefix-dominated; the absolute cost is $0.003-$0.004). The unattended budget guard (80%) is unaffected.

### 4.4 Nightly run

- `daily 02:30`; one run per day; **TTL 5m**; the prefix will always be cold because the previous run was 24h ago, so one cold write of ~13k tokens (~$0.0016 on Haiku) is unavoidable.
- A 24-step run of +2k per step: ~93% read share, ~82% saving vs uncached (`$0.015` vs `$0.086` on Haiku).
- Nightly writes Notion and **profile** entries (`prompts.py`: "The nightly run may also write Notion and profile entries"). With a frozen per-thread profile snapshot (section 6) those writes no longer invalidate chat threads. This is the biggest nightly-related cache win.
- Batch API (50% off) does not apply: the nightly run is an interactive tool loop, and batch cache hits are best-effort ([batch FAQ](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#faq)).

### 4.5 Research subagents

- Own prefix per role: tools (2-4) + SYSTEM + `DIGEST_RULES` + role prompt + profile/skills. ~2-4k tokens, above the 512 minimum. **TTL 5m**.
- Within a child loop (<=12 steps, $0.25 cap) hit share is ~85% on a 3k prefix, +2k/step. Dollar effect is cents.
- Parallel siblings (2 slots) of the same role both write the prefix because the entry is readable only after the first response begins ([limitations](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#cache-limitations)). At 3k tokens this is $0.0004 on Haiku; do **not** add a stagger. (Claude Code staggers fan-outs by up to 5s because its prefixes are large, `CLAUDE_CODE_WORKFLOW_PREFIX_STAGGER_MS`, [env vars](https://code.claude.com/docs/en/env-vars).)
- **The parent matters more.** While a child runs (a minute or more with web searches), the parent's own entry is not refreshed. If delegation plus other work exceeds 5 minutes between two parent requests the parent history misses entirely. Rule: **a run that may delegate or call sandbox tools uses 1h** on the parent's breakpoints.

### 4.6 Coding agents (sandbox)

Three different cases; they share the rule "long tool waits need 1h".

1. **atrium's own agent using typed sandbox tools** (the plan's primary path). Same layout as chat. The tool list must be fixed per thread: do not add/remove coding tools when a sandbox suspends/resumes (`research/coding-tools` R5). Use **1h**: a `bash` call running a build or test suite for >5 minutes would otherwise make the next step a full rewrite. At 40 steps of +4k tokens on Opus, 1h costs $2.16 vs $1.63 for 5m if every step is within 5 minutes and no gap occurs; one 6-minute tool wait at ~100k history adds $0.50 (5m rewrite) while 1h adds $0.30 (premium on the already-written part) `[inference on the model in section 5]`. Keep the existing stub threshold so sandbox output joins history in <=2k-token chunks (`context.py:9`).
2. **Claude Code in the sandbox.** CC manages caching itself: main conversation 5m by default on an API key, **1h by default on a subscription within plan usage**; `CLAUDE_CODE_PROMPT_CACHE_TTL=1h|5m` (main conversation) and `CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL` (subagents, workflows, compaction, titles) override it; `FORCE_PROMPT_CACHING_5M`, `ENABLE_PROMPT_CACHING_1H`, `DISABLE_PROMPT_CACHING*` also exist ([TTL buckets](https://code.claude.com/docs/en/prompt-caching#which-ttl-each-request-gets), [env vars](https://code.claude.com/docs/en/env-vars)). Its cache is scoped to machine + directory because the system prompt embeds paths and the git status snapshot ([cache scope](https://code.claude.com/docs/en/prompt-caching#cache-scope)). Recommendation: the sandbox manager sets `CLAUDE_CODE_PROMPT_CACHE_TTL=1h` for API-key runs (atrium's key, injected by the egress proxy per `research/pi-claude-code-sandbox`), and mounts repos at a fixed path (e.g. `/work/<repo>`) so successive sandboxes of one environment share CC's system prompt entry. For subscription runs the default is already 1h and there is no dollar cost to meter (plan windows only).
3. **pi.** `cacheWarming` (`streaming` default, `idle`, `off`) refreshes eligible provider caches during active runs when pi estimates >= $0.05 in avoided miss cost; the model catalog declares `promptCache: {short: 300, long: 3600}` ([settings](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/settings.md), [models](https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/docs/models.md#prompt-cache-lifetimes)). Long retention is selected with `PI_CACHE_RETENTION=long` per a search result I could not confirm in pi's own docs (secondary source; treat as `[unverified]`). pi's summary requests disable cache writes (`research/coding-harnesses` section 3.3). Cost reporting differs per tool; whichever path, atrium must read the tool's reported usage (cache read/write split) into its own `usage` rows to stay in the budget.

Usage metering for CC/pi runs is outside this ticket (see `research/metering-outside-loop`).

### 4.7 Compaction and context editing

| Mechanism | Cache effect | Plan |
|---|---|---|
| Server threshold compaction `compact_20260112` (atrium today) | The compaction iteration is billed separately; BP2 after the system keeps system+tools; the first request after it writes the summary and the kept turns once. `cache_control` may sit on the compaction block. | Keep as is for Phase A. Sum `usage.iterations` for billing (`claude-models` gap 1); iteration entries carry their own cache statistics, so cost can be priced per iteration. Verify with a canary whether the compaction iteration reads the thread's cache `[inference: docs do not say]` |
| On-demand compaction `compact-2026-09-04` | Separate request; cannot be combined with `context_management` on one request; kept recent turns keep their thinking valid ([compaction](https://platform.claude.com/docs/en/build-with-claude/compaction)) | Phase B target. Send the summary request with **the same tools, system, thinking, effort, betas and history as the thread** plus the instruction, like CC, so it reads the warm cache. Setting effort to `low` for the summary (today) changes the prefix and loses the read. |
| Client preflight `client_compact` | Cold, uncached, up to ~950K tokens (section 3 defect 3) | Replace with the same-prefix summarizer above; if the thread is cold anyway, compare cost (section 5.3: compacting at a cold boundary costs 1.0x H input, continuing costs 1.25x H write plus ongoing reads) |
| `clear_tool_uses` (server) | Each batch rewrites from the first cleared block | At Opus/Sonnet 5.5 read prices a batch clearing 10K tokens from a 100K history costs ~$0.23 (Sonnet) to ~$0.45 (Opus) in rewrite and saves ~$0.001-$0.002 per later request; payback ~100-225 requests. Keep it for Haiku (>100K tier) and for quality (context rot), with a much larger `clear_at_least` and trigger on Sonnet/Opus. Better: apply edits only at **cache-cold boundaries** (section 6), where the rewrite is free |
| Context ledger (drop/fold/pin, `context.py`) | Applied at the next operator message or when >=20K tokens are saved (`COMMIT_SAVINGS`); changes the message prefix from the edit point | Already aligned with "edit when you must rewrite anyway". Make the mid-run commit conditional on the cache being cold or the saving exceeding the rewrite cost. |
| Thinking-block clearing (`clear_thinking_20251015`) | Invalidates from the point of clearing; keeping blocks preserves the cache | Not used by atrium; do not add. Thinking is kept by default on all three 5.5 models |
| Compaction block with `cache_control` | Cheap one-time write of the summary | Place BP4 (automatic) there on the first request after compaction |

## 5. Cost and hit-rate model

Assumptions (explicit `[inference]`): prompt tokens only (outputs excluded); tools+system prefix 12k; growth per step 2-4k (tool results are stubbed above 2,000 tokens, `context.py:9`); rates from `claude-models` section 4; Haiku staying in the <=100K tier unless noted. Script and arithmetic are reproducible from section 5.1; I did not measure real atrium prompts.

### 5.1 Per-run read share and cost

| Flow (model) | Shape | Read share of prompt tokens | Cached cost | Uncached cost | Saving |
|---|---|---|---|---|---|
| Chat, warm (Sonnet 5.5) | 60k history, 6 steps, +2.5k | 96.9% | $0.070 | $0.795 | 91% |
| Chat, cold start (Sonnet 5.5) | same, cache expired | 81.8% | $0.214 | $0.795 | 73% |
| Chat, warm (Opus 5.5) | 150k, 8 steps, +3k | 98.4% | $0.358 | $5.136 | 93% |
| Chat, cold start (Opus 5.5) | same | 86.7% | $1.078 | $5.136 | 79% |
| Cron (Haiku), cold prefix | 14k, 6 steps, +2k | 78.9% | $0.004 | $0.011 | 66% |
| Cron (Haiku), warm prefix | same | 89.5% | $0.003 | $0.011 | 78% |
| Nightly (Haiku) | 13k, 24 steps, +2k | 93.2% | $0.015 | $0.086 | 82% |
| Subagent (Haiku) | 3k, 12 steps, +2k | 85.1% | $0.005 | $0.017 | 73% |
| Coding in sandbox (Opus) | 20k, 40 steps, +4k, 5m | 95.5% | $1.63 | $15.68 | 90% |

Same coding run with every write at 1h: $2.16 (+$0.53). The `MAX_RUN_COST_USD = 2.0` ceiling (`loop.py:23`) would stop it near step 40; on Opus a cold 400k-token thread costs $2.00 in 5m write alone (400k x $5/MTok) and $3.20 at 1h before any output, so the per-run cap must become model- and flow-aware (`claude-models` gap 6).

Per-step cost of just re-reading a large history, which is what the "context size" control on the new-chat page really prices:

| History | Haiku 5.5 (>100K tier read $0.05) | Sonnet 5.5 ($0.10) | Opus 5.5 ($0.20) |
|---|---|---|---|
| 200K | $0.010 | $0.020 | $0.040 |
| 500K | $0.025 | $0.050 | $0.100 |
| 1M | $0.050 | $0.100 | $0.200 |

A 24-step run at 800k on Opus is ~$3.8 of reads alone. The UI should show this next to the context-size choice.

### 5.2 5m vs 1h break-even

Extra cost of 1h per run = 0.75 x base input x D (the new tokens D written over the run, at 2x instead of 1.25x). Saving per turn that arrives 5-60 minutes after the previous one = (1.25 x base - read) x P (prefix P that would be rewritten). 1h wins when p x (write5m - read) x P >= 0.75 x base x D, where p is the share of human turns with a 5-60 minute gap:

| Prefix P / D per turn | Break-even p (Haiku, Sonnet, Opus alike within 1 pt) |
|---|---|
| 30k / 8k | ~17% |
| 100k / 20k | ~12.5-13% |
| 400k / 30k | ~4.7-4.9% |

Gaps longer than 1h cost the same under both TTLs (or more under 1h: 2.0x vs 1.25x on the cold rewrite). Hence 1h for chat; 5m where runs are isolated (cron, nightly, subagents).

### 5.3 Rewrite events and when they pay

- Cold turn after TTL: rewrite of H at 1.25x (5m) or 2x (1h) base.
- Compaction at a cold boundary: summarizer input H at 1.0x (no `cache_control`) + summary writes, vs 1.25x/2x to rewrite H. Compacting first is cheaper whenever the thread is cold and large, and every later step then reads a smaller history. Candidate policy: cold + prompt >150k -> offer or auto-compact (quality trade-off for the operator to pick).
- Tool clearing batch (warm): rewrite (H - c) at 1.25x vs saving c x read per later request. Payback ~ 1.25 x (H - c) / (read x c) requests ~ 100-225 requests at 10% clearing: not a cost saver on 5.5 models.

## 6. Invalidation rules for the new-chat page and module toggles

Principle: **a thread has a frozen request prefix** (`model`, `thinking`, base effort, `betas`, tools, static system, snapshot system), and everything else is appended. A prefix change is legal only at a *cold boundary* (cache already expired: now - last request start > TTL), at compaction, or when the operator explicitly accepts the cost shown.

| Change | Effect on cache | Rule |
|---|---|---|
| Model | Cache is per model; unreadable thinking blocks are dropped (readability ladder in `claude-models` section 3). Full rewrite on the next request | Choose at thread start. Mid-thread switch allowed with a cost line (`prompt_tokens x write rate` while warm, free when cold). Down-switching also loses thinking. |
| Effort | Messages cache always; tools/system model-specific. **Free** with per-message effort (beta `mid-conversation-output-config-2026-07-01`, 5.5 models) | Send the beta header from thread creation so the header set never changes. Change effort via a `role: system` message with `output_config.effort`; re-send the level after compaction (a compaction summary drops it, [compaction limits](https://platform.claude.com/docs/en/build-with-claude/compaction-on-demand#how-it-fits-with-the-rest-of-the-api)). Not allowed with Sonnet `between_tools` or Haiku `disabled` thinking if the levels differ (`claude-models` section 3). |
| Thinking mode / display | Invalidates messages; maybe system/tools | Fixed at thread creation |
| Context size (atrium-side) | Changes `context_management` trigger values `[inference: affects hash; diagnostics lists it as prompt-affecting]` | Editable; applies from the next cold boundary; show per-step read cost (5.1) |
| Project | System block and project files in the first user turn change; whole prefix | Set at creation only. "Move to project" creates a branched thread (`service.py:387` already branches threads) |
| Sandbox environment / repo / branch | Tools and system notes (sandbox prompt) change | Environment and repo fixed at creation. Branch changes are sandbox actions (the typed git tool) and are announced to the model by an appended mid-conversation system message, not a prompt edit |
| Sandbox on/off (today: code_execution toggle) | Tools + system | Gone in v2 (Monty and code_execution are replaced by the sandbox). A sandbox-less thread keeps its tool list for life; attaching a sandbox later = `tool_addition` (beta `inline-tools-2026-09-15`) or a new thread |
| Module enabled/disabled | Tool list changes -> everything | Each thread snapshots its tool list at creation. A module disabled later: its tools stay declared in open threads (byte-stable) and calls return a clear "module disabled" error; the thread re-snapshots at its next cold boundary or at compaction. A module enabled later: appears in new threads, in old threads via `tool_addition` or at the re-snapshot. This reconciles "disabled modules register no tools" with cache stability: the rule applies to new requests of *new snapshots*. |
| Profile / skills / subagent roles / env vars notes | System or tool description changes | Snapshot at thread creation; later edits arrive as an appended system message ("Profile updated: key=value") or at the next cold boundary. Same for `research` role descriptions: keep them out of the tool definition (put the list in the snapshot system block) |
| Web search toggle | System + messages | Per-thread constant |
| Attaching the first image/PDF | Messages (images presence) | Accept; attachments are expanded each request already (`files.expand`), so only the first image in a thread rewrites. Show nothing; note it in the metrics |
| Fast mode / `speed` | System + messages; header is in the cache key | Not exposed on the new-chat page by default |
| Beta header set | Changes `unavailable` in diagnostics = prompt-affecting | Decide the full set at thread creation; add nothing mid-thread |
| API key rotation / workspace change | Caches are per workspace; Sonnet/Haiku 5.5 thinking is account-bound | Treat as a cold boundary for every thread |
| atrium upgrade changing tool text or `SYSTEM` | Every thread rebuilds on its next turn | Keep tool definition bytes stable across releases; do not interpolate versions, dates or counts into tool text; one-time rebuild is the accepted cost otherwise |

Tool ordering: order tools by **stability class then name**: always-on core tools first, then module groups in a fixed module order, then any dynamic definition last (never render another tool's stub or the list of subagent roles into a tool description). With per-thread freezing, order matters only across thread snapshots (cross-thread BP1 sharing: two threads share the prefix only if their tool lists are byte-equal up to BP1). If the tool count keeps growing (github, sandbox, grafana, notion), move rarely used module tools behind **tool search** (`defer_loading`), which preserves the prefix when tools load on demand (`research/claude-models` candidate 7).

## 7. Metering and budget

- Every response goes through one function that records, per call and **per iteration**: model, effort, `input`, `cache_write_5m`, `cache_write_1h`, `cache_read`, `output`, searches, and the TTL actually requested. Rate lookup by model and Haiku tier (`claude-models` section 4). Cache reads are 0.05x on Opus/Sonnet 5.5, so a single `0.1x` constant is wrong for two of three models.
- Server-tool auto-breakpoints add `ephemeral_5m` writes even when every marker is 1h; the split in `usage.cache_creation` already prices that.
- Sum `usage.iterations` (compaction, advisor, fallback) and record the compaction share separately so the operator sees what a compaction cost (`claude-models` gaps 1-2). With cache statistics per iteration this is priceable exactly.
- Add per kind and per model: `cache_read_share`, `cache_write_5m/1h` tokens, estimated uncached cost (all prompt tokens x base input) and **savings = uncached - actual**. `usage.summary`/`by_kind` already have the pieces (`usage.py:85-97`).
- Budget enforcement uses actual cost. The 80%/100% guard is unaffected, but 1h by default raises worst-case cost of cold starts 1.6x; with the default $150 month the effect is cents per thread.
- Observability (ticket R56): export `atrium_cache_read_tokens`, `atrium_cache_write_tokens{ttl}` and a hit-share gauge computed from SQLite, per kind and model, plus a counter of cache-miss reasons when diagnostics is enabled.
- Optionally attach `diagnostics: {previous_message_id}` (Go: `BetaMessageNewParams.Diagnostics`) behind a debug setting and store `cache_miss_reason.type` and `cache_missed_input_tokens` on the usage row. Per the docs it stores only hashes and token-count estimates, scoped to the workspace; ZDR note: "excludes Covered Models" ([diagnostics](https://platform.claude.com/docs/en/build-with-claude/cache-diagnostics)), so check eligibility before enabling it for an operator on ZDR.

## 8. Which cache settings the operator gets

Fits the plan's Settings decision (UI-editable, pinnable by config).

| Setting | Default | Note |
|---|---|---|
| `cache.ttl.chat` = `5m` / `1h` | `1h` | Applies to all breakpoints of a chat thread (fixes defect 1). Replaces `cache_ttl_chat` |
| `cache.ttl.agent_runs` (cron, nightly, subagents) | `5m` | Advanced; `1h` only helps jobs that wait on slow tools |
| `cache.ttl.sandbox` | `1h` | Sandbox-attached threads; also passed to CC (`CLAUDE_CODE_PROMPT_CACHE_TTL`) |
| `cache.cold_boundary_compaction` | off | Compacts a large cold thread before continuing (section 5.3) |
| `cache.diagnostics` | off | Debug toggle; stores miss reasons |
| Show cache stats | on | Usage page (hit share, savings) and the "warm for N min" hint |

Not exposed: number and placement of breakpoints, automatic vs explicit, the beta header set, `max_tokens: 0` pre-warm. Those are implementation invariants; exposing them only lets the operator break caching. Disabling caching entirely (Claude Code has `DISABLE_PROMPT_CACHING*`) is a debug switch, not a setting `[inference]`; atrium has no use for it.

## 9. Options, trade-offs, recommendation

| # | Option | Pros | Cons |
|---|---|---|---|
| A | Keep today's layout; fix TTL to cover the tail | Minimal change; port as is in Phase A | Keeps defects 2-6 |
| B | **Frozen per-thread prefix, 2 static BPs + previous-tail BP + automatic tail, 1h chat / 5m agent runs, same-prefix compaction** (recommended) | Removes the largest invalidations; robust to the 20-position lookback; no operator knobs needed beyond TTL | Edits to profile/skills/modules apply late (next thread or cold boundary); needs appended-message UX and the two betas (`mid-conversation-output-config`, `inline-tools`) |
| C | B plus keep-warm pre-warm (`max_tokens: 0`) for 5m threads | Saves TTFT when a thread is revisited inside a TTL | Extra writes; a 1h TTL already covers the revisit; pi only warms when >= $0.05 is at stake. Only if Phase B shows TTFT complaints |
| D | Tool search for all module tools | Smallest prefix; add modules without invalidation | Extra model round trip to discover tools; tune after measuring tool token size |

Phase split. **Phase A (port as is)**: reproduce today's request exactly (goldens compare canonical JSON; `research/anthropic-go-sdk`), but fix the two metering items and make `cache_ttl_chat` also set the history breakpoint, since that is a bug fix to a documented setting. **Phase B**: option B, per-message effort, tool/system snapshots, same-prefix compaction, stats and diagnostics.

## 10. Risks and unverified items

- Nothing here was measured on atrium's real prompts: tool-definition size (~8-15k tokens est. from ~62 tool definitions across 22 modules, `src/atrium/agent/tools/*.py`), block counts of web search responses, and the production hit rate. A one-time `count_tokens` call per kind and the existing `usage` table settle all three.
- Whether a request marked 1h hits an entry written under a 5m marker (and vice versa) is not stated; `[inference]` the hash does not include the TTL. The 400 for a mismatched TTL applies only to automatic vs a same-block explicit marker.
- Whether the compaction iteration reads the thread's cache is not documented; the iteration usage objects expose cache statistics, so a canary resolves it.
- Whether the server-tool auto-breakpoint counts toward the 4-slot limit is not documented; atrium runs with four explicit markers plus web search in production today, which suggests it does not (observed behaviour, not a doc statement).
- The beta header set for mid-conversation effort and `tool_addition` are betas (`mid-conversation-output-config-2026-07-01`, `inline-tools-2026-09-15`) and can churn; pin them in one table (`claude-models` risk).
- Subscription (Claude Code) runs: TTL default 1h within plan usage and 5m once on extra usage credits; nothing atrium can meter in dollars. See `research/pi-claude-code-sandbox` section 4 for the terms questions.
- A frozen snapshot means a profile fact the nightly run just wrote is invisible to an open chat thread until its next cold boundary. The appended "profile updated" message mitigates it but adds tokens to the tail.

## 11. Candidate new tickets

1. **Per-thread frozen request prefix (spec)** - type: spec, phase: B, area: assistant/chat. Define the snapshot (model, thinking, effort, betas, tools, static + snapshot system), the cold-boundary rule, and the appended-message formats for profile/skills/module/environment changes.
2. **Mid-thread effort and tool changes via per-message effort and `tool_addition` (research+build)** - phase: B, area: chat. Verify the two betas against the Go SDK and the 5.5 models, decide fallback when a beta is withdrawn, and re-apply effort after compaction.
3. **Same-prefix compaction (build)** - phase: B, area: context. Replace `client_compact` with a same-prefix summarizer, evaluate on-demand compaction vs threshold, canary whether compaction iterations read the cache.
4. **Cold-boundary context edits (spec)** - phase: B, area: context. Apply `clear_tool_uses`, ledger commits and optional compaction only when the cache is cold or the saving exceeds the rewrite; per-model triggers (Haiku tier vs Opus/Sonnet).
5. **Cache stats, savings and diagnostics in usage and metrics (build)** - phase: B, area: budget/observability. Per kind and model hit share, per-iteration pricing, miss reasons behind a debug toggle; show "warm for N minutes" and model-switch cost on the chat page.
6. **Measure the real baseline (research)** - phase: A-prep, area: budget. Read `usage` by kind from the live instance, `count_tokens` for each kind's tools+system, and sample web-search block counts to settle sections 3.5 and 10. Question: is 1h worth defaulting on, and are lookback misses real?
7. **Context-size picker pricing (spec)** - phase: B, area: chat. Show per-step cache read cost and compaction share next to context size; decide whether the default stays at 200K on Opus/Sonnet and 90K on Haiku (tier cliff at 100K).
8. **Sandbox cache config (build)** - phase: B, area: sandbox. Fixed repo mount path, `CLAUDE_CODE_PROMPT_CACHE_TTL`, pi retention, and reading CC/pi usage (cache split) into `usage` rows.
9. **Prompt byte-stability tests (build)** - phase: A, area: tooling. A golden test that serializes tools+system twice and across Go versions, forbids map-ordered schemas, and fails if a release changes tool bytes unintentionally (shared with the request-golden approach in `research/anthropic-go-sdk`).
10. **Subagent role text out of the tool description (spec)** - phase: B, area: assistant. Move the enabled-role list from `research` tool description to the system snapshot so editing a role no longer rewrites the CHAT/CRON/NIGHTLY tools prefix.

## 12. Sources

- Prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- Tool use with prompt caching: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-use-with-prompt-caching
- Cache diagnostics: https://platform.claude.com/docs/en/build-with-claude/cache-diagnostics
- Context editing: https://platform.claude.com/docs/en/build-with-claude/context-editing
- Compaction (overview, threshold, on-demand, background): https://platform.claude.com/docs/en/build-with-claude/compaction
- Mid-conversation system messages and tool changes: https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages
- Thinking and prompt caching: https://platform.claude.com/docs/en/build-with-claude/thinking#thinking-and-prompt-caching
- Pricing: https://platform.claude.com/docs/en/about-claude/pricing
- Claude Code prompt caching and env vars: https://code.claude.com/docs/en/prompt-caching, https://code.claude.com/docs/en/env-vars
- pi settings and models: https://github.com/earendil-works/pi/tree/main/packages/coding-agent/docs
- Go SDK: https://github.com/anthropics/anthropic-sdk-go (v1.82.0)
- Repo research: `research/claude-models`, `research/coding-harnesses`, `research/coding-tools`, `research/anthropic-go-sdk`, `research/pi-claude-code-sandbox`
