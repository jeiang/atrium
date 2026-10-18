# SQLite driver matrix for the existing atrium.db (ticket #51)

Date: 2026-10-17. Question: which Go SQLite driver opens the current `atrium.db` with FTS5 and sqlite-vec `vec0`
(768-dim cosine, partition keys), supports WAL + `busy_timeout` + several processes, builds static, and leaves a
Phase B encryption path; plus pool/transaction conventions and a benchmark on a realistic DB copy.

Everything marked **measured** was run for this ticket (throwaway Go modules and a generated DB under `/tmp`,
not committed). `[inference]` marks reasoning that was not tested.

## 1. What atrium needs from the driver (facts from the code)

| Need | Where |
|---|---|
| WAL, `busy_timeout=30000` (also Python `timeout=30`), `foreign_keys=ON`, autocommit mode (`isolation_level=None`) | `src/atrium/db.py:198-205` |
| sqlite-vec loaded as an extension on every connection | `db.py:6,206-208` |
| FTS5 virtual tables `message_fts(subject,sender,body)`, `notion_fts(title,path,body)`, `thread_fts(thread_id UNINDEXED,text)` | `db.py:86`, `notion_sync.py:59`, `agent/schema.py:25` |
| vec0 tables: `message_vec(account TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)`, `notion_vec(visibility TEXT PARTITION KEY, ...)`; `EMBED_DIM = 768` | `db.py:87-90`, `notion_sync.py:60-63`, `config.py:11` |
| KNN query shape: `SELECT rowid, distance FROM message_vec WHERE embedding MATCH :vec AND k = :k [AND account = :account]`, then a `json_each(:ids)` join; exact fallback `ORDER BY vec_distance_cosine(v.embedding, :vec)` | `search.py:140-186`, `router.py:51-60`, `notion_search.py:79-86` |
| Vector writes: per batch `BEGIN; (DELETE FROM x_vec WHERE rowid=?; INSERT INTO x_vec(rowid, part, embedding) ...)*; UPDATE ...; COMMIT`, float32 blobs via `sqlite_vec.serialize_float32` | `embed.py:50-76`, `notion_sync.py:300-322` |
| Several processes share one file: web, scheduler, watch, sync/notion/proposals timers, `atrium-run`; systemd units `ExecStart=... serve / watch / agent scheduler / sync / notion sync / proposals scan-all` | `nix/module.nix:162,369,382,398,406,414` |
| Cross-process mutual exclusion with `flock(LOCK_EX)` on `<db>.lock` (and a separate `notion` lock file) | `db.py:215-221`, callers `run.py:83`, `cli.py:155-284`, `web/app.py:279`, `executors.py:277` |
| `BEGIN IMMEDIATE` only in: schema/view refresh, `agent/files.py:67`, `journal.py:84,145,193,231`, `threads.py:122`, `scheduler.py:400`. Plain (deferred) `BEGIN` + reads-then-writes in `sync.py:180,265`, `embed.py:60`, `rules.py:181`, `ruleedit.py:90`, `notion_sync.py:309`, `corrections.py:28`, `live.py:74`, `rebalance.py:317` | grep |
| Idempotent migrations rely on catching "duplicate column" and on `BEGIN IMMEDIATE` rebuilds because three processes run init at startup | `db.py:185-195`, `agent/files.py:65-67` |
| `PRAGMA defer_foreign_keys = ON` inside a transaction | `money.py:251` |
| One connection used from several threads (`check_same_thread=False`) | `web/chat.py:275` |

Reference DB: Python 3.14 + sqlite-vec 0.1.9 (pip) + SQLite 3.53.1, built by running `atrium.db.connect()` itself, so the schema is the
real one (all tables incl. agent/notion/money). Filled with 60 000 messages (+FTS5 bodies), 60 000 vec0 rows
(2 partitions, 768-d, 200 clusters), 4 000 Notion pages (+FTS5, 4 000 vec0 rows, public/private), 3 000 thread messages.
File: 533 MB, `journal_mode=wal`, page size 4096. Written by Python, then opened read/write by every Go candidate.

## 2. Candidates

Versions are current as of 2026-10-17 (Go module proxy / GitHub API).

| Candidate | Version | cgo | vec0 | FTS5 |
|---|---|---|---|---|
| `mattn/go-sqlite3` + `asg017/sqlite-vec-go-bindings/cgo` | mattn v1.14.52 (2026-09-05), bindings v0.1.6 (2024-11-20, vendors C `v0.1.7-alpha.2` at HEAD), SQLite 3.53.4 | yes | yes via `sqlite3_auto_extension` | build tag `sqlite_fts5` |
| `modernc.org/sqlite` + built-in `modernc.org/sqlite/vec` | v1.60.1 (2026-09-29), SQLite 3.53.4, sqlite-vec v0.1.9 | no | yes, built in | built in |
| `ncruces/go-sqlite3` (current) | v0.35.6 (2026-09-23), SQLite 3.54.1 sources, wasm2go-translated Go (no wazero) | no | **no** (see 3.4) | per-conn `ext/fts5` registration |
| `ncruces/go-sqlite3` v0.20.0 + sqlite-vec bindings `ncruces` | wazero, SQLite 3.47.0, vec 0.1.6 | no | yes, but frozen on a 2024 stack | included in that wasm |
| SQLCipher forks of mattn (`BitBoxSwiss/go-sqlite3-sqlcipher` HEAD `d03fd26`, 2026-07-03) | SQLCipher 4.16.0 | yes | via same cgo bindings | tag `sqlite_fts5` |
| `gosqlite.org` (`go-again/sqlite`) v0.14.0 | modernc-based fork, repo created 2026-05-26, 2 contributors, 13 stars | no | yes (typed `vec`) | yes |

Not evaluated further: `tursodatabase/go-libsql` (pseudo-version only, last commit 2026-04-24), `zombiezen/go-sqlite` v1.4.2
(modernc-based, no vec). `[inference]` neither adds anything the above lacks.

## 3. Findings

### 3.1 Opens the existing file and returns identical answers (measured)

mattn+vec, modernc+vec and ncruces v0.20+vec all opened the Python-written 533 MB DB, ran `PRAGMA quick_check` = ok, and
reproduced the Python KNN top-10 (`account='gmail'`, k=10) with identical rowids and **max |distance diff| = 0.0**.
sqlite-vec 0.1.6 (mattn bindings, ncruces v0.20) and 0.1.9 (modernc, Python) read and write the same vec0 on-disk
format for this schema; FTS5 `MATCH` returns the same hits. Current ncruces (v0.35.6) opens the file, FTS5 works, but
`SELECT count(*) FROM message_vec` fails with `no such module: vec0` (measured).

### 3.2 Benchmark (measured; p50 over 100 random unit queries, 2 passes, warm cache)

Mac: Apple M3 Pro, macOS, Go 1.26.7, `synchronous` left at the default (FULL, as Python). Python column is the existing app's
`sqlite3` + sqlite-vec 0.1.9 on the same file.

| Workload | Python (today) | mattn+vec (cgo) | modernc+vec | ncruces v0.20+vec (wazero) | ncruces v0.35.6 (no vec) |
|---|---|---|---|---|---|
| KNN k=40, `account` partition (30k rows in partition) | 31.3 ms | 31.5 ms | 52.5 ms | 38.1 ms | - |
| KNN k=200, no partition (60k) | 69.6 ms | 69.9 ms | 120.8 ms | 100.3 ms | - |
| notion KNN k=10, `visibility=public` | 3.5 ms | 3.5 ms | 5.7 ms | 4.4 ms | - |
| exact `ORDER BY vec_distance_cosine` over 30k partition rows (8 queries) | 3.60 s | 3.59 s | 4.86 s | 5.20 s | - |
| FTS5 bm25+snippet, limit 50 (300 queries) | - | 1.87 ms | 2.12 ms | 0.72 ms | 2.32 ms |
| vec0 insert, 10 000 rows, batches of 64 (embed_pending shape) | 6 840 rows/s | 6 903 rows/s | 4 983 rows/s | 4 720 rows/s | - |
| messages+FTS5 insert, 50 000 rows, 5 000/tx | - | 26 744 rows/s | 16 364 rows/s | 13 273 rows/s | 17 919 rows/s |
| static binary size (bench program) | - | 6.3 MB (musl, Linux) | 10.7 MB | 15.5 MB | 13.2 MB |

Linux check on the owner's NixOS test machine (x86_64, 8 cores, shared with the live atrium so noisy; same 533 MB DB copy, deleted afterwards):

| Workload | mattn+vec, musl static, default flags | mattn+vec, musl static, `-mavx -DSQLITE_VEC_ENABLE_AVX -O3` | modernc+vec (`CGO_ENABLED=0`) |
|---|---|---|---|
| KNN k=40 account | 70.7 ms | **27.5 ms** | 37.9 ms |
| KNN k=200 | 155 ms | **62.7 ms** | 88.4 ms |
| notion KNN k=10 | 7.9 ms | **3.3 ms** | 4.2 ms |
| vec0 insert (rows/s, mattn built without AVX) | 3 997 | n/m | 3 241 |
| messages+FTS5 insert (rows/s) | 9 489 | n/m | 14 363 |
| FTS5 query p50 | 2.8 ms | n/m | 2.6 ms |

Reading: on arm64 the C build (NEON) equals Python exactly and modernc is ~1.5-1.7x slower on KNN and ~1.4x slower on
vector inserts. On x86 the C extension is only fast when built with AVX (`-mavx -DSQLITE_VEC_ENABLE_AVX`; default cgo flags are
scalar and lose to modernc); with AVX it beats modernc by ~1.4x. All absolute numbers are tens of ms at atrium's scale (tens of
thousands of vectors), so the speed gap matters less than the behavioural and shipping differences below. The exact-cosine
fallback (`search.py:exact_semantic`) is 3.6-5.4 s on every driver, including Python today: a pre-existing cliff, not a driver issue.

### 3.3 Several processes, WAL, busy_timeout (measured)

Harness: 3 writer processes (2 goroutines each; `BEGIN; SELECT count(*); INSERT; COMMIT` like `journal.py`) + 2 reader processes, 8 s, `busy_timeout=30000`, same file, macOS.

| Driver | `BEGIN` deferred (Go default; today's plain `BEGIN`) | `_txlock=immediate` | `immediate` + write pool size 1 |
|---|---|---|---|
| mattn | 33-34k `database is locked` per writer proc in 8 s, instantly (latency <20 ms) | 0 errors, max wait 1.8-3.7 s | 0 errors, max 1.3-4.7 s (one proc starved to 7k tx vs 50-94k) |
| modernc | 60-62k errors per proc | 0 errors, max wait 3.7-7.5 s | 0 errors, max 2.7-6.1 s (one proc 1.6k tx vs 48-57k) |
| ncruces v0.35.6 | 58-59k errors | 0 errors, max wait **33-47 ms**, fair (15.5k tx each) | - |

* Deferred read-then-write transactions fail immediately with `SQLITE_BUSY` (snapshot upgrade) and **ignore** `busy_timeout`; that is the
  documented behaviour of upgrading a read transaction in WAL when another writer committed ([wal.html](https://sqlite.org/wal.html), [lang_transaction.html](https://sqlite.org/lang_transaction.html#deferred_immediate_and_exclusive_transactions)). Python hits the same today in the plain-`BEGIN` call sites
  listed in section 1; it survives because writers are mostly serialized by `db.exclusive()`.
* `BEGIN IMMEDIATE` makes the wait go through the busy handler, so there are no errors. mattn/modernc use SQLite's sleep-polling
  busy handler: fine for atrium's light write load, but tail latency under saturation is seconds and unfair across processes.
  ncruces uses blocking OFD locks (`SQLITE_ENABLE_SETLK_TIMEOUT 2` in its build config, [build/sqlite_opt.h](https://github.com/ncruces/go-sqlite3-wasm/blob/main/build/sqlite_opt.h); [vfs/README](https://github.com/ncruces/go-sqlite3/blob/main/vfs/README.md#file-locking)) and was fair; it also starved a concurrent Python writer (68k Go commits vs 1) on macOS. `[inference]` same on Linux; not tested there.
* Mixed Python + Go (mattn, modernc, ncruces) writers on the same WAL file for 8 s: no errors, `quick_check` ok on both sides afterwards, KNN reference still identical. Rolling back from Go to Python on the same file is safe at the storage level.
* Cross-process WAL needs only the `-shm` mmap and fcntl/OFD locks; all three work on macOS. Linux was exercised only for single-process runs of the Go binaries.
* `flock(2)` on `atrium.db.lock`: use `golang.org/x/sys/unix.Flock(fd, LOCK_EX)` (or `syscall.Flock`); both Python's `fcntl.flock` and Go's call the same syscall, so Python and Go processes exclude each other. `[inference]` (not tested).

### 3.4 Why current ncruces cannot be the driver, and what its sqlite-vec story is

* ncruces v0.35.x no longer uses wazero; it depends on `github.com/ncruces/go-sqlite3-wasm/v6` (wasm2go translation); FTS5, rtree, spellfix and `vec1` are separate per-connection modules there ([go.mod](https://github.com/ncruces/go-sqlite3/blob/main/go.mod), [ext/README](https://github.com/ncruces/go-sqlite3/blob/main/ext/README.md)).
  Announcement: [discussion #361](https://github.com/ncruces/go-sqlite3/discussions/361).
* `ext/vec1` is SQLite's own new ANN extension ([sqlite.org/vec1](https://sqlite.org/vec1)), **not** sqlite-vec: different virtual-table name and storage; existing `vec0` tables cannot be read by it. `[inference]` from the module name and docs; not tried.
* `asg017/sqlite-vec-go-bindings/ncruces` embeds a prebuilt wasm and pins ncruces v0.17.1. I tried pairing it with ncruces v0.17.1, v0.18.0, v0.20.0, v0.22.0, v0.25.0: only **v0.20.0** starts (v0.17.1/0.22/0.25 fail with a wazero `atomic.store` feature error, v0.18.0 with a host-import signature mismatch). That is a 2024 stack (SQLite 3.47.0, vec 0.1.6) that cannot follow SQLite fixes.

### 3.5 Static build (measured on Linux x86_64 NixOS)

* modernc and ncruces: `CGO_ENABLED=0 go build` gives a statically linked ELF.
* mattn + sqlite-vec cgo bindings: static with `nixpkgs#pkgsStatic.stdenv.cc` (musl gcc) and `-ldflags "-linkmode external -extldflags -static"` works, but needed three workarounds:
  (1) the bindings' `sqlite-vec.h` includes `sqlite3.h`, absent on Linux; point `CGO_CFLAGS=-I<dir with sqlite3.h>` (I used nixpkgs `sqlite.dev`; mattn only ships `sqlite3-binding.h`), (2) `sqlite-vec.c:66-71` typedefs `u_int8_t/u_int16_t/u_int64_t`, unknown on musl: `-D__COSMOPOLITAN__` skips that block, (3) add `-mavx -DSQLITE_VEC_ENABLE_AVX` for x86 speed (section 3.2). In Nix this is a derivation with `CGO_CFLAGS` set, not a code change. Cross-compiling to arm64 Linux would need the matching musl cross compiler and NEON (default) instead of AVX.
* The bindings are stale: v0.1.6 tagged 2024-11-20 while sqlite-vec is at v0.1.9 (2026-03-31) with v0.1.10-alpha.4 from 2026-05-18 ([releases](https://github.com/asg017/sqlite-vec/releases)); HEAD of the bindings vendors `v0.1.7-alpha.2`. To get 0.1.9 in a cgo build, vendor `sqlite-vec.c/.h` from the release ourselves (small file; same `Auto()` wrapper) - `[inference]` not tried.
* modernc: downstream `go.mod` must pin the same `modernc.org/libc` as modernc's own go.mod (v1.77.1 for v1.60.1) ([CHANGELOG](https://gitlab.com/cznic/sqlite/-/blob/master/CHANGELOG.md), issue 177), and `modernc.org/sqlite/vec` cannot be combined with a custom page cache.

### 3.6 Encryption path for Phase B (measured where stated)

Tested on Linux x86_64, 20 000 vec0 rows (768-d) + FTS5, one connection, small page cache:

| Option | Works with vec0 + FTS5? | Result |
|---|---|---|
| SQLCipher 4.16.0 via `BitBoxSwiss/go-sqlite3-sqlcipher` (tag `sqlcipher`, crypto = LibTomCrypt on Linux, CommonCrypto on macOS, no OpenSSL, static musl link OK) + same cgo sqlite-vec | yes: created vec0+fts5 in an encrypted file, wrong/no key gives `file is not a database`; `sqlcipher_export` of the atrium test DB copied schema, messages (3 000) and `message_vec_rowids` (3 000) | **insert 20 000 rows: 75.8 s; KNN k=40 over 20k: 741 ms** vs ~5 s and ~25 ms unencrypted (AVX build). LibTomCrypt software AES is the likely cause `[inference]`: every query decrypts ~60 MB through the default 2 MB cache. OpenSSL-backed `libsqlcipher` (AES-NI) and a larger `cache_size` were **not** tested |
| ncruces v0.20 + sqlite-vec + `vfs/adiantum` (pure-Go VFS, `vfs=adiantum&hexkey=...`, macOS arm64) | yes | insert 20 000: 11.9 s vs 1.8 s plain; KNN 73 ms vs 28 ms. Encrypts main DB, journal/WAL frames, temp; no MAC, no SQLCipher format ([adiantum README](https://github.com/ncruces/go-sqlite3/blob/main/vfs/adiantum/README.md)). Stuck on the frozen 2024 stack |
| `gosqlite.org` `vfs/crypto` (Adiantum or AES-XTS-256 on its modernc fork, plus typed vec) | claims yes ([encryption guide](https://github.com/go-again/sqlite/blob/main/docs/guides/encryption.md)); **not tested** | repo is 5 months old with 2 contributors; "WAL -shm index stays plaintext"; confidentiality only |
| modernc upstream | no encryption VFS found (README has no crypto/VFS mention; a `vfs` DSN parameter exists) | pair with OS-level encryption (LUKS/ZFS native) instead |
| SQLite3 Multiple Ciphers (`utelle/SQLite3MultipleCiphers` v2.6.1, 2026-10-12) with a Go binding | Go fork `jgiannuzzi/go-sqlite3` last pushed 2026-02-27; mattn [PR #1109](https://github.com/mattn/go-sqlite3/pull/1109) (SQLCipher) is still open (updated 2026-07-01) | **not tested** |

Gotchas found while testing SQLCipher in Go: `PRAGMA key` must run before anything reads the file, so do not use mattn's
`_journal_mode=` DSN parameter (it runs first and corrupts the header read: "hmac check failed"); apply `key` then `journal_mode` in a `ConnectHook`.
The `sqlite_fts5` tag is still required alongside `sqlcipher`.

## 4. Options and trade-offs

1. **mattn/go-sqlite3 + sqlite-vec (cgo)**. Same C code as Python today: byte-identical results, equal speed on arm64, fastest on x86 with AVX. Smallest binary (6.3 MB musl static). SQLCipher is a drop-in fork with the same API. Costs: cgo toolchain in every build and CI cell, musl/header/typedef workarounds above, no cgo cross-compile for free, stale bindings (vendor vec ourselves), SQLCipher LibTomCrypt speed.
2. **modernc.org/sqlite + built-in vec (pure Go)**. `CGO_ENABLED=0`, trivial cross-compile and static, sqlite-vec 0.1.9 included and maintained in the same module, identical results. Costs: ~1.4-1.7x slower KNN/insert than C, `libc` pin, no page-cache plugin with vec, no encryption path of its own (Phase B would need OS-level encryption or the young `gosqlite.org` fork).
3. **ncruces**: best locking behaviour and fastest FTS (wazero build), pure Go; but no vec0 on the maintained line, only a frozen 2024 combo. Rejected for Phase A unless vec0 is dropped (see ticket C).
4. **Fork `gosqlite.org`**: only candidate that bundles vec0 + encryption in pure Go, but too young (created 2026-05-26) to bet the data on.

## 5. Recommendation (tied to the plan)

* **Phase A (port as-is, same file, same data, static binary): `mattn/go-sqlite3` + sqlite-vec (cgo)**, because the port must produce the same results on the existing `atrium.db` and it is the only option with the identical C implementation; build it musl-static via Nix with the three flags above, vendoring `sqlite-vec` 0.1.9 sources rather than the stale bindings tag. Keep **modernc + `modernc.org/sqlite/vec`** as the tested pure-Go fallback: same file, identical KNN results, ~1.5x slower, and the code can be written against `database/sql` so the swap is a build-tag + DSN change (keep driver-specific DSN and `Auto()` behind one `internal/db/open_*.go`).
* **Connection conventions** (all drivers; derived from 3.3):
  * Two `database/sql` pools on the same file: a **writer** pool with `SetMaxOpenConns(1)` and `_txlock=immediate` (every `BEGIN` is `IMMEDIATE`, including today's plain-`BEGIN` call sites, so the read-then-write upgrade failure cannot occur) and a **reader** pool (`N` = a few, deferred, `PRAGMA query_only=1`) for web/search reads; SQLite snapshots keep readers unblocked in WAL.
  * Per-connection pragmas via the DSN/connect hook: `busy_timeout=30000`, `foreign_keys=ON`; `journal_mode=WAL` once at init (persistent). Leave `synchronous` at default FULL to match Python; `NORMAL` is a Phase B tuning choice `[inference]`.
  * Run schema creation and column migrations once per process under the `atrium.db.lock` flock (as `db.exclusive` does), keep the "catch duplicate column" idempotence, and keep `BEGIN IMMEDIATE` for table rebuilds.
  * Keep flock on `<db>.lock` and the `notion` lock via `golang.org/x/sys/unix.Flock`.
  * Retry only `SQLITE_BUSY`/`SQLITE_LOCKED` at transaction level after the busy handler gave up (30 s); do not loop on deferred-upgrade errors, avoid them structurally.
* **Phase B encryption**: do not pick a library from this ticket alone. SQLCipher via LibTomCrypt is 15-30x slower on vec0 reads/writes in my test, so either (a) benchmark OpenSSL-backed `libsqlcipher`/SQLite3MultipleCiphers and a larger `cache_size` against this DB, or (b) use host-level encryption and drop whole-file encryption from the DB, or (c) accept an Adiantum-style VFS on a pure-Go driver. Ticket B below carries this.

## 6. Risks

* Python and Go builds differ in sqlite-vec version (0.1.9 vs 0.1.6 via the bindings tag): compatible for this schema (tested both directions); re-test on every vec bump, vec0 is pre-1.0 and `0.1.10-alpha` is moving ([sqlite-vec #320](https://github.com/asg017/sqlite-vec/issues/320) shows shadow-table naming still changing).
* cgo static builds on musl need the `-D__COSMOPOLITAN__` typedef workaround or an upstream fix; `-mavx` makes the binary x86-64-AVX only (CPU baseline decision for release binaries).
* modernc ships transpiled C: `libc` pin discipline; performance is lower and varies by arch.
* Busy-handler fairness across processes: under saturation a writer can wait seconds (measured 1.3-7.5 s with 6 hammering writers); atrium's real write rate is far lower, but the web request path should use a timeout shorter than 30 s for user-facing writes.
* All timings above are single-run p50s on a laptop and a shared server; use for ranking, not capacity planning. Linux locking behaviour of ncruces vs Python was not tested.
* The generated DB has synthetic text/vectors (clustered Gaussians, uniform vocabulary), so KNN recall/latency on real embeddings may differ; vec0 KNN is brute-force so latency scales linearly with rows in the partition.

## 7. Candidate new tickets

| Title | Type | Phase | Area | Question |
|---|---|---|---|---|
| Vendor sqlite-vec 0.1.9 for the cgo build | task | A | build | Replace the stale bindings tag with vendored `sqlite-vec.c/.h` v0.1.9 plus an `Auto()` wrapper; pin flags (`-mavx`/NEON, musl typedef) in the Nix derivation |
| Release-binary CPU baseline and target matrix | research | A | shipping | x86-64 AVX vs generic, arm64 NEON, cgo musl cross-compile for static release binaries on CI (garret matrix) |
| DB encryption approach vs vec0 performance | research | B | security/db | Benchmark OpenSSL-backed SQLCipher, SQLite3MultipleCiphers, Adiantum VFS and host-level (LUKS/ZFS) encryption on a 500 MB vec0 DB; define the threat model first (whole-file vs filesystem); key rotation and backup interplay |
| Exact-cosine fallback performance | research | B | search | `exact_semantic` takes 3.6-5.4 s for 30k rows on every driver; compare brute-force in Go over a vec0 blob scan, larger k with filter pushdown, vec0 metadata columns |
| Transaction layer spec | spec | A | db | Writer/reader pool split, `IMMEDIATE`-everywhere, retry policy, per-request write timeouts, `defer_foreign_keys`, cross-process flock replacement; golden tests that run Python and Go writers on one file |
| Schema/migration runner | spec | A | db | Once-per-process init under flock; replace `executescript(SCHEMA)` + `add_column` races with ordered idempotent steps, `user_version`, and cross-version (Go N to N+1, Go to Python rollback) compatibility |
| Multi-process topology after the port | spec | B | architecture | Is a single process with in-proc scheduler/watch feasible (removing most cross-process locking), or do systemd timers stay? affects pools and busy-handler fairness |
| Vector index scaling | research | B | search | vec0 is brute-force; at what row count does KNN exceed the latency budget; ANN options (sqlite-vec IVF/diskann in 0.1.10 alphas, SQLite `vec1`) and what they mean for on-disk migration |
| SQLite version policy | decision | A | db | Pin SQLite 3.53.x/3.54 per driver; FTS5 tokenizer/`unicode61` behaviour must match Python's 3.53.1 results for existing indexes (`INSERT INTO fts(fts) VALUES('integrity-check')` in the port's acceptance test) |
