# Audit: devbox-tools

Audited 2026-11-22 at `020c04a` (`main`, clean tree). Read-only: no code changed, nothing committed. This file is untracked.

**What was and wasn't done.** I read every non-generated `.go` file, the scripts, the workflow and all manifests. I ran `scripts/plan.sh` (it prints `any=false`, so every module has a tag matching HEAD's content) and `GOWORK=off GOPROXY=off go vet ./...` in all 15 modules (all clean, which is what CI's vet step runs). I did not build the pairs with buf, run any binary, or look at the `devbox` repo, so every statement about the other repo is "what this repo assumes", not "what the other repo does". Where I could not tell, it says **unclear**.

Line numbers refer to the tree at `020c04a`. Generated `*.pb.go` files were not read.

---

## 0. Things to know first

1. **There are zero tests.** `find . -name '*_test.go'` finds none. CI runs only `go vet` (`.github/workflows/release.yml:45`) and, for pairs, a generated-code freshness check (`:46-51`). Every claim below about behaviour comes from reading, not from a test.
2. **`tools/unwrapped` is released but is not in `manifest.json`.** It is in `go.work` and has a tag (`tools/unwrapped/v0.1.0`). `plan.sh` finds modules by globbing `tools/*/go.mod` (`scripts/plan.sh:23`) and only checks that every manifest entry exists (`:10-20`), not the reverse. `CLAUDE.md` says a new tool needs a manifest entry. Whether this is intentional is **unclear**.
3. **`.claude/CLAUDE.md` is not tracked.** `.gitignore:3` ignores `.claude/` (commit `020c04a`), and `git ls-files .claude` is empty. The project instructions I was given as "checked into the codebase" exist only in this working copy.
4. **The control plane has a legacy fallback that was meant to be retired** (`control.proto:50-51` "Retires the legacy `forward_port`"; `:20-21` "legacy exec allowlist goes away when the dispatch link is retired"). The legacy path is fully alive; see §2.1.
5. **Three copies of the reserved-port list disagree** (§2.3).

---

## 1. Inventory

### 1.1 Modules (`go.work:3-19`)

15 modules, all `go 1.27.1`. Dependencies between them are `v0.0.0` plus a local `replace`, so no module can be consumed outside this workspace at its released tag (nothing in this repo needs that; the releases are binaries).

| Module | Kind | Released as | Notes |
| --- | --- | --- | --- |
| `pkg/tether` | lib | tag only (`build.sh:10-13`) | |
| `pkg/tetherapp` | lib | tag only | |
| `pkg/dispatch` | lib | tag only | |
| `pairs/control` | pair | `pair-control_<v>_{vm,mac}.tar.gz` | no app binary on either side |
| `pairs/desk` | pair | `pair-desk_…` | |
| `pairs/ping` | pair | `pair-ping_…` | |
| `tools/agentic` `dispatch` `dispatcher` `proj` `scratch` `tether-gw` `tetherlog` `tetherping` `unwrapped` | tool | `<tool>_<v>_<os>_<arch>.tar.gz` | targets from each `.release` |

### 1.2 Binaries

"Called by" is limited to what this repo shows. "Used?" means referenced by manifest/code here; whether `devbox` actually runs it is **unclear** unless stated.

| Binary | Entry | What it does | Called by (in this repo) | Used? |
| --- | --- | --- | --- | --- |
| `dispatcher` | `tools/dispatcher/main.go:34` | VM daemon. Tether server on `:4005` for the Mac (`main.go:64-73`), unix socket for the `dispatch` CLI (`:83-96`), forward registry (`forwards.go`), supervises the control gateway and runs the control pair (`control.go:31`), runs the port watcher (`ports.go:42`) and starts/stops other pairs on `ApplyPairs` (`pairs.go:79`) | nothing; it is a daemon | manifest `devbox: true` |
| `dispatch` | `tools/dispatch/main.go:23` | CLI to the dispatcher socket: `open`, `open-or-print`, `expose`, `unexpose`, `ports`, `notify`, `clip`, `status` (`:35-59`). Behaves as `xdg-open` when argv[0] is `xdg-open` (`:24`) | nothing in this repo creates the `xdg-open` name; that is an assumption about the image | manifest `devbox: true` |
| `tether-gw` | `tools/tether-gw/main.go:29` | Gateway for one pair on one side. Checks manifest + schema hash (`schema.go:54-76`), handshakes (`gateway.go:102`), relays only calls listed in the manifest (`:211`, `:244`) | spawned by `dispatcher` for each pair (`control.go:75`, `pairs.go:251`); by the Mac `dev` (not visible); by `fakemac` example (`fakemac/main.go:57`) | manifest `devbox` + `mac` |
| `agentic` | `tools/agentic/main.go:12` | `run`, `hook pre\|post`, `ask`, `pending`, `cancel`, `log` (`:20-41`). Audit log + `git push` approval through the spool | `claude` via the settings overlay (`run.go:29-40`) | manifest `devbox: true` |
| `proj` | `tools/proj/main.go:30` | `start`, `work`, `init zsh` | the zsh wrapper (`internal/shell/proj.zsh`) | manifest `devbox: true, init: zsh` |
| `scratch` | `tools/scratch/main.go:27` | `[name]`, `init`, `ls`, `clean` (`:51-59`) | zsh wrapper | manifest `devbox: true, init: zsh` |
| `tetherping` | `tools/tetherping/main.go:24` | Demo tether server + HTTP `/ping` on `0.0.0.0:8082` (`:33`) | none | manifest `devbox: true`. See §2.4 |
| `tetherlog` | `tools/tetherlog/main.go:21` | Demo tether client that logs `RequestSeen` | none | manifest `devbox: false` yet built for `linux/amd64` too (`.release`) |
| `unwrapped` | `tools/unwrapped/main.go:22` | Web dashboard (CPU, mem, disk, tmux clients, home size) on `0.0.0.0:8089` (`:30`) | none | **not in `manifest.json`** (§0.2) |
| `ping-vm` / `ping-mac` | `pairs/ping/vm/main.go:22`, `mac/main.go:20` | Link check pair: VM serves `/ping` on `0.0.0.0:8082` (`vm:30`) and reports each request; Mac calls `Ping` every 30 s (`mac:27`) | started by dispatcher / `dev` | manifest pair `ping` |
| `desk-vm` | `pairs/desk/vm/main.go:41` | Reads asks from the spool, forwards them to the Mac, writes the answers back | started by dispatcher on `ApplyPairs` | manifest pair `desk` |
| `desk-mac` | `pairs/desk/mac/main.go:33` | Shows each ask as an `osascript` dialog (`dialog_darwin.go`), returns the decision | started by `dev` (not visible) | same |
| *(control pair)* | — | No binary. VM side is the dispatcher, Mac side is `dev` (`control.proto:14`) | — | — |

Examples (never released, compiled by `go vet ./...`): `pairs/control/examples/fakemac/main.go:28`, `pkg/dispatch/examples/recv/main.go:18`, `pkg/tetherapp/examples/echo/main.go:21`, `pkg/tether/examples/demo/demo.go` (a package, see §2.4).

### 1.3 Packages

| Package | Purpose | Importers |
| --- | --- | --- |
| `pkg/tether` | JSON-over-WebSocket messaging, bearer-token auth (`server.go:80-81`), request/response calls (`call.go:30`, `:45`), TOML link config (`config.go:22`) | `dispatcher`, `tether-gw`, `tetherping`, `tetherlog`, `pkg/dispatch/examples/recv` |
| `pkg/tetherapp` | App-side client (`Call`, `Post`), app server (`Server`, `Handle`), error codes, caller headers, `FromEnv` (`server.go:141`) | `tether-gw`, `dispatcher`, all four pair apps, both examples |
| `pkg/dispatch` | `Exec` events, local socket protocol, validators, constants | `dispatch`, `dispatcher` (+ one example) |
| `pairs/*/gen` | Generated protobuf Go (regenerate with `scripts/pair-generate.sh`) | `dispatcher` imports `control/gen` (`control.go:14`); each pair imports its own |
| `pairs/desk/internal/clean` | Text sanitising + `IDPattern`, used by both halves (`desk/vm`, `desk/mac`) | same module only |
| `tools/proj/internal/*`, `tools/scratch/internal/*`, `tools/unwrapped/internal/*` | per-tool internals | own tool |

### 1.4 Scripts, config, CI

There is **no Makefile** and no other build driver. `README.md:67-71` documents the commands.

| File | What it does | Called by |
| --- | --- | --- |
| `scripts/lib.sh` | Pins `BUF_VERSION=v1.75.0`, `PROTOC_GEN_GO_VERSION=v1.36.11` (`:4-5`); `dir_of`, `tag_prefix`, `mod_deps`, `release_paths` | sourced by the four scripts below |
| `scripts/plan.sh` | Validates `manifest.json`, finds modules changed since their last tag, computes next version from `Release-As` trailers (`:46`), prints `any=` and `matrix=` | workflow `plan` job (`release.yml:25`) |
| `scripts/build.sh` | Cross-compiles and tars one tool or pair into `dist/` (`:46-61`, `:18-44`) | workflow (`:53`) |
| `scripts/release.sh` | Refuses to overwrite an existing tag (`:13`), then `gh release create … --latest=false` (`:26`) | workflow (`:56`) |
| `scripts/pair-generate.sh` | Regenerates `pairs/<name>/gen` with pinned tools | developers; workflow freshness check (`:47`) |
| `scripts/install.sh` | Downloads a **tool** release by `gh`, verifies sha256, extracts (`:38-59`). No support for pairs (it greps `^tools/$tool/v`, `:39`, and tags `tools/…`, `:46`) | README; caller in `devbox` is **unclear** |
| `.github/workflows/release.yml` | On push to `main` touching `tools/`, `pkg/`, `pairs/` (not `.md`) (`:5`) or manual dispatch (`:6`): plan → per-module matrix (vet, pair freshness check, build, release) | GitHub |
| `go.work` | workspace of 15 modules | — |
| `manifest.json` | tools/pairs to release and which side installs them. `plan.sh` reads only `.name` and `.kind` (`plan.sh:20`); `devbox`, `mac`, `init` are not read anywhere in this repo | `plan.sh`; the other fields presumably `devbox-tools-sync` in the other repo (**unclear**) |
| `pairs/*/manifest.json` | per-pair: `pair`, `vm`/`mac` `app` and `calls`, `limits.max_message`. `build.sh:23` adds `version` and `schema.sha256` | `build.sh`; `tether-gw` at runtime (`schema.go:54`) |
| `pairs/*/buf.yaml`, `buf.gen.yaml`, `proto/…` | schema and codegen config | `pair-generate.sh`, `build.sh:21` |
| `tools/*/.release` | `TARGETS="…"` per tool; default `linux/arm64` (`build.sh:48`) | `build.sh:49` |
| `.gitignore` | `dist/`, `bin/`, `.claude/` | — |

`dist/` (ignored) holds a stale local build of `pair-ping_v0.1.0` (two tarballs and a checksum file, from 2026-11-21); latest tag is `pairs/ping/v0.3.0`. Safe to delete; I did not.

---

## 2. Bloat

### 2.1 Legacy and duplicate paths

- **Legacy forwarding (`forward_port`) survives next to the control plane.** `main.go:186-189` falls back to `fwd.expose` whenever the control plane is down or `Unavailable`; `main.go:213-218` does the same for unexpose; `main.go:221` serves `ports` only from the legacy registry. Legacy code: `tools/dispatcher/forwards.go` (421 lines), `pkg/dispatch/forward.go` (207 lines: `ActionForwardPort/Cancel`, `ForwardReady/Failed`, `MaxForwards`, `ReservedPorts`), `tools/dispatch/forward.go:88,161-174` prints "(legacy link)". The proto says the legacy link is retired in v0.2 (`control.proto:50-51`), and `control.proto:20-21` says the allowlist goes away with it. Whether the Mac side (`dev`) still answers `forward_port` is **unclear**; removing this is safe only if it does not.
- **Two registries of forwards.** The legacy one persists to `~/.local/state/dispatch/forwards.json` (`forwards.go:57`); the control plane's lives on the Mac. `dispatch ports` shows the legacy one (`main.go:221`), `dispatch` has a separate `list_forwards` (`main.go:223-231`). So the two commands can disagree.
- **Demo pair duplicating a real pair.** `tetherping` + `tetherlog` + `pkg/tether/examples/demo` implement a link check over raw `pkg/tether`; `pairs/ping` implements the same over `tetherapp`. Both serve `/ping` on `0.0.0.0:8082` (`tetherping/main.go:33`, `ping/vm/main.go:30`), so they cannot run together.
- **`MacDialPort` and `MacExecPort` are dead constants.** Defined at `pkg/dispatch/dispatch.go:20-21`, never referenced (grep: one hit each). The same numbers are repeated as literals in `ReservedPorts` (`forward.go:36`) and `reservedPort` (`ports.go:28`).
- **`agentic ask`** (`main.go:35`; `spool.go:123`) is a generic ask command. Only `hook pre` is wired to `git push`; I found no other caller. **Unclear** whether it is used by hand.
- **`install.sh` handles tools only,** not pairs. Pair assets are installed by something that isn't here.
- **Default build target is `linux/arm64`** (`build.sh:48`); every tool overrides it with `.release`, so the default is only reached by a new tool without a `.release`. The VM is x86_64 (per the brief), so that default would produce a binary the VM can't run.

### 2.2 Test-only / example code inside shipped modules

- `pkg/dispatch/go.mod` requires `pkg/tether`, toml and websocket, but no non-example file in `pkg/dispatch` imports `pkg/tether`; only `examples/recv/main.go:11` does. A library's dependency graph is carrying an example.
- `pkg/tether/examples/demo` is imported by two production tools (`tetherping/main.go:19`, `tetherlog/main.go:16`), so it is not test-only, but it is a demo type shipped in the library module.
- `pairs/control/examples/fakemac` lives inside the shipped pair module and is vetted on each release.
- `pkg/tetherapp/examples/echo` has its own `buf.yaml`, `buf.gen.yaml`, `manifest.json` and committed `gen/`. CI's "generated code is current" step runs for `kind == pair` only (`release.yml:47`), so `echo/gen` can go stale unnoticed.

### 2.3 Values duplicated or inconsistent inside this repo

| Value | Where | Problem |
| --- | --- | --- |
| Reserved ports | `pkg/dispatch/forward.go:36` `{22, 4005, 14005, 14006}`; `tools/dispatcher/ports.go:27-29` `4005, 4100–4199, 8085, 14005, 14006`; `control.proto:44-45` same as ports.go | Three lists. `22` is only in the first; `4100–4199` and `8085` only in the other two. `controlfwd.go:42-50` uses the second, the legacy path uses the first, so the same port can be accepted on one path and refused on the other. |
| Max forwards | `pkg/dispatch/forward.go:15` = 10; `control.proto:41` and `controlfwd.go:117` say 32 | Different limits for the same concept. `32` is a string literal in `controlfwd.go:117` that the dispatcher does not enforce itself. |
| Dispatcher port | `pkg/dispatch/dispatch.go:19` (4005); env `DISPATCHER_PORT` (`main.go:64`) | Reserved-port logic partly honours the env override (`control.go:97`), partly doesn't (`ports.go:28`, `forward.go:36`). |
| Control port | `control.go:19` (4100), env `DISPATCHER_CONTROL_PORT` (`:61`); pair range `pairs.go:25-26` (4101–4199) | ok, but 4100/4101–4199 appear in three places (also `ports.go:28`, `control.proto:11`, `:78`). |
| Spool path and ID pattern | `tools/agentic/spool.go:18,15` and `pairs/desk/vm/main.go:34`, `clean.IDPattern` | Intentional (`spool.go:17` says it is a file contract, not an import). Two copies must be kept in step by hand; nothing checks them. |
| Hook timeout 7 days | `run.go:17` | README says "up to seven days" (`README.md:56`); consistent. `spool.wait` itself never times out (`spool.go:84`). |

### 2.4 Hard-coded values that belong to the other repo

- `/home/sam` default for `unwrapped -home` (`unwrapped/main.go:31`).
- `sam` / `work` default spool owner and group in `desk-vm` (`desk/vm/main.go:49-50`); "denied by Sam" text sent to the agent (`agentic/hook.go:75`) and `run.go:17` comment.
- `/var/lib/devbox-desk` (`agentic/spool.go:18`, `desk/vm/main.go:34`) and its layout `asks/` `answers/` `cancel/` with modes `0770/0750/0770` (`desk/vm/main.go:127`), which `desk-vm` checks but never creates (`:112` comment).
- `/etc/devbox/static-forwards` (`ports.go:22`).
- Port `8085` (`ports.go:28`, `control.proto:45`). What owns it is **unclear**.
- `~/.config/devbox/env` (`dispatcher/main.go:58,127`, `proj.zsh:2`, `scratch.zsh:2`) and `~/.config/devbox/links.toml` (`tetherping/main.go:34`, `tetherlog/main.go:30`). Why `proj` and `scratch` wrappers source the env file is **unclear**; neither binary reads anything from it that I could find (they read `PROJECTS_ROOT`, `SCRATCH_ROOT`, `TMUX*` from the environment, which the file could set).
- `hamshoes/devbox-tools` (`install.sh:7`).
- Dialog title prefix `devbox · ` (`desk/mac/main.go:233`).
- `localhost` / `devbox:<port>` / `127.0.0.2` assumptions about how the Mac forwards ports (`forwards.go:170`; `control.proto:147-149,188`).

---

## 3. Cross-repo contract (what this repo assumes about `devbox`)

### 3.1 Ports

| Port | Meaning | Source |
| --- | --- | --- |
| 4005 | dispatcher tether server in the container, bound `0.0.0.0` | `dispatch.go:19`, `main.go:70` |
| 14005 | Mac end of the ssh forward to 4005 | `dispatch.go:20` (unused constant) |
| 14006 | "Mac executor" | `dispatch.go:21` (unused constant) |
| 4100 | control pair VM gateway, bound `0.0.0.0`; proto says "published on VM loopback" | `control.go:19,75`; `control.proto:11-12` |
| 4101–4199 | VM ports for other pairs, one per pair, requested by the Mac | `pairs.go:25-26,95` |
| 8082 | `ping-vm` and `tetherping` HTTP | `ping/vm/main.go:30`, `tetherping/main.go:33` |
| 8089 | `unwrapped` | `unwrapped/main.go:30` |
| 8085, 22 | reserved (owner unclear) | `ports.go:28`, `forward.go:36` |

Only the dispatcher's own binds are in this repo. Docker port publishing, the ssh forwards and IAP are all the other repo's; I cannot verify that 4005 and 4100 are published or that the container is reachable on the IP `containerIP()` returns (`procnet.go:87`, default-route interface first, then first up non-loopback IPv4).

### 3.2 Paths (all under the container user's home unless absolute)

| Path | Role | Source |
| --- | --- | --- |
| `~/.local/share/devtools/bin` | where `install.sh` puts Linux binaries | `install.sh:10` |
| `~/.local/bin` | macOS install dir; also the dispatcher's fallback for finding `tether-gw` after `PATH` | `install.sh:11`, `pairs.go:411` |
| `~/.local/share/devtools/pairs/<name>/{current,<version>}` | where an installed pair must be (a directory with `manifest.json`, `api.binpb`, and the `vm.app` binary) | `control.go:39-42`, `pairs.go:175-203` |
| `~/.local/state/tether/<name>/{token,gw.sock,app.sock,gw.log,app.log}` | per-pair runtime state; control uses `…/control/` | `control.go:40,55-62`, `pairs.go:76,234-253` |
| `~/.local/state/dispatch/dispatch.sock` | CLI ↔ dispatcher socket, mode 0600 | `dispatch.go:137`, `main.go:83-94` |
| `~/.local/state/dispatch/forwards.json` | legacy forward registry | `forwards.go:57` |
| `~/.local/state/agentic/runs` | audit logs, 200 kept | `audit.go:56`, `:20` |
| `~/.config/agentic/claude-settings.json` | settings overlay | `run.go:57-58` |
| `~/.config/devbox/env` | `DISPATCH_TOKEN` fallback | `dispatcher/main.go:118-147` |
| `~/.config/devbox/links.toml` | tether demo links | `tetherping/main.go:34` |
| `/var/lib/devbox-desk/{asks,answers,cancel}` | approvals spool; `DESK_SPOOL` overrides | `desk/vm/main.go:34,61`; `agentic/spool.go:18,27` |
| `/etc/devbox/static-forwards` | image-defined static forwards; free-form: whitespace/comma separated, bare port or `host:port`, `#` comments | `ports.go:22,185-207` |
| `devbox-tools-sync` | binary the dispatcher runs (from `PATH`, 25 s timeout) when a requested pair isn't installed, once per `ApplyPairs` | `pairs.go:144-148,206-219` |

### 3.3 Environment variables

| Variable | Read by | Source |
| --- | --- | --- |
| `DISPATCH_TOKEN` | dispatcher (env first, then `~/.config/devbox/env`, parsing `export K=V` with quotes); also the token of the `control` pair (`control.go:56`); stripped from child environments | `dispatch.go:23`, `main.go:118-147`, `supervise.go:32-42` |
| `TETHER_GW_SOCKET`, `TETHER_APP_SOCKET` | set by the dispatcher on pair apps; read by `tetherapp.FromEnv` | `pairs.go:258`, `server.go:141` |
| `DISPATCHER_PORT`, `DISPATCHER_CONTROL_PORT` | dispatcher; "so a second dispatcher can run beside the real one" | `control.go:21-27` |
| `DESK_SPOOL` | `desk-vm`, `agentic` | above |
| `AGENTIC`, `AGENTIC_RUN_ID`, `AGENTIC_RUN_NAME` | set by `agentic run`, read by `agentic` | `run.go:91-93`, `spool.go:60` |
| `PROJECTS_ROOT`, `SCRATCH_ROOT`, `TMUX`, `TMUX_PANE` | `proj`, `scratch` | `proj/main.go:78`, `scratch/main.go:41`, `tmux.go:15-16` |
| `GH_TOKEN` / `GITHUB_SHA` | CI release | `release.yml:55`, `release.sh:27` |

Each tether link's token is a bearer token (`server.go:80-81`, `client.go:64`). A pair's token is chosen by the Mac per session and must match `^[0-9a-fA-F]{32,128}$` (`pairs.go:35,99`).

### 3.4 Tag and asset names

| What | Name | Source |
| --- | --- | --- |
| tool tag | `tools/<name>/vX.Y.Z` | `lib.sh:10`, `release.sh:12` |
| pair tag | `pairs/<name>/vX.Y.Z` | same |
| library tag | `pkg/<name>/vX.Y.Z` (no assets) | same |
| tool asset | `<tool>_<v>_<os>_<arch>.tar.gz`, containing one file `<tool>`; `<tool>_<v>_checksums.txt` | `build.sh:57,61` |
| pair asset | `pair-<name>_<v>_<vm\|mac>.tar.gz` containing `manifest.json`, `api.binpb`, and the side's app if it has one; `pair-<name>_<v>_checksums.txt` | `build.sh:23-27,38,42` |
| release flags | `--latest=false`: no release is ever "latest" on GitHub, so consumers must list tags; `install.sh` does this with `sort -V` (`:38-40`) | `release.sh:27` |
| version stamp | `-X main.version=<tag version>` (so `dev` in untagged builds) | `build.sh:33,56` |
| pair targets | VM `linux/amd64`, Mac `darwin/arm64`, fixed, no `.release` | `build.sh:31` |
| tool targets | from `tools/<t>/.release`: `agentic`, `dispatch`, `dispatcher`, `tetherping` linux/amd64; `proj`, `scratch`, `unwrapped` linux/arm64 + amd64; `tether-gw` linux/amd64 + darwin/arm64; `tetherlog` darwin/arm64 + linux/amd64 | `.release` files |

`control.proto:33-35` also says `dev` downloads "the control `_mac` asset" of the version the VM reports; that matches the pair asset name above.

### 3.5 `manifest.json` fields

Root (`manifest.json`): `version: 1` (checked, `plan.sh:7`), `tools[].name` and `.kind` (read by `plan.sh:20`; `kind` defaults to `tool`). `devbox`, `mac`, `init` are **not read by anything here**. They are a contract with something outside this repo (**unclear** what).

Pair (`pairs/<n>/manifest.json`):

| Field | Read by |
| --- | --- |
| `pair` | `tether-gw` (`schema.go:63`), dispatcher `locate` (`pairs.go:192`) |
| `version`, `schema.sha256` | injected at build time (`build.sh:23`); `tether-gw` requires both (`schema.go:63,75`) and refuses to start without them. The committed `manifest.json` has neither, so a gateway can't run directly against the source tree; `fakemac` needs a packaged dir (`fakemac/main.go:32`) |
| `vm.app`, `mac.app` | `build.sh:28-37` (must be consistent with `vm/` or `mac/` existing); dispatcher (`pairs.go:42-46`, plain file name only `:196`) |
| `vm.calls`, `mac.calls` | `tether-gw`: each call is owned by exactly one side (`schema.go:96`); the gateway enforces direction (`gateway.go:211-214,244-247`) |
| `limits.max_message` | `tether-gw` adds 4096 for the envelope (`main.go:81`); default 1 MiB (`schema.go:64-66`) |

### 3.6 Schema hashes and version matching

- `api.binpb` is `buf build proto` output (`build.sh:21`); its SHA-256 goes in the manifest (`:22-23`). The gateway recomputes and refuses a mismatch (`schema.go:73-76`). On connect the Mac gateway sends `Hello{Pair, Version, SchemaSHA256}`; either side refuses a different pair name or hash (`gateway.go:89-98,102-130,148-160`). A refusal is final until restart (`gateway.go:100-101`).
- **Versions need not match, hashes must.** A version difference only logs a warning (`gateway.go:121-123,158-160`). `control.proto:31-35` puts the "download the matching asset" logic in `dev`.
- **Possible trap, not verified:** `build.sh:21` passes no `--exclude-source-info`. If buf includes source info by default (my recollection of its documented default), then editing a comment in a `.proto` would change the hash and force both sides to upgrade together. I could not confirm it: the only built artefact (`dist/…ping_v0.1.0`) comes from a proto with no comments, and I did not run buf. Worth one test with `control.proto`, which has a large header comment.
- Because `release_paths` includes a module's replaced dependencies (`lib.sh:26-29`), a change in `pkg/tetherapp` bumps `tether-gw`, `dispatcher`, `control`, `ping` and `desk` too. That is why `pairs/ping/v0.3.0` and `pairs/control/v0.4.0` carry the same commit (`a68e565`) as `dispatcher/v0.5.0`. Their schema hash is unchanged unless the `.proto` changed; I did not check whether `ping`'s did.
- `lib.sh:22` only follows `=> ../../(pkg|pairs)/x` replaces, so transitive dependencies are not tracked: `tools/dispatch` replaces only `pkg/dispatch` (`go.mod`), and a change in `pkg/tether` doesn't release it. That happens to be right today because `pkg/dispatch` doesn't import `pkg/tether` (§2.2), but `pkg/dispatch`'s own `=> ../tether` isn't matched by the regex either.

### 3.7 Wire protocol between dispatcher and the Mac

- Legacy tether message types: `exec` (`dispatch.go:42`), `forward_ready` (`forward.go:58`), `forward_failed` (`:67`). Exec actions the Mac must handle: `open_url`, `notify`, `clip`, `forward_port`, `forward_cancel` (`dispatch.go:27`, `notify.go:13-14`, `forward.go:13-14`).
- Tether path `/tether` with `Authorization: Bearer <DISPATCH_TOKEN>` (`dispatch.go:22`, `server.go:80`).
- Control pair RPCs (`control.proto`): Mac→VM `Control/ApplyPairs`, `Control/Health`; VM→Mac `ControlEvents/PairStatus`, `Health`, `PortsChanged`, `Forwards/Expose`, `Unexpose`, `ListForwards`. The Mac decides which pairs run; the VM stops all pairs after the control connection has been down 30 s (`pairs.go:29,361-383`).
- `ApplyPairs` validation: name `^[a-z0-9-]{1,32}$`, port 4101–4199, token hex 32–128, version `latest` or `[A-Za-z0-9._-]{1,64}` (`pairs.go:34-36,91-103`). `latest` maps to the `current` directory (`:175-177`). `PairResult.state` values: `started`, `failed`, `not_installed`, `mismatch` (`pairs.go:151-157`, `control.proto:83`).
- **Not enforced here:** `control.proto:76,68` say the name is "never `control`". `ApplyPairs` has no such check (`pairs.go:91-109`), and a pair named `control` would use `~/.local/state/tether/control/` (`pairs.go:76`), the same directory as the control gateway (`control.go:55`). Since `tether-gw` deletes and recreates its socket at startup (`server.go:127-129`), that would break the control gateway. I haven't tried it; the Mac is trusted by design (`control.proto:19-21`), so this is a robustness gap rather than a security one.
- Caller identity: `tether-gw` stamps `SO_PEERCRED` uid/pid on outbound calls (`gateway.go:318-321`, `peercred_*.go`); the dispatcher reads it itself for the CLI socket (`creds_linux.go:22`) and passes it as `Requester`. Header names `Tether-Caller-Side/Uid/Pid` (`caller.go:18-20`).

### 3.8 Sync and install behaviour (this repo's side)

- This repo never installs anything on the VM or Mac itself. It publishes release assets, and `install.sh` is a convenience for tools.
- The dispatcher expects an installed pair directory to be produced by `devbox-tools-sync` (`pairs.go:206`) and runs it itself on first miss (`:144-148`). What sync downloads, from where, and how it picks `current` is **unclear** from here.
- `agentic` and `desk` meet only at the spool (`spool.go:17`, `desk/vm/main.go:112`): the image must create `/var/lib/devbox-desk/{asks,answers,cancel}`, setgid group `work`, owner `sam` (`desk/vm/main.go:49-50,127`; `spool.go:55` "setgid"). If it doesn't, `agentic` denies every push (`hook.go:62-65`, `spool.go:26-35`).

---

## 4. How `devbox` uses `devbox-tools`

Not applicable: this audit is of `devbox-tools`. For the other repo's audit, §3 lists the surfaces to check.

---

## 5. Deploy: how a change reaches the VM and the Mac

### 5.1 In this repo (verified from the scripts and workflow)

1. Commit to `main` with a subject like `feat(scope): …`. Add `Release-As: minor|major` as a trailer on any commit that touches the module (or a replaced dependency) to bump higher (`plan.sh:46-51`). Default is patch.
2. Push triggers `release` only if the push touches `tools/`, `pkg/` or `pairs/` outside `.md` files (`release.yml:5`). Changes to `scripts/`, `manifest.json`, `go.work`, `README.md` or `.github/` alone release nothing. The same workflow can be started by hand (`:6`).
3. Job `plan` (`:17-25`): `scripts/plan.sh` lists modules whose content differs from their last tag, including changes in replaced dependencies (`lib.sh:26-29`, `plan.sh:29-43`). No previous tag → `v0.1.0` (`plan.sh:39-40`). Output is a matrix.
4. Job `release`, one runner per module, `fail-fast: false` (`:27-31`): `go vet` with `GOWORK=off` (`:44-45`); for pairs, regenerate and fail on a diff (`:46-51`); `scripts/build.sh` (`:53`); `scripts/release.sh` (`:56`), which creates the tag and release in one `gh release create … --target $GITHUB_SHA` (`release.sh:26`). Concurrency group `release`, no cancellation (`:11-13`). A failed module doesn't block the others, so one push can publish some modules and not others.
5. Nothing runs on PRs, and nothing runs tests (there are none).

### 5.2 What reaches the VM and Mac, and what decides the version

- **This repo's output** is a GitHub release per changed module, and nothing else. It has no deploy step and no knowledge of the VM.
- **Which version runs on the VM** is decided outside this repo for every tool (the image build, container start, or `devbox-tools-sync`, **unclear**), except two things I can see:
  - `install.sh` with no version picks the highest `tools/<tool>/v*` tag by `sort -V` among the 200 most recent releases (`install.sh:37-41`). The 200 limit is a silent cut-off once the total of tags passes 200 (there are 43 tags now).
  - Pairs: the **Mac** names a `version` per pair in `ApplyPairs` (`control.proto:77`); the dispatcher uses `latest` → the `current` symlink, or the named version directory (`pairs.go:173-178`). The dispatcher doesn't download; it runs `devbox-tools-sync` once if the directory is missing (`:144-148`).
- **Which version runs on the Mac** is decided by `dev` (other repo); the proto says it follows the VM's version on a schema mismatch by downloading the `_mac` asset and restarting the control gateway (`control.proto:33-35`). I cannot see that code.
- **What keeps the two sides compatible** is the schema hash handshake (§3.6), not the version number.
- **Updating a running VM:** the dispatcher supervises its children with backoff (`supervise.go:56-92`) but nothing here restarts the dispatcher itself when its binary changes. A pair app or gateway picks up a new version only after `ApplyPairs` stops and restarts it (the keep-or-restart rule compares name, requested version, token and port, `pairs.go:112-127`). Note that with `version: "latest"` the requested string doesn't change when `current` moves, so a running pair isn't restarted on its own. **Unclear** how `dev` handles this; it may change the token every session (`control.proto:79` "fresh every session"), which would force a restart.

---

## 6. Suggested follow-ups (not done)

Ordered by how cheaply each removes a risk.

1. Decide whether `unwrapped` belongs in `manifest.json`, and add it or document why not (§0.2).
2. Unify the reserved-port list and forward limit into one place in `pkg/dispatch` (§2.3).
3. Reject `ApplyPairs` with `name == "control"` (§3.7).
4. Test whether a comment-only edit to a `.proto` changes `schema.sha256`; if so, pass `--exclude-source-info` or document it (§3.6).
5. Decide whether `dev` still answers `forward_port`; if not, delete the legacy forwarding path (~800 lines across `forwards.go`, `pkg/dispatch/forward.go`, `tools/dispatch/forward.go`) (§2.1).
6. Move `pkg/dispatch/examples/recv` and `pkg/tether/examples/demo` so that library `go.mod` files carry only what the library needs (§2.2).
7. Add at least tests for `push.go` (the parser is the security-relevant part of `agentic`), `plan.sh` version logic, and `ApplyPairs` validation.
8. Track `.claude/CLAUDE.md` or fix the statement that it is checked in (§0.3).
9. Note for `agentic`: `hookPre` returns "no decision" when the hook JSON can't be read (`hook.go:38-42`), so a malformed hook payload is not gated. That fits "the hook only adds restrictions", but it is the one path where an ungated `git push` is possible without the parser's lean-toward-asking rule applying; worth a deliberate decision.
