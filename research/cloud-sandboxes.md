# Cloud sandbox survey (ticket #28)

Researched 2026-10-17 from vendor docs (links inline). Items marked `[inference]` are my reading, not a vendor statement. Feeds #41 (sandbox manager design/API).

## 1. Answer in brief

Every product converges on the same shape:

1. **An environment is a named, reusable definition**, not a container: base image + one setup/install script + env vars + secrets + network policy (+ repo list). It is built *ahead of time* into a **filesystem snapshot** that new sessions boot from. Only disk state is captured, never processes.
2. **Secrets are split by who may see them.** Plain env vars are visible to the agent. A second class is *never* in the VM: the egress proxy substitutes a placeholder for the real value on HTTPS requests to allowed hosts (Claude Code "network secrets", Codex "network secrets", Daytona secrets, Vercel credential brokering, E2B header transforms). A third class is build/setup-only (Codex secrets, Cursor Build Secrets, atrium's "setup-only").
3. **Network policy is a domain allowlist enforced outside the VM**, with presets ("package managers"/"trusted") + custom domains + "none" + "full", editable on live sessions in most products (Claude ~1 min, Daytona, Modal, Vercel, Sprites).
4. **Lifecycle = idle -> pause/suspend (disk kept) -> reclaim after N days; TTL on snapshots (7-30 d).** Resume re-reads env vars; it does not re-run setup.
5. **Git credentials never enter the VM**; a git proxy swaps a scoped token for the real one, and restricts what can be pushed.
6. **Resource tiers are small and fixed** (2-4 vCPU / 8-16 GB / 8-32 GB disk) and admin/plan-set, not user-chosen.

## 2. Per-product findings

### Claude Code on the web
- Environment = saved config (name, network level, env vars in `.env` format, setup script); personal or org-shared; archived not deleted. [cloud-environments](https://code.claude.com/docs/en/cloud-environments)
- Image: fixed Ubuntu 24.04 x86_64 with preinstalled toolchains; **replacing the base image is not supported**; customize via setup script or docker compose. Setup script runs as root before Claude starts, must exit 0, and is cached only if it finishes in ~5 min.
- Caching: filesystem snapshot after setup; rebuilt when setup script or allowed hosts change or after ~7 days; setup does **not** rerun on idle-restore. Files only, no processes.
- Secrets: env vars readable by anyone using the environment; **network secrets** (name, allowed hosts incl. `*.`, header name/prefix/value) injected by Anthropic's proxy after the request leaves the VM; cannot be edited, only delete+re-add; never applied to Anthropic API, public registries, setup-script traffic, or GitHub.
- Network: levels None / Trusted (default allowlist) / Full / Custom (own list, optional "include defaults"). Changes apply to running sessions in ~1 min. GitHub, MCP connectors, secret hosts and the Anthropic API bypass the allowlist even at None.
- Resources: ~4 vCPU, 16 GB RAM, 30 GB disk. Commands: 2 min default foreground, up to 10 min, then backgrounded up to 30 more min.
- Lifecycle: VM pauses with files saved after a few minutes idle; paused VM later reclaimed (then re-provisioned, conversation restored, background processes lost).
- Git: GitHub App or `/web-setup` token; credentials stay on Anthropic's side; **GitHub proxy** verifies a scoped credential, rejects branch deletes and tag pushes, scopes API to attached repos, blocks GraphQL. Bundle upload fallback for repos without GitHub.
- UX: env selector under the prompt box, live setup checklist, `--cloud` / `--teleport`, diff view with inline comments, auto-fix PRs, session sharing.

### OpenAI Codex cloud
- New model: environments are created by an agent-led setup conversation, then **Published** (captures prepared filesystem) / Republished; fields = Install script + "Start skill" (instructions to start services) + repos + vars + network secrets + VPN (Tailscale) + OIDC. [cloud-environments](https://learn.chatgpt.com/docs/environments/cloud-environments). Legacy: setup script + maintenance script, 12 h cache, cache invalidated by any script/var/secret change. [legacy](https://learn.chatgpt.com/docs/environments/cloud-environment)
- Secrets: env var (direct) vs **network secret** (placeholder in VM, proxy substitutes on HTTPS:443 to allowed domains) vs **personal vault** (per-user values a shared environment can *request*). Legacy: secrets exist only during setup and are removed before the agent phase.
- Network: **off by default in the agent phase, on during setup**. Options: Package-managers preset / custom only / all; legacy also restricts HTTP methods to GET/HEAD/OPTIONS. All traffic behind an HTTP(S) proxy. Published egress IP feed. [internet-access](https://learn.chatgpt.com/docs/cloud/internet-access)
- Resources: Plus 2 vCPU/8 GiB/8 GiB disk; Pro/Business/Enterprise 4/16/32; larger for Enterprise.
- Lifecycle: task VM state recoverable up to 7 days after last turn; existing tasks keep own files; new tasks start from published image.

### Cursor cloud agents (ex-Background Agents)
- Env resolution: `.cursor/environment.json` in repo > personal saved env > team saved env. `build.dockerfile` (+context) or `snapshot` id, `install`, `start`, `terminals` (tmux). **Builds** run install ahead of time, capture disk, and a failed build never replaces the active one; env has version history. [setup](https://cursor.com/docs/cloud-agent/setup.md)
- Secrets: three types: Environment Variable (visible), **Runtime Secret** (env var, but redacted to `[REDACTED]` in transcript/commits), **Build Secret** (Docker build only). Env-scoped secrets; OIDC tokens from a local socket; AWS role assumption. [security-network](https://cursor.com/docs/cloud-agent/security-network.md)
- Network: Allow all / Default+allowlist / Allowlist only, at user, team, environment level, inheritable; enterprise lock. **Default is internet access on.** Always-on exceptions (Cursor services, SCM).
- Lifecycle: snapshots kept 30 days from last use; conversations indefinite (or 90 d policy).
- Git: HSM-signed commits, git egress proxy with fixed IPs, multi-repo environments; "start from scratch" with a hosted draft repo; forwarded ports and shared tmux terminal.

### Devin
- Environment = **blueprint** YAML (`initialize` runtimes, `maintenance` deps, `knowledge` free text for lint/test/build, `runs-on`, `clone`, enterprise `post-build` health check gating the snapshot), built into one snapshot per platform per org; rebuilt on change and ~every 24 h; differential builds. Every session boots a fresh copy; session changes never go back to the snapshot. [environment](https://docs.devin.ai/onboard-devin/environment.md), [blueprint reference](https://docs.devin.ai/onboard-devin/environment/blueprint-reference.md)
- Secrets: org / personal / repo / session-scoped; raw, cookie, TOTP types; injected per command rather than exported in every shell. [secrets](https://docs.devin.ai/product-guides/secrets.md)
- **Security profiles**: network allowlist (hosts with `*`, CIDRs), MCP allowlist, git access Read-only/Full, GitHub CLI token removal; bound at enterprise/org/automation/session level; "mandatory" profiles only intersect (tighten) downward; agent can *request* a domain and approval widens that session only within the envelope. [security-profiles](https://docs.devin.ai/product-guides/security-profiles.md)
- SSH into session VM, port forward, Windows/macOS VMs, Outposts (self-hosted workers; network policy is published to the operator to enforce).

### Jules (Google)
- Short-lived Ubuntu VM per task; agent infers setup from repo/AGENTS.md/README; optional **Initial Setup** script per repo with "Run and Snapshot" button that validates and snapshots. Preinstalled toolchains listed by `/opt/environment_summary.sh`. [environment](https://jules.google/docs/environment/) I found no documented network-policy, secret-injection or resource-tier controls `[inference: little or no user-configurable policy]`.

### GitHub Copilot cloud agent
- Environment = `.github/workflows/copilot-setup-steps.yml`, a restricted Actions workflow (only `steps`, `permissions`, `runs-on`, `services`, `snapshot`, `timeout-minutes` <= 59 honored); runs on hosted, larger, or self-hosted ARC runners; failing step skips the rest but the agent still starts. Validates itself as a normal workflow. [environment](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/customize-the-agent-environment)
- Secrets/vars: dedicated "Agents" secrets and variables at org/repo level (repo wins); exposed as env vars, masked in logs; `COPILOT_MCP_` prefix for MCP only. [secrets](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-cloud-agent/configure-secrets-and-variables)
- Network: **firewall on by default** with recommended allowlist (OS pkg repos, registries, language registries, CAs); org can force/let repos decide/allow repo custom rules; domain or URL-prefix rules; a blocked request adds a warning to the PR body. Only covers processes started by the agent's Bash tool, not setup steps or MCP servers; docs say not a comprehensive control. [firewall](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall)
- Limits: one repo, one branch, one PR per task; 59 min max session. [concepts](https://docs.github.com/en/copilot/concepts/copilot-surfaces/copilot-on-github)

### E2B
- **Templates** (code-defined: base image, envs, copy, run, start command captured *running* in the snapshot; layering via `fromTemplate`; build <= 1 h) vs snapshots. [templates](https://docs.e2b.dev/template/quickstart.md)
- Lifecycle: `timeout` (default 5 min, up to 1 h Hobby/24 h Pro), `setTimeout`, `onTimeout: kill|pause`, **pause keeps filesystem + memory indefinitely**, connect resumes; kill is explicit. [lifecycle](https://docs.e2b.dev/sandbox), [persistence](https://docs.e2b.dev/sandbox/persistence.md)
- Network: internet on by default; `allowInternetAccess:false`; `allowOut`/`denyOut` with domains (HTTP Host / TLS SNI only, ports 80/443), CIDRs, wildcards; allow wins over deny; **per-host header transforms** (beta) injected by the egress proxy; BYO SOCKS5 proxy. [internet-access](https://docs.e2b.dev/network/internet-access.md)
- Resources: per-template CPU/memory/disk at build time (Hobby 8 vCPU/8 GiB/10 GiB build caps).

### Daytona
- Containers by default; VM-class sandboxes add fork, pause/resume, memory snapshots, nested virt. Snapshots from OCI image; tiers small 1/1/3 up to large 4/8/10 (vCPU/GiB/GiB); org cap 4/8/10. [sandboxes](https://www.daytona.io/docs/en/sandboxes/)
- Lifecycle knobs: auto-stop (15 min default, 0=never; **running processes do not count as activity**), auto-pause (VM, 60 min default), auto-archive (7 d default, max 30), auto-delete, hard wall-clock TTL.
- Network: tier-based defaults (tier 1-2 restricted and not overridable; 3-4 open); per-sandbox `networkAllowList` (CIDR), `domainAllowList`, `networkBlockAll`, mutable at runtime; when an allow list is set even GitHub/npm are blocked unless listed. [network-limits](https://www.daytona.io/docs/en/network-limits.md)
- Secrets: org-scoped, mapped to env var names at create time, placeholder in VM, proxy substitutes **request headers only over HTTPS**, scrubs echoed values from responses, per-secret host allowlist (omitting it = send anywhere; they recommend always setting). [secrets](https://www.daytona.io/docs/en/secrets.md)

### Modal sandboxes
- Defined at runtime from an Image; `exec` with streaming stdout; timeout default 5 min, up to 24 h; `idle_timeout`. Network: `block_network`, `outbound_cidr_allowlist`, `outbound_domain_allowlist` (TLS/443 via SNI, beta), runtime update (experimental). Documents domain fronting and recommends a sidecar proxy. [sandboxes](https://modal.com/docs/guide/sandboxes), [networking](https://modal.com/docs/guide/sandbox-networking.md)
- Snapshots: filesystem (diff vs base, 30 d TTL default, forkable), directory (mountable into another sandbox), memory (7 d, not extendable). [snapshots](https://modal.com/docs/guide/sandbox-snapshots.md)

### Fly Sprites
- Persistent Ubuntu VM per sprite: 100 GB storage, filesystem persists, RAM does not; wakes 100-500 ms warm / 1-2 s cold; idle = no exec, no open TCP, no TTY; **Services** auto-restart on wake; checkpoints (fs only, 10-30 s, count against quota); URL per sprite (private by default). [working-with-sprites](https://docs.fly.io/sprites/working-with-sprites.md)
- Egress: open by default; per-sprite policy API `POST /v1/sprites/{name}/policy/network` with `allow`/`deny` domain rules, enforced outside the VM (denied DNS returns REFUSED, raw IP blocked) per a search summary of Fly pages `[inference: not read from the API reference page, which 404'd for the policy path]`.

### Vercel Sandbox
- Firecracker microVMs; default image Node LTS + Python 3.14 + agents; custom OCI images via Vercel Container Registry; 1 or even vCPUs 2-32 (2 GB/vCPU), 64 GB disk; **Active-CPU billing** (idle waiting on LLM free); persistence default with auto-snapshot on stop (30 d from last use, `keepLastSnapshots`), `onCreate`/`onResume` hooks, sessions up to 24 h (Pro), 45 min Hobby; sandboxes without resumable snapshot deleted after 14 d. [overview](https://vercel.com/docs/sandbox), [persistence](https://vercel.com/docs/sandbox/concepts/persistent-sandboxes.md), [pricing/limits](https://vercel.com/docs/sandbox/pricing.md)
- Firewall: `allow-all` / `deny-all` (incl. DNS) / user-defined (domains with whole-label wildcards, CIDR allow, CIDR deny), switchable at runtime; **credential brokering** (`transform.headers` per domain, with path/method/query matchers; TLS terminated only for those domains), request forwarding to your own proxy; documents SNI/Host fronting gap and that CIDR allows bypass domain rules. [firewall](https://vercel.com/docs/sandbox/concepts/firewall.md)

## 3. Prompt-injection / exfiltration separation (addendum from Main)

Question: how do Claude Code web, Codex cloud and Copilot separate untrusted content (issues, web pages) from egress and push rights inside one session?

None of them separate by *content provenance* inside a session. All three apply **capability reduction at the boundary**, so a hijacked agent has little to exfiltrate to and little it can push:

| Control | Claude Code web | Codex cloud | Copilot cloud agent |
|---|---|---|---|
| Egress default | Trusted allowlist (registries, GitHub, cloud SDKs); None available | Off in agent phase; setup phase online | Firewall on, recommended allowlist |
| Egress dimensions | domains | domains (+HTTP method restriction in legacy) | domains and URL prefixes |
| Credentials in VM | none: GitHub proxy + network-secret proxy | secrets removed before agent phase (legacy); network-secret placeholders | scoped, "simple push" only; agent can't run raw `git push` |
| Push scope | proxy rejects branch delete and tag push; **does not restrict which branch**, advises GitHub rulesets | n/a in docs read | only one branch (`copilot/...` or the PR's branch); branch protections apply |
| PR rights | n/a | n/a | draft PRs only; cannot mark ready, approve or merge; requester can't approve; Actions workflows need a human "Approve and run" |
| Untrusted input | docs warn of exfil even at None (Anthropic API path) | docs spell out the issue-hijack example and advise narrow domains + methods | only write-access users' comments reach the agent; hidden characters (HTML comments) filtered |
| Residual holes (vendor-stated) | Anthropic API, MCP connectors, GitHub, secret hosts bypass allowlist | Agent-security proxy can be bypassed by escalated commands | Firewall only covers agent Bash tool, not setup steps/MCP; "not comprehensive" |

Sources: [Claude cloud-environments](https://code.claude.com/docs/en/cloud-environments#github-proxy), [Claude security and isolation](https://code.claude.com/docs/en/claude-code-on-the-web#security-and-isolation), [Codex internet access](https://learn.chatgpt.com/docs/cloud/internet-access), [Copilot risks](https://docs.github.com/en/copilot/concepts/security-governance-and-network-settings/risks-and-mitigations), [Copilot firewall](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall).

Patterns others add beyond these three: Devin's mandatory security profiles (git read-only, remove `gh` token, intersect-only narrowing, agent can only *request* more access); Cursor's redacted Runtime Secrets and signed commits; Vercel/Modal's **dynamic policy** (open egress for install, lock down before untrusted work); Daytona's response scrubbing and host-scoped placeholders. Nobody solves the "lethal trifecta" (private data + untrusted content + egress) within one session `[inference]`; the practical defense is that the three never coexist with write power: default-off/allowlisted egress, token never visible, push limited to non-protected branches with human merge.

## 4. Options and trade-offs for atrium

| Decision | Option A | Option B | Notes |
|---|---|---|---|
| Environment definition | **Declarative record**: toolsets + env + secrets + setup script + egress list (matches plan) | Dockerfile/OCI | Plan already chose Nix-built toolset rootfs; Claude/Jules/Codex accept "no base image replacement"; Cursor/Vercel/Daytona allow OCI. OCI is a later extension. |
| Setup caching | Build a **per-environment disk snapshot** (setup script once, reflink/overlay on create); invalidate on script/var/secret/toolset change; TTL ~7 d + manual reset | Re-run setup per sandbox | Claude/Codex/Cursor/Devin/Jules all snapshot; Codex invalidates on any config change; Devin rebuilds ~24 h. |
| Idle | Suspend VM (memory+disk) after N min, resume on exec; delete with thread (plan) | Disk-only stop | E2B/Daytona VM class pause both; Claude/Sprites keep disk only. Memory suspend is nicer for dev servers, costs RAM-snapshot disk. |
| Secrets | 3 classes: setup-only, runtime-env, **proxy-injected (host-scoped, header-only, response-scrubbed)** | All env vars | Plan's setup-only/runtime is Codex/Cursor; add injected class (Claude/Codex/Daytona/Vercel). GitHub token fits the injected class. |
| Egress | Presets (none / package managers / custom / full) + per-env list, default **package-managers preset**, editable live; setup phase may be broader than agent phase | one flat list | Codex shape fits plan best. |
| Egress enforcement | MITM-less SNI+DNS proxy; TLS-terminate only hosts with injection rules | Full MITM | Vercel's design; documents SNI/Host fronting, so injection hosts should be TLS-terminated. |
| Resources | Manager-config profiles (`small/medium/large`) per plan decision | Free-form | Matches Daytona sizes; all vendors fix tiers. |

## 5. Recommendation: minimal replica atrium needs

Tied to plan decisions (one manager per instance, microVM, KVM, mTLS HTTP/JSON, one sandbox per thread):

1. **Environment** = `{name, repo?, toolsets[], profile, env{}, secrets[{name, scope: setup|runtime|inject, hosts[]}], egress{preset, domains[], methods?}, setup_script}`; stored in atrium DB, pushed to manager at sandbox create. No Dockerfile, no org sharing.
2. **Build step**: manager builds toolset rootfs (Nix) once; per environment, runs `setup_script` with setup-scope secrets and wide-but-listed egress, captures a disk snapshot, records content hash of (toolsets+script+env+setup secrets). Mismatch -> rebuild. Failed rebuild never replaces last good (Cursor).
3. **Per-thread sandbox** boots from that snapshot (copy-on-write), clones the repo through the manager's git proxy, injects runtime env, runs a post-setup health command.
4. **Lifecycle**: idle suspend (default 10 min), hard TTL on suspended sandboxes (default 7 d idle), explicit delete on thread delete, snapshot TTL 30 d from last use; env vars re-read on every resume (Claude semantics); setup is *not* rerun on resume.
5. **Egress**: manager-side proxy; default preset "package managers" + user domains; `deny-all` and `allow-all` modes; live update via API; blocked requests surfaced to the agent/UI as an "access requested" event (Devin/Copilot) that the operator approves per sandbox.
6. **Credential brokering**: GitHub token and any `inject` secrets live only in the manager/proxy; header-only, https-only, host-scoped, response-scrubbed (Daytona rules). Typed git/gh tools (plan) enforce: push non-default branches, draft PRs, no delete/tag/merge (Claude proxy + Copilot rules).
7. **UX minimum**: environment editor (settings page), per-thread picker (plan), setup log stream, "Reset cache", effective-network-policy display, blocked-domain list, diff view.

## 6. Risks

- Domain allowlists are fronting-prone (SNI vs Host) in Modal/Vercel/E2B/Copilot docs; allowing github.com or a CDN is an exfil channel (push to attacker repo). Mitigate with TLS-terminating proxy for sensitive hosts and per-repo scoping like Claude's proxy.
- Disk snapshots can capture secrets written during setup (Cursor warns about `.env.local`). Setup-scope secrets must be scrubbed or the build sandbox discarded after snapshot.
- Memory suspend persists in-RAM secrets/tokens to disk.
- Cache invalidation churn (Codex invalidates on any change; Claude only caches <=~5 min setups): decide a setup timeout.
- Placeholders break tools that transform the secret (Basic auth encoding; body/query auth) - Daytona/Codex both document this.
- Atrium today has no equivalent: code mode runs Monty in a socket-activated, no-network, read-only systemd unit (`src/atrium/agent/codemode.py:7-12`, `nix/module.nix:320-346`), and a per-thread opt-in Anthropic code sandbox (`src/atrium/agent/files.py:56,432`; `src/atrium/web/templates/_sandbox.html:2`) with an explicit data-retention warning; v2 replaces both.

## 7. New questions / candidate tickets

1. **Egress proxy: SNI vs TLS termination** - type: research/design, phase B, area sandbox/security. Which hosts get TLS-terminated (injection, GitHub), how to ship a CA into toolset images, how to handle Host/SNI mismatch and non-HTTP protocols (git over ssh, postgres). (Overlaps #25; confirm no duplicate.)
2. **Environment snapshot/build semantics** - design, B, sandbox. Build-once-per-env, invalidation hash, setup timeout, secret scrubbing before snapshot, failed-build fallback, reset-cache button.
3. **Access-request flow for blocked egress** - design, B, sandbox+UI. Agent/operator approval to widen an environment or a single sandbox (Devin/Copilot pattern); persistence of approved domains.
4. **Prompt-injection posture for secretary + coding in one agent** - research/design, B, security. Which tools may combine untrusted content (mail, web, issues) with push/egress rights; per-thread "untrusted input seen" taint that drops egress/push (no vendor does this `[inference]`). Includes mail threads mixing with sandbox threads.
5. **Active-resource accounting and quotas for the manager** - design, B, sandbox. Concurrent sandboxes cap, suspended-disk quota, per-profile limits (Daytona org caps, Vercel concurrency).
6. **Port forwarding / live preview and terminal attach** - feature, B, sandbox+UI. Cursor forwarded ports, Devin ssh/forward, Sprites URLs: do we need web preview of dev servers and an interactive terminal?
7. **Services that survive suspend** - design, B. Sprites "Services", Cursor `start`/`terminals`: how a thread runs a dev server/DB across suspend-resume (and the Daytona caveat that processes don't count as activity).
8. **Personal vs shared values** - skip (single user per instance), recorded so nobody re-opens: Codex personal vault, Claude org-shared environments do not apply.
9. **Attribution and audit trail** - feature, B. Signed commits (Cursor/Copilot), session URL trailers (Claude), co-author, link from commit to thread. Commit identity for GitHub OAuth user vs bot.
10. **Snapshot/fork of a thread's sandbox** - feature, later. Modal/Daytona/E2B fork and "branch from message" already exists in atrium chat (`src/atrium/agent/service.py:387-391`); decide whether a branched thread forks its sandbox.

## 8. Facts I could not verify

- Jules network policy, secret handling, tier sizes and idle lifecycle: not in the fetched docs page.
- Sprites network-policy API reference page (404 on my path); the details above come from a search summary of Fly pages.
- Devin session sleep/wake timing and VM size: not read (docs index shows blueprints, security profiles, outposts; I did not open a lifecycle page).
- Claude Code "default allowed domains" full list not reproduced (see [doc](https://code.claude.com/docs/en/cloud-environments#default-allowed-domains)).
