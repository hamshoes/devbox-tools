# devbox-tools

The tools for **hq**: small tools for working on an hq box from a Mac (the `hq` client): project and scratch-folder helpers, the box half of the control plane, the tether pairs that link the box and the Mac, and a wrapper that keeps agent runs on a leash. In code the remote side is just "the box" and the Mac binary "the client"; the product name stays out of it.

It is a Go workspace (`go.work`) of independent modules. Each tool, pair and library is versioned and released on its own.

## Layout

| Path | What lives there |
| --- | --- |
| `tools/` | Single-binary commands |
| `pairs/` | Two-sided apps (a VM half and a Mac half) that talk over tether, defined by a protobuf schema |
| `pkg/` | Libraries shared by the above |
| `scripts/` | Build, install and release scripts |
| `manifest.json` | Which tools and pairs the box installs: `box`, optional `init: "zsh"`, and optional `service: {args, autostart}` for a tool that runs as a service |

### Tools

| Tool | Purpose |
| --- | --- |
| `proj` | Create projects and jump between them |
| `scratch` | Create and manage throwaway folders |
| `agentic` | Wrap agent runs: log every Bash command and hold each `git push` for approval on the Mac (see below) |
| `controld` | The VM half of the control plane: runs the control gateway, the pairs the Mac asks for and the services, watches ports, serves `bb` on a local socket and the status page on `:8080` |
| `bigbrother` (`bb`) | Short-lived client for the control plane and the pairs: `forward`, `unforward`, `ports`, `svc`, `status`, `open`, `clip`, `send`, `notify` |
| `tether-gw` | Generic gateway for one tether pair on one side: apps call it over a unix socket, and it relays checked calls to its peer gateway |
| `tetherping` | Example of an optional tool: serves `GET /ping`; not deployed |

### Pairs

| Pair | Purpose |
| --- | --- |
| `control` | The control plane: pairs, services, forwards and ports. Its VM side is `controld`, its Mac side is the client (`hq`) |
| `notify` | Surfaces things to the Mac: notifications, approval dialogs for VM asks, and an approval socket for the other Mac pair apps |
| `open` | `bb open`: open an http(s) URL or a file on the Mac, once allowed there |
| `clip` | `bb clip`: put stdin on the Mac clipboard, once allowed there |
| `send` | `bb send`: save a file under `~/Downloads/box/` on the Mac, once allowed there |

### Libraries

| Package | Purpose |
| --- | --- |
| `pkg/tether` | Typed, fire-and-forget JSON messages over a WebSocket between one server and one client |
| `pkg/tetherapp` | What a pair app links against: a client for its `tether-gw`, a server for the calls the gateway forwards, and `chunk` for sending files as a series of calls |
| `pkg/platform` | The box platform constants (`BOX_*`: ports, paths, spool), read from the environment, then `/etc/box/platform.env`; plus the optional `BOX_CLIENT_NAME`, how messages name the client (default "the client") |

## Restricting agent runs with `agentic`

`agentic` does not sandbox the agent. It adds two things on top of Claude Code's own permissions:

- An **audit log** of every Bash command the agent runs, and its result.
- An **approval gate on `git push`**. The push is held until someone approves it on the Mac, and anything other than an explicit approval is a denial. If the pushed commits add or change env-like files (`.env`, `.env.*`, `*.env`, `env.example` and the like, examples included), a second dialog lists them, and it must be approved too.

```sh
agentic run -- claude          # run Claude Code with the hooks installed
agentic pending                # asks waiting for an answer
agentic cancel <id> | --all | --run <run_id>
agentic log [--run R] [-f]     # the audit trail
```

How it works:

1. `agentic run` writes a settings overlay to `~/.config/agentic/claude-settings.json` and passes it to `claude` with `--settings`. The overlay registers `PreToolUse`, `PostToolUse` and `PostToolUseFailure` hooks for the `Bash` tool; they merge with your own hooks. If the overlay can't be written, the run fails rather than starting without the gate.
2. The pre-hook logs the command, then parses it as bash and looks for `git push`. It sees through lists, pipes, subshells, command substitutions, `sudo`/`env`/`command`/`exec`/`nohup`/`time`/`nice`, quoting tricks (`g\it`), git global options (`-C`, `-c`, ...), and nested `sh -c` / `eval` strings up to three levels deep. When it can't tell what will run (unparseable text, or a variable in command position) but `git` and `push` both appear, it asks anyway.
3. For a push it submits an ask to the spool (`$BOX_NOTIFY_SPOOL`) describing the repo, branch, remote, commits ahead, a diffstat and any force-push or branch-delete flags, then waits. The `notify` pair carries the ask to the Mac and the answer back.
4. If the push is allowed, it lists the files the pushed commits add or change, commit by commit (so a file added and later deleted still counts), against the upstream or, with none, against every remote. Env-like files, or a list that can't be worked out, mean a second ask.
5. The hook is registered with a 24-hour timeout and denies with "approval timed out" a minute before it, because Claude Code lets a call through when a hook times out. Interrupting the hook (Ctrl-C, SIGTERM) withdraws the dialog from the Mac and denies.
6. A hook payload it can't read blocks the call with an error the agent sees, rather than letting it through.
7. Commands that are not pushes get no decision from the hook, so Claude Code's normal permission rules apply. The hook only ever adds restrictions.

Limits:

- Only the `Bash` tool is covered. A push made through another tool, an MCP server, or a script the agent writes and then runs is not seen.
- `eval` with a non-literal argument, and git options the parser doesn't know, are not detected.
- A missing spool denies the push rather than letting it through.

## Installing

Releases are published to GitHub. Install one with:

```sh
scripts/install.sh [--os darwin|linux] [--arch arm64|amd64] [--dir <path>] <tool> [version]
```

It defaults to this machine's OS and architecture, and installs into `~/.local/share/devtools/bin` on Linux (the box) or `~/.local/bin` on macOS.

## Development

```sh
go build ./...                       # from within a module; the workspace wires local modules together
scripts/build.sh <tool|pair|pkg> <name> <version>   # cross-compile and package into dist/
scripts/pair-generate.sh <name>      # regenerate pairs/<name>/gen from its protobuf schema
```

Adding a tool or pair: create its module under `tools/` or `pairs/`, add it to `go.work`, and list it in `manifest.json`. A pair also needs its own `manifest.json` and a `proto/` schema; its VM half builds for `linux/amd64` and its Mac half for `darwin/arm64`. A tool's `.release` file can set `TARGETS`; the default is `linux/amd64`, the VM's architecture.

Platform values (ports, paths, the spool) are never written here: programs read their names through `pkg/platform`, and the devbox repo owns the values.

## Releasing

Pushes to `main` that touch `tools/`, `pkg/` or `pairs/` (other than Markdown files) run the `release` workflow. `scripts/plan.sh` works out which modules changed since their last release and their next versions, and each is tagged `<prefix>/<version>`. Changes bump the patch version; add a `Release-As: minor` or `Release-As: major` trailer to a commit to bump higher. A module with no tag yet starts at `v0.1.0`.
