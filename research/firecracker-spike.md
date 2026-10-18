# Firecracker host spike: KVM, snapshot size, resume latency (ticket #84)

Measured 2026-10-17/18 on the owner's NixOS machine `artemis` (throwaway dir under `/tmp`, deleted afterwards; no sudo, no system or service changes, atrium units untouched). Follows [microvm-runtime (#24)](https://github.com/jeiang/atrium/blob/research/microvm-runtime/research/microvm-runtime.md), which left snapshot size and latency as "unmeasured".

## 1. Answer in brief

- **artemis has usable KVM without sudo.** `/dev/kvm` is `crw-rw-rw- root:kvm`, so the login user (`aidanp`, not in group `kvm`) opens it read/write. Physical AMD Ryzen 7 7800X3D (8 cores/16 threads reported as 8 here; `svm` present), 93 GiB RAM, kernel 7.2.4-cachyos, cgroup v2 (controllers cpu io memory pids...), swap on (46 GiB), `/tmp` on btrfs (`compress=zstd:3`, so reflink-capable). This is bare metal, so **nested virtualisation was not tested** (see risks).
- **Firecracker v1.16.1 from nixpkgs runs unprivileged** (`nix build nixpkgs#firecracker`; no jailer used) with the upstream CI guest kernel `vmlinux-6.1.155` (v1.15 CI artifacts) and a ~4 MiB initramfs (static busybox + static socat as vsock agent).
- **Cold boot to first vsock reply: ~55 ms median** (min 51, max 60) for 512 MiB and 2048 MiB, 2 vCPU. Add ~1.5 ms for process spawn + API configuration. This is a **floor**: no rootfs mount, no real agent, no console.
- **Warm resume: ~8-15 ms to first agent reply** with the memory file in the page cache (API `snapshot/load` call itself 4-10 ms); **~40-55 ms with the memory file evicted from the page cache** (NVMe; load call 15-28 ms). Resume latency is flat in guest RAM (512 MiB to 4 GiB) because the `File` backend maps the file lazily.
- **Warm resume barely beats cold boot for this minimal guest** (about 10 ms vs 55 ms; evicted warm is about equal to cold). The value of warm is preserved process and page-cache state, not speed. A real guest (erofs root, Go agent, toolchains, `setup scripts`) will have a slower cold path [inference].
- **Snapshot size is the guest RAM size on disk, always.** Firecracker writes a dense file (`st_blocks` == apparent size, even for 97% zero pages). Free page reporting (FPR) changes the *content*, not the size: it zeroes freed pages, so the file becomes extremely compressible/sparse-able. Post-process (`fallocate --dig-holes`, or fs compression) is what saves disk.
- **Clock: the guest wall clock does not advance across snapshot/resume.** After a 3 s pause the guest was 3.7-7 s behind (integer-second `date`); stepping with `date -s` from the manager fixed it (residual is the 1 s granularity). The agent/manager must step the clock on every resume.
- **vsock exec round trip: 0.5 ms median for a fresh connection** (`CONNECT 5000`, run command, read reply; p95 0.58 ms, 300 samples), **0.1 ms on a persistent connection** (1000 samples). Post-resume round trip is the same (0.6-0.7 ms).

## 2. Method (what was actually run)

- Host: `ssh artemis.jeiang.vpn`; Firecracker 1.16.1 (nixpkgs; 1.17.0 not yet in nixpkgs), guest kernel [`vmlinux-6.1.155`](https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155) with `PAGE_REPORTING`, `VIRTIO_BALLOON`, `VMGENID`, `VIRTIO_VSOCKETS`, `VIRTIO_MMIO`, `BLK_DEV_INITRD` set (checked in its published `.config`).
- Guest: initramfs with static busybox 1.37 + static socat 1.8.1.3 (`VSOCK-LISTEN:5000,fork EXEC:/bin/sh`) as PID 1's only child; `/dev/shm` tmpfs for dirtying memory. Boot args `reboot=k panic=1 pci=off nomodules 8250.nr_uarts=0 i8042.noaux i8042.nokbd quiet` (no serial console).
- A throwaway Python 3.14 driver (not committed) spoke Firecracker's HTTP API over the Unix socket and the vsock UDS (`CONNECT <port>` handshake). 2 vCPU. Balloon variant: `PUT /balloon {amount_mib:0, deflate_on_oom:true, free_page_reporting:true}` before boot.
- Snapshot: `PATCH /vm Paused`, `PUT /snapshot/create` (Full, default `fsync`); resume: kill VMM, wait 3 s, new VMM, `PUT /snapshot/load` with `mem_backend: File`, `resume_vm: true`. "Evicted" = `posix_fadvise(DONTNEED)` on state and memory files before load (an approximation of cold page cache; no root, so no `drop_caches`).
- Workloads before snapshot: **idle** (fresh boot); **freed** (write 40% of RAM of urandom into tmpfs, delete it, wait 6 s); **live** (keep 25% of RAM in tmpfs). 3 repetitions (2 at 4 GiB); sizes from rep 0.

## 3. Results

### Cold boot (10 runs each, ms; spawn..first reply)

| guest RAM | FPR | boot to agent min/med/max | total incl. spawn+config med |
|---|---|---|---|
| 512 MiB | off | 51.6 / 54.6 / 59.5 | 62.7 |
| 512 MiB | on | 51.3 / 53.8 / 56.7 | 60.9 |
| 2048 MiB | off | 55.2 / 57.8 / 59.4 | 64.8 |
| 2048 MiB | on | 55.7 / 58.3 / 60.2 | 65.7 |

### Snapshot size and create time (full snapshot)

File apparent size = guest RAM in every row and `st_blocks` equals it (dense). "Non-zero" = pages that are not all-zero (what `--dig-holes` would keep); zstd -3 and lz4 -1 are whole-file compressed sizes (to estimate fs compression or an offline compressor).

| RAM | workload | FPR | VMM RSS before snap | non-zero | zstd-3 | lz4-1 | create ms |
|---|---|---|---|---|---|---|---|
| 512 | idle | off/on | 76 MiB | 54 MiB | 12 | 20 | 226-259 |
| 512 | freed | off | 274 | 253 | 217 | 224 | 304-327 |
| 512 | freed | **on** | 78 | **58** | 21 | 29 | 282-289 |
| 512 | live (128 MiB) | off/on | 200 | 177 | 141 | 149 | 295-349 |
| 2048 | idle | off/on | 105 | 83 | 13 | 32 | 485-509 |
| 2048 | freed | off | 922 | 898 | 833 | 846 | 623-636 |
| 2048 | freed | **on** | 168 | **144** | 73 | 90 | 504-543 |
| 2048 | live (512 MiB) | off/on | 618 | 594 | 526 | 543 | 541-607 |
| 4096 | idle | off/on | 214 | 118 | 14 | 48 | 774-795 |
| 4096 | freed | off | 1852 | 1757 | 1652 | 1675 | 988-990 |
| 4096 | freed | **on** | 352 | **258** | 156 | 184 | 833-847 |
| 4096 | live (1 GiB) | off/on | 1232 | 1135 | 1040 | 1069 | 789-894 |

Reading it:
- **FPR works** (guest reports freed 2 MiB+ blocks; the host drops them): after freeing 40% of RAM, RSS fell from 922 to 168 MiB and the non-zero content from 898 to 144 MiB at 2 GiB (about 6x), 1757 to 258 MiB at 4 GiB (about 7x). It does **not** shrink `vm.mem` itself and has no effect on idle or live data. Cost: none measurable on boot or resume.
- Create time scales with RAM (about 0.23 s at 512 MiB, 0.5 s at 2 GiB, 0.8-1.0 s at 4 GiB) because Firecracker writes and `fsync`s every page including zeros; with FPR the freed workload created faster (504 vs 623 ms at 2 GiB, 835 vs 990 ms at 4 GiB).
- Idle guest of any size: a 2 GiB snapshot compresses to ~13 MiB with zstd, i.e. 96-97% zero pages.
- **Measured post-processing:** `fallocate --dig-holes` on the 2 GiB freed+FPR snapshot took 0.54 s, cut allocated size from 2048 to **123 MiB** (1130 extents), and the digged file **resumed fine** (load call 7.8 ms, 26 ms to agent, memory file evicted). btrfs `compress=zstd:3` did *not* shrink the freshly written dense file in `st_blocks` right after the write (so do not count on fs compression at snapshot time) [inference: compression may apply later; not measured].
- Guest memory content check after resume in the live case: the 128/512/1024 MiB tmpfs file was still present and the same size (`ls -l` length, not content hash) [measured only for existence/size].

### Resume latency (ms; first successful vsock echo after `snapshot/load`)

| RAM | page cache | load API call | spawn+load+first reply |
|---|---|---|---|
| 512 | warm | 3.9-10.0 | 7.5-14.1 |
| 512 | evicted | 17-27 | 41-51 |
| 2048 | warm | 4.0-10.3 | 7.9-14.3 |
| 2048 | evicted | 13-28 | 41-51 |
| 4096 | warm | 5.0-11.3 | 8.8-15.1 |
| 4096 | evicted | 9-25 | 34-54 |

Page cache state matters (2-5x), RAM size and FPR do not. Lazy `File` backend: later page faults hit the NVMe, so a first command that touches lots of guest memory is slower than the numbers above [inference; the echo touches few pages].

### Parallel resume (2048 MiB idle, warm cache)

| N concurrent | wall for all | per-VM to-agent min/med/max ms |
|---|---|---|
| 1 | 16 | 15.3 |
| 4 | 13 | 6.6 / 7.9 / 12.4 |
| 8 | 24 | 13.7 / 17.7 / 23.2 |

Resident idle 2 GiB guest: 106 MiB RSS 3 s after boot, so many idle running VMs are cheap on RAM; the cost of "stay running" is far below "snapshot size" for idle guests.

### vsock and clock

- Fresh vsock exec RTT 0.4-0.7 ms (300 samples), persistent 0.1 ms (1000), both before and after resume (0.6-0.7 ms median per resumed VM, 50 samples).
- Guest wall clock after resume with 3 s of host time passed: behind by 3.7-7.1 s (varies with the extra ~1 s the loop took plus whole-second rounding); after `date -s @<host epoch>` residual within +/- 0.5 s (rounding of busybox `date +%s`; true accuracy not measured). `snapshot/load` has a `clock_realtime` option in 1.17 docs [not tested].
- Connections from before the snapshot do not survive; the guest *listener* did, and every post-resume `CONNECT 5000` succeeded first try (as the microvm-runtime sheet predicted from [vsock.md](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/vsock.md)).

## 4. Policy input for the manager (suspend / TTL)

1. **Warm is cheap to resume, costly to store.** Resume is ~10 ms (cache warm) to ~50 ms (cold cache) regardless of RAM. Storage cost = profile RAM unless post-processed.
2. **Recommendation:** after `snapshot/create`, run `fallocate --dig-holes` (0.5 s per 2 GiB, measured) on the memory file. With FPR enabled in the guest, a 2 GiB sandbox that built something and freed memory costs ~120-150 MiB; a heavily loaded one costs its live set. Budget per warm sandbox = live memory set + ~100 MiB, not the profile RAM. Keep the optional "mem file compression" (zstd) out of v1: restore needs a decompress-to-disk step and lazy mapping is lost.
3. **Enable `free_page_reporting` in every VM** (zero measurable boot or resume cost here) and make sure the guest kernel has `PAGE_REPORTING` (upstream CI kernel does).
4. **Warm TTL is a disk budget decision, not a latency one**, for minimal guests: decaying warm to cold only costs ~50 ms plus the loss of process state and `/tmp`. For real guests the cold path (kernel + erofs mount + real agent + workspace mount) must be re-measured with the real rootfs before setting TTL defaults.
5. **Snapshot creation takes 0.25-1 s and pauses the VM**; suspend must not block user-visible actions: do it in the background after the idle timeout; resume must wait.
6. **Always step the guest clock** on resume (agent op `time.set`) before running anything that uses TLS or tokens.
7. **Concurrency:** 8 simultaneous resumes finished in 24 ms wall: no need for a resume queue at this scale; admission control by RAM/`max_running` instead.

## 5. Host prerequisites for the sandbox manager (what artemis already satisfies)

| Need | artemis | Notes |
|---|---|---|
| `/dev/kvm` openable by the service user | yes, mode 0666 (`root:kvm`) | Measured: artemis's generated `/etc/udev/rules.d/50-udev-default.rules` line 116 is `KERNEL=="kvm", GROUP="kvm", MODE="0666"` (systemd's default rules, also covers `vhost-vsock`); a hardened host should use `SupplementaryGroups=kvm` instead of relying on 0666. |
| CPU virt extensions | yes (AMD `svm`) | Intel needs `vmx`. `kvm_amd nested=1` here; nested only matters if the manager itself runs in a VM. |
| cgroup v2 | yes | needed for jailer limits |
| reflink fs for state dir | btrfs on `/` | XFS with reflink also fine |
| `/dev/userfaultfd` | exists, `root:root 0600` | UFFD memory backend needs access (a udev rule/group, or `vm.unprivileged_userfaultfd`); **not used** in this spike (File backend only) |
| Swap | **on (46 GiB)** | microvm-runtime sheet recommends swap off (guest memory can reach disk); artemis is a general-purpose machine, so the manager should run in a cgroup with `memory.swap.max=0` or the host should be dedicated [inference] |
| Jailer capabilities | not tested | no root here; jailer needs root/CAP_SYS_ADMIN etc. (see sheet #24) |

Not satisfied or unknown, so state it in docs: host kernel 7.2.4 is outside Firecracker's tested host kernel set (5.10/6.1/6.18, [kernel-policy](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/kernel-policy.md)) yet worked; nixpkgs packages 1.16.1, not 1.17.0.

## 6. Options and trade-offs

- **Warm-everything vs warm+decay vs cold-only.** With resume ~10-50 ms and cold ~55 ms (floor), cold-only is nearly as fast as warm for this guest and removes snapshot storage, version-strand and secrets-in-memory-file risks (#24 §11). Warm keeps running processes (dev servers, background tasks, shell state), which is the product feature. Recommendation stays warm + decay, with disk budget as the real knob.
- **Dig holes vs compress vs diff snapshots.** Dig-holes: measured, simple, preserves lazy mapping. Compress: stronger on live data (small gain: 1040 vs 1135 MiB at 4 GiB live) but loses lazy mapping. Diff snapshots: developer preview, not measured.
- **File vs UFFD backend.** File: measured, trivial, no privileges. UFFD: needed only for sharing/streaming memory between clones; skip in v1.

## 7. Risks

- **Nested virtualisation is untested.** artemis is physical. If the "beefier machine" is a VM, the nested-virt latency penalty (boot, resume page faults) is unknown; Firecracker only tests on metal ([README](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/README.md)). A second spike on the actual target host is cheap with this method.
- **Minimal-guest numbers are floors.** The real rootfs (erofs + overlay, Go agent, workspace ext4) will add boot time and page-cache/page-fault work after resume.
- **Stored snapshots contain guest memory**, including any runtime secrets (as in #24 §11); digging holes makes them smaller but not safer.
- **Wall-clock jump after resume** can trip TLS verification and token expiry for tools that run before the agent steps the clock.
- Page-cache-evicted figures use `posix_fadvise` on a fast NVMe; a slower disk (VPS, network volume) changes resume latency a lot.
- Results are from 1.16.1; 1.17.0 added `sync_snapshot_files` and virtio-blk discard, which may change create time.

## 8. Candidate new tickets

1. **Spike: boot and resume with the real toolset rootfs** (type: spike, phase: B/sandbox, area: sandbox). Question: with a Nix-built erofs root, overlay + workspace ext4, and the Go guest agent, what are cold boot and warm resume latency and the post-resume first-command latency for python/go/bun/rust sets? Sets the warm TTL default.
2. **Spike: nested-virt and non-bare-metal host** (type: spike, phase: B, area: sandbox). Question: on the actual "beefier machine" (provider, CPU, nested KVM), do these latency and size numbers hold, and does Firecracker work there at all?
3. **Decide: memory-file post-processing and warm-store budget policy** (type: decision, phase: B, area: sandbox). Question: dig-holes after every suspend, budget accounting by allocated size, eviction order, and whether to encrypt/zero memory files (ties to #40 and secrets-in-snapshot risk).
4. **Decide: clock and entropy resync protocol on resume** (type: decision, phase: B, area: sandbox). Question: agent op for `time.set`, `clock_realtime` load option, VMGenID behaviour, and which tools get notified.
5. **Spike: UFFD memory backend and `/dev/userfaultfd` access without root** (type: spike, phase: later, area: sandbox). Question: needed only for sharing memory between clones or streaming from remote storage; what host permission setup does it require.
6. **Decide: swap policy for the manager host** (type: decision, phase: B, area: sandbox/nix). Question: swap exists on artemis; require a dedicated host, or run the manager in a cgroup with `memory.swap.max=0`?
