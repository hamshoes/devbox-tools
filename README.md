# devbox-tools

Small tools for working in a devbox VM from a Mac: project and scratch-folder helpers, a link between the VM and the Mac, and a wrapper that keeps agent runs on a leash.

It is a Go workspace (`go.work`) of independent modules. Each tool, pair and library is versioned and released on its own.

## Layout

| Path | What lives there |
| --- | --- |
| `tools/` | Single-binary commands |
| `pairs/` | Two-sided apps (a VM half and a Mac half) that talk over tether, defined by a protobuf schema |
| `pkg/` | Libraries shared by the above |
| `scripts/` | Build, install and release scripts |
| `manifest.json` | Which tools and pairs get released, and for which side (`devbox`, `mac`) |

### Tools

| Tool | Purpose |
| --- | --- |
| `proj` | Create projects and jump between them |
| `scratch` | Create and manage throwaway folders |
| `agentic` | Wrap agent runs: log every Bash command and hold each `git push` for approval on the Mac (see below) |
| `dispatcher` | Push exec events to the Mac over a tether server, fed by `dispatch` over a unix socket |
| `dispatch` | Ask the local dispatcher to run an action on the Mac; invoked as `xdg-open`, it opens a URL there |
| `tether-gw` | Generic gateway for one tether pair on one side: apps call it over a unix socket, and it relays checked calls to its peer gateway |
| `tetherping`, `tetherlog` | Test server and log viewer for a tether link |
| `unwrapped` | Web dashboard of the VM's status |

### Pairs

| Pair | Purpose |
| --- | --- |
| `control` | Forwards and ports control plane |
| `desk` | Approvals: the VM asks, the Mac answers |
| `ping` | Link check |

### Libraries

| Package | Purpose |
| --- | --- |
| `pkg/tether` | Typed, fire-and-forget JSON messages over a WebSocket between one server and one client |
| `pkg/tetherapp` | What a pair app links against: a client for its `tether-gw` and a server for the calls the gateway forwards |
| `pkg/dispatch` | The exec events and local socket protocol shared by `dispatcher` and `dispatch` |

## Restricting agent runs with `agentic`

`agentic` does not sandbox the agent. It adds two things on top of Claude Code's own permissions:

- An **audit log** of every Bash command the agent runs, and its result.
- An **approval gate on `git push`**. The push is held until someone approves it on the Mac, and anything other than an explicit approval is a denial.

```sh
agentic run -- claude          # run Claude Code with the hooks installed
agentic pending                # asks waiting for an answer
agentic cancel <id> | --all | --run <run_id>
agentic log [--run R] [-f]     # the audit trail
```

How it works:

1. `agentic run` writes a settings overlay to `~/.config/agentic/claude-settings.json` and passes it to `claude` with `--settings`. The overlay registers `PreToolUse`, `PostToolUse` and `PostToolUseFailure` hooks for the `Bash` tool; they merge with your own hooks. If the overlay can't be written, the run fails rather than starting without the gate.
2. The pre-hook logs the command, then parses it as bash and looks for `git push`. It sees through lists, pipes, subshells, command substitutions, `sudo`/`env`/`command`/`exec`/`nohup`/`time`/`nice`, quoting tricks (`g\it`), git global options (`-C`, `-c`, ...), and nested `sh -c` / `eval` strings up to three levels deep. When it can't tell what will run (unparseable text, or a variable in command position) but `git` and `push` both appear, it asks anyway.
3. For a push it submits an ask to the spool (`/var/lib/devbox-desk`) describing the repo, branch, remote, commits ahead, a diffstat and any force-push or branch-delete flags, then waits, for up to seven days. The `desk` pair carries the ask to the Mac and the answer back.
4. Commands that are not pushes get no decision from the hook, so Claude Code's normal permission rules apply. The hook only ever adds restrictions.

Limits:

- Only the `Bash` tool is covered. A push made through another tool, an MCP server, or a script the agent writes and then runs is not seen.
- `eval` with a non-literal argument, and git options the parser doesn't know, are not detected.
- A missing spool denies the push rather than letting it through.

## Installing

Releases are published to GitHub. Install one with:

```sh
scripts/install.sh [--os darwin|linux] [--arch arm64|amd64] [--dir <path>] <tool> [version]
```

It defaults to this machine's OS and architecture, and installs into `~/.local/share/devtools/bin` on Linux (the devbox) or `~/.local/bin` on macOS.

## Development

```sh
go build ./...                       # from within a module; the workspace wires local modules together
scripts/build.sh <tool|pair|pkg> <name> <version>   # cross-compile and package into dist/
scripts/pair-generate.sh <name>      # regenerate pairs/<name>/gen from its protobuf schema
```

Adding a tool or pair: create its module under `tools/` or `pairs/`, add it to `go.work`, and list it in `manifest.json`. A pair also needs its own `manifest.json` and a `proto/` schema. A tool's `.release` file can set `TARGETS` (for example `linux/amd64`).

## Releasing

Pushes to `main` that touch `tools/`, `pkg/` or `pairs/` (other than Markdown files) run the `release` workflow. `scripts/plan.sh` works out which modules changed since their last release and their next versions, and each is tagged `<prefix>/<version>`. Changes bump the patch version; add a `Release-As: minor` or `Release-As: major` trailer to a commit to bump higher.
