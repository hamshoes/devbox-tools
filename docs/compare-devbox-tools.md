# Expected vs actual: devbox-tools

Compares the blind "expected state" write-up against the repo at `020c04a` (see `docs/audit-devbox-tools.md` for the evidence behind most rows; the audit is unchanged). Numbers in the `#` column follow the expected document's own numbering (§2 layout = L, §3 modules = M, §4 devbox usage = U, §5 findings = 1–15, plus X for things it didn't cover). Where I can't see the devbox repo the verdict is **unclear**, which I fold into "matches"/"diverged" only when this repo's side settles it.

Verdicts: matches / diverged / missing / extra.

## Layout and conventions

| # | Expected | Actual (file:line) | Verdict | Why |
| --- | --- | --- | --- | --- |
| L1 | `manifest.json`, `go.work`, `release.yml`, `scripts/plan.sh` at the root | all four exist: `manifest.json`, `go.work:3-19`, `.github/workflows/release.yml`, `scripts/plan.sh` | matches | |
| L2 | `GOWORK=off` in CI | `release.yml:44` (`env: { GOWORK: 'off' }` on vet); `build.sh:21,32,55` | matches | |
| L3 | each tool has its own module and a `.release` with `TARGETS` | 9 of 9 tools have both (`tools/*/.release`) | matches | |
| L4 | tool main at `main.go` or `cmd/…` | always `tools/<t>/main.go` (e.g. `tools/dispatcher/main.go:34`); no `cmd/` anywhere | matches | |
| L5 | pairs have `go.mod proto/ buf.yaml buf.gen.yaml gen/ manifest.json vm/ mac/ [examples/]` | `control` has no `vm/` or `mac/` (no apps); `ping` has no examples; `desk` also has `internal/clean` | diverged (minor) | control is schema-only by design (`control.proto:14`); `desk` shares a sanitising package between halves (`pairs/desk/internal/clean/clean.go`) |
| L6 | pairs and tools use `replace ../../pkg/...` | e.g. `pairs/control/go.mod`, `tools/dispatcher/go.mod`; all dependencies are `v0.0.0` + replace | matches | `pkg/dispatch` uses `=> ../tether` (one level), a form `lib.sh:22` does not match |
| L7 | generated code committed, CI fails on diff | `release.yml:46-51` | matches | only for `kind == pair`; `pkg/tetherapp/examples/echo/gen` is committed but unchecked |
| L8 | `pkg/tether`, `pkg/tetherapp`, `pairs/<name>` | present | matches | |
| X1 | (not listed) a third library | `pkg/dispatch` (exec events, socket protocol, validators): `pkg/dispatch/dispatch.go`, `forward.go`, `notify.go`, `control.go` | extra | The expected layout names only two libraries; this one is imported by `dispatch` and `dispatcher` |
| X2 | (not listed) other scripts | `scripts/build.sh`, `release.sh`, `install.sh`, `lib.sh`, `pair-generate.sh` | extra | Only `plan.sh` was expected. `install.sh` (tools only, `:38-59`) isn't something the expected doc mentions |

## Modules

| # | Expected | Actual (file:line) | Verdict | Why |
| --- | --- | --- | --- | --- |
| M1 | `tools/proj` CLI | `tools/proj/main.go:30`; `start`, `work`, `init zsh` (`:53-63`) | matches | "categorised projects dir": actual root is `$PROJECTS_ROOT` or `~/projects` (`:77-83`); categories not visible in what I read (**unclear**) |
| M2 | `tools/scratch` CLI | `tools/scratch/main.go:27` | matches | |
| M3 | `tools/unwrapped` VM service on `:8089` | `unwrapped/main.go:22,30`; stats from `/proc`, statfs, tmux (`stats/*.go`) | matches | `forward 18089:8089` belongs to devbox; unverifiable. **Not in `manifest.json`** (audit §0.2) |
| M4 | `tetherping` on `:8082`, legacy | `tetherping/main.go:33`; still in `manifest.json` (`devbox: true`) | matches | |
| M5 | `tetherlog` runs on the Mac (darwin), `"mac": true` in the manifest | `.release` = `darwin/arm64 linux/amd64`; manifest has `"devbox": false` and **no** `mac` | diverged | The manifest flag the expected doc shows (`{"name":"tetherlog","mac":true}`) isn't set; the binary is still built for the Mac. Either the flag is unused or the Mac never fetches it (**unclear**) |
| M6 | dispatcher on `:4005` plus a local unix socket | `dispatch.go:19`, `main.go:70`; socket `dispatch.go:137`, `main.go:83-94` | matches | |
| M7 | dispatcher, `dispatch` and `xdg-open` maybe in one module (`cmd/dispatcher` + `cmd/dispatch`, a guess) | two modules: `tools/dispatcher` and `tools/dispatch`; xdg-open is argv[0] detection inside `dispatch` (`tools/dispatch/main.go:24`) | diverged | They are separate released modules sharing `pkg/dispatch` |
| M8 | legacy exec actions `open_url`, `forward_port`, `notify`, `clip` | `main.go:236-275` (`open_url`, `notify`, `clip`); `forwards.go` (`forward_port`, `forward_cancel`) | matches | plus `forward_cancel` (`forward.go:14`), not listed |
| M9 | control plane VM half: runs the control gateway, serves `Control/*`, supervises pairs, stops pairs 30 s after losing control | `control.go:31-122`; `ApplyPairs` `pairs.go:79`; `controlDownMax = 30s` `pairs.go:29`, `watchControl` `:361-383` | matches | |
| M10 | v0.2 port watcher from `/proc/net/tcp*` → `PortsChanged`; `Forwards` client behind `dispatch expose/unexpose/ports` with legacy fallback | `ports.go:42`, `procnet.go`; `controlfwd.go:73-103`; fallback `main.go:186-189,213-218` | matches | `dispatch ports` reads only the legacy registry (`main.go:221`), so it does not use the `Forwards` client; only `list_forwards` does (`:223`). Small divergence from "behind `ports`" |
| M11 | `tether-gw` on VM (linux/amd64) and Mac (darwin/arm64) | `tools/tether-gw/.release` | matches | |
| M12 | gateway: Connect unary over unix sockets ↔ WebSocket, payload checks against `api.binpb`, enforces own manifest, `Hello` hash check, per-pair token, `/healthz`, caller stamping | `gateway.go:148` (hello), `:211`, `:244` (direction + validate), `:280` (`/healthz`), `:318` (peer creds); `schema.go:54-76`; token `main.go:122-139` | matches | token file must be mode 0600 (`main.go:127`) |
| M13 | `pkg/tether` has `DialFromEnv` | none: `grep DialFromEnv` finds nothing. Config comes from `LoadConfig` over a TOML links file (`pkg/tether/config.go:22`) | missing | `LoadConfig` is used only by `tetherping`/`tetherlog` |
| M14 | `pkg/tether` v0.1.2 | latest tag `pkg/tether/v0.1.2` | matches | |
| M15 | `pkg/tetherapp`: `Call`, `NewServer`, `Handle`, `Serve`, `FromEnv`, `CallerFrom` | `client.go:99`, `server.go:29,34,104,141`, `caller.go:27`; latest tag `v0.3.0` | matches | |
| M16 | `pairs/control` is schema-only, v0.2.0 / v0.3.0 / v0.4.0 | no `vm/` or `mac/`; tags `v0.1.0…v0.4.0`; manifest has no `app` (`pairs/control/manifest.json`) | matches | there is also `v0.1.0` |
| M17 | `pairs/ping` with `ping-vm`, `ping-mac` | `pairs/ping/vm/main.go:22`, `mac/main.go:20` | matches | latest `v0.3.0` |
| M18 | `pairs/desk` **being built** (D1) | built and tagged `pairs/desk/v0.1.0` (commit `5dad34b`); VM `desk/vm/main.go:41` (509 lines), Mac `desk/mac/main.go:33` with an `osascript` dialog (`dialog_darwin.go`) | diverged | It is finished enough to be released |
| M19 | `tools/agentic` **being built** (D2): `run`, hooks, audit log, `ask`/`pending`/`cancel`/`log` | built and tagged `tools/agentic/v0.1.0` (`89445d9`); all subcommands present (`main.go:20-41`) | diverged | Same: already released |
| M20 | "Not in this repo": `devbox-tools-sync`, `devbox-services`, `devbox-entry`, `devbox-start`, `dev`, `devui` | none present; the dispatcher only shells out to `devbox-tools-sync` (`pairs.go:206-219`) | matches | |
| M21 | "Specced but not built" should not exist: `pairs/xcb`, `tools/harness`, `pairs/clip`, `pairs/snap` | none present | matches | |
| M22 | X0: `Connect-Timeout-Ms` deadline passing should not exist yet | exists: `tetherapp/server.go:84-90` (`requestContext`), `gateway.go:` `app()` (`strconv.ParseInt(r.Header.Get("Connect-Timeout-Ms")…`, `:~295`) | diverged | Built already |
| M23 | X0: extra Connect codes `failed_precondition`, `not_found`, `aborted`, `resource_exhausted` should not exist | `failed_precondition` and `resource_exhausted` exist (`error.go:21-22`); `not_found` and `aborted` are not among the constants (`error.go:14-24`) | diverged (partly) | Half built. `HTTPStatus` has a 404 branch (`error.go:52`); I did not check which code string it matches |
| M24 | X0: `vm.bin` / `mac.bin` pair CLIs should not exist | none; manifests use `vm.app` / `mac.app` (`build.sh:28`) | matches | |

## How devbox uses this repo (only what this repo shows)

| # | Expected | Actual (file:line) | Verdict | Why |
| --- | --- | --- | --- | --- |
| U1 | root manifest entries like `{ "name": "proj", "devbox": true }`; pairs use `"kind": "pair"` | `manifest.json:3-16` | matches | extra field `"init": "zsh"` on `proj` and `scratch`; `plan.sh` reads only `name`/`kind` (`plan.sh:20`). `devbox`/`mac`/`init` are read by nothing here |
| U2 | `plan.sh` validates manifest | `plan.sh:7-20` | matches | validates one direction only (listed ⇒ exists); an unlisted module like `unwrapped` is still released (`plan.sh:23`) |
| U3 | tags `tools/…`, `pkg/…`, `pairs/…` | `lib.sh:7-18`; tags exist for all three | matches | |
| U4 | tool assets `<name>_<ver>_<os>_<arch>.tar.gz` + checksums | `build.sh:57,61` | matches | |
| U5 | pair assets `pair-<name>_<v>_vm\|mac.tar.gz` with manifest (+`version`, `schema.sha256`), `api.binpb`, side app | `build.sh:23-27,38,42` | matches | for `control` neither tarball contains an app |
| U6 | re-release dependents when a `pkg/*` changes | `lib.sh:26-29`, `plan.sh:29-43` | matches | follows direct `../../pkg|pairs` replaces only |
| U7 | patch default, `Release-As:` | `plan.sh:45-56` | matches | |
| U8 | `CGO_ENABLED=0`, per-`.release` targets | `build.sh:32,55,48-49` | matches | pairs ignore `.release`: VM linux/amd64, Mac darwin/arm64 fixed (`build.sh:31`) |
| U9 | `buf build`, hash stamped into manifest | `build.sh:21-23` | matches | |
| U10 | sync extracts to `~/.local/share/devtools/{bin,pairs/<name>/<v>}` with a `current` symlink, keeps 2 versions | dispatcher reads `~/.local/share/devtools/pairs/<name>/{current,<version>}` (`control.go:39-42`, `pairs.go:175-178`); `install.sh:10` uses `…/devtools/bin` | matches (this side) | "keep 2", `tools.json` pins/excludes: not visible here |
| U11 | `dev` downloads `tether-gw` darwin and `_mac` pair halves into `~/.local/share/devbox/tether` | `control.proto:33-35` says `dev` downloads the control `_mac` asset; the path is not in this repo | unclear | |
| U12 | `dev` imports `pkg/tether` v0.1.2, `pkg/tetherapp` v0.3.0, `pairs/control` v0.4.0 | nothing here refers to `dev`; tags match those versions | unclear | The versions exist; the importing side is invisible |
| U13 | `devbox-services` runs released tools under supervision | the dispatcher supervises its own children (`supervise.go:56-92`); `devbox-services` not visible | unclear | |

## The expected audit findings

| # | Expected | Actual (file:line) | Verdict | Why |
| --- | --- | --- | --- | --- |
| 1 | `tetherping` and `tetherlog` still released and in `manifest.json` | `manifest.json:8-9`; both tagged (`tools/tetherping/v0.1.1`, `tools/tetherlog/v0.1.1`) | matches | `tetherping` is `devbox: true`; `tetherlog` is `devbox: false`. They also share `:8082` with `ping-vm` |
| 2 | legacy dispatch link alive alongside the control plane, `forward_port` only a fallback | `main.go:186-218`; `forwards.go`, `pkg/dispatch/forward.go` | matches | `open_url`, `notify`, `clip` have no control-plane replacement at all (`main.go:236-275`), so they are the primary path, not legacy |
| 3 | two APIs in `pkg/tether`; fire-and-forget probably only serves legacy and demos | `Send` + `On` (fire-and-forget, `session.go:79`, `message.go:75`) vs `Call` + `Handle` (`call.go:30,45`). Users of `On`/`Send`: dispatcher legacy (`main.go:67-68`), `deliver` (`main.go:305`), tetherping/tetherlog. Users of `Call`/`Handle`: `tether-gw` (`main.go:79-80`, `gateway.go:105`) | matches | the dispatcher's `notify` for crashed pairs also uses the legacy `Send` (`pairs.go:313-314`) |
| 4 | dispatcher does five jobs and is the largest, tangled module | broker + control plane + pair supervisor + port watcher + CLI backend all in `tools/dispatcher` (~1,900 lines over 9 files: `main.go` 317, `forwards.go` 421, `pairs.go` 417, `ports.go` 286, …) | matches | largest hand-written module; split by file per job, but the legacy and control paths meet in `handle` (`main.go:167-278`). "Tangled" is a judgement I did not test |
| 5 | test-only code in shipped modules: `echo`, `fakemac` | `pkg/tetherapp/examples/echo`, `pairs/control/examples/fakemac/main.go:28` | matches | also `pkg/dispatch/examples/recv` (drags `pkg/tether` into `pkg/dispatch/go.mod`) and `pkg/tether/examples/demo`, which two real tools import |
| 6 | paths `~/.local/share/devtools/…`, `~/.local/state/tether/<name>/`, `/var/lib/devbox-desk`, maybe `/etc/devbox/static-forwards` | `control.go:39-40`; `desk/vm/main.go:34`, `agentic/spool.go:18`; `ports.go:22` | matches | all four exist |
| 7 | control `4100`, pair range `4101–4199`, reserved-port list duplicated in dispatcher and `dev`; container-IP lookup duplicated | `control.go:19`, `pairs.go:25-26`; reserved list in **three** places inside this repo: `pkg/dispatch/forward.go:36`, `ports.go:27-29`, `control.proto:44-45`; container IP: single implementation `procnet.go:87`, used by both the legacy path (`forwards.go:62`) and control (`control.go:` `hostIP`) | diverged | The reserved list is duplicated, and inconsistent, within this repo too. The container-IP lookup is **not** duplicated here (one function, reused) |
| 8 | dispatcher assumes `devbox-tools-sync` and shells out to it when a pair is missing | `pairs.go:144-148,206-219` (25 s timeout, once per `ApplyPairs`) | matches | |
| 9 | cascade of re-releases: `control` v0.2.0→v0.4.0 with the same schema, caused by `pkg/*` bumps | `git diff pairs/control/v0.2.0 pairs/control/v0.4.0 -- pairs/control/proto pairs/control/manifest.json` is empty; same for `ping` v0.1.0→v0.3.0. `control/v0.4.0`, `ping/v0.3.0`, `dispatcher/v0.5.0` share commit `a68e565` | matches | I checked the proto and manifest diff, not the generated code |
| 10 | `replace` directives in released modules are ignored by consumers, so `dev` resolves `pkg/tetherapp` from its own `require` | every internal dep is `v0.0.0` + `replace` (`pairs/control/go.mod`) | matches (this side) | consistent with the claim: the released `v0.0.0` requirement can't resolve without the replace. What `dev` does is **unclear**. Note `pairs/control/gen` imports only protobuf; `pkg/tetherapp` is a `go.mod` requirement of the pair but not of `gen/` (not verified beyond the module file) |
| 11 | one contract, three versions (compiled into `dev`, VM `control` via sync, Mac `control` asset); strict hash check | hash check `schema.go:73-76`, `gateway.go:89-98,110-113,148-160`; version differences only warn (`gateway.go:121-123`) | matches (VM/Mac side) | the compile-time copy in `dev` is invisible here. The hash covers the schema; whether it covers comments is untested (audit §3.6) |
| 12 | arm64 leftovers in `.release` `TARGETS` | `proj`, `scratch`, `unwrapped` still list `linux/arm64`; `build.sh:48` default is `linux/arm64` | matches (partly) | I can't tell whether arm64 is a leftover or wanted for local use. The default in `build.sh` is the clearer leftover |
| 13 | repeated helpers: unix-socket HTTP clients, 0600 token files, container-IP lookup, `/healthz` polling | unix HTTP clients: `tetherapp/client.go:26`, `desk/vm/main.go:76`, `dispatcher/pairs.go:390`, `tether-gw/main.go:90`; 0600 atomic writes: `dispatcher/pairs.go:269-275`, `forwards.go:111`, `desk/vm` temp-file answer write (`:241`), `agentic/run.go:65-71`, `spool.go:56-80`; `/healthz` client: `pairs.go:386-404` (one); container IP: one | matches (partly) | the first two are repeated; container-IP and `/healthz` polling each have a single implementation here |
| 14 | no tests anywhere | zero `*_test.go` | matches | |
| 15 | no README or docs | `README.md` exists (95 lines): layout, tool table, `agentic` section, install and release instructions; `docs/` did not exist before the audit | diverged | The README documents tools and the release flow but not the contracts with devbox (ports, paths, tokens). Those are only in code and `control.proto` |

## Things the expected document missed

| # | Expected | Actual (file:line) | Verdict | Why |
| --- | --- | --- | --- | --- |
| X3 | (nothing) | `tools/unwrapped` is released but **absent from `manifest.json`** (`manifest.json:3-16`; `plan.sh:23`) | extra | CLAUDE.md says a new tool needs an entry |
| X4 | (nothing) | `.claude/CLAUDE.md` is git-ignored and untracked (`.gitignore:3`) | extra | |
| X5 | (nothing) | reserved ports, max forwards (10 in `pkg/dispatch/forward.go:15`, 32 in `control.proto:41`, `controlfwd.go:117`) disagree | extra | |
| X6 | (nothing) | `ApplyPairs` does not reject a pair named `control` although the proto says it must (`pairs.go:91-109`, `control.proto:68,76`); it would reuse the control gateway's state directory (`control.go:55`, `pairs.go:76`) | extra | untested |
| X7 | (nothing) | dead constants `MacDialPort`, `MacExecPort` (`pkg/dispatch/dispatch.go:20-21`) | extra | |
| X8 | (nothing) | hard-coded personal values: `/home/sam` (`unwrapped/main.go:31`), owner `sam`, group `work` (`desk/vm/main.go:49-50`), "denied by Sam" (`agentic/hook.go:75`) | extra | |
| X9 | (nothing) | `agentic` lets a push through if the hook's JSON can't be read (`hook.go:38-42`); the spool wait never times out by itself (`spool.go:84`; only the 7-day hook timeout, `run.go:17`) | extra | |
| X10 | (nothing) | `install.sh` can't install pairs; `release.sh` uses `--latest=false` (`release.sh:27`) so GitHub has no "latest" release and consumers must sort tags | extra | |
| X11 | (nothing) | CI runs only on push to `main` (`release.yml:3-6`) and only for changes under `tools/`, `pkg/`, `pairs/` outside `.md`; scripts/manifest/workflow edits don't release | extra | |
| X12 | (nothing) | a failed module in the matrix doesn't stop the others (`release.yml:30`) | extra | one push can publish some modules and not others |
| X13 | (nothing) | `tether-gw` refuses to start from the source tree because the committed pair `manifest.json` has no `version` or `schema.sha256` (`schema.go:63,75`; stamped at `build.sh:23`) | extra | `fakemac` also needs a packaged directory (`fakemac/main.go:32`) |
| X14 | (nothing) | `pkg/dispatch/go.mod` requires `pkg/tether` only for an example (`examples/recv/main.go`) | extra | |
| X15 | (nothing) | `dispatch ports` reads only the legacy forward registry, while `list_forwards` reads the Mac's (`main.go:221-231`), so the two can disagree | extra | |
