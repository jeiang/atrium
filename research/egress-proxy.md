# Egress proxy and credential injection (ticket #25)

Question: per-environment domain allowlist enforced outside the VM, and getting a GitHub token (and the Anthropic credential) used by `git`/`gh`/Claude Code in the VM without the secret entering the VM. Plan context: one sandbox per thread, microVM on a Linux/KVM manager, HTTPS+mTLS manager API, GitHub token never in the VM, typed git/gh harness tools run by the manager, Claude Code in the sandbox with subscription login, Anthropic key one per instance.

Today is 2026-10-17. `[inference]` marks my reasoning rather than a cited fact.

## 1. Answer in short

1. **Allowlist enforcement**: the VM gets no route except to a manager-owned proxy. Enforce at three layers: (a) host nftables default-drop on the VM's tap interface, allowing only proxy IP:port (and optionally a manager DNS resolver); (b) an HTTP CONNECT proxy outside the VM that matches the CONNECT host against the environment's allowlist, then peeks the TLS ClientHello and requires SNI == CONNECT host, rejects ECH, resolves the name itself, and refuses private/link-local/metadata addresses; (c) no DNS for the VM except what the proxy needs (proxy resolves, VM never has to). This is the shape both vendors use (below). A SNI/CONNECT filter without decryption is sufficient for *allowlisting*; it cannot see paths, methods or bodies.
2. **GitHub without the token in the VM**: three viable mechanisms; I recommend layering two: **(A) manager-side typed git/gh tools** (plan already says this) for push/PR, and **(B) a manager-hosted plain-HTTP git smart-HTTP gateway** (git `url.<base>.insteadOf` → gateway on the host-only link, per-sandbox scoped credential, gateway attaches the real short-lived GitHub App token and checks repo/branch/op). This is Anthropic's cloud design. **Do not** hand the token to a credential helper over vsock: even short-lived, the token then exists in VM memory and processes, which contradicts the plan. TLS MITM of `github.com`/`api.github.com` is the fallback only if raw `gh` inside the VM is a requirement.
3. **Anthropic API key**: yes, the proxy can inject it and meter it. Best placement: sandboxes point `ANTHROPIC_BASE_URL` at a gateway endpoint **hosted by atrium itself** (reached via the manager over the existing mTLS channel); atrium injects the instance key, parses `usage`, writes its existing `usage` table and enforces the budget. The key never leaves the atrium host and never reaches the (possibly remote) manager. No MITM needed.
4. **Claude Code subscription credential**: Anthropic's policy permits an end user signing in to the *unmodified* Claude Code binary with their own subscription, including where a platform hosts it, but forbids developers from "collect, store, or intermediate Claude.ai credentials or session tokens". So the policy-clean path is `claude` logging in inside the VM (or the owner pasting a `claude setup-token` token as a runtime secret), accepting that the credential lives in the VM; substituting the subscription token at a proxy is a policy gray area and should not be built without asking Anthropic. Default recommendation is the metered API-key path.

## 2. Domain allowlist enforcement outside the VM

### 2.1 What the vendors do (primary sources)

| Source | Mechanism | Detail |
|---|---|---|
| Claude Code on the web (Anthropic-hosted) | All outbound traffic through an HTTP/HTTPS proxy; per-environment access level None/Trusted/Full/Custom; custom list supports `*.` wildcards | [Cloud environments: network access](https://code.claude.com/docs/en/cloud-environments#access-levels), [Security proxy](https://code.claude.com/docs/en/cloud-environments#security-proxy). Changes apply to running sessions within about a minute. Exceptions that bypass the list: GitHub proxy, MCP connectors, hosts of network secrets, the Anthropic API even at None. |
| Claude Code self-hosted | "Default-deny egress at your own network boundary"; block `169.254.169.254` in the container netns; do not rely on IP allowlists | [Self-hosted deploy: harden](https://code.claude.com/docs/en/self-hosted-environments-deploy) |
| Claude Code local sandbox / `sandbox-runtime` | No route out; proxy on the host reached through a unix socket (bubblewrap) ; HTTP proxy + SOCKS5 proxy enforce `allowedDomains`/`deniedDomains` with `:port` suffixes; after the name passes, proxy resolves it and refuses private/loopback/link-local results; optional experimental `tlsTerminate` | [Sandboxing blog](https://www.anthropic.com/engineering/claude-code-sandboxing), [sandboxing docs](https://code.claude.com/docs/en/sandboxing), [sandbox-runtime README](https://github.com/anthropic-experimental/sandbox-runtime) |
| Codex cloud (legacy docs) | Setup phase has internet; agent phase internet off by default; per-environment: Off / On with allowlist preset (None, Common dependencies, All) and an allowed-HTTP-methods restriction (GET/HEAD/OPTIONS only) | [Codex internet access](https://learn.chatgpt.com/docs/cloud/internet-access). Method restriction implies body-level inspection (TLS termination) [inference]. Secrets are available only in the setup phase (OpenAI support statement summarized from [community thread](https://community.openai.com/t/codex-cloud-secret-injection/1263867/8), not a primary doc). |

Takeaways: (1) both clouds default-deny and make the allowlist a per-environment object; (2) Anthropic explicitly documents that hostname-only enforcement can be bypassed by domain fronting ([sandboxing docs, security limitations](https://code.claude.com/docs/en/sandboxing): "the proxy makes its allow decision from the client-supplied hostname without inspecting TLS ... can potentially use domain fronting"); (3) allowing a broad host such as `github.com` is itself an exfiltration path (same page, and [Codex prompt-injection example](https://learn.chatgpt.com/docs/cloud/internet-access)).

### 2.2 Layers for atrium's manager

Layer 0, network topology (prefer this over relying on the proxy alone):
- Each microVM gets a tap device in its own host netns (or shares a per-manager bridge with isolation). nftables on the manager: default `drop` for forward; allow only `tcp -> <proxy ip>:<proxy port>` (and `udp/tcp 53 -> manager resolver` if DNS is provided). Block all other private ranges, `169.254.169.254`, the manager API port, the host's loopback services. Reference: [nftables quick reference](https://wiki.nftables.org/wiki-nftables/index.php/Quick_reference-nftables_in_10_minutes).
- Alternative with no NIC at all: virtio-vsock only, with the manager listening and bridging into the proxy. Firecracker exposes vsock to the host as a unix socket ([firecracker vsock doc](https://github.com/firecracker-microvm/firecracker/blob/main/docs/vsock.md)). A NIC-less VM cannot bypass the proxy by construction, but every tool must be proxy-aware: in-VM shim listens on `127.0.0.1:3128`, forwards bytes over vsock (what `sandbox-runtime` does with socat + unix socket on Linux; [sandboxing docs](https://code.claude.com/docs/en/sandboxing)). Tools that ignore `HTTP(S)_PROXY` (plain ssh, DB clients) simply fail, which matches the plan (HTTPS-only egress) [inference].

Layer 1, the proxy (new component in the manager, Go):
- Accept `CONNECT host:port`. Match host against the environment's allowlist (exact, `*.suffix`, optional `:port`). Deny-list wins.
- Peek ClientHello (no decryption): require SNI present and equal to CONNECT host (defeats the CONNECT-host vs SNI mismatch used for domain fronting; compare Squid's peek-and-splice fix described in [gh-aw issue #60771](https://github.com/github/gh-aw/issues/60771) and [PR #8878](https://github.com/github/gh-aw-firewall/pull/8878)). Squid reference: [SslPeekAndSplice](https://wiki.squid-cache.org/Features/SslPeekAndSplice), [`ssl_bump`](https://www.squid-cache.org/Doc/config/ssl_bump/).
- Reject ClientHellos carrying the ECH extension: with [Encrypted Client Hello (RFC 9849)](https://www.rfc-editor.org/rfc/rfc9849.txt) the inner SNI is hidden, so the SNI==CONNECT check is no longer verifiable; clients fall back to plaintext SNI when the server/proxy refuses [inference].
- Resolve the name on the proxy and dial the resolved IP itself (never a client-supplied IP), reject non-public results ([Smokescreen](https://github.com/stripe/smokescreen) does hostname ACL + public-IP check; `sandbox-runtime` has the same resolved-address check).
- Non-CONNECT plain HTTP (port 80): allow by Host header + same resolver checks, or deny entirely (simpler; pip/npm/go default to https). `[inference]` recommend deny.
- Log every allow/deny with sandbox id and domain for the thread UI ("blocked: example.com, add to allowlist?"), as `sandbox-runtime` does with its ask callback and Claude Code does with its permission prompt.
- Allowlist is a live object: environments are user-editable, so the proxy reads the current list per connection (as `sandbox-runtime` does) and the manager API needs `PUT /sandboxes/{id}/egress`.

Layer 2 (optional, selective TLS termination): only for hosts that need header injection or method/path policy. Everything else is spliced untouched, so cert-pinned clients and mTLS upstreams keep working (same split as `sandbox-runtime`'s `tlsTerminate.excludeDomains`, [README](https://github.com/anthropic-experimental/sandbox-runtime)).

Library options for the Go proxy: write on `net/http` + `crypto/tls` (ClientHello peek is ~100 lines; the `ClientHelloInfo` callback exposes SNI but not a pre-dial hook, so peek via `tls.Server` with `GetConfigForClient` or a byte-peeking `net.Conn` wrapper); [Smokescreen](https://github.com/stripe/smokescreen) (Go, hostname ACL, mTLS client auth, no MITM, library-embeddable) for the CONNECT+ACL layer; [mitmproxy](https://docs.mitmproxy.org/stable/concepts-certificates/) or goproxy only if broad MITM is chosen [inference, not evaluated in depth; R17GoLibs may cover library choice].

### 2.3 DNS filtering

- With a CONNECT proxy the VM does not need DNS for allowed hosts: CONNECT carries the name, the proxy resolves. Give the VM no resolver at all (or a resolver that answers NXDOMAIN) so DNS tunnelling has no path [inference].
- If transparent mode is needed (tools that ignore proxy env), give the VM a manager-side resolver that answers only allowlisted names and have nftables redirect 443 to the proxy; the proxy must still resolve SNI itself and not trust the original destination IP, else SNI spoofing against a CDN IP is possible.
- Beware DoH/DoT: an allowlisted public resolver or any host that can relay would defeat DNS filtering; with no direct route, only allowed hosts are reachable anyway.

### 2.4 What an allowlist still cannot stop

- Exfiltration through any allowed host that accepts writes (github.com gists/issues/PR comments, package registries with publish, `raw.githubusercontent.com` reads that encode data in URLs). Both Anthropic and OpenAI document this. Mitigation is method/path policy via TLS termination (Codex GET-only mode) and keeping the preset small. Cross-repo exfiltration through the *agent's own GitHub credential* is a separate scope question (section 3.5).
- Allowed hosts sharing CDN anycast (fronting) are handled by SNI==CONNECT; host-header fronting inside a spliced TLS session cannot be seen without termination [inference].

## 3. GitHub access without the token in the VM

### 3.1 How Anthropic does it
Source: [cloud environments: GitHub proxy](https://code.claude.com/docs/en/cloud-environments#github-proxy), [sandboxing blog](https://www.anthropic.com/engineering/claude-code-sandboxing).
- git client in the VM uses a scoped credential; a dedicated proxy verifies it, checks the content of the git interaction, swaps in the real token, forwards to GitHub. Real credentials stay encrypted server-side.
- Proxy rejects branch deletions and pushes of anything other than a branch (tags). It deliberately does **not** limit which branch is updated ("use branch protection rules or rulesets on GitHub").
- API requests are scoped to repositories attached to the session; GraphQL is rejected outright (so `gh pr`/`gh issue` fail; `gh api repos/...` REST works).
- `gh`/scripts see `GH_TOKEN`/`GITHUB_TOKEN` = literal `proxy-injected`; the proxy substitutes the credential on outbound requests ([same page, "work with GitHub issues"](https://code.claude.com/docs/en/cloud-environments#github-proxy)). Mechanism for the substitution on HTTPS is not documented [inference: it must see decrypted requests, i.e. TLS termination or an in-path gateway].
- Self-hosted runners: "mint a short-lived, least-scoped token per session" in the wrapper, or opt in to the Anthropic git proxy ([Configure git](https://code.claude.com/docs/en/self-hosted-environments-deploy)).
- Sandbox-runtime also supports masking: env var/file values replaced with a per-session sentinel and substituted in the proxy, which requires `tlsTerminate`, and injects only to named `injectHosts` ([sandboxing docs: mask credentials](https://code.claude.com/docs/en/sandboxing)).

### 3.2 How Codex does it
Secrets exist only in the setup phase and are removed before the agent phase; `gh auth setup-git` in setup leaves a usable credential behind (so push in the agent phase means the credential persists) ([agent internet access](https://learn.chatgpt.com/docs/cloud/internet-access), [community thread](https://community.openai.com/t/codex-cloud-secret-injection/1263867/8)). No injection-proxy equivalent is documented, so Codex's model is "no credential during agent phase" rather than "credential outside VM".

### 3.3 Options for atrium

| Option | Token in VM? | Raw `git push`/`gh` in VM works? | Cost / risk |
|---|---|---|---|
| **A. Manager-side typed tools** (`git_push`, `pr_create`, ...). VM streams commits to the manager (git bundle / packfile over vsock or workspace disk), manager pushes. | Never | No (by design) | Must never run git against the untrusted `.git` directory on the host: repo config can execute commands (`core.fsmonitor`, hooks, `core.sshCommand`, `core.pager`, credential helpers) [inference from git behaviour; verify in implementation ticket]. Safe pattern: unpack the received pack into a fresh scratch bare repo with an empty config (`GIT_CONFIG_NOSYSTEM`, `-c core.hooksPath=/dev/null`, `protocol.allow=never` except https), validate refs, then push. Or run git inside a second, throwaway microVM. |
| **B. Git smart-HTTP gateway on the manager (plain HTTP on the host-only link)**. VM: `git config url."http://gw/gh/<sandbox>/".insteadOf https://github.com/` plus a per-sandbox bearer in the URL or a static credential helper. Gateway: verify bearer, parse the receive-pack command list (git pkt-line `command-list`, [gitprotocol-pack](https://git-scm.com/docs/gitprotocol-pack)), enforce policy, forward with the real token. | Never | `git clone/fetch/push` yes; `gh` no | No MITM, no CA. Needs a small git-protocol-aware reverse proxy (pkt-line parse of the first request chunk; stream the pack through unchanged). Policy: reject deletes, tags, default/protected branch, repos outside the sandbox's attached repos. |
| **C. Selective TLS MITM** of `github.com`, `api.github.com`, `codeload.github.com` with header injection (`Authorization: Basic base64(x-access-token:<tok>)` for git, `Bearer` for REST), placeholder `GH_TOKEN` in the VM. | Never | Yes, including `gh` (REST and GraphQL) | CA install in the VM (section 4), in-path TLS termination, you own request parsing for policy. Pinning clients: none for github.com expected [inference]. Largest attack surface in the manager (CA private key), but key never in the VM. |
| **D. Credential helper over vsock returning a short-lived installation token** | **Yes**, for token lifetime | Yes | GitHub App installation tokens expire after 1 hour and can be scoped to repos and permissions via the `repositories`/`permissions` body parameters ([GitHub docs](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app)). Limits blast radius but violates "token never enters the VM". An agent that can run code can read it from the helper's stdout, and exfiltrate over any allowed host. Acceptable only as an explicit, user-opted downgrade. |
| **E. Deploy keys / SSH agent forwarding over vsock** | Key never, but the agent socket is usable by anyone in the VM | git over ssh only | Needs port 22 egress or an ssh-aware proxy; repo-scoped deploy keys are write-capable per repo with no branch restriction. Not recommended. |

### 3.4 Is a SNI allowlist without MITM enough if the only injected credential is GitHub's via a vsock credential helper?
Enough for **allowlisting**; not enough for the plan's **"token never enters the VM"**, because option D is the only one that works without terminating TLS and it hands the token (short-lived, scoped) to the VM. If you want the plan statement to hold with an SNI-only proxy, use A and/or B (plain-HTTP gateway on the host-only link), neither of which terminates anything. Caveats if SNI-only is the whole story:
1. SNI==CONNECT enforcement and ECH rejection are mandatory (2.2).
2. No method/path policy (no "GET-only" mode like Codex).
3. A credentialed channel to `github.com` means the agent can push to any repo the installation token covers (if D) or only attached repos (A/B, enforced by the gateway). Scope the App installation token to the repos in the sandbox's environment.
4. With B in place, the VM needs no direct `github.com` access for authenticated pushes; keep `github.com` on the allowlist only for anonymous reads of public repos if wanted. Unauthenticated requests from the VM cannot write to GitHub, but a direct path to any allowed host remains a data-egress channel for unauthenticated services [inference].

### 3.5 Recommendation (ties to plan decisions)
- Phase A (port as-is): none of this exists in the Python app; this is Phase B (sandbox is new). No impact on the port.
- Phase B: ship **A + B**. A is already in the plan (typed tools: push non-default branch, open draft PR, never merge/delete). B gives the agent normal `git clone/fetch/commit/push` ergonomics inside the VM (and avoids a token round trip per push) without any CA. `gh`-style reads the agent needs (issues, PR comments, CI logs) become typed read-only harness tools (plan: "read-only tools work without sandbox"), which also removes GraphQL and cross-repo concerns.
- Enforce branch policy in **two places**: gateway/manager (no default/protected branch, no deletes/tags, only attached repos), and GitHub rulesets/branch protection on the repo as the backstop (Anthropic says rulesets are the real control; [rulesets docs: bypass lists](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets)). Provide a "protect default branch" checklist in the environment UI.
- Mint per-sandbox GitHub App installation tokens (1h, `repositories` limited to the environment's repo, `permissions` limited to contents:write + pull_requests:write) at the manager or atrium side right before each forwarded request; never persisted on the manager disk. Where tokens come from (OAuth user token vs GitHub App installation) is owned by the auth tickets; the egress design only needs "a bearer that can be minted/refreshed server-side" [inference].
- Defer C (MITM) until a concrete need (raw `gh` in VM, GraphQL, method policy) shows up; design the proxy so selective termination can be added per host (section 2.2 Layer 2).

## 4. Which toolchains honour a custom CA (only relevant for option C or for MITM of any host)

Install the CA into the rootfs system trust store at build/boot (Nix: add to the CA bundle used by the rootfs) *and* export env vars, because several tools ignore the system store. Build one combined PEM (public roots + atrium MITM CA) and point every variable at it, because several variables **replace** rather than extend the default roots (uv).

| Tool | Honours | Evidence |
|---|---|---|
| curl | `CURL_CA_BUNDLE`; `SSL_CERT_FILE` and `SSL_CERT_DIR` also supported; system store | [curl SSL certs](https://curl.se/docs/sslcerts.html) |
| git (HTTPS) | `http.sslCAInfo` / `GIT_SSL_CAINFO`; uses libcurl so likely also the curl vars | [git-config](https://git-scm.com/docs/git-config) (libcurl note is inference) |
| Go (`crypto/x509`, `go mod`, `go get`) | `SSL_CERT_FILE`, `SSL_CERT_DIR` on Unix; also the system store | [x509.SystemCertPool](https://pkg.go.dev/crypto/x509#SystemCertPool) |
| pip | `PIP_CERT`/`--cert`; also `REQUESTS_CA_BUNDLE` and `CURL_CA_BUNDLE`; system store only with `--use-feature=truststore` | [pip HTTPS certificates](https://pip.pypa.io/en/stable/topics/https-certificates/) |
| uv | Uses bundled Mozilla roots by default. `SSL_CERT_FILE`/`SSL_CERT_DIR` **replace** the default source; `--system-certs` (or `system-certs = true`) uses the OS store | [uv certificates](https://docs.astral.sh/uv/concepts/authentication/certificates/) |
| Node / npm | `NODE_EXTRA_CA_CERTS` (additive); `NODE_USE_SYSTEM_CA=1` / `--use-system-ca` | [Node CLI](https://nodejs.org/api/cli.html) |
| Bun (`bun install`, `fetch`) | `SSL_CERT_FILE` / `SSL_CERT_DIR` in addition to bundled roots | [Bun env vars](https://bun.sh/docs/runtime/env) (I did not verify whether Bun also reads `NODE_EXTRA_CA_CERTS`; set both) |
| Cargo | `http.cainfo` / `CARGO_HTTP_CAINFO` | [Cargo config](https://doc.rust-lang.org/cargo/reference/config.html) |
| Claude Code | `NODE_EXTRA_CA_CERTS`; `CLAUDE_CODE_CERT_STORE` default `bundled,system`; `HTTPS_PROXY`/`NO_PROXY` | [network config](https://code.claude.com/docs/en/network-config) |
| rustls-based Rust binaries, Python `certifi`-pinned apps, JVM, Java `keytool` stores, browsers/Playwright (own NSS/Chromium stores) | tool-specific | **Not verified**; Java and Chromium/Firefox need separate trust-store installs [inference]. Add to the toolset ticket's test matrix. |

Recommended env block inside every sandbox when any termination is enabled: `SSL_CERT_FILE`, `SSL_CERT_DIR` (omit if dir set up), `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`, `CARGO_HTTP_CAINFO`, all → one bundle path. Mitigations: selective termination (only injection hosts) so most traffic is verified against real roots; X.509 name constraints on the MITM CA limiting it to the injection hosts so a leaked CA cannot impersonate others (support varies by client [inference]); CA private key only in the manager, never in the rootfs or VM.

## 5. Injecting the Anthropic API key and metering

### 5.1 Mechanism
Claude Code supports a gateway through `ANTHROPIC_BASE_URL`; it then sends Anthropic Messages requests to `/v1/messages` (and optionally `/v1/messages/count_tokens`); the gateway must forward `anthropic-beta` and `anthropic-version` unchanged and relay SSE incrementally including `ping` events ([gateway protocol](https://code.claude.com/docs/en/llm-gateway-protocol)). Credential variable: `ANTHROPIC_AUTH_TOKEN` is sent as `Authorization: Bearer`, `ANTHROPIC_API_KEY` as `X-Api-Key` ([authentication precedence](https://code.claude.com/docs/en/authentication)). The VM gets a placeholder value (or a per-sandbox token) and the gateway replaces it. Other agents (pi) normally accept a base URL too [inference; R32PiClaude owns pi specifics].

Caveat from the docs: setting only `ANTHROPIC_BASE_URL` without a gateway credential variable keeps a saved claude.ai login as the credential; a credential variable overrides the subscription login ([subscriptions and gateways](https://code.claude.com/docs/en/llm-gateway)). So API-key mode needs the placeholder variable set.

### 5.2 Where to host it
Recommended: **atrium hosts the gateway** (`POST /gw/v1/messages`), the manager just relays from the VM over the existing authenticated channel with a per-sandbox bearer that atrium maps to a thread/run. Reasons:
- The instance Anthropic key stays on the atrium host (plan: atrium may be on a VPS, manager remote).
- Budget enforcement lives with the data: existing `atrium.agent.budget` and `usage` table (`src/atrium/agent/usage.py:20-66` records input, cache write 5m/1h, cache read, output, web searches, tier, cost; `budget.py` `check` pauses scheduled at 80% and chat at 100% per plan). A gateway can return 429 `budget_exhausted` when the cap is hit.
- Thread attribution is trivial: one sandbox per thread.
Alternative: manager-side injection with usage events POSTed to atrium: more moving parts and puts the key on the remote machine.

### 5.3 Metering (Anthropic response format)
- Non-streaming: `usage` object in the JSON response; total input = `input_tokens` + `cache_creation_input_tokens` + `cache_read_input_tokens` ([Messages API](https://platform.claude.com/docs/en/api/messages)).
- Streaming: `message_start` carries the initial `usage` (input and cache fields); `message_delta` carries top-level changes and its `usage` token counts are cumulative ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)). Gateway tees the SSE stream (strip/avoid compression: send `Accept-Encoding: identity` upstream so it can parse), records at `message_stop` or on disconnect.
- Edge cases to specify: client abort mid-stream (record input from `message_start` plus last cumulative output), server-side tools (`server_tool_use`, e.g. web search counts, `usage.py:34-35` already handles `searches`), errors returned as SSE `error` events, `count_tokens` calls (free; do not record), retries, multiple models per run.
- Key rotation without VM changes; per-sandbox revocation by deleting the bearer.

### 5.4 If a MITM-injection path is wanted instead (no base URL)
Set `HTTPS_PROXY` and `NODE_EXTRA_CA_CERTS` (Claude Code honours both) and terminate TLS for `api.anthropic.com` only. Strictly more machinery than the base-URL gateway for the same result; only useful for tools that cannot take a base URL [inference].

## 6. Claude Code subscription credential case

Facts:
- Credential forms: interactive `/login` credentials in `~/.claude/.credentials.json` (mode 0600; Keychain on macOS) or `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`, a one-year token that "can only make model requests" and cannot establish Remote Control or fetch claude.ai connectors ([authentication](https://code.claude.com/docs/en/authentication)). Cloud sessions always use subscription credentials and ignore `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` there (same page; applies to Anthropic's cloud, not to atrium's VM).
- Hosts needed for subscription login/refresh: `api.anthropic.com`, `claude.ai`, `platform.claude.com` (OAuth exchange/refresh/revocation) ([network access requirements](https://code.claude.com/docs/en/network-config#network-access-requirements)). The environment allowlist must include them for subscription mode; they are not needed for API-key mode (only `api.anthropic.com` via the gateway).
- Policy ([legal and compliance, "Authentication and credential use"](https://code.claude.com/docs/en/legal-and-compliance)): OAuth is for subscription purchasers' ordinary use of Claude Code and other native Anthropic apps; developers building products "should use API key authentication"; Anthropic "does not permit third-party developers to ... route requests through Free, Pro, or Max plan credentials on behalf of their users" and "developers may not collect, store, or intermediate Claude.ai credentials or session tokens"; explicitly not restricted: "an end user signing in to the unmodified Claude Code binary with their own Claude subscription, including where a platform hosts Claude Code".

Options:
| Option | Credential in VM? | Policy fit | Notes |
|---|---|---|---|
| 1. Owner runs `claude` login in the VM (device/URL flow, performed from the thread UI), credential lives in the sandbox home | Yes (refresh token + access token on VM disk) | Matches the explicit allowance (unmodified binary, own subscription) | Persist per sandbox; thread deletion deletes it; repeated logins per sandbox are annoying; refresh needs `platform.claude.com`. Compromised VM = subscription token theft; egress allowlist limits where it can go. |
| 2. Owner pastes a `setup-token` token as an atrium runtime secret → `CLAUDE_CODE_OAUTH_TOKEN` env in the VM | Yes (env var), inference-only scope | Gray: atrium "stores" the token (single-owner self-hosted [inference: arguably the owner's own use]) | One paste per instance instead of per sandbox; one-year lifetime; revocable at claude.ai; narrow scope (model requests only) limits blast radius. |
| 3. Proxy substitutes the real subscription token at the egress proxy (placeholder in VM; TLS termination on `api.anthropic.com` or base-URL gateway forwarding the OAuth bearer and `anthropic-beta` OAuth flag) | No | **Most at risk**: atrium would be intermediating Claude.ai credentials. The gateway doc notes the gateway must forward the OAuth capability in `anthropic-beta` ([subscriptions and gateways](https://code.claude.com/docs/en/llm-gateway)), so it is technically feasible [inference] | Do not build without written clarification from Anthropic (their page points to contact sales for edge cases). |
| 4. API-key mode (section 5): placeholder + atrium gateway | No | Clean (customer's own API key, billed to key owner) | Costs per token instead of subscription limits. Default. |

Recommendation: default to 4 (also gives budget enforcement and metering, which the subscription path bypasses entirely: subscription usage is not visible to atrium's `usage` table or budget gate). Offer 1 as the opt-in "use my Claude subscription" path with a clear UI warning that the credential lives in the VM and that atrium cannot meter it; offer 2 only if the owner accepts the policy reading. Do not ship 3 without Anthropic's confirmation. For any mode, mark the secret setup-only vs runtime as in the plan (this is a *runtime* secret by nature).

## 7. Risks
- **Manager-side git on attacker-controlled repos** executes repo-configured commands (hooks, fsmonitor, sshCommand). Use scratch bare repo or throwaway VM.
- **Proxy confused-deputy / SSRF**: the proxy runs on the manager and can reach its LAN, the metadata service and the manager API; must refuse private/link-local results after resolution (Smokescreen/sandbox-runtime both do this).
- **DNS rebinding / TOCTOU**: resolve once, dial that IP.
- **Fronting and ECH** (2.2); **allowed-domain exfiltration** (2.4).
- **CA private key** compromise (only if option C/MITM adopted): isolate, name-constrain, rotate per manager boot or per sandbox.
- **Streaming metering gaps** on abort; **cache-write TTL tier** fields must be mapped to the existing 5m/1h columns.
- **GitHub token scope**: installation tokens cannot restrict branches; rely on gateway checks plus rulesets.
- **Agent side effects via allowed registries** (`pip install` runs arbitrary code; npm postinstall): allowlist = trust of those registries.
- Subscription ToS (section 6).
- Per-sandbox identity: the VM-asserted identity (proxy username / bearer) is only as strong as its secrecy; `sandbox-runtime` documents that a client-presented id is the only binding ([README](https://github.com/anthropic-experimental/sandbox-runtime)). Bind identity to the tap device / vsock CID instead of a bearer where possible [inference].

## 8. Candidate new tickets

1. **Egress proxy design and spec** (decision, Phase B, sandbox): CONNECT proxy semantics, SNI==CONNECT check, ECH rejection, resolver rules, nftables ruleset, tap-vs-vsock topology; allowlist matching grammar (`*.`, `:port`, deny list), live-update API, audit log and "blocked domain" UI flow.
2. **Environment egress presets** (decision, Phase B, sandbox/UI): "none / trusted / custom" like Anthropic, with per-toolset preset lists (python → pypi.org, files.pythonhosted.org; go → proxy.golang.org, sum.golang.org; rust → crates.io, index.crates.io, static.crates.io; bun/npm → registry.npmjs.org) and GitHub hosts; include which hosts the Claude Code and pi installs need.
3. **Git gateway on the manager** (build, Phase B, sandbox/github): smart-HTTP reverse proxy with pkt-line command parsing, branch/tag/delete policy, per-sandbox scoped credential, attached-repo scoping, token minting, no persistent token storage.
4. **Manager-side git push from untrusted workspaces** (decision, Phase B, sandbox/github): bundle-over-vsock vs shared disk, scratch bare repo, hardened git invocation, or throwaway VM.
5. **Anthropic gateway in atrium for sandbox agents** (build, Phase B, assistant/sandbox): `/gw/v1/messages` with key injection, SSE tee, usage recording into the existing `usage` table, budget enforcement (80%/100%), per-sandbox bearer, abort handling.
6. **Subscription credential policy for Claude Code in sandbox** (decision, Phase B, sandbox/assistant, needs owner + possibly Anthropic): which of options 1-4, UI flow for login inside the VM, warning copy, unmetered-usage handling.
7. **MITM CA lifecycle and toolchain trust matrix** (research/decision, Phase B, sandbox, only if option C or any termination is adopted): CA generation, name constraints, rotation, rootfs integration, per-toolset CA env test (Java, Chromium/Playwright, rustls apps untested here).
8. **Egress exfiltration policy** (decision, Phase B, sandbox): method/path limits on allowed hosts (Codex GET-only analogue), whether GitHub write hosts stay on the allowlist when GitHub gateway exists, default stance on `github.com` reads.
9. **Sandbox network identity binding** (decision, Phase B, sandbox): bind per-sandbox policy to tap/netns/vsock CID instead of client-presented bearer.
10. **Proxy observability** (build, Phase B, sandbox): per-thread egress log (allowed/denied, bytes), metrics, surface in the thread UI; ties to the fog item on observability.
11. **Atrium own outbound proxy support** (decision, Phase B, core): should atrium itself (VPS) egress via an allowlist for integrations, or only the sandbox? (Not asked in the plan; currently unrestricted.)

## 9. Sources not independently verified
The "Codex secrets removed after setup" detail comes from a search summary of an OpenAI support reply, not a first-party doc page; the first-party page confirms agent-phase internet defaults, allowlist presets and method restriction. Items marked `[inference]` above need confirmation in the implementation tickets.
