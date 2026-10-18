# Sandbox link reachability and PKI (ticket #54)

Researched 2026-10-17 from primary docs and source (links inline). `[inference]` marks my reading, not a source statement. Feeds the sandbox manager design/API ticket (#41) and the GitHub token-injection design. Related: [cloud-sandboxes survey (#28)](https://github.com/jeiang/atrium/blob/research/cloud-sandboxes/research/cloud-sandboxes.md).

## 1. Answer in brief

1. **Reachability.** Atrium (VPS, public IP) must reach a manager at home behind NAT. Four shapes work: public HTTPS (port-forward), overlay network (WireGuard/Tailscale/NetBird), outbound-only reverse tunnel, or a public relay. Recommend: **the manager is a plain HTTPS+mTLS server on a configurable listen address, and the overlay is a deployment choice, not a dependency.** mTLS is mandatory even inside an overlay, because the manager is a remote-code-execution API.
2. **Overlay options.** The owner's fleet already runs self-hosted NetBird; it fits (ACL policy can restrict atrium to the manager port). For other deployers, plain WireGuard with the VPS as the public endpoint is enough, since the home side can initiate and keep the NAT mapping alive.
3. **PKI.** There is exactly one client (atrium) and one server (manager), so a CA is optional. Recommend **mutual public-key pinning with self-generated keys** (SPKI SHA-256 pins, SSH/Syncthing-style enrollment), with an alternative "private CA" mode for deployers who have one. Public CAs are out: Let's Encrypt no longer issues `clientAuth` certificates.
4. **Keys and sops.** Private keys are generated on the host that uses them, into `StateDirectory`, never in the Nix store and never in sops. Only public pins are config. Deployers who prefer declarative keys supply them via `*_file` + sops-nix + systemd `LoadCredential=`. Rotation uses overlapping pins and per-handshake cert reload, no restart.
5. **One atrium only.** Enforced by data model, not only by cert: the manager stores one trusted-peer record (pin + instance id); enrollment is single-shot; every resource is tagged with the instance id.
6. **At rest.** The dangerous file is the **VM memory snapshot** (full guest RAM) plus rootfs overlays. Firecracker explicitly leaves snapshot security to the integrator and mmaps the memory file for the VM's lifetime, so application-level encryption does not fit. Use **block-level encryption of the manager data volume** (LUKS/ZFS) and keep **secret values out of manager persistence** where possible (atrium re-pushes them on resume).
7. **Backup.** Sandboxes are caches of work whose durable output is pushed git branches and atrium's DB. Back up manager config + identity only; do not back up VM state by default.
8. **Reconcile.** Atrium owns desired state, manager owns what physically exists. Client-chosen IDs (idempotent create), a persisted manager journal, a startup sweep, an atrium-side reconcile loop with grace periods, and a lease that suspends (never deletes) VMs when atrium goes silent.

## 2. What exists today (atrium facts)

- The only isolation boundary today is a local, socket-activated systemd worker: `atrium-codemode@` per connection, `ListenStream = /run/atrium-codemode/codemode.sock`, mode 0600, user/group `atrium` (`nix/module.nix:319-343`); env `ATRIUM_CODEMODE_SOCKET` (`nix/module.nix:87`, `src/atrium/agent/codemode.py:107`). There is no network link, no TLS, no peer auth today; the Unix socket's file mode is the auth. Everything in this ticket is new surface.
- AGENTS.md states the sandbox is the security boundary and Monty is not (`AGENTS.md:97-101`).
- Host wiring: secrets arrive as one sops file exposed as an env file (`sops.secrets."atrium/env"`, mode 0400, `restartUnits` listing the atrium units; `cornn-flaek/modules/atrium/default.nix:115,154-158`). The same repo uses sops-nix with `useSystemdActivation = true` and host SSH key as age identity (`cornn-flaek/modules/sops/default.nix`), and no `defaultSopsFile`, so each secret names its shard.
- NetBird in the fleet: self-hosted management at `netbird.jeiang.dev`, clients on every host with `setupKeyFile` from sops (`cornn-flaek/modules/netbird.nix:7-37`), `networking.firewall.trustedInterfaces` includes the netbird interface (`netbird.nix:42-44`), peer IPs pinned as literals (`modules/netbird-peers.nix`), management on host `vida` with relay/STUN (`modules/netbird-server/default.nix`).
  - Caveat for a NixOS module: `trustedInterfaces` means any mesh peer reaches any local port. If the manager listens on the mesh interface, the NetBird policy (not the host firewall) is what limits who can connect; and mTLS must still reject everyone but atrium.

## 3. Reachability options

| Option | How | Pros | Cons |
|---|---|---|---|
| A. Public HTTPS | Manager listens on home WAN IP (port forward + DDNS) | No extra software; matches plan's "HTTPS+mTLS" literally | Needs inbound port on a home router; impossible behind CGNAT `[inference]`; exposes an RCE-API TLS stack to the internet (mTLS bounds it, but handshakes from scanners reach Go's TLS stack); dynamic DNS |
| B. Overlay + mTLS | WireGuard / Tailscale / NetBird between VPS and home, manager listens on overlay IP | No public port; NAT traversal handled; ACLs add a second gate; owner already runs NetBird | One more moving part; overlay outage = sandbox outage; overlay is a trust anchor if mTLS is skipped |
| C. Reverse tunnel | Manager dials out to atrium (or a relay) and atrium sends API calls over that connection | Works behind any NAT/CGNAT; no inbound port at home | Needs a stream multiplexer or HTTP/2/WebSocket tunnel layer and role inversion for TLS; atrium must expose a listener for managers; non-standard API transport |
| D. Third-party relay | Cloudflare Tunnel-style outbound connector | Outbound-only; no home port | Third-party in the data path; Cloudflare notes the tunnel's connector credentials, not origin mTLS, authenticate the origin side ([docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)); client-cert mTLS must be terminated at the edge or passed through `[inference]` |

### Facts about the overlays

- **WireGuard** needs NAT keepalives: a peer behind NAT that wants to receive packets must send periodic keepalives to keep the mapping alive ([WireGuard quickstart, "NAT and Firewall Traversal Persistence"](https://www.wireguard.com/quickstart/#nat-and-firewall-traversal-persistence)). Because the VPS has a public IP, the home manager can be the initiator; atrium then reaches it over the tunnel IP. No coordination server needed. `[inference]` this is the simplest host-neutral recipe.
- **Tailscale** starts every connection through DERP relays and then tries to upgrade to direct UDP; if NAT traversal fails (hard NAT on both sides, blocked UDP) it falls back to peer relay or DERP, all WireGuard end-to-end encrypted ([connection types](https://tailscale.com/docs/reference/connection-types)). Here only one side (home) is NATed and the VPS is public, so a direct path is the likely outcome `[inference]`.
- **NetBird**: clients, management, signal and relay; peers form direct WireGuard tunnels, relay is "Plan B" ([how NetBird works](https://docs.netbird.io/about-netbird/how-netbird-works)). Access policies are group-based allow-only rules with protocol/port restrictions and a direction flag (unidirectional means only the source initiates); the `Default` allow-all policy should be removed for least privilege ([access control docs](https://docs.netbird.io/manage/access-control/manage-network-access)). Recipe: group `atrium-instance` (VPS) to group `sandbox-manager` (home), TCP, manager port only, unidirectional.
- **Cloudflare Tunnel** is an outbound-only connector (`cloudflared`) ([docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)); listed to show option D exists, not recommended as default.

### Recommendation (reachability)

- Manager config: `listen = "<addr>:<port>"`, `advertise` hostname optional. No overlay logic in either binary. The overlay is configured by the deployer; docs/runbook shows recipes for (1) plain WireGuard, (2) NetBird (owner's), (3) Tailscale, (4) public port-forward.
- Default in the NixOS module: bind to an explicit address supplied by the deployer, never `0.0.0.0` by default `[inference: avoids accidental public exposure]`.
- Egress proxy for sandboxes is a **separate listener/network namespace** on the manager and is independent of this link.
- Keep option C as a documented future mode (candidate ticket below); it does not change the API, only the transport.

## 4. PKI: who issues, what to verify

### Constraints from Go and public CAs

- Go's TLS server verifies client certs with `ExtKeyUsageClientAuth`; clients verify servers with `ExtKeyUsageServerAuth` ([handshake_server.go:522](https://github.com/golang/go/blob/master/src/crypto/tls/handshake_server.go#L522), [handshake_client.go:423](https://github.com/golang/go/blob/master/src/crypto/tls/handshake_client.go#L423)). Certs must carry the right EKUs.
- Let's Encrypt stopped issuing `clientAuth` certs: default profile dropped it 2026-02-11, the `tlsclient` profile was retired 2026-07-08; it says client auth belongs on a private CA ([LE announcement](https://letsencrypt.org/2025/05/14/ending-tls-client-authentication)). **Atrium's client cert cannot come from a public ACME CA.** A public cert for the *server* side is still possible but pointless when the client pins it.
- `tls.Config` has no revocation field. `VerifyPeerCertificate` and `VerifyConnection` are the hooks; `VerifyPeerCertificate` is **not called on resumed connections** (including across `GetConfigForClient` configs), `VerifyConnection` runs on all connections ([common.go:665-692](https://github.com/golang/go/blob/master/src/crypto/tls/common.go#L665-L692)). Pin checks must use `VerifyConnection`, or disable session tickets.
- `GetClientCertificate` (client) and `GetConfigForClient`/`GetCertificate` (server) allow loading a renewed certificate per handshake without a restart ([common.go:629-658](https://github.com/golang/go/blob/master/src/crypto/tls/common.go#L629-L658)). Latest Go is 1.27.2 ([go.dev/VERSION](https://go.dev/VERSION?m=text)).

### Options

| Option | Who issues | Trust check | Pros | Cons |
|---|---|---|---|---|
| P1. Mutual pinning (recommended default) | Each side self-signs its own cert from its own key | Peer's SPKI SHA-256 equals stored pin | No CA to run, protect, or rotate; "exactly one peer" is literal; no revocation problem (remove the pin); private keys never leave their host | Needs an enrollment exchange; rotation needs overlap |
| P2. Private CA, small | `atrium pki init` / `sandbox-manager pki init` creates a CA (Go `crypto/x509`), issues server+client certs; CA key kept offline or on atrium host | Chain to CA + subject/SAN allowlist | Familiar; scales to future multi-manager; standard tooling | CA key custody problem for one pair; CRLs are not supported by Go's TLS config, so revocation = short lifetime `[inference]` |
| P3. step-ca | Smallstep CA daemon | Chain to CA; 24 h default lifetime with automated renewal | Mature; short-lived certs, systemd renewal timers, optional CRL, HSM/KMS options ([production guide](https://smallstep.com/docs/step-ca/certificate-authority-server-production/), [renewal](https://smallstep.com/docs/step-ca/renewal/)) | Another daemon and root-key policy for a 2-node system; heavy for default |
| P4. Public CA for server + private for client | LE for manager hostname | Server: WebPKI; client: private CA | Browser-friendly server cert | Two PKIs; manager needs a public DNS name and ACME reachability; adds nothing over pinning here |

### Recommendation (PKI)

- **Default P1.** Keys: ECDSA P-256 or Ed25519 `[inference: both supported by Go TLS 1.3; choose P-256 for broadest tooling]`. Certificates are self-signed with both `serverAuth` and `clientAuth` EKUs, validity long (years) because identity is the key, not the expiry. Authorize by `sha256(SubjectPublicKeyInfo)`.
- **Enrollment (SSH/Syncthing-like, [inference: pattern, not a cited spec]):**
  1. Manager first start generates its key + cert and prints its pin (also in `sandbox-manager status`).
  2. Admin opens atrium's "Connect sandbox manager" in the web UI (admin only), enters URL + manager pin. Atrium generates its own key (in DB or state dir), connects with TLS pinned to that pin.
  3. Atrium shows its pin; the manager admin runs `sandbox-manager trust <atrium-pin>` on the host (or the manager prints a one-time enrollment token on the host console that atrium presents). Until trust is set, the manager serves only the enrollment endpoint.
  4. Manager stores one peer record and refuses further enrollment until `sandbox-manager trust --reset` on the host.
- **Alternative P2 mode** via config: `client_ca_file`/`server_ca_file` + expected subject; same code path with a different `VerifyConnection`.
- Always verify in `VerifyConnection`, set `MinVersion: tls.VersionTLS13`, `ClientAuth: tls.RequireAndVerifyClientCert` (with a custom verifier for P1) `[inference]`.

## 5. Provisioning and rotation with NixOS and sops-nix

- sops-nix decrypts at activation to per-secret files with declared owner/mode and supports `restartUnits` (and `reloadUnits`) per secret ([sops-nix README](https://github.com/Mic92/sops-nix), README example around `restartUnits`/`reloadUnits`) and `sops.templates` for config files containing secrets.
- systemd `LoadCredential=`/`LoadCredentialEncrypted=` hand secrets to a service as read-only files under `$CREDENTIALS_DIRECTORY`, accessible only to the unit's user, and systemd documents that environment variables are not suitable for secrets ([systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#LoadCredential=)). `systemd-creds` can bind credentials to the TPM2 ([systemd-creds](https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html)).
- The existing host pattern passes a whole sops file as `EnvironmentFile` (`cornn-flaek/modules/atrium/default.nix:115`). The v2 plan already moves to TOML + `*_file`; for PKI that means `tls_key_file`, never an env value.

### Recommended module behaviour

| Item | Where it lives | Declarative? |
|---|---|---|
| Manager TLS key + cert | `StateDirectory=sandbox-manager`, generated on first start by `sandbox-manager init` (idempotent) | Imperative, host-local, persisted (cornn-flaek: add to `persistence.directories` as with `/var/lib/netbird`, `netbird.nix:46`) |
| Atrium client key + cert | Atrium state dir (or DB, encrypted with the DB key from Phase B) | Same |
| Peer pins | Module options `services.atrium-sandbox-manager.trustedPeerPin` and atrium `sandbox.manager.pin`; non-secret, safe in Nix store | Yes |
| Optional bring-your-own key/cert/CA | `tls_key_file` etc. in TOML, files from sops, delivered via `LoadCredential=` | Yes, deployer's choice |
| Rotation | `sandbox-manager rotate` / atrium "Rotate link key" button: new key generated, new pin *added* to the peer's accepted set, switch over, old pin removed after ack. Cert reload per handshake via `GetCertificate`/`GetClientCertificate`, no restart | Operator action; declarative pins list may hold two pins during overlap |
| CA mode | Short-lived leaf certs from step-ca or internal CA; reload on renewal (`systemctl try-reload-or-restart`, as in step's `cert-renewer@.service`) | Yes |

Why not put the TLS private key in sops by default: it would be in git history encrypted forever, rotation = commit + deploy + restart, and the key is only ever needed by one process. Keeping it host-generated shrinks the secret inventory. `[inference]`

## 6. Authenticating exactly one atrium instance

Layers, all required:

1. **Transport**: mTLS with the pinned atrium key (or CA chain + subject).
2. **Peer record**: manager DB has one row `{instance_id (UUID minted by atrium at enrollment), pin, enrolled_at}`. Enrollment endpoint is disabled once set.
3. **Request binding**: every API call carries `X-Atrium-Instance: <instance_id>`; mismatch is 403. Cheap defense against misconfiguration (two atrium instances pointed at one manager, restored backups).
4. **Resource ownership**: every sandbox, snapshot, environment and policy stores `instance_id`; list/delete only act on that id. If trust is reset and a new instance enrolls, old resources are *not* adopted automatically; operator chooses `adopt` or `purge`. `[inference]`
5. **Least API**: even authenticated, the API exposes only create/start/exec/suspend/delete-style verbs; no raw host filesystem, no arbitrary kernel/rootfs path (candidate #41 detail).
6. **Rate/size limits and audit log** on the manager (candidate ticket).

Multiple instances = multiple managers (plan: one manager per atrium instance), so no tenancy inside the manager.

## 7. What the manager must encrypt at rest

Inventory of sensitive state on the manager `[inference from plan + Firecracker docs]`:

| Data | Sensitivity | Notes |
|---|---|---|
| Manager TLS key | High | File mode 0400, own user |
| Atrium pin / instance id | Low | Integrity matters, not secrecy |
| Environment definitions: env vars, secret values (setup-only / runtime) | High | Plan marks secrets setup-only or runtime |
| Injected tokens (GitHub), egress-proxy secret substitutions | High | Plan: token never enters the VM; manager/proxy injects |
| VM rootfs overlays / workspace disks | Medium-high | Cloned repos, build outputs, any runtime secret written to disk |
| **Memory snapshots** | High | Full guest memory including secrets in process memory |
| Logs / audit | Medium | Redact secrets |

Facts: Firecracker's snapshot consists of the guest memory file, state file and disks; the memory file is `MAP_PRIVATE` mmapped and "kept around for the entire lifetime of the resumed microVM"; Firecracker only CRC-checks the state file and its threat model trusts the host and snapshot files, so users must "implement authentication and encryption schemes" when managing or moving them ([snapshot-support.md](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)). (Which VMM the manager uses is another ticket; the snapshot concern applies to any memory-snapshotting VMM `[inference]`.)

### Options

| Option | Fit |
|---|---|
| E1. Encrypted data volume (LUKS/dm-crypt, ZFS native, fscrypt) under the manager's state dir | Covers disks and mmapped memory files without app code. Unlock at boot is the cost: passphrase over SSH, or TPM2-bound (`systemd-cryptenroll`/`systemd-creds`) `[inference]` |
| E2. App-level envelope encryption (age/AES-GCM) of the manager DB's secret columns | Good for secret values; useless for memory files (mmapped) |
| E3. Do not persist secret values in the manager; atrium re-pushes on each start/resume | Smallest at-rest surface; costs a dependency on atrium availability for resume (atrium initiates resume anyway). Secrets still sit in the running VM/RAM and therefore in suspended memory snapshots |
| E4. Never snapshot memory (suspend = stop VM, keep disk) | Removes the worst file; loses process state and fast resume (cloud-sandboxes survey: most products only keep disk state) |

### Recommendation

- **E1 mandatory in docs, checked by the NixOS module** (assertion or warning if the state dir is not on an encrypted filesystem is not detectable reliably; make it a documented requirement + preflight warning `[inference]`).
- **E3 for secret values** by default; **E2** only for the small set persisted for unattended resume (opt-in).
- Treat memory snapshots as secret-bearing: restrict dir mode 0700, delete on thread delete, TTL.
- **Setup-only secrets must not survive in snapshots/overlays**: run setup in a build phase and snapshot disk after removing them, matching the setup-only/runtime split in the plan; flag as candidate.
- Redact tokens in manager logs; never include env var values in API responses after creation (write-only, like Claude's network secrets which "cannot be edited, only delete+re-add" — [cloud-sandboxes](https://github.com/jeiang/atrium/blob/research/cloud-sandboxes/research/cloud-sandboxes.md)).

## 8. Backup

- Durable outputs: pushed branches (GitHub), atrium DB (threads, transcripts). Sandbox workspaces are reconstructible: environment definition + repo + setup script. Therefore **manager VM state is not backed up by default**.
- Back up: manager config, TLS key + pins (small; losing them just means re-enrollment), and the manager's environment/policy DB *only if* atrium is not the source of truth for them. Recommendation: **atrium is the source of truth** for environments/policies and pushes them (declarative sync); manager DB is then a cache and not backed up.
- Optional: users who want workspace backups can use `restic`, which always encrypts repositories ([restic design](https://github.com/restic/restic/blob/master/doc/design.rst)) `[inference: cited as a general tool; not verified against the doc text]`. Snapshot files taken while a VM runs are crash-consistent only `[inference]`.
- Atrium's own DB backup (encrypted DB, Phase B) is a separate fog item already in the plan.

## 9. Reconciliation after restarts

Failure modes: manager reboots with VMs "running" in its DB; atrium restores an old DB and references sandboxes that exist or not; atrium crashes mid-create; link partitions for hours; VM processes survive a manager restart.

### Protocol

- **Idempotent create**: atrium chooses `sandbox_id` (UUID) and calls `PUT /v1/sandboxes/{id}` with a desired spec; repeated PUTs converge. (HTTP idempotency-key draft exists as a pattern: [draft-ietf-httpapi-idempotency-key-header](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/) `[inference: not a standard yet]`.) Storing the id before the call lets atrium retry after a crash.
- **Manager journal**: persist `{id, instance_id, desired_state, observed_state, thread_id, created_at, last_seen_by_atrium}` before acting. VM processes run under named systemd transient scopes/cgroups `atrium-sb-<id>` so the sweep can find them even if the DB is lost `[inference]`.
- **Manager startup sweep** (before opening the API): enumerate cgroups/scopes, tap devices, mounts, proxy rules; for each: in journal with desired=running and process gone -> mark `stopped(crashed)`; process present but not in journal -> kill + quarantine disk (never delete automatically); journal entry with no disk -> mark `lost`. Re-create network and proxy state from the journal.
- **Atrium reconcile loop** (on start, then every N minutes, and after link recovery): `GET /v1/sandboxes` and diff with threads:
  - thread has sandbox, manager lacks it -> thread marked `workspace lost`; next turn re-provisions from environment + repo; user told which uncommitted state was lost.
  - manager has sandbox, no matching live thread -> **orphan**: suspend immediately, delete after a grace period (e.g., 24 h) and only if `instance_id` matches; log. Grace avoids deleting work when atrium DB was restored from an older backup. `[inference]`
  - state mismatch -> converge to atrium's desired state.
- **Leases**: running sandboxes carry a lease that atrium renews by heartbeat/any call; on expiry the manager **suspends** (never deletes), so a partitioned or dead atrium cannot leave VMs burning resources indefinitely, and nothing is destroyed because of a network blip.
- **Independent manager GC**: suspended sandboxes untouched for a configured TTL are reclaimed (cloud products use 7-30 days, see survey); disk quotas per sandbox and per manager; refuse create when over quota.
- **Events**: polling `GET` at low frequency is enough at v1 scale; add an SSE stream (`/v1/events`) for state changes and exec output `[inference]`.
- **Deletes ordering**: thread delete -> atrium sends `DELETE` and records tombstone; reconcile retries until 404. Deleted sandboxes' snapshots and secrets are shredded where the filesystem allows (on an encrypted volume, deletion + key policy suffices) `[inference]`.

## 10. Risks

- **Pin-based trust has no standard revocation.** Remove the pin, rotate the key. Documented procedure + test needed.
- **Resumed TLS sessions bypass `VerifyPeerCertificate`**; use `VerifyConnection` ([common.go](https://github.com/golang/go/blob/master/src/crypto/tls/common.go#L665-L692)) or disable tickets.
- **Overlay `trustedInterfaces`**: in the owner's fleet, the NetBird interface is a trusted firewall interface (`netbird.nix:42-44`). Do not treat that as authentication.
- **Home machine reachability**: dynamic IP, power loss, ISP changes; overlay hostnames help. In the owner's fleet mesh DNS "does not resolve on the Legion nodes", so raw peer IPs are used (`netbird-peers.nix`): a re-enroll changes the IP. Manager URL must be config/UI-editable.
- **Cert pin stored in atrium DB**: if the DB is restored from older backup, a rotated pin may be stale; the link falls closed (fail safe) but needs a documented recovery.
- **Secrets in snapshots**: suspended memory images include whatever was in RAM; an encrypted volume protects only against disk theft, not a compromised manager host. The manager host is therefore a trusted component `[inference]`.
- **Time**: certificate validity checks need sane clocks on both sides; self-signed long-lived certs reduce this risk.
- **Exposure of enrollment**: the one-shot enrollment endpoint is the only unauthenticated surface; must be disabled after trust is set and rate limited.

## 11. Candidate new tickets (phrased for the map)

1. **Sandbox manager: peer enrollment and trust-reset UX** (type: grill/decision, phase B, area: sandbox). Question: exact enrollment flow (pin exchange vs one-time token), CLI verbs (`trust`, `trust --reset`, `rotate`, `adopt`/`purge`), and what atrium's web UI shows for link health.
2. **Link PKI config schema** (type: decision, phase B, area: config). Question: TOML keys and Nix module options for pin mode vs CA mode (`tls_key_file`, `peer_pin`, `client_ca_file`), rotation overlap lists, and which are UI-editable vs pinned.
3. **Manager data-volume encryption and unlock** (type: research, phase B, area: sandbox/ops). Question: LUKS vs ZFS native vs fscrypt for the manager state dir, TPM2/remote unlock on a home server, and a NixOS module preflight.
4. **Secret lifecycle between atrium and manager** (type: decision, phase B, area: sandbox/security). Question: re-push-on-resume vs persisted-encrypted; how setup-only secrets are excluded from snapshots; behavior for the GitHub token injection (short-lived tokens).
5. **Suspend semantics: memory snapshot vs disk-only** (type: research, phase B, area: sandbox). Question: given memory images contain secrets and mmapped files can't be app-encrypted, is memory snapshotting worth it for v1?
6. **Reconcile/lease protocol spec** (type: decision, phase B, area: sandbox API). Question: lease duration, orphan grace, TTL defaults, quota rules, idempotency and event stream for the manager API.
7. **Reverse-tunnel link mode** (type: research, phase B/fog, area: sandbox). Question: is an outbound-only manager-to-atrium transport (yamux/HTTP2/WebSocket) needed for CGNAT homes, and does it change the API?
8. **Deploy recipes for the link** (type: docs, phase B, area: docs). Question: runbook sections for plain WireGuard, NetBird (policy example), Tailscale, public port-forward; cornn-flaek wiring as the worked example without personal literals.
9. **Manager audit log and API rate limits** (type: decision, phase B, area: sandbox/security). Question: what is logged (no secrets), retention, and abuse limits for an authenticated-but-compromised atrium.
10. **Manager host trust model statement** (type: docs/decision, phase B, area: security). Question: threat model: compromised atrium vs compromised manager vs compromised sandbox; what each can reach, so the PKI and at-rest choices are tied to explicit assumptions.

## 12. Sources

- Go TLS: [common.go](https://github.com/golang/go/blob/master/src/crypto/tls/common.go), [handshake_server.go](https://github.com/golang/go/blob/master/src/crypto/tls/handshake_server.go), [handshake_client.go](https://github.com/golang/go/blob/master/src/crypto/tls/handshake_client.go)
- Let's Encrypt client auth: <https://letsencrypt.org/2025/05/14/ending-tls-client-authentication>
- Overlays: [WireGuard quickstart](https://www.wireguard.com/quickstart/), [Tailscale connection types](https://tailscale.com/docs/reference/connection-types), [NetBird how it works](https://docs.netbird.io/about-netbird/how-netbird-works), [NetBird access control](https://docs.netbird.io/manage/access-control/manage-network-access), [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)
- PKI tooling: [step-ca production](https://smallstep.com/docs/step-ca/certificate-authority-server-production/), [step-ca renewal](https://smallstep.com/docs/step-ca/renewal/)
- Secrets: [sops-nix](https://github.com/Mic92/sops-nix), [systemd.exec credentials](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html), [systemd-creds](https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html)
- Snapshots: [Firecracker snapshot-support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
- Owner fleet (local): `cornn-flaek/modules/netbird.nix`, `netbird-peers.nix`, `netbird-server/default.nix`, `sops/default.nix`, `atrium/default.nix`
