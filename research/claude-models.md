# Claude 5.5 model capabilities (ticket #26, slug `claude-models`)

Researched 2026-10-17 from Anthropic's current docs (platform.claude.com `.md` mirrors). Items marked `[inference]` are my reading, not a doc statement. atrium facts cite `path:line` in the checked-out main.

## 1. Answer in brief

- The 5.5 family is current: `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5` ([models overview](https://platform.claude.com/docs/en/models/overview)). A fourth, pricier model, `claude-fable-5-1` ($10/$50), is also current and is not in the plan's model list (section 8).
- All four have a 1M-token context window and 128K max output (sync). 1M is the default with no beta header ([context windows](https://platform.claude.com/docs/en/build-with-claude/context-windows)). Long-context surcharge exists only on Haiku 5.5 (prompt over 100,000 tokens). The old 200K/1M split is gone, so "context size (as the model supports)" is a free user choice (an atrium-side compaction limit), not an API option.
- Thinking is adaptive on all three; there is no `budget_tokens` any more (400). Per-model thinking-off rules differ (table 3). Effort levels `low|medium|high|xhigh|max` are supported by all three; defaults differ (Opus 5.5 and Haiku 5.5 `medium`, Sonnet 5.5 `high`) ([effort](https://platform.claude.com/docs/en/build-with-claude/effort)).
- Pricing differs per model and, for Haiku only, per prompt-size tier; cache reads are 0.05x base on Opus/Sonnet 5.5 and 0.1x on Haiku (table 2) ([pricing](https://platform.claude.com/docs/en/about-claude/pricing)).
- atrium's cost code is hard-wired to Haiku 5.5 (`src/atrium/agent/pricing.py:1-23`). It must become a per-model rate table keyed by the model recorded on each usage row. It also has two metering gaps with compaction and code execution (section 6).

## 2. Model facts

| | Opus 5.5 | Sonnet 5.5 | Haiku 5.5 | Fable 5.1 |
|---|---|---|---|---|
| API ID (also Vertex/Foundry) | `claude-opus-5-5` | `claude-sonnet-5-5` | `claude-haiku-5-5` | `claude-fable-5-1` |
| Bedrock ID | `anthropic.claude-opus-5-5` | `anthropic.claude-sonnet-5-5` | `anthropic.claude-haiku-5-5` | `anthropic.claude-fable-5-1` |
| Released | 2026-09-22 | 2026-09-28 | 2026-10-07 | n/a in my sources |
| Retirement | not before 2027-09-22 | not before 2027-09-28 | not before 2027-10-07 | not before 2027-09-01 |
| Context / max output | 1M / 128K | 1M / 128K | 1M / 128K | 1M / 128K |
| Batch max output (beta `output-300k-2026-03-24`) | 300K | 300K | 300K | not listed |
| Default effort | medium | high | medium | high |
| Min cacheable prompt | 512 tok | 512 tok | 512 tok | 512 tok |
| Knowledge cutoff | Jun 2026 | Jun 2026 | Jun 2026 | Jun 2026 |
| Latency (docs' relative label) | Moderate | Fast | Fastest | Slower |

Sources: [Opus 5.5](https://platform.claude.com/docs/en/models/opus-5-5/overview), [Sonnet 5.5](https://platform.claude.com/docs/en/models/sonnet-5-5/overview), [Haiku 5.5](https://platform.claude.com/docs/en/models/haiku-5-5/overview), [overview](https://platform.claude.com/docs/en/models/overview), [prompt caching limits](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#cache-limitations). All input: text and images; output: text. Up to 600 images/PDF pages per request.

Programmatic discovery: the Models API returns `max_input_tokens`, `max_tokens`, a `capabilities` object (including `thinking.types.disabled.supported`, `server_tools.web_search/code_execution.supported`) and a `line` field for grouping in a model picker ([overview, "Using the Models API"](https://platform.claude.com/docs/en/models/overview#using-the-models-api)). The picker can be populated from it instead of hard-coding.

Tokenizer: Claude 4.7+ (all 5.x) use a tokenizer that yields ~30% more tokens for the same text than Sonnet 4.6 and earlier ([pricing](https://platform.claude.com/docs/en/about-claude/pricing)). atrium's thresholds are token-count based, so there's no migration issue, but any hard-coded char/token ratios are stale `[inference]`.

## 3. Thinking and effort (the chat page's controls)

`thinking` request values per model ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking#configuring-thinking)):

| | omitted / `adaptive` | `enabled`+`budget_tokens` | `disabled` | `between_tools` |
|---|---|---|---|---|
| Opus 5.5 | adaptive (always on) | 400 | **400 at every effort** | 400 |
| Sonnet 5.5 | adaptive | 400 | **400** | up-front thinking off, only at low/medium/high |
| Haiku 5.5 | adaptive | 400 | thinking off, only at low/medium/high (400 at xhigh/max) | 400 |

- `display` defaults to `"omitted"` on these models (empty `thinking` text, same billing). atrium already sets `"summarized"` (`loop.py:104`), which is right for showing thinking in the UI.
- Effort `max`, `xhigh` exist on all three ([effort levels table](https://platform.claude.com/docs/en/build-with-claude/effort#effort-levels)); at `xhigh`/`max` set a large `max_tokens` (docs suggest ~64k start). atrium already does (`loop.py:19-23`, `MAX_TOKENS_DEEP = 65536`).
- Recommended starting points: Opus 5.5 medium by default and `xhigh` for long coding runs; Sonnet 5.5 `medium` for well-specified agentic work, `high` harder; Haiku 5.5 `medium`, `low` for chat/high-volume ([effort per-model sections](https://platform.claude.com/docs/en/build-with-claude/effort#recommended-effort-levels-for-claude-opus-5-5)).
- Per-message effort change (beta header `mid-conversation-output-config-2026-07-01`): a `role: system` message with `output_config.effort` changes effort mid-chat and keeps the cache. Without it, changing top-level effort invalidates the message cache ([effort, per-message](https://platform.claude.com/docs/en/build-with-claude/effort#per-message-effort-beta); [caching invalidation table](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#what-invalidates-the-cache)). Not combinable with Sonnet `between_tools` or Haiku `disabled` (400 if effort differs).
- Forced tool use is **not supported on Opus 5.5 and Sonnet 5.5**: `tool_choice` `any`/`tool` returns 400; only `auto`/`none` ([Opus 5.5 what's new](https://platform.claude.com/docs/en/models/opus-5-5/whats-new-opus-5-5#forced-tool-use-is-not-supported), [Sonnet](https://platform.claude.com/docs/en/models/sonnet-5-5/whats-new-sonnet-5-5#forced-tool-use-is-not-supported)). atrium never sets `tool_choice` (no match under `src/atrium`), so no impact now; the Go port must not add it for these models.
- Temperature/top_p/top_k: Sonnet 5.5 and Haiku 5.5 reject non-default values (400); omit them ([Sonnet](https://platform.claude.com/docs/en/models/sonnet-5-5/overview#good-to-know), [Haiku](https://platform.claude.com/docs/en/models/haiku-5-5/overview#good-to-know)). Haiku also rejects assistant-message prefill.
- Text between tool calls arrives as `thinking` blocks ("progress updates") on Opus/Sonnet 5.5, empty at `display: omitted`. With `summarized` they carry text; the UI should render thinking blocks as activity ([Opus what's new](https://platform.claude.com/docs/en/models/opus-5-5/whats-new-opus-5-5#behavior-differences)).

### Thinking blocks are model-, account-, and prefix-bound

- Each model reads only some other models' thinking blocks; unreadable blocks are silently dropped (not billed). Opus 5.5 reads Sonnet 5.5 and Haiku 5.5 blocks; Sonnet 5.5 reads Haiku 5.5's; Haiku 5.5 reads none of the three 5.5s ([switching models](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking#switching-models)). So moving up the ladder in a thread (Haiku→Sonnet→Opus) keeps reasoning; moving down loses it for those turns. Always replay full history and let the API drop.
- The API also checks that nothing before a 5.5 thinking block changed (system prompt, tools, earlier messages), enforced by default for accounts created on/after 2026-08-31, else 400. Beta header `thinking-binding-controls-2026-08-01` + `thinking.block_binding.prefix_mismatch_behavior: "drop_block"` drops instead of failing ([preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking)). Sonnet/Haiku 5.5 blocks only work in the account that produced them (matters if an operator rotates API keys to a different org: stored thinking is silently dropped).
- atrium today: strips thinking from assistant turns made under a different system/tools signature (`context.py:264-290`, `loop.py:89-93`), which satisfies the prefix check, and uses server-side `clear_tool_uses` which the docs say does not count as an edit ([server-side trimming](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking#trim-context-on-the-server)). Keep this, and key a thread's replay on model too.
- Changing the system prompt or tools mid-thread: use mid-conversation system messages (available on Fable 5.1, Opus 5.5, Sonnet 5.5, Haiku 5.5, no beta header; not on Sonnet 5) and `tool_addition`/`tool_removal` (beta `inline-tools-2026-09-15`) to keep cache + thinking valid ([docs](https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages)). Relevant to a chat page that lets users flip sandbox/repo/project mid-thread.
- Switching the model (or effort) mid-thread invalidates the prompt cache for the new model regardless: caches are per model `[inference, standard for provider caches]`; the doc table states effort changes invalidate the message cache.

## 4. Pricing (USD per million tokens), in the shape of `RATES_PER_MTOK`

Per request, from [pricing](https://platform.claude.com/docs/en/about-claude/pricing#model-pricing) and the model pages. A request's tier uses all prompt tokens `input + cache_write_5m + cache_write_1h + cache_read` (identical to today's `pricing.prompt_tokens`, `pricing.py:13-14`), per [long context pricing](https://platform.claude.com/docs/en/about-claude/pricing#long-context-pricing).

```python
# threshold_tokens: prompt tokens above which the "high" tier applies; None = single tier
RATES_PER_MTOK = {
    "claude-opus-5-5":   {"threshold_tokens": None,
        "standard": {"input": 4.00,  "output": 20.00, "cache_write_5m": 5.00,  "cache_write_1h": 8.00,  "cache_read": 0.20}},
    "claude-sonnet-5-5": {"threshold_tokens": None,
        "standard": {"input": 2.00,  "output": 10.00, "cache_write_5m": 2.50,  "cache_write_1h": 4.00,  "cache_read": 0.10}},
    "claude-haiku-5-5":  {"threshold_tokens": 100_000,
        "<=100K": {"input": 0.10, "output": 0.50, "cache_write_5m": 0.125, "cache_write_1h": 0.20, "cache_read": 0.01},
        ">100K":  {"input": 0.50, "output": 2.50, "cache_write_5m": 0.625, "cache_write_1h": 1.00, "cache_read": 0.05}},
    "claude-fable-5-1":  {"threshold_tokens": None,   # if offered
        "standard": {"input": 10.00, "output": 50.00, "cache_write_5m": 12.50, "cache_write_1h": 20.00, "cache_read": 0.25}},
}
```

Checks: Haiku tiers equal today's table (`pricing.py:7-10`). Cache multipliers: write 5m 1.25x, write 1h 2x, read 0.1x base, except read 0.05x on Opus/Sonnet 5.5 and 0.025x on Fable 5.1 ([caching pricing](https://platform.claude.com/docs/en/about-claude/pricing#prompt-caching)). Model IDs are pinned snapshots, so rates are stable per ID.

Modifiers (multiply on top, none used by atrium today):

| Modifier | Effect | Source |
|---|---|---|
| Batch API | 50% off input and output; stacks with cache multipliers; Haiku tiers halve too ($0.05/$0.25, >100K $0.25/$1.25) | [batch pricing](https://platform.claude.com/docs/en/about-claude/pricing#batch-processing) |
| Fast mode (`speed: "fast"`, Opus 5.5 only here) | $8 in / $40 out, full context; not with Batch; changing speed invalidates cache | [fast mode](https://platform.claude.com/docs/en/about-claude/pricing#fast-mode-pricing) |
| `inference_geo: "us"` | 1.1x on every category (incl. Haiku >100K tier); default `global` is 1x | [data residency](https://platform.claude.com/docs/en/about-claude/pricing#data-residency-pricing) |
| Web search | **$10 per 1,000 searches** (= $0.01, matches `SEARCH_USD`, `pricing.py:5`) plus tokens; failed searches not billed | [web search pricing](https://platform.claude.com/docs/en/about-claude/pricing#web-search-tool) |
| Web fetch | no extra charge, tokens only (`max_content_tokens` caps cost) | [web fetch pricing](https://platform.claude.com/docs/en/about-claude/pricing#web-fetch-tool) |
| Code execution (server) | free when `web_search_20260209+`/`web_fetch_20260209+` is in the same request; otherwise container-hours, min 5 min, 1,550 free h/org/month then $0.05/h | [code execution pricing](https://platform.claude.com/docs/en/about-claude/pricing#code-execution-tool) |
| Tool-use system prompt | 286 tokens (auto/none) on all three 5.5 models; bash tool +325, text editor +974 | [tool use pricing](https://platform.claude.com/docs/en/about-claude/pricing#tool-use-pricing) |
| Advisor tool | advisor sub-inference billed at advisor model's rates, reported as `usage.iterations[type="advisor_message"]` | [advisor](https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool) |
| Refusals | refusals before any output are billed only in categories `bio`, `frontier_llm`, `reasoning_extraction`; mid-stream refusals billed | [refusals billing](https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback#how-refusals-are-billed) |

Cost shape for budgeting ($150/month default, plan): Opus 5.5 is 40x Haiku's low-tier input price and 40x its output; Sonnet is 20x. Anthropic's own table: Opus 5.5 costs more than Sonnet 5.5 by 2x on every token class ([pricing](https://platform.claude.com/docs/en/about-claude/pricing#model-pricing)). A single 500K-token Opus turn fully uncached costs $2.00 of input alone (500,000 x $4/1M); the same cached read costs $0.10 (x $0.20/1M). The current per-run ceiling `MAX_RUN_COST_USD = 2.0` (`loop.py:23`) is hit after one such request on Opus.

### Rate limits ([rate limits](https://platform.claude.com/docs/en/api/rate-limits))

| Tier | Monthly spend cap | Opus 5.5 / Sonnet 5.5 / Haiku 5.5, each its own bucket (RPM / ITPM / OTPM) | Fable 5.x |
|---|---|---|---|
| Start | $500 | 1,000 / 2M / 400K | 1,000 / 500K / 100K |
| Build | $1,000 | 5,000 / 5M / 1M | 2,000 / 1.5M / 300K |
| Scale | $200,000 | 10,000 / 10M / 2M | 4,000 / 4M / 800K |

Cache reads do not count toward ITPM; cache writes and uncached input do. `max_tokens` does not count toward OTPM. Opus 5.5, Sonnet 5.5, and Haiku 5.5 have separate buckets from older models. Batch API: separate limits (queue 200K/300K/500K requests by tier, 100,000 requests or 256 MB per batch, results kept 29 days, 24h expiry) ([batch](https://platform.claude.com/docs/en/build-with-claude/batch-processing)). New orgs may start in a lower "Evaluation" tier. When the org monthly spend cap hits, the API returns HTTP 429 `rate_limit_error` with `error.details.error_code = "enforced_spend_limit_reached"` and **no `retry-after`**; a user-set console spend limit returns 400 `invalid_request_error` instead. The atrium budget design (soft cap at $150) should also detect those two errors and surface them as "Anthropic-side spend cap" rather than retry. Note: the Start-tier org cap ($500) is above a $150 default, but a first-month org can be below it `[inference]`.

## 5. Tool-use and context features relevant to the chat page

| Feature | Models / status | atrium today | Notes |
|---|---|---|---|
| Web search | `web_search_20260318` (latest; 20260209 adds dynamic filtering, 20250305 basic); GA, no beta | `registry.py:5` uses `20260318`, `max_uses: 5` | $10/1k; dynamic filtering runs code (free with the search tool) |
| Web fetch (server) | `web_fetch_20260318`/`20260309`/`20260209`/`20250910`; no extra fee; on Claude API only (not Bedrock/Vertex) | atrium has its own client-side `web_fetch` with an allowlist (`tools/web_fetch.py:27-32`) | server tool supports `allowed_domains`, `max_uses`, `max_content_tokens`; atrium's allowlist injection defence would need parity if switched |
| Code execution (server) | `code_execution_20260521` (latest) / `20260120` / `20250825`; GA on Opus/Sonnet/Haiku 5.5 | `files.py:43` | replaced by the sandbox in v2 (plan); until then see billing gap in section 6 |
| Memory tool | client-side `memory_20250818`, `/memories` directory, you execute the file ops; no beta | none (atrium has `recall`, `profile`) | could back the agent's cross-thread memory with atrium's own store; cross-link to scheduler/nightly redesign fog |
| Compaction | threshold: beta `compact-2026-01-12`, `compact_20260112` strategy (what atrium sends, `context.py:28-32,159`); on-demand: beta `compact-2026-09-04` + top-level `compaction` param, keeps recent turns with their thinking valid | threshold compaction + `pause_after_compaction` + preflight client compaction (`context.py:147-165,605`) | Docs say use on-demand where available. Supported on all three 5.5 models |
| Context editing | `context-management-2025-06-27`: `clear_tool_uses_20250919`, `clear_thinking_20251015` | `context.py:28,150-157` | clearing counts as non-edit for thinking prefix check |
| Task budgets | beta `task-budgets-2026-03-13`; Opus 5.5, Sonnet 5.5, Haiku 5.5; advisory token countdown for a whole agent loop | none | candidate for sandbox/coding runs; note 5.5 models no longer get injected context-awareness tags |
| Tool search | `tool_search_tool_{regex,bm25}_20251119`; all three 5.5 models; no beta | none | atrium registers all module tools each request; once modules add github/sandbox/grafana tools, deferred loading would cut the 286+ tokens/tool definitions and keep the cache |
| Programmatic tool calling | GA via `code_execution_20260120+`, Opus/Sonnet/Haiku 5.5 | atrium has its own `run_code` (Monty) | matches the user's "offload to harness tools" aim but runs in Anthropic's container; the sandbox should be the replacement |
| Advisor tool | beta `advisor-tool-2026-03-01`; advisor must be at least as capable as executor; Sonnet 5.5 executor needs Fable/Mythos/Opus 5.5/Opus 5/Sonnet 5.5 advisor; Haiku 5.5 is not accepted as advisor for Sonnet 5.5 | none | Haiku executor with Opus 5.5 advisor is a cheap-default pattern worth a ticket; advisor tokens are billed at advisor rate via `usage.iterations` |
| Structured outputs | GA on all three; `output_config.format` json_schema | `notion_migrate.py:180-183` | fine with `tool_choice: auto` |
| Batch API | all three; 50% off; 24h; results 29d | none | fits the nightly/cron runs that don't need a human (scheduler fog ticket) |
| Files API | GA; 500 MB/file, 1 TB/org, ~500 req/min; **not ZDR-eligible; files visible to the whole workspace, not scoped to user/thread** | `files.py:303-314` uploads/deletes/downloads | single-instance-per-user plan makes workspace scoping OK, but use a dedicated workspace per instance; also a `file_id` in prompts keeps thinking-prefix stable vs URLs |
| Fast mode | Opus 5.5 only here; `speed: "fast"`, up to 2.5x output tokens/s, $8/$40; no beta | none | optional chat-page toggle for Opus |
| Mid-conv system msgs / tool changes | see section 3 | none | |
| MCP connector | `mcp_toolset`, beta `mcp-client-2025-11-20` | none | out of scope unless the plan adds remote MCP |
| Refusals | HTTP 200 with `stop_reason: "refusal"` and `stop_details.category` (`cyber|bio|frontier_llm|reasoning_extraction|general_harms`); server fallback beta (`fallbacks: "default"`) | **no handling found** (no `refusal`/`stop_details` match under `src/atrium`) | see section 7 |
| Context overflow | 5.x: input + `max_tokens` over window is accepted; stops with `stop_reason: "model_context_window_exceeded"` | atrium estimates ahead and raises `RunLimit` (`loop.py:376-378`) | handle the new stop reason too |

## 6. How atrium computes cost today, and what breaks with multiple models

Flow: every model response goes through `usage.record` (`loop.py:386`, `context.py:612`, `notion_migrate.py:187`) -> `pricing.cost_usd(tokens, searches)` (`usage.py:38-41`) -> row in `usage` with `model`, `effort`, token columns, `tier`, `cost_usd`. Budget = `SUM(cost_usd)` for the month in `TIMEZONE` (`usage.py:74-76`), compared by `budget.check` (`budget.py:32-40`): 80% pauses unattended runs, 100% pauses chat; default `$75` (`budget.py:4`), plan raises to $150. Per-run ceiling $2 (`loop.py:23,366-368`).

What is correct already
- Haiku 5.5 rates and the >100K tier rule (all prompt tokens including cache) match the docs exactly.
- `$0.01` per web search matches. `searches_of` reads `server_tool_use.web_search_requests` (`usage.py:34-35`).
- 5m vs 1h cache write split is read from `cache_creation.ephemeral_*` (`usage.py:20-31`).
- Cost is stored per row, so historical rows survive a later price change.

Gaps found (each applies once other models or features are used; first two are real already)
1. **Compaction iterations are not billed.** With the compaction beta enabled, `usage.iterations` is always present; top-level `input_tokens`/`output_tokens` exclude the compaction iteration, and the docs tell you to sum `usage.iterations` for billing ([compaction usage](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold)). `grep iterations src/atrium/agent` has no match, so every server compaction (a summary of up to ~875K tokens at `COMPACT_SHARE=0.875`, `context.py:20`) is undercounted. Worst case is a large input read at full price: e.g. 800K Haiku tokens input = $0.40 per compaction. On Opus it would be $3.20 uncached. `[inference]` on exact cache billing of the compaction iteration; the docs say only to sum all entries. Advisor iterations have the same shape (billed at the advisor model's rates).
2. **Code execution container time is unmetered** when the sandbox is on without web search/fetch 20260209+ in the same request (`files.py:43`; `registry.py:5` is only added for CHAT/CRON kinds). Free hours (1,550/org/month) make this likely irrelevant for one instance, but billing is by container, not tokens (`server_tool_use.code_execution_requests` is the only signal).
3. **Single hard-coded rate table.** `pricing.MODEL = "claude-haiku-5-5"` is used by `loop.py:17`, `notion_migrate.py:26`, `service.py` run records. For v2 pick rates by the `model` stored on the run/usage row; unknown model ID must fail the budget check closed (refuse to run) rather than assume a default.
4. **Tier column is Haiku-specific.** `usage.tier` and `SUMMARY_SQL`'s `pricing.TIER_HIGH` (`usage.py:83-95`) assume two tiers. Keep the column but store `standard` for single-tier models.
5. **Context-share maths already assumes 1M** (`context.py:16`, `threads.py:16` offers 90K/200K/500K/1M). With Haiku's surcharge above 100K the 90K default keeps Haiku threads in the cheap tier; the same limits on Opus/Sonnet have no price cliff, so "context size" there is only a quality/spend knob (context rot per the docs' [context window page](https://platform.claude.com/docs/en/build-with-claude/context-windows)).
6. **Per-run `$2` ceiling and the 80%/100% shares** were tuned on Haiku. At Opus prices one 1M-token prompt costs $4 uncached, so the ceiling would block any long Opus run. Make it a per-model or user-editable setting `[inference]`.
7. **Fast mode / `inference_geo`** change prices by multiplier; if exposed, record the multiplier in the usage row.
8. **Refused requests** can bill with zero output; they'd still produce a usage object (`usage.iterations` on fallback), so recording usage before parsing the stop reason is required.
9. **Thinking tokens** are already inside `output_tokens` and billed as output; `display: omitted` does not change billing ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)).

Budget vs tier: Anthropic's org spend caps and tier limits are independent of atrium's soft budget; atrium's budget cannot see other uses of the same API key `[inference]`. Recommend a dedicated workspace/key per instance, which also fits the Files API workspace-scoping warning.

## 7. What the Claude apps' new-chat page exposes (UI model for the plan's chat page)

From [Change the model, effort, and thinking settings](https://support.claude.com/en/articles/8664678-change-the-model-effort-and-thinking-settings) and [Claude Code model configuration](https://support.claude.com/en/articles/11940350-claude-code-model-configuration):

- A model menu next to the send button: model name, "More models" for additional ones, an **Effort** submenu (Low, Medium, High, Extra high (xhigh), Max; each model marks a "Default"), and a **Thinking** toggle (labelled "Extended" on older models). Effort selector exists for Haiku/Sonnet/Opus 5.5, Fable 5.1, Opus 5, Sonnet 5, Fable 5, Opus 4.7, 4.6, Sonnet 4.6.
- Model, effort and thinking can be changed at any time in a conversation; they apply from the next response.
- Thinking cannot be turned off for Haiku 5.5, Sonnet 5.5, Opus 5.5, Fable 5.1, Opus 5 in the app. On the API: Opus 5.5/Fable 5.1 never; Haiku 5.5 and Opus 5 only at high or below; Sonnet 5.5 via `between_tools` (table 3).
- Thinking is shown as an expandable "Thinking" section with a timer; summaries can stop early when safety systems flag content.
- Enterprise admins can hide models/effort levels or fix defaults per role (maps to atrium's pinned settings model).
- Claude Code adds `/model`, `--model`, `ANTHROPIC_MODEL` and lists all current IDs (Haiku/Sonnet/Opus 5.5, Fable 5.1, ...). No context-size picker exists in the apps because 1M is the default; I found no source for a context-size option in the new-chat page `[inference: the plan's "context size" control is atrium-specific]`.
- Claude Code in the sandbox (ticket R32): model choice there is independent of atrium's.

I did not find Anthropic documentation of claude.ai's project/repo/environment pickers; the "like the Claude Code app" repo+branch+environment controls are Claude Code on the web features and should be checked in its own docs (candidate ticket).

## 8. Options, recommendation, risks

Options for the chat page's model list:
- A. Exactly three (Haiku/Sonnet/Opus 5.5) as in the plan. Simple, matches decisions.
- B. Three plus a hidden/admin-enabled `claude-fable-5-1`. Fable 5.1 is current ($10/$50, 2.5x Opus 5.5, always-on adaptive thinking), reads Opus 5.5's thinking blocks, and cannot be read by the others; add only as an opt-in in settings, because the budget burns fast.
- C. Populate from the Models API at runtime with a per-ID rate table in code. Prevents stale lists but unknown IDs have no price, so they must be disabled until priced.

Recommendation: A for the new-chat page, with the model registry being a data table (ID, label, context, max output, effort set, default effort, thinking-off mode, rates, features) so B/C are config changes. Per-model settings object stored in DB (the plan's Settings decision). Defaults: chat on Sonnet 5.5 `medium` (Anthropic: "fast, best combination"); nightly/cron on Haiku 5.5 `medium`/`high` (cheapest, still gets 1M); Opus 5.5 `medium`→`xhigh` for coding; subagents Haiku `low`/`medium`. Always send full history including thinking blocks; store `model` on every message/run so the replay key can include it. Keep `thinking: adaptive, display: summarized`. Never send `tool_choice` any/tool, `budget_tokens`, `disabled` (Opus, Sonnet), `between_tools` (Opus, Haiku), or sampling parameters. Replace `MODEL` constants by the registry; make budget check read per-row `model`.

Risks:
- Silent drops of thinking on downgrade within a thread (not an error). Accept, show model badge per turn.
- Per-run `$2` cap and budget shares tuned for Haiku (gap 6).
- Beta churn: `compact-2026-01-12` (threshold) and `compact-2026-09-04` (on demand) both beta, plus `context-management-2025-06-27`, `thinking-binding-controls-2026-08-01`, `mid-conversation-output-config-2026-07-01`, `inline-tools-2026-09-15`, `task-budgets-2026-03-13`, `advisor-tool-2026-03-01`. Pin them in one table; Go SDK exposes them under the `Beta` namespace.
- Fable/Mythos 5.1 name collision: only Fable 5.1 is generally available; "Mythos" is limited-availability ([pricing](https://platform.claude.com/docs/en/about-claude/pricing#model-pricing)).
- Stale cost data: prices change with each release (Sonnet 5.5 cache read halved vs Sonnet 5; Opus dropped from $5/$25 to $4/$20). Treat the rate table as versioned data with a date and test it against live `usage` from a canary call.
- Refusal handling is absent (section 5): in a mail/secretary setting `bio`/`cyber` classifier false positives would show up as empty assistant turns.

## 9. Candidate new tickets (not covered by the plan)

1. **Model registry and per-model rate table** (type: spec/build, phase: A-prep/B, area: assistant). Question: structure of the registry (id, thinking-off mode, effort set, rates, tier thresholds, betas), where it lives (code vs DB), and the closed-failure rule for unknown IDs.
2. **Usage metering parity fixes** (bug, phase A freeze or B, area: budget). Sum `usage.iterations` (compaction/advisor/fallback), record code-execution requests, record multiplier fields (fast, `inference_geo`). Decide whether to fix in the frozen Python app (fixes only is allowed) or in Go.
3. **Refusal and stop-reason handling** (feature, phase B, area: agent loop). Handle `refusal` + `stop_details`, `model_context_window_exceeded`, `pause_turn`; decide on server-side fallback (`fallbacks: "default"`) and what to show/alert in mail/cron runs.
4. **Per-model budget policy** (spec, phase B, area: budget). Replace `MAX_RUN_COST_USD=2.0` and the 80/100 shares with per-kind, per-model, UI-editable limits; surface Anthropic `enforced_spend_limit_reached` (429, no `retry-after`) and console-limit 400s distinctly.
5. **Switch to on-demand compaction** (research/build, phase B, area: context). Evaluate `compact-2026-09-04` (background compaction, kept turns keep thinking) versus today's threshold + `pause_after_compaction` + client preflight (`context.py`); needed before per-thread model switching.
6. **Mid-thread model/effort switching and cache cost** (spec, phase B, area: chat). Use per-message effort (`mid-conversation-output-config-2026-07-01`) and mid-conversation system messages for sandbox/repo/project changes; define the UX (warn that switching model resets the cache and drops unreadable thinking).
7. **Tool search / deferred tools** (research, phase B, area: assistant). As modules (github, sandbox, grafana, mail, notion) add tools, measure tool-definition tokens and consider `tool_search_tool_*_20251119` so disabled/rare tools aren't sent every turn.
8. **Cheap-executor + advisor pattern** (research, phase B, area: assistant). Haiku executor with Opus advisor (beta), cost vs quality for scheduled runs; also Batch API for non-interactive nightly jobs (50% off).
9. **Fable 5.1 opt-in** (decision, phase B, area: settings). Include in the registry but hidden by default?
10. **Anthropic workspace/key hygiene** (spec, phase B, area: security/settings). Files API content is workspace-wide and not ZDR; thinking blocks for Sonnet/Haiku 5.5 are account-bound; recommend one workspace per instance and document key rotation effects (stored thinking dropped).
11. **Move server web_fetch vs client web_fetch** (decision, phase B, area: tools). The server tool (free, dynamic filtering, `allowed_domains`) versus atrium's allowlisted client fetch that defends against prompt-injected URLs.
12. **Claude apps "environment/repo/branch" chat controls** (research, phase B, area: chat). Find primary docs for Claude Code on the web's environment, repo, branch pickers to mirror them.
13. **Task budgets for sandbox coding runs** (research, phase B, area: sandbox). `task-budgets-2026-03-13` as an additional guard next to the per-run dollar cap.

## 10. Source index

- Models overview: https://platform.claude.com/docs/en/models/overview
- Pricing: https://platform.claude.com/docs/en/about-claude/pricing
- Effort: https://platform.claude.com/docs/en/build-with-claude/effort
- Thinking: https://platform.claude.com/docs/en/build-with-claude/thinking
- Preserved thinking: https://platform.claude.com/docs/en/build-with-claude/preserved-thinking
- Context windows: https://platform.claude.com/docs/en/build-with-claude/context-windows
- Opus 5.5 / Sonnet 5.5 / Haiku 5.5 pages and what's-new: https://platform.claude.com/docs/en/models/{opus-5-5,sonnet-5-5,haiku-5-5}/overview
- Rate limits: https://platform.claude.com/docs/en/api/rate-limits
- Prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- Compaction (overview, threshold, on-demand): https://platform.claude.com/docs/en/build-with-claude/compaction
- Batch: https://platform.claude.com/docs/en/build-with-claude/batch-processing
- Files: https://platform.claude.com/docs/en/build-with-claude/files
- Tool reference, web search, web fetch, memory, code execution, tool search, advisor: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference
- Refusals: https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback
- Claude app settings: https://support.claude.com/en/articles/8664678-change-the-model-effort-and-thinking-settings
