# License options for atrium v2 (ticket #36)

Status: research only. The owner chooses. No licence file exists in the repo today (`ls LICENSE*` in main checkout: none; no `license` key in `pyproject.toml:1-17` or `nix/package.nix:1-24`), so as of now the code is "all rights reserved" by default even though the repo is public.

## 1. Answer in brief

- Every dependency we use today, and every Go dependency we are likely to use, is permissive (MIT / BSD / Apache-2.0 / 0BSD / MPL-2.0 at worst). Nothing in the dependency set forces a licence on atrium. Copyleft-licensed things we touch (qBittorrent GPL, Claude Code proprietary, Grafana AGPL, ntfy Apache-2.0-or-GPL-2.0) are separate processes reached over a network or CLI, not linked.
- Any of MIT, Apache-2.0, MPL-2.0, GPL-3.0, AGPL-3.0 or FSL is therefore legally available. The decision is a values/strategy choice, not a compatibility constraint.
- Recommendation (owner decides): **Apache-2.0** if the goal is maximum adoption plus Go-contributor comfort plus an explicit patent grant; **AGPL-3.0-or-later** only if the owner specifically wants to prevent closed hosted forks. Reasoning in section 5.

## 2. Inventory: current Python deps

Source of the list: `pyproject.toml:9` and `nix/package.nix:15`. Licences from PyPI metadata (queried 2026-10-17, `https://pypi.org/pypi/<name>/json`).

| Dependency | Version | Licence |
|---|---|---|
| anthropic | 1.15.0 | MIT |
| caldav | 3.5.0 | GPL-3.0-or-later OR Apache-2.0 (dual; we can take Apache) |
| fastapi | 0.143.3 | MIT |
| google-api-python-client | 2.201.0 | Apache-2.0 |
| google-auth | 2.62.0 | Apache-2.0 |
| httpx | 0.28.1 | BSD-3-Clause |
| icalendar | 7.3.0 | BSD-2-Clause |
| jinja2 | 3.1.6 | BSD-3-Clause (classifier) |
| markdown-it-py | 4.2.0 | MIT |
| nh3 | 0.3.7 | MIT |
| openpyxl | 3.1.5 | MIT |
| pydantic-monty | 1.1.0 | MIT |
| python-docx | 1.2.0 | MIT |
| python-multipart | 0.0.32 | Apache-2.0 |
| sqlite-vec | 0.1.9 | MIT OR Apache-2.0 (dual) |
| trafilatura | 2.3.1 | Apache-2.0 |
| uvicorn | 0.54.0 | BSD-3-Clause |
| vobject | 0.9.9 | Apache-2.0 |

Runtime tools wired by `nix/module.nix` (not linked): vdirsyncer (config at `nix/module.nix:14-22`; BSD-3-Clause [inference from memory, not re-verified]), qBittorrent (GPLv2+ source, GPLv3+ binary, with OpenSSL exception; https://raw.githubusercontent.com/qbittorrent/qBittorrent/master/README.md; talked to over its Web API only), ntfy (Apache-2.0 / GPLv2 dual; https://github.com/binwiederhier/ntfy README "License" section). Python is deleted after cutover, so this table matters only for the Phase A window and for derivative-licence history (we are the sole copyright holder of the Python code unless others contributed: [inference] check `git shortlog -sne` before relicensing).

## 3. Web assets (shipped in the build)

Built in `nix/static.nix:1-40` (pinned npm tarballs):

| Asset | Version | Licence | Source |
|---|---|---|---|
| basecoat-css | 1.0.2 | MIT | https://registry.npmjs.org/basecoat-css/latest |
| htmx.org | 2.0.11 | 0BSD (Zero-Clause BSD; GitHub shows NOASSERTION because of the unusual id) | https://registry.npmjs.org/htmx.org/latest, https://raw.githubusercontent.com/bigskysoftware/htmx/master/LICENSE |
| tailwindcss (build tool, v4) | 4.3.3 | MIT | https://registry.npmjs.org/tailwindcss/latest |

Notes: Tailwind is only a build tool, its generated CSS output carries no licence obligation beyond the MIT notice of the preflight if we choose to keep it. Basecoat and htmx ship inside the binary via `embed.FS` in v2; MIT needs the copyright notice retained (ship a `THIRD_PARTY_NOTICES` file or a `/licenses` page). 0BSD needs nothing. All of them are compatible with every option below, including GPL/AGPL (MIT and BSD code may be included in GPL works).

## 4. Likely Go deps (inferred from the plan)

Licences via `gh api repos/<r>` on 2026-10-17 unless noted. "NOASSERTION" in GitHub means the SPDX detector failed; for those I read the LICENSE file head.

| Need | Candidate | Licence |
|---|---|---|
| IMAP | emersion/go-imap (v2) | MIT |
| MIME messages | emersion/go-message | MIT |
| CalDAV/CardDAV | emersion/go-webdav | MIT |
| iCal / vCard | emersion/go-ical, emersion/go-vcard | MIT |
| Anthropic API | anthropics/anthropic-sdk-go | MIT |
| SQLite (cgo) | mattn/go-sqlite3 | MIT |
| SQLite (no cgo) | modernc.org/sqlite | BSD-3-Clause ("The Sqlite Authors", https://gitlab.com/cznic/sqlite/-/raw/master/LICENSE) |
| SQLite (wasm, no cgo) | ncruces/go-sqlite3 | MIT |
| SQLite vectors | asg017/sqlite-vec | Apache-2.0 (repo) / MIT per PyPI package: dual |
| Encrypted SQLite | SQLCipher itself | BSD-style (BSD-3-Clause per GitHub); go bindings mutecomm/go-sqlcipher: BSD/MIT-style LICENSE, unmaintained since 2023 (https://github.com/mutecomm/go-sqlcipher). Encryption choice belongs to the DB-encryption ticket |
| OIDC | coreos/go-oidc | Apache-2.0 |
| OIDC (alt, full RP/OP) | zitadel/oidc | Apache-2.0 |
| OAuth2 | golang.org/x/oauth2 | BSD-3-Clause |
| JWT | golang-jwt/jwt | MIT |
| GitHub API | google/go-github | BSD-3-Clause |
| git in-process (alt) | go-git/go-git | Apache-2.0 |
| Router | go-chi/chi | MIT |
| Sessions | alexedwards/scs (MIT), gorilla/sessions (BSD-3) | permissive |
| TOML | BurntSushi/toml (MIT), pelletier/go-toml v2 (MIT; GitHub NOASSERTION) | permissive |
| CLI | spf13/cobra | Apache-2.0 |
| Logging | rs/zerolog | MIT |
| Markdown / sanitising | yuin/goldmark (MIT), microcosm-cc/bluemonday (BSD-3) | permissive |
| Spreadsheets | xuri/excelize | BSD-3-Clause |
| Docx | nguyenthenguyen/docx | MIT (small, low activity) |
| Web extraction | go-shiori/go-readability (MIT), markusmobius/go-trafilatura (Apache-2.0) | permissive |
| Migrations | pressly/goose (MIT), golang-migrate (MIT) | permissive |
| Job queues | riverqueue/river | **MPL-2.0** (file-level copyleft; fine as an unmodified dependency) |
| Cron | robfig/cron, go-co-op/gocron | MIT |
| Notion | jomei/notionapi | BSD-2-Clause |
| Sandbox VMM | firecracker-microvm/firecracker (Apache-2.0), firecracker-go-sdk (Apache-2.0), Cloud Hypervisor (dual Apache-2.0 / BSD-3-Clause per upstream; I could not fetch a LICENSE file to confirm, treat as [inference]) | permissive, separate process |
| gVisor (alt) | google/gvisor | Apache-2.0 |
| mTLS / QUIC helpers | stdlib crypto/tls; quic-go (MIT) | permissive |
| JS-in-Go (if ever wanted) | dop251/goja | MIT |
| Go itself / stdlib / x/* | | BSD-3-Clause |

Peer systems atrium integrates with over the network: Pocket ID (BSD-2-Clause), ntfy (Apache-2.0/GPLv2), qBittorrent (GPL), Grafana (AGPL-3.0; https://github.com/grafana/grafana/blob/main/LICENSE), Claude Code (proprietary, "All rights reserved ... Commercial Terms of Service", https://raw.githubusercontent.com/anthropics/claude-code/main/LICENSE.md), pi coding agent (badlogic/pi-mono, MIT). Talking to a GPL/AGPL/proprietary service over HTTP/CLI does not make atrium a derivative work of it. Redistribution is the only trap: if the NixOS module or a release bundle ever ships Claude Code in a rootfs, that needs Anthropic's terms, not just our licence. [inference]

Finding: no copyleft (GPL/AGPL/LGPL) library is planned to be linked. Only MPL-2.0 (River, optional) appears, and MPL imposes duties only on modified MPL files. This means the repo licence is free of dependency constraints.

## 5. Licence options and trade-offs

Context for the trade-off: self-hosted personal assistant that others deploy (one instance per user, no multi-tenant), public repo, Go developers collaborating, two binaries (atrium + sandbox manager), static release binaries and NixOS modules shipped, mail/credential-handling so trust matters, OIDC and optional hosted offers are plausible but not planned.

| Option | Patent grant | Network copyleft | Contributor friction | Dep compatibility | Fit |
|---|---|---|---|---|---|
| **MIT** (or 0BSD/BSD) | No explicit grant | None | Lowest; Go ecosystem default for libs and many apps (emersion, chi, anthropic-sdk-go) | All deps | Simple; but no patent clause, and anyone may fork into a closed SaaS |
| **Apache-2.0** | Yes, explicit, with defensive termination | None | Low; familiar to Go people (cobra, go-oidc, ntfy, Kubernetes) and corporate-friendly; needs NOTICE retention and state-changes | Compatible with all listed deps (one-way with GPLv2-only, irrelevant here) | Strongest permissive choice |
| **MPL-2.0** | Yes | File-level only (modified files stay MPL) | Low-moderate; small, well understood; allows proprietary larger works | All deps | Middle ground: keeps improvements to our files open without scaring integrators; less common in Go apps (River uses it) |
| **GPL-3.0** | Yes | No (hosting a modified copy privately without distributing is allowed) | Moderate; some Go devs and employers refuse GPL; blocks others from embedding atrium into permissive projects | All deps can be combined into a GPLv3 work (Apache-2.0 deps OK with GPLv3, not GPLv2) | Protects distributed binaries, not hosted forks |
| **AGPL-3.0-or-later** | Yes | Yes: §13 requires offering source to users who interact with a modified version over a network (https://www.gnu.org/licenses/agpl-3.0.txt §13) | Highest among OSI licences; many companies ban AGPL, some contributors avoid it; compliance questions for the personal NixOS-module host config | All deps | Best for "nobody builds a closed hosted atrium"; Grafana, Pocket ID-adjacent ecosystems (Grafana is AGPL) already live with it |
| **Source-available: FSL-1.1-MIT / FSL-1.1-Apache-2.0** | Per converted licence (Apache variant) | Not copyleft; forbids "Competing Use" (commercial product substituting for the software) for two years, then converts to MIT/Apache-2.0 automatically (https://fsl.software/, https://github.com/getsentry/fsl.software) | Higher; not OSI open source, so some devs and distros (nixpkgs treats it as unfree [inference]) will not touch it; needs a CLA-free but explicit story | Does not conflict with deps, but a source-available project cannot be submitted to nixpkgs as free [inference] | Only if the owner worries about a competing paid hosted service; unusual for a personal tool |
| BUSL, SSPL, Elastic | n/a | broad | high | n/a | Not recommended: bespoke terms, hostile to community |
| Dual (AGPL + commercial) | Yes | Yes | Needs a CLA or DCO-plus-assignment to relicense; heavy for a hobby project | All deps | Only if the owner may sell exceptions |

Specific points:

- **Patent grant.** Matters mostly for corporate contributors or users. Apache-2.0, MPL-2.0, GPL-3.0, AGPL-3.0 include one. MIT and BSD do not (implied licence only).
- **Network copyleft.** Only AGPL (and SSPL) trigger on remote use. With one-instance-per-user self-hosting, AGPL mostly bites a hypothetical third party running a modified hosted version. It also bites the owner if they ever want to combine atrium with closed code on their own hosts [inference]. It does NOT infect user data, config, or services atrium talks to.
- **Contributor friction / DCO vs CLA.** Permissive licences with the Developer Certificate of Origin (sign-off) is the lowest friction. A CLA is only needed if the owner wants the right to relicense later (e.g. to move from AGPL to a commercial exception). If the owner wants the option to relicense, adopt DCO now and keep contributions few, or require CLA from day one; retrofitting is hard.
- **Dependency compatibility.** No constraint (section 4). The one dual-licensed Python dep (caldav GPL-or-Apache) must be consumed under Apache-2.0 only if atrium is permissive, which is allowed. Python goes away anyway.
- **Sandbox manager as a second binary.** It can carry the same licence or a different one. It runs untrusted code sandboxes; a permissive licence helps hosting providers adopt it, AGPL would make a hosted sandbox service disclose changes. Choose one licence for the monorepo unless there is a reason to split.
- **Nixpkgs / packaging.** OSI licences (all except FSL) are "free" in nixpkgs; FSL would be marked unfree [inference, check nixpkgs `lib.licenses`].
- **Trademark/naming.** None of the licences grant trademark; "atrium" is a common word. Consider a short TRADEMARKS note only if a hosted offer is planned (new question below).
- **Copyright holders.** Before relicensing or adding a header, confirm who authored the existing Python code (`git shortlog -sne`); [inference] it is the owner alone, since the repo is private-to-public and tickets mention Go devs only as future collaborators.

## 6. Recommendation (non-binding)

Apache-2.0 for the whole repo, with DCO sign-off and no CLA. Reasons tied to the plan:

1. The stack is Go, where Apache-2.0/MIT/BSD is the default for almost everything we depend on (section 4); Go devs will recognise it.
2. "Others deploy it" and Phase-B plans for an OIDC-fronted multi-instance world favour a licence companies and distros accept; Apache-2.0 adds an explicit patent grant over MIT, relevant because atrium wraps third-party APIs and VMMs (Firecracker is Apache-2.0 too).
3. The risk Apache does not cover (a closed hosted fork) is small for a per-instance, self-hosted assistant and the owner has said multi-user means multiple instances, not a hosted service.

If the owner values copyleft over adoption, the runner-up is **AGPL-3.0-or-later** (patent grant, network clause, no dependency conflicts). Pick MIT only if simplicity beats the patent grant. Avoid FSL/BUSL unless a commercial hosted competitor is a real concern.

## 7. Risks

- Publishing without a licence file leaves the repo legally unusable by others today; add the choice before inviting collaborators.
- Changing licence later requires consent of all contributors; settle before Go devs start committing.
- Redistributing proprietary agents (Claude Code) in sandbox images: separate terms, outside our licence choice.
- SBOM/attribution drift: MIT/BSD/Apache dependencies all require notice retention in distributed binaries; the static release binaries need a generated notices file.
- Dep churn: unmaintained candidates (mutecomm/go-sqlcipher last push 2023-06, nguyenthenguyen/docx 2024-07) are risks regardless of licence.

## 8. Candidate new tickets

1. **Add LICENSE, headers policy and third-party notices** | type: build | phase: A (before first Go commit) | area: repo/release | question: which SPDX identifier goes in LICENSE, are per-file headers needed, how is THIRD_PARTY_NOTICES generated (go-licenses / nix) and embedded in the binary and release archives?
2. **Contribution policy: DCO vs CLA** | type: decision | phase: A | area: community | question: do we need relicensing rights; if yes CLA now, else DCO sign-off enforced in CI.
3. **Audit copyright ownership of existing Python code and docs** | type: research | phase: A | area: repo | question: who holds copyright (git history, any AI-generated or third-party snippets, vendored files) before applying a licence.
4. **Licence for the sandbox manager and rootfs toolsets** | type: decision | phase: A | area: sandbox | question: same licence as atrium or different; what licences apply to Nix-built rootfs contents (python, go, bun, rust, git, gh) that the manager distributes or runs for others.
5. **Redistribution terms for coding agents in sandbox images** | type: research | phase: B | area: sandbox/agents | question: can Claude Code (proprietary, Commercial Terms) and pi (MIT) be baked into rootfs or must they be fetched at setup per user.
6. **Trademark / name policy** | type: decision | phase: B | area: community | question: is "atrium" protected, do we want a TRADEMARKS note if third parties host it.
7. **Dependency licence gate in CI** | type: build | phase: A | area: CI | question: add a go-licenses/nix check to the `check` aggregate that fails on GPL/AGPL/unknown additions, aligned with the chosen licence.
8. **Docs/UI asset licence** | type: decision | phase: B | area: docs | question: should docs/** and the README text carry the same licence or CC-BY, and should the UI show an About/licences page.
