# Direct tools for coding (ticket #30)

Question: typed git/gh/file/search/edit/test tools vs a shell tool. What Claude Code, Codex, pi and opencode expose, their schemas, how they truncate output, what it costs in tokens, and which tool set the sandbox manager should implement.

Researched 2026-10-17. Versions checked: Claude Code docs as of v2.1.301 (Oct 15 2026), Codex `rust-v0.163.0`, pi `earendil-works/pi` main, opencode `anomalyco/opencode` dev (pushed 2026-10-18). Marked `[inference]` where I am reasoning rather than quoting.

## 1. Answer in brief

1. **Every harness is built on the same small core: a shell tool plus file read/write/edit.** Claude Code, pi and opencode ship typed `read`, `write`, `edit` and a shell tool; Codex ships a shell tool (`exec_command` + `write_stdin`) and an `apply_patch` freeform edit tool and has *no* typed read/search/git tools ([codex shell_spec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/shell_spec.rs), [apply_patch_spec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/apply_patch_spec.rs), handler list in the [repo tree](https://github.com/openai/codex/tree/main/codex-rs/core/src/tools/handlers)). pi's default set is exactly four tools (`read`, `write`, `edit`, `bash`), with `grep`/`find`/`ls` opt-in ([pi blog](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/), [pi tools/index.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/index.ts)).
2. **Search and git are the contested categories, and the trend is toward the shell.** Claude Code now leaves `Grep` and `Glob` out of the default tool set on macOS/Linux/WSL and has the model call `find`/`grep` through Bash, where they are embedded `bfs`/`ugrep` ([tools reference](https://code.claude.com/docs/en/tools-reference#glob-tool-behavior)). None of the four harnesses has a typed local-git tool. Claude Code carries its git/PR policy as prose inside the Bash tool description: 1,477 tokens for commit instructions plus 864 for PR creation ([Piebald token counts](https://github.com/Piebald-AI/claude-code-system-prompts)).
3. **Tool-definition tokens are a weak argument for or against typed tools in atrium.** Definitions sit first in the cached prefix and re-read at 0.1x (Haiku 5.5, $0.01/MTok) or 0.05x (Sonnet 5.5/Opus 5.5, $0.10/$0.20 per MTok) ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [pricing](https://platform.claude.com/docs/en/about-claude/pricing)). A ~3k-token coding tool set costs about $0.0003 per cached Sonnet 5.5 request. What costs money is **tool results**, which get re-read every later turn, and **tool-set churn**, which invalidates the whole cache. Measured evidence agrees: JetBrains found that compressing shell output (rtk) saved nothing on real agent work (+7.6% cost at low effort, ±0% at high) ([JetBrains](https://blog.jetbrains.com/ai/2026/07/rtk-claude-code-token-savings/)); an arXiv study found scaffolding, not MCP-vs-CLI, dominated cost ([arXiv 2608.08654](https://arxiv.org/abs/2608.08654)).
4. **Typed tools earn their place on four things that are not tokens:** (a) credential isolation (plan: token never enters the VM), (b) deterministic output bounds, (c) edit reliability (staleness, uniqueness, atomicity), (d) policy enforcement the shell cannot give (no push to default branch, draft-only PRs, no force/delete/tags).
5. **Recommendation: 7 sandbox tools + 2 git-transport tools + 3 consolidated GitHub tools = 12 tools (~3k tokens), loaded only on threads that have a sandbox.** Shell for everything local and non-credentialed (status/diff/add/commit/log, build, test, lint, package managers). Typed tools for file IO/edit/search (bounded output), for credentialed git (`git_fetch`, `git_push`) and for the GitHub REST surface (`gh_pr`, `gh_issue`, `gh_ci`). No typed test runner and no typed local git. Details in section 7.

## 2. What each harness exposes

### 2.1 Tool inventory

| Capability | Claude Code | Codex | pi | opencode |
| --- | --- | --- | --- | --- |
| Shell | `Bash` (+ `Monitor`, `TaskStop`, background) | `exec_command` (PTY) + `write_stdin` | `bash` | `bash`/shell (background flag) |
| Read | `Read` (text, images, PDF, notebooks; refuses dirs) | none (shell `cat`/`sed`); `view_image` | `read` (text + images) | `read` (file **or directory**) |
| Write | `Write` | `apply_patch` (Add File) | `write` | `write` |
| Edit | `Edit` (exact `old_string`→`new_string`, `replace_all`) | `apply_patch` (freeform, Lark grammar, diff-like hunks) | `edit` (`edits[]` of `oldText`/`newText`, atomic, matched against original) | `edit` (`oldString`/`newString`/`replaceAll`) + `apply_patch` |
| Content search | `Grep` (ripgrep) — off by default on unix | none (shell `rg`) | `grep` (opt-in) | `grep` (ripgrep) |
| File search | `Glob` — off by default on unix | none | `find`, `ls` (opt-in) | `glob` |
| Git | none typed (Bash + prompt); `EnterWorktree`/`ExitWorktree` | none | none | none |
| GitHub | cloud sessions: built-in GitHub tools (issues, PR list, diffs, comments) + pre-installed `gh` | none | none | none |
| Code intelligence | `LSP` (needs plugin; auto-reports diagnostics after edits) | none | none | `lsp` (experimental) |
| Planning | `TaskCreate`/`Update`/`List` | `update_plan` | none (by design) | `skill`, `question` |

Sources: [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference), [Claude Code cloud environments, GitHub tools](https://code.claude.com/docs/en/cloud-environments#work-with-github-issues-and-pull-requests), [Codex handlers dir](https://github.com/openai/codex/tree/main/codex-rs/core/src/tools/handlers), [pi README + tools dir](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/src/core/tools), [opencode tools docs](https://opencode.ai/docs/tools/), [opencode plugin tool sources](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/tool/plugin). pi's author: "these four tools are all you need... Models know how to use bash and have been trained on the read, write, and edit tools with similar input schemas... Codex's tool definitions are similarly minimal to pi's" ([blog](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/)).

### 2.2 Schemas (the parts that matter for a manager API)

**Claude Code** ([SDK tool input types](https://code.claude.com/docs/en/agent-sdk/typescript#tool-input-types)):
```
Bash  {command, timeout? (ms; default 2 min, max 10 min), description?, run_in_background?, dangerouslyDisableSandbox?}
Read  {file_path, offset?, limit?, pages?, allow_large?}
Edit  {file_path, old_string, new_string, replace_all?}
Write {file_path, content}
Glob  {pattern, path?}
Grep  {pattern, path?, glob?, type?, output_mode: content|files_with_matches|count, -i,-n,-A,-B,-C, head_limit?, offset?, multiline?}
```
Behavioural rules worth copying ([Edit behaviour](https://code.claude.com/docs/en/tools-reference#edit-tool-behavior)): `old_string` must match exactly once or `replace_all`; read-before-edit (relaxed on newer models); a file that changed on disk since the read is still editable if `old_string` still matches uniquely, and the result says the file carries other changes; non-UTF-8 files refused rather than corrupted. Bash: each command is a separate process; `cd` persists inside the project, env vars do not; a timed-out foreground command is moved to background rather than killed ([Bash behaviour](https://code.claude.com/docs/en/tools-reference#bash-tool-behavior)).

**Codex** ([shell_spec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/shell_spec.rs)):
```
exec_command {cmd, workdir?, tty?, yield_time_ms? (default 10000; 250-30000), max_output_tokens? (default 10000), shell?, login?, + approval params}
  -> {chunk_id?, wall_time_seconds, exit_code?, session_id?, original_token_count?, output}
write_stdin  {session_id, chars? ("" = poll), yield_time_ms?, max_output_tokens?}
apply_patch  freeform tool, Lark grammar: "*** Begin Patch" / "*** Add|Update|Delete File: p" / "*** Move to:" / "@@" hunks with ' ', '+', '-' lines / "*** End Patch"
```
The output schema reports `original_token_count`, so the model learns how much was cut.

**pi** ([read.ts, edit.ts, grep.ts, find.ts, ls.ts, bash.ts, write.ts](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/src/core/tools)):
```
read  {path, offset? (1-indexed line), limit? (lines)}
write {path, content}                       (creates parent dirs)
edit  {path, edits: [{oldText, newText}, ...]}   each unique in the ORIGINAL file, non-overlapping, merged if adjacent
bash  {command, timeout? (s, no default)}   -> {output, truncated, full_output_path?, exit_code, wall_time_seconds}
grep  {pattern, path?, glob?, ignoreCase?, literal?, context?, limit? (100)}
find  {pattern, path?, limit? (1000)}
ls    {path?, limit? (500)}
```
pi also keeps a per-file mutation queue ([file-mutation-queue.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/file-mutation-queue.ts)) so parallel tool calls on one file serialize.

**opencode** ([plugin tool sources](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/tool/plugin)):
```
read  {path (file or dir), offset? (1-based), limit? (default and cap 2000)}
edit  {path, oldString, newString, replaceAll?}
grep  {pattern, path?, glob?, literal?, caseSensitive? (default true), limit? (100)}   30 s timeout
glob  {pattern, path?, hidden?, limit? (100)}                                          30 s timeout
shell {command, workdir?, timeout? (ms, default 120000, 0 = none), background?}  -> {exit?, signal?, shellID?, truncated, timeout?, output, status: completed|running}
```
opencode's file tools are Claude-Code-derived; pi's author notes the same structure, "same git commit flow" ([blog](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/)).

**Anthropic-defined schema tools** (alternative to custom schemas): `bash_20250124` is schema-less because "the schema is built into Claude's model" (`command`, `restart`); `text_editor_20250728` (`str_replace_based_edit_tool`) offers `view` (file or directory, optional `view_range`), `str_replace`, `create`, `insert`, with an optional `max_characters` truncation setting. Extra input tokens for the text editor: 974 (Claude 4.7+), 745 (4.6 and earlier) ([bash tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/bash-tool), [text editor tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/text-editor-tool)). Tool-use system prompt overhead is 286 tokens for Claude Haiku 5.5, Sonnet 5.5 and Opus 5.5 ([tool use overview](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview#pricing)).

### 2.3 Truncation strategies

| Harness | Tool | Limit | Strategy | What the model is told |
| --- | --- | --- | --- | --- |
| Claude Code | Bash, success | ~30,000 chars inline (`bashOutputMaxChars` ≤128,000) | over limit: full output saved to a file in the session dir (cut at 64 MiB) + first 2,000 chars preview | file path; model `Read`s/greps it |
| Claude Code | Bash, failure | ~10,000 chars | head+tail excerpt, no file path | n/a |
| Claude Code | Read | 25,000 tokens per call; whole file >256 KB refused | first page + `PARTIAL view` notice; or error "use offset/limit or Grep"; `allow_large` opt-in | how to page |
| Claude Code | Glob | 100 files, mtime-sorted | truncation flag | "narrow the pattern" |
| Claude Code | Grep | `head_limit` + `offset`; three output modes (`files_with_matches` default) | paged | `No entries at this offset` |
| Codex | exec_command / write_stdin | 10,000 tokens (model may raise, policy caps) | **middle truncation**, head and tail kept, `…N tokens truncated…`, ~4 bytes/token ([truncate.rs](https://github.com/openai/codex/blob/main/codex-rs/utils/string/src/truncate.rs)) | `original_token_count` |
| Codex | long commands | yield after 10 s | returns `session_id`, poll with `write_stdin` | session id |
| pi | read | 2,000 lines or 50 KiB, whichever first, never a partial line | head | continue with `offset` |
| pi | bash | last 2,000 lines or 50 KiB | **tail** (errors are at the end); full output to temp file | temp-file path |
| pi | grep / find / ls | 100 matches / 1,000 / 500, plus 50 KiB; grep lines cut at 500 chars | head | counts |
| opencode | all tools | 2,000 lines / 50 KiB (`tool_output.max_lines/max_bytes` configurable); spill kept 7 days | head; `[showing N of M; full output saved to FILE]` | file path |
| opencode | grep / glob | 100 results, 30 s timeout | head | "use a more specific path or pattern" |

Sources: [tools reference output limits](https://code.claude.com/docs/en/tools-reference#output-limits), [large files](https://code.claude.com/docs/en/tools-reference#large-files), pi [truncate.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/truncate.ts), opencode [tool-output.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool-output.ts).

Pattern across all four: **hard cap per result, spill the full output to a file inside the execution environment, tell the model the path, let it page with `read`/`grep`**. Differences: head vs tail vs middle. pi's rationale for tail on shell output ("errors, final results") and Codex's head+tail both beat plain head for build/test logs `[inference]`; plain head is right for file reads and search.

## 3. Token costs: documented and measured

### 3.1 Tool definitions (standing cost)

| Item | Tokens | Source |
| --- | --- | --- |
| pi system prompt + 4 tool definitions | < 1,000 | [pi blog](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/) |
| Claude Code `Read` / `Edit` / `Write` / `Grep` / `Glob` descriptions | 578 (285 compact) / 311 (178 lean) / 185 / 437 / 90 | [Piebald counts](https://github.com/Piebald-AI/claude-code-system-prompts) |
| Claude Code Bash: git commit instructions / PR creation instructions | 1,477 / 864 | same |
| Anthropic `text_editor_20250728` | 974 (Claude 4.7+) | [docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/text-editor-tool#pricing-and-token-usage) |
| Tool-use system prompt, Haiku/Sonnet/Opus 5.5 | 286 | [docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview#pricing) |
| GitHub MCP server, default 46 tools / read-only 27 / all 91 / 3 explicit tools | 11,207 / 6,492 / 23,206 / 1,693 (`o200k_base`, not Claude's tokenizer) | [importstatic](https://importstatic.com/ai/mcp-tool-definition-token-overhead) |
| `gh issue create --help` vs GitHub MCP issue-write definition | 869 vs 807 | same |
| MCP: 58 tools across 5 servers | ~55K; 50+ tools ~72K; with tool search ~8.7K | [Anthropic advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use) |
| atrium today | ~52 tool definitions in `src/atrium/agent/tools/*.py` (counted by `name="`) | repo |

### 3.2 Do the definition tokens matter? (arithmetic, `[inference]` on volumes)

Pricing ([pricing page](https://platform.claude.com/docs/en/about-claude/pricing)): Haiku 5.5 input $0.10/MTok, cache read $0.01 (prompts over 100k tokens: $0.50 / $0.05); Sonnet 5.5 input $2, cache read $0.10; Opus 5.5 input $4, cache read $0.20. Tools are the first block of the cache hierarchy `tools → system → messages` and any change to tool definitions invalidates the whole cache ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#what-invalidates-the-cache)).

- 3k tokens of coding tools, cached: Sonnet 5.5 $0.0003/request, Haiku 5.5 $0.00003/request. Uncached: $0.006 and $0.0003.
- One 7.5k-token tool result (30,000 chars at ~4 bytes/token) re-read as cached input on 40 later turns: 300k cache-read tokens = $0.03 on Sonnet 5.5, $0.003 on Haiku 5.5. A 50 KiB result is ~12.5k tokens, ~$0.05 / $0.005 on the same 40 turns.
- So one uncapped result costs 100x one tool-set definition. A 3k definition set is noise next to the $150/month budget; **output caps and a stable tool list are the cost levers.** Haiku 5.5's 5x price step above 100k prompt tokens makes large results doubly bad on cron runs.

### 3.3 Measured: typed vs shell, and output compression

- **rtk (CLI output compressor), JetBrains paired A/B**, Claude Code 2.1.201, claude-sonnet-5, SkillsBench 86 tasks, 425 trials: advertised 60-90% savings; measured **+7.6% cost at low effort (p=0.004), +13.8% turns, ±0% at high effort**, quality unchanged. Reasons: the hook only sees ~33% of Bash calls carrying <20% of tool-result chars; cached re-reads dominate input cost; the tool's own counter claimed 96.2M tokens saved while the bill rose ([JetBrains](https://blog.jetbrains.com/ai/2026/07/rtk-claude-code-token-savings/)). Takeaway: do not build elaborate shell-output summarisers; build hard caps.
- **MCP vs CLI, controlled**: one fixed task (six operations on a private online git repo), seven scaffoldings, five models. Two scaffoldings with no MCP support completed every run on CLI alone. They were 5.0x-28x cheaper than the five MCP-capable scaffoldings (CLI runs only). 13 strictly paired MCP-to-CLI ratios span 0.43x to 29x. 12.9% of MCP spend bought no completed work vs 2.2% for CLI ([arXiv 2608.08654](https://arxiv.org/abs/2608.08654)). The abstract draws the conclusion that MCP is unnecessary for this class of work and that scaffolding is the dominant variable.
- **Edit format** (author-run, 16 models, 3 runs, 180 React-edit tasks): `patch` is the worst format for nearly every model (Grok 4 patch failure 50.7%, GLM-4.7 46.2%); `hashline` (content-hash line anchors) beat patch on 14/16 models and was within noise of `str_replace` on strong models, larger gain on weak ones (Grok Code Fast 1: 6.7%→68.3%); Grok 4 Fast output tokens −61% from fewer retries ([Bölük, The Harness Problem](https://blog.can.ac/2026/02/12/the-harness-problem/)). Earlier, aider found JSON-wrapped edits worse than plain-text formats and GPT-4 Turbo's laziness benchmark rose 20%→61% with unified diffs ([aider](https://aider.chat/docs/unified-diffs.html)). Takeaway: edit reliability varies a lot by model and format; `str_replace` is the safe floor for Claude (pi's author and Claude Code both use it).
- **Code execution instead of many direct calls**: Anthropic reports 150,000→2,000 tokens (98.7%) for a tool-file-tree approach ([code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp)) and 43,588→27,297 (−37%) for programmatic tool calling on complex research tasks ([advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use)). Relevant to atrium because "sandboxes run scripts" already gives the agent code execution; the open question is whether atrium's domain tools should also be callable from the sandbox (candidate ticket C4).
- **IDE-grade code intelligence**: JetBrains/Netflix report 20-30% more efficient agents (up to ~65%) with a headless IDE code engine, measured on Java/Kotlin ([JetBrains](https://blog.jetbrains.com/ai/2026/10/how-netflix-made-ai-coding-agents-more-efficient/)). Not directly transferable to python/go/bun/rust toolsets; see candidate ticket C5.
- **Claude Code's own LSP tool** reports type errors/warnings after each file edit "so Claude can fix issues without a separate build step" ([LSP behaviour](https://code.claude.com/docs/en/tools-reference#lsp-tool-behavior)).

## 4. Typed vs shell, category by category

| Category | Shell | Typed | Verdict for atrium |
| --- | --- | --- | --- |
| Read file | `cat`/`sed -n` works, output uncapped, no line numbers, binary/image breakage | bounded, line-numbered, images, staleness tracking | **typed `read`** |
| Write/edit | heredocs and `sed` are quoting-fragile; CC itself tells the model to prefer Edit | atomic, unique-match, staleness check, small diff result | **typed `write`, `edit`** |
| Content/file search | `rg`/`fd` in rootfs; CC now does exactly this | bounded output, no shell quoting, ~0.5k tokens | **typed `grep`, `glob`** (cheap; see risk R3) |
| Directory listing | `ls` | pi `ls`; opencode folds into `read` | fold into `read` (no `ls` tool) |
| Local git (status, diff, add, commit, log, branch, stash) | native, models know it, no credential | none of the harnesses has it | **shell**, 2-3 sentence guidance in the system prompt, not CC's 1,477-token manual |
| Remote git (clone, fetch, pull, push) | needs a credential inside the VM or a TLS-intercepting proxy | manager-side, policy enforced before bytes leave | **typed `git_fetch`, `git_push`** (plan decision) |
| GitHub issues/PRs/CI | `gh` needs a token; CC cloud blocks `gh pr`/`gh issue` (GraphQL) through its proxy and ships built-in GitHub tools instead | REST from the manager, token never in VM, diff/log caps | **typed `gh_pr`, `gh_issue`, `gh_ci`** |
| Build/test/lint | native | none of the four harnesses has a typed runner | **shell** (+ optional named tasks, ticket C2) |
| Long-running processes | tmux (pi) or background flag | CC `run_in_background`+`Monitor`, Codex `session_id`+`write_stdin`, opencode `background` | **shell background flag + one `proc` tool** |
| Code intelligence | none | CC `LSP`, opencode `lsp` | defer (ticket C5) |

Credential isolation is the decisive reason for typed git/gh. Claude Code's cloud sessions solve it differently: the VM runs real `git` and `gh`, and a **GitHub proxy** swaps a scoped credential for the real token and (for `gh`) substitutes the placeholder `proxy-injected`; it rejects branch deletes and non-branch pushes but "doesn't limit which branches a push can update" and blocks GraphQL ([cloud environments: GitHub proxy](https://code.claude.com/docs/en/cloud-environments#github-proxy), [web docs](https://code.claude.com/docs/en/claude-code-on-the-web#github-authentication-options)). atrium's plan wants the stronger rule (default/protected branches never, draft PRs only); that policy has to live where the token lives, i.e. the manager.

## 5. Options for git transport (credential never in VM)

| Option | How | Pros | Cons |
| --- | --- | --- | --- |
| **A. Proxy injection (Claude Code model)** | Real `git`/`gh` in VM; egress proxy MITMs github.com and swaps a placeholder for the token | Works with any git command, models use git natively | Needs TLS interception and a CA in the VM; policy is at HTTP level (cannot see branch names in smart-HTTP push easily); overlaps ticket #25/#58 |
| **B. Manager-side typed ops over a vsock remote helper** `[inference]` | Guest `origin` URL is a custom scheme served by a tiny remote helper that streams the pack over vsock to the manager; manager validates (repo bound to the environment, ref allowlist, no force/delete/tag), then pushes/fetches with the token | Token never leaves the manager; policy checked on parsed ref updates; no MITM; no network egress to github.com needed from VM | Custom code (git remote helper + pack plumbing); `git push` from bash would also work through the helper, so the typed `git_push` is only a convenience wrapper |
| **C. Manager execs git in the VM with a short-lived token** | Manager injects a one-shot credential into the process env | Simple | Token enters the VM for the duration; violates the plan |
| **D. Host-side clone, VM mounts it** | Manager holds the clone, exposes it to the VM | No token in VM | Shared FS across the microVM boundary widens the attack surface; defeats per-VM disk isolation |

Recommendation: **B for transport, with typed `git_fetch`/`git_push` as the model-facing surface**, so `git push` typed in bash still goes through the same helper (no bypass) and fails with the policy message. Ticket #25/#58 owns the generic REST egress injection; GitHub REST in this ticket runs from the manager, so it does not need the proxy.

## 6. Risks

- **R1 Edit brittleness.** `str_replace` needs exact whitespace; pi and CC both report this class. Mitigation: return the nearest-match line range and a few lines of context on failure; keep multi-edit atomic; staleness check by content hash. Consider hashline-style anchors if Haiku 5.5 shows high failure rates (ticket C3).
- **R2 Secret leakage through tool results.** `env`, `cat .env`, CI logs and setup output can echo environment-defined secrets into the transcript and the DB. Mitigation: manager scrubs known secret values (runtime and setup-only) from every result before it leaves (ticket C6).
- **R3 More tools, worse selection.** atrium already has ~52 tools. Anthropic reports accuracy rising from 49%→74% (Opus 4) and 79.5%→88.1% (Opus 4.5) with tool search on large tool libraries ([advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use)). Mitigation: coding tools load only on sandbox threads; consolidate GitHub tools by `action` (GitHub MCP's own `pull_request_read` uses a `method` parameter for 5 sub-operations, [README](https://github.com/github/github-mcp-server)); tools fixed for the life of the thread.
- **R4 Prompt injection via repo, issues, PR comments and CI logs.** All `gh_*` results and file contents are untrusted. The typed GitHub tools can at least wrap results in a clearly delimited block and refuse to follow instructions embedded there `[inference]`. No automatic merge/protected-branch path exists, which bounds the damage.
- **R5 Cache invalidation.** Any change to tool definitions mid-thread invalidates `tools→system→messages` ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#what-invalidates-the-cache)). Do not add/remove coding tools mid-thread (for example when a sandbox resumes), and keep definition text byte-stable across releases.
- **R6 Runaway output inside the VM.** `grep -r` over a huge tree or a chatty process can exhaust VM memory before the cap applies; CC kills a command past 5 GB of output ([Bash output limits](https://code.claude.com/docs/en/tools-reference#output-limits)). The guest agent must stream output to a file with a byte ceiling, not buffer in memory.
- **R7 Parallel tool calls on one file.** Serialize per path (pi's file-mutation queue).
- **R8 Binary/non-UTF-8 files.** Refuse edits rather than corrupt (CC's behaviour); `read` returns a notice, images only for `read` of image types.
- **R9 Opus/Sonnet defaults vs Haiku.** Claude Code relaxes read-before-edit only for newer models, and always requires it for Haiku 4.5 ([Edit behaviour](https://code.claude.com/docs/en/tools-reference#edit-tool-behavior)). Haiku 5.5 is not listed on that page `[inference: treat as needing the read]`; enforce read-before-edit for all models, it is cheap.

## 7. Recommendation: the tool set

Design rule: the **manager exposes primitive HTTP/JSON endpoints** (ticket #41); **atrium maps them to model-facing tools** (ticket #42). Limits live in the manager so every caller (atrium, pi in the sandbox, a CLI) gets the same bounds.

### 7.1 Model-facing tools (12)

All results share an envelope: `{ok, ...fields, truncated?, output_path?, notice?}` and are plain text where the content is text.

**Sandbox tools (7)**

| Tool | Params | Notes |
| --- | --- | --- |
| `bash` | `command`, `workdir?`, `timeout_s?` (default 120, max 1800), `background?` | One process per call, combined stdout+stderr; cwd persists per sandbox (CC model), env does not (set in the environment definition). Returns `{exit_code, output, duration_s, truncated, output_path?, proc_id?}`. A foreground call that exceeds `timeout_s` is moved to background, not killed (CC). |
| `proc` | `id`, `action` (`read`, `wait`, `kill` or `stdin`), `wait_s?`, `chars?` | Replaces CC's `Monitor`/`TaskStop` and Codex's `write_stdin` with one small tool; only registered when `bash.background` is enabled. |
| `read` | `path`, `offset?`, `limit?` | Line-numbered, ≤2,000 lines and ≤50 KiB, never a partial line; directory → entries with `/` suffix (opencode); images returned as image blocks; PDFs/notebooks out of scope. |
| `write` | `path`, `content` | Creates parents; overwrites with UTF-8; refuses if the existing file changed since the same thread last read it. |
| `edit` | `path`, `edits: [{old_string, new_string, replace_all?}]` | Atomic, all-or-nothing, each `old_string` unique in the **original** file (pi) unless `replace_all`; staleness by content hash since last `read`; result is a ≤40-line unified diff. |
| `grep` | `pattern`, `path?`, `glob?`, `literal?`, `ignore_case?`, `context?`, `output_mode?` (`files`, `content` or `count`), `limit?` (100), `offset?`, `multiline?` | ripgrep in the guest; respects `.gitignore`; match lines cut at 500 chars; 100 matches / 50 KiB. |
| `glob` | `pattern`, `path?`, `limit?` (100) | mtime-sorted, respects `.gitignore`, truncation flag. |

**Git transport (2), manager-executed**

| Tool | Params | Policy enforced by manager |
| --- | --- | --- |
| `git_fetch` | `branch?`, `prune?` | Remote bound to the environment's repo; read credential only. Clone happens at sandbox setup, not as a tool. |
| `git_push` | `branch?`, `set_upstream?` | Refuses default/protected branches, non-fast-forward (no force), deletes, tags, and any remote other than the bound repo. Returns `{ref, commits, compare_url}`. A `git push` run through bash hits the same helper and the same checks (option B above). |

**GitHub, manager-executed over REST (3)**

| Tool | Actions | Bounds |
| --- | --- | --- |
| `gh_pr` | `get`, `files`, `diff`, `review_comments`, `checks`, `create_draft`, `update` (title/body), `comment` | `diff` capped at 50 KiB with "use `files` and `read` the file in the sandbox" notice; `create_draft` always `draft: true`; no `merge`, no ready-for-review, no close (operator actions) |
| `gh_issue` | `get`, `list`, `search`, `comment` | paged, 30 items default |
| `gh_ci` | `runs`, `job_logs` (`failed_only`, `tail_lines` default 200) | mirrors GitHub MCP's `get_job_logs` parameters ([README](https://github.com/github/github-mcp-server)) so failing CI is one cheap call |

Estimated definitions: ~3k tokens `[inference from §3.1: CC's read/edit/write/grep/glob descriptions are ~1.1-1.6k; add bash, proc, git and three GitHub tools]`, i.e. ~$0.0003 per cached Sonnet 5.5 request. Read-only GitHub tools (plan: work without a sandbox) are the same three tools registered without the sandbox group.

### 7.2 Manager API surface (maps 1:1)

`POST /v1/sandboxes/{id}/exec` (+ `/procs/{pid}` read/wait/kill/stdin), `/fs/read`, `/fs/write`, `/fs/edit`, `/fs/grep`, `/fs/glob`, `/git/fetch`, `/git/push`, `/github/pr|issue|ci`. Implemented in a guest agent over vsock plus manager-side GitHub client. Streaming output goes to a file on the sandbox disk with a byte ceiling; the guest agent applies the limiter and returns the envelope.

### 7.3 Truncation spec (one limiter, manager-enforced)

| Result | Cap | Strategy | Spill |
| --- | --- | --- | --- |
| `bash`/`proc` output | 50 KiB or 2,000 lines, whichever first (≈12k tokens; opencode/pi default) — start at **20 KiB** for cron runs `[inference: Haiku 5.5 over 100k tokens costs 5x]` | tail-biased: keep first ~20% and last ~80%, `…N lines truncated…` marker in the middle (Codex middle + pi tail) | full output to `/var/tmp/atrium/out/<call>.log` for the sandbox's life; notice names the path and says to use `read`/`grep` |
| `read` | 2,000 lines / 50 KiB, head | `offset` continuation | n/a |
| `grep` | 100 matches / 50 KiB, line cut at 500 chars, head, `offset` paging | | |
| `glob` | 100 paths | head + flag | |
| `gh_pr diff`, `gh_ci job_logs` | 50 KiB; logs tail-biased | head for diff, tail for logs | none (re-fetchable) |
| per-turn aggregate | 100 KiB across parallel calls `[inference]` | later calls in the batch get shorter caps | |

Every truncation result carries `original_bytes`/`original_lines` (Codex's `original_token_count` idea) so the model knows how much it did not see. Caps are per-environment settings (plan: nearly everything UI-editable) with the above as defaults; the model cannot raise them (CC does not; Codex does).

### 7.4 What not to build

- No typed local git (status/diff/commit/log): the shell works, models know it, no credential needed. Put 2-3 sentences of guidance in the system prompt (commit on a branch, push with `git_push`, open drafts with `gh_pr`), not CC's ~2.3k tokens of git/PR procedure.
- No typed test runner or output summariser: no harness has one, and rtk's measurement says output compression does not pay (JetBrains). Hard caps plus tail bias are enough.
- No `ls`, `find`, `patch`, notebook or PDF tools.
- No MCP in the sandbox path; MCP's problem is eager definition loading (11.2k tokens for GitHub's defaults) while atrium's three GitHub tools are ~1k `[inference]`.

### 7.5 Consistency with the plan

- Plan: "typed git/gh harness tools run by the manager; token never enters the VM" → §5 option B, §7.1.
- Plan: "agent may push non-default branches and open draft PRs without a click; default/protected branches, merges, deletes stay operator actions" → enforced in the manager (`git_push`, `gh_pr.create_draft`), not in prompts.
- Plan: "pi (coding agent) invocable by the atrium agent" → pi's own tools are `read/write/edit/bash` (+`grep/find/ls`); the manager's file endpoints are a superset, so pi can run inside the sandbox on the same files. pi's credentialed-git need is covered by the same helper.
- Plan: "Code mode: Monty ... replaced by the sandbox" → atrium's `run_code` (8 KiB output cap, 24 tool calls, 30 s, `src/atrium/agent/tools/run_code.py`, `src/atrium/agent/codemode.py`) is removed; `bash` with a script takes its place.

## 8. Candidate new tickets

| # | Title | Type | Phase | Area | Question |
| --- | --- | --- | --- | --- | --- |
| C1 | Git remote helper over vsock for credentialed git | Spike | B | sandbox | Can a git remote helper stream packs over vsock to the manager, which validates ref updates (bound repo, no force/delete/tag, default-branch refusal) and pushes with the token? Compare against proxy injection (#25/#58) on complexity, policy precision and `git push`-from-bash bypass. |
| C2 | Named environment tasks (`test`, `lint`, `build`) | Decision | B | sandbox/ui | Should environments define named commands that a typed `run_task` tool runs (no guessing, known JUnit/`go test -json` parsers, tail caps), or is `bash` + AGENTS.md enough? |
| C3 | Edit-tool A/B on Haiku 5.5, Sonnet 5.5, Opus 5.5 | Research | B | assistant | `str_replace` multi-edit (custom schema) vs Anthropic-defined `text_editor_20250728` (974 tokens, built-in schema) vs hashline. Failure rate, retries, output tokens on a real atrium task set. |
| C4 | Atrium tools callable from the sandbox | Decision | B | assistant/sandbox | Expose atrium's read-only domain tools to sandbox scripts as CLIs or a local socket with progressive disclosure (pi "CLI tools with READMEs"; Anthropic programmatic tool calling −37% tokens), and what the egress/credential story is. Replaces Monty's tool-calling role. |
| C5 | Code intelligence in the sandbox (LSP/diagnostics after edit) | Research | B+ | sandbox | Run gopls/pyright/rust-analyzer/tsserver in the toolset rootfs and report diagnostics in `edit` results (CC `LSP` tool does this); cost in RAM and rootfs size vs fewer build/test round trips. |
| C6 | Secret scrubbing of tool results and transcripts | Build | B | security | Manager redacts runtime and setup-only secret values (and common token patterns) from every result before it reaches the LLM or the DB; defines the replacement marker and handles multi-line/base64 forms. |
| C7 | Tool-result untrusted-content framing | Decision | B | assistant | How are repo files, issue/PR text and CI logs delimited in results to reduce prompt injection, and does the loop enforce a "no instructions from tool results" policy in the system prompt? |
| C8 | Tool-set stability and cache policy | Decision | B | assistant | Fix the model-facing tool list and definition bytes per thread; define behaviour when a sandbox is suspended/resumed or a module toggles mid-thread; add tool search if the combined list exceeds a budget (atrium already has ~52 tools). |
| C9 | Output limits as environment settings | Build | B | settings | Expose bash/read/grep caps, default timeout and aggregate cap in the settings model (#37) with the §7.3 defaults. |
| C10 | Background process wakeups | Decision | B+ | sandbox | Do long-running dev servers/test runs need event-driven wakeups (CC `Monitor`) for scheduled runs, or is `proc wait` polling sufficient? |
| C11 | Commit identity and signing in sandboxes | Decision | B | github | Which author/committer identity and signing key (if any) do agent commits use, given the token never enters the VM? (GPG/SSH signing needs a key; manager-side signing is possible if `git_push` re-signs `[inference]`.) |
| C12 | Issue/PR comment and review write scope | Decision | B | github | Plan allows push + draft PR without a click; are issue comments, PR comments, review replies and label changes also click-free, or operator-approved? |

## 9. Sources

Harness docs and source:
- Claude Code: [tools reference](https://code.claude.com/docs/en/tools-reference), [SDK tool input types](https://code.claude.com/docs/en/agent-sdk/typescript), [cloud environments](https://code.claude.com/docs/en/cloud-environments), [Claude Code on the web](https://code.claude.com/docs/en/claude-code-on-the-web), [Piebald extracted prompts + token counts (v2.1.301)](https://github.com/Piebald-AI/claude-code-system-prompts)
- Codex: [shell_spec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/shell_spec.rs), [apply_patch_spec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/apply_patch_spec.rs), [apply_patch.lark](https://github.com/openai/codex/blob/main/codex-rs/core/assets/tools/apply_patch.lark), [truncate.rs](https://github.com/openai/codex/blob/main/codex-rs/utils/string/src/truncate.rs)
- pi: [packages/coding-agent](https://github.com/earendil-works/pi/tree/main/packages/coding-agent), [tools](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/src/core/tools), [author's design post](https://mariozechner.at/posts/2025-11-30-pi-coding-agent/)
- opencode: [tools docs](https://opencode.ai/docs/tools/), [tool sources](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/tool/plugin), [tool-output.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool-output.ts)
- GitHub MCP server tool list: [README](https://github.com/github/github-mcp-server)

Anthropic:
- [Tool use overview and tool-use system prompt tokens](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview#pricing), [bash tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/bash-tool), [text editor tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/text-editor-tool), [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [pricing](https://platform.claude.com/docs/en/about-claude/pricing)
- [Code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp), [Advanced tool use](https://www.anthropic.com/engineering/advanced-tool-use), [Writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents)

Measurements and benchmarks:
- [JetBrains: rtk A/B](https://blog.jetbrains.com/ai/2026/07/rtk-claude-code-token-savings/), [JetBrains/Netflix IDE intelligence](https://blog.jetbrains.com/ai/2026/10/how-netflix-made-ai-coding-agents-more-efficient/)
- [arXiv 2608.08654, scaffolding vs MCP/CLI](https://arxiv.org/abs/2608.08654)
- [importstatic: MCP definition token overhead](https://importstatic.com/ai/mcp-tool-definition-token-overhead)
- [Bölük: The Harness Problem](https://blog.can.ac/2026/02/12/the-harness-problem/), [aider unified diffs](https://aider.chat/docs/unified-diffs.html)

atrium code: `src/atrium/agent/tools/run_code.py` (8 KiB output cap), `src/atrium/agent/codemode.py` (24 calls, 30 s), `src/atrium/agent/tools/*.py` (~52 tool definitions), `nix/module.nix` (`atrium-run`, `codemode-worker` units).

Caveats: the Piebald counts are extracted strings and may differ ±20 tokens in a live session; the importstatic counts use OpenAI's `o200k_base` tokenizer, not Claude's; the edit-format and arXiv results are single-study and not independently replicated; Claude Haiku 5.5 behaviour with read-before-edit is not documented on the Claude Code page.
