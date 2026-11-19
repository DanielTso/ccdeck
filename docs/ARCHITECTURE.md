# ccdeck architecture

`ccdeck` is a terminal dashboard for Claude Code sessions. It is a companion to
Claude Code, not a replacement: it reads the files Claude Code already keeps
under `~/.claude`, shows every session across every project, follows the live
ones, and hands the terminal to `claude` when you want to resume one. Chatting
happens in Claude Code.

Module `github.com/DanielTso/ccdeck`, binary `ccdeck`, Go 1.27, Linux first
(WSL2 and Windows Terminal are the primary target), cross-compiles to macOS.

## What it must do

1. **Session browser.** Every session in every project: filter, sort, group by
   project, search past prompts, preview, read the transcript, resume or fork.
2. **Live usage.** For running sessions: status (busy, idle, waiting and what
   for), model, how full the context window is, tokens, cost, burn rate.
   Plus totals per day.
3. **Multi-session view.** A grid of panes, each following one live session's
   conversation as it happens, with quick switching and a jump to the session.
4. **Custom look and layout.** Themes, layout presets and key bindings, all
   overridable from a config file.

Non-goals for now: sending prompts into a session, approving permissions,
embedding a terminal emulator, any network access, hooks or status-line helpers
installed into Claude Code.

## Hard rules

- **Read-only under the Claude directory.** Never create, modify or delete
  anything there.
- **Never open `sessions/*.key`** (they hold a peer secret) and never touch the
  sockets under `/run/user/*/cc-socks`.
- **Never surface `attachment`, `api-request`, `api-request-blob`,
  `api-request-shape`, `session-owner` lines.** They are prompt plumbing and
  contain account details. Skip them while parsing.
- **No network.** No telemetry, no price fetching.
- **The cache holds derived numbers only**, never conversation text, and is
  written with mode 0600 under `$XDG_CACHE_HOME/ccdeck` (default
  `~/.cache/ccdeck`).
- **The on-disk formats are undocumented and will change.** Decode tolerantly:
  unknown line types and unknown fields are ignored, a malformed line is
  skipped and counted, and nothing panics on surprising input.
- **No hardcoded prices.** They go stale. See "Cost".

## Packages

```
main.go              thin: calls cmd.Execute
cmd/                 cobra commands: root (the TUI), list, show, doctor, version
internal/core/       FROZEN contract: Session, Entry, Store, Launcher. Stdlib only.
internal/claude/     parsers for everything under ~/.claude. No UI imports.
internal/index/      core.Store implementation: scan, cache, watch, live updates
internal/demo/       core.Store with synthetic, moving data (ccdeck --demo, tests)
internal/config/     config file: theme, layout, keys, resume mode, prices
internal/launch/     core.Launcher implementation: suspend-and-exec, tmux
internal/tui/        Bubble Tea v2 application
```

Dependency direction, strictly: `core` ← `claude` ← `index`; `core` ← `demo`;
`core` ← `launch`; `core` ← `tui`; `config` imports nothing internal; `cmd`
wires them together. `tui` must not import `claude`, `index`, `config` or
`launch`: it receives a `core.Store`, a `core.Launcher` and a plain
`tui.Options` value. This is what lets the packages be built in parallel and
tested with fakes.

`internal/core/core.go` is already written. Read it first. Do not change it; if
you believe it is wrong, say so in your report instead.

## Libraries (verified to exist, 2026-11-18)

```
charm.land/bubbletea/v2        v2.1.2
charm.land/lipgloss/v2         v2.1.1
charm.land/bubbles/v2          v2.2.1
github.com/fsnotify/fsnotify   v1.10.1
github.com/sahilm/fuzzy        v0.1.3
github.com/spf13/cobra         (same version as ~/cli_projects/go_cli)
github.com/BurntSushi/toml     latest
```

Use the `charm.land/...` v2 import paths, never `github.com/charmbracelet/bubbletea`
(frozen v1). The v2 upgrade guides are in the module cache
(`UPGRADE_GUIDE_V2.md` in bubbletea, lipgloss and bubbles): read them before
writing UI code. The differences that bite:

- `View()` returns `tea.View` (build with `tea.NewView(s)`); alt screen and
  mouse are fields on it (`v.AltScreen = true`, `v.MouseMode = ...`), not
  program options.
- Keys arrive as `tea.KeyPressMsg`; match on `msg.String()`; space is `"space"`.
- Mouse is split into `MouseClickMsg`, `MouseWheelMsg` and friends.
- `tea.ExecProcess(cmd, callback)` still suspends the program and hands the
  terminal to a child.
- Lip Gloss v2 has no renderers; light/dark comes from
  `tea.RequestBackgroundColor` and `lipgloss.LightDark`.

## On-disk formats (observed, Claude Code 2.1.328)

The Claude directory is `--claude-dir`, else `$CLAUDE_CONFIG_DIR`, else
`~/.claude`. Everything below is relative to it.

### `projects/<slug>/<sessionId>.jsonl` — transcripts

Append-only JSON Lines. A single line can exceed 400 KB: read with
`bufio.Reader.ReadBytes('\n')`, never `bufio.Scanner` with its default limit.
A line is complete only when it ends in `\n`; keep the byte offset of the last
complete line and resume from there.

The slug is the working directory with `/`, `_` and `.` turned into `-`; it
cannot be decoded reliably. Take the project directory from the first `cwd`
seen in the file (later ones drift when the session `cd`s around).

Lines have a top-level `type`:

| type | use |
|---|---|
| `user` | A human prompt when `message.content` is a string and `isMeta` is absent. A tool result when `message.content` is an array of `{type:"tool_result", tool_use_id, content, is_error}` (`content` is a string or an array of blocks). Slash commands show up as text containing `<command-name>/x</command-name>`. |
| `assistant` | Exactly one content block per line in `message.content[0]`: `thinking`, `text`, or `tool_use {id, name, input}`. `message.id`, `message.model`, `message.usage`, `message.stop_reason`. |
| `ai-title` | `aiTitle`. Re-appended every turn: the last one wins. |
| `custom-title` | `customTitle`. Beats `aiTitle`. (Seen in the binary, not in data.) |
| `last-prompt` | `lastPrompt`. Last one wins. |
| `cost-state` | Written when a session ends: `totalCostUSD`, `modelUsage{<model>:{inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens, costUSD,…}}`. Often 0 on subscription plans. |
| `system` | `subtype` `turn_duration` (`durationMs`), `local_command`, `compact_boundary` (`compactMetadata.preTokens`; seen in the binary, not in data). |
| everything else | Ignore. In particular the plumbing types listed under "Hard rules". |

Common fields on `user`, `assistant` and `system` lines: `uuid`, `parentUuid`,
`timestamp` (RFC 3339 UTC with milliseconds), `cwd`, `gitBranch`, `version`,
`sessionId`, `isSidechain`. Roughly 40% of lines (the metadata ones) have no
timestamp.

**Usage** is at `message.usage`: `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`,
`cache_creation.{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`,
`output_tokens_details.thinking_tokens`, `server_tool_use.web_search_requests`.
When the `cache_creation` object is absent, count all of
`cache_creation_input_tokens` as 5-minute writes.

**Count usage once per `message.id`.** One API response is split over up to
three lines (thinking, text, tool_use) and every one of them repeats the same
final usage. Summing per line triples the numbers.

**Context tokens** = `input_tokens + cache_read_input_tokens +
cache_creation_input_tokens` of the latest main-thread assistant message.

### `projects/<slug>/<sessionId>/subagents/agent-<agentId>.jsonl`

Same format, `isSidechain: true`, `sessionId` is the parent's. Beside it,
`agent-<agentId>.meta.json` has `agentType`, `description`, `model`,
`toolUseId`. Subagent usage counts toward the parent session's `Usage` (by
model) and fills `Session.Subagents`; it does not affect `ContextTokens`.

### `sessions/<pid>.json` — live session registry

One file per running Claude Code process:

```json
{"pid":63709,"sessionId":"…","cwd":"/path","startedAt":1795058779656,
 "procStart":"962684","version":"2.1.328","kind":"interactive",
 "name":"cli-ui-19","status":"busy","waitingFor":"approve Bash: …",
 "updatedAt":1795059115666,"statusUpdatedAt":1795059115666}
```

Times are epoch milliseconds. `status` is `busy`, `idle` or `waiting` (with
`waitingFor`); map anything else to `core.StatusUnknown`. The file is rewritten
whenever the status changes, so watch the directory.

A registry file can outlive its process. It is live only if `/proc/<pid>`
exists **and** field 22 of `/proc/<pid>/stat` (start time) equals `procStart`.
Parse the stat line from the last `)` because the command name may contain
spaces and parentheses. Put the check behind an interface so tests can fake it,
and behind build tags so non-Linux builds fall back to "the pid exists".

A live session may have no transcript yet (nothing typed): still list it.

### Other files

- `history.jsonl`: one line per submitted prompt, `{display, timestamp (epoch
  ms), project (cwd), sessionId}`. The source for prompt search.
- `cache/model-catalog/*.json`: `.catalog.config.models[]` with `id` and
  `runtime.max_input_tokens`. The source for context-window sizes. Match the
  transcript's model id exactly, then by prefix. Unknown model → window 0, and
  the UI shows the token count without a percentage.
- `~/.claude.json` (in `$HOME`, or inside the Claude directory when
  `CLAUDE_CONFIG_DIR` is set): `additionalModelCostsCache` maps model ids to
  `{inputTokens, outputTokens, promptCacheWriteTokens, promptCacheWrite1hTokens,
  promptCacheReadTokens, webSearchRequests}` in dollars per million tokens
  (per request for web searches). Read only that key.

## Cost

In order: (1) `cost-state.totalCostUSD` when present and above zero →
`CostReported`; (2) tokens × prices → `CostEstimated`, where prices come from
`[prices]` in the config file, then `additionalModelCostsCache`; (3) otherwise
`CostNone`, and the UI shows tokens only. Estimated figures are always
labelled as estimates in the UI.

## `internal/claude`

Pure functions and small types over the formats above. No goroutines, no
watching. Roughly:

- `Paths` — resolves the directory layout; lists transcript and registry files.
- `Tailer` — incremental reader for one transcript: remembers its offset, holds
  back a partial last line, resets if the file shrank, and feeds complete lines
  to an accumulator.
- `Accumulator` — folds lines into a `core.Session` summary (deduplicating
  usage by message id) and, on request, into `[]core.Entry`. Its numeric state
  must be serialisable so the index can cache it and continue from an offset.
- `ReadRegistry` — registry files → live info, with the liveness check.
- `ReadHistory`, `ReadCatalog`, `ReadPrices`.

Entry text rules: prompts and assistant text verbatim; tool_use as a one-line
summary of the input (the command for Bash, the path for file tools, the
description for Agent, otherwise compact JSON truncated to ~200 runes);
tool_result truncated to ~2,000 runes; thinking as a marker with whatever text
is present.

## `internal/index`

Implements `core.Store`.

- **Start-up:** list transcripts; for each, reuse the cached accumulator state
  when path, size and mtime allow (continue from the cached offset if the file
  only grew), otherwise parse from the start. Parse in a bounded worker pool.
  First paint must not wait for a full parse of large histories: publish
  sessions as they complete.
- **Watching:** fsnotify on `projects/`, each project directory, each
  `<sessionId>/subagents/` directory, `sessions/`, and `history.jsonl`'s parent.
  Watches are not recursive: add new directories on `Create` and rescan them
  after adding the watch. Debounce bursts (~100 ms). A slow poll (a few
  seconds) is the safety net for missed events and for noticing dead pids.
- **Entries** are not kept in memory for every session: parse on demand, with a
  small LRU for the sessions currently on screen, extended incrementally as the
  file grows.
- **Subscribers** get buffered, coalesced, non-blocking events.

## `internal/demo`

A `core.Store` with a dozen invented sessions across invented projects, some
live and changing on a timer (status flips, tokens climbing, entries
appending). Deterministic given a seed and a clock, so views can be tested and
screenshots reproduced. No real names, paths or prompts from this machine.

## `internal/launch`

Implements `core.Launcher`. Resume is `claude --resume <id>` (plus
`--fork-session` to fork) with the working directory set to
`Session.ProjectDir`; new is `claude` in a directory.

Modes (`[resume] mode` in config): `suspend` returns a non-detached launch, and
the UI hands over the terminal with `tea.ExecProcess`; `tmux-window` and
`tmux-split` return detached `tmux new-window|split-window -c <dir> …`
launches; `auto` picks `tmux-window` when `$TMUX` is set and `suspend`
otherwise.

`Focus` works inside tmux only: list panes with their pids
(`tmux list-panes -a -F …`), find the pane whose process tree contains the
session's pid (walk parents through `/proc/<pid>/stat`), and return
`tmux switch-client`/`select-window`/`select-pane` for it.

A session that is already running must not be resumed a second time: the UI
offers Focus, and otherwise explains where the session is running.

The `claude` binary is found on `PATH` (overridable in config). Everything that
shells out goes through one small interface so tests never run tmux or claude.

## `internal/config`

`$XDG_CONFIG_HOME/ccdeck/config.toml` (default `~/.config/ccdeck/config.toml`).
A missing file means defaults; a bad file is a clear error naming the key.
`ccdeck config init` is not needed: `ccdeck doctor` prints the path and an
example.

```toml
theme = "ember"            # a built-in name, or a key under [themes]

[layout]
preview_ratio = 0.55       # share of width for the preview pane
live_panes = 4             # panes in the live grid
group_by_project = false

[resume]
mode = "auto"              # auto | suspend | tmux-window | tmux-split
claude = "claude"

[keys]                     # action = ["key", ...]; unlisted actions keep defaults
quit = ["q", "ctrl+c"]

[themes.mine]              # custom theme: start from a built-in, override tokens
base = "ember"
accent = "#ff8800"

[prices."claude-opus-5-5"] # dollars per million tokens
input = 0.0
output = 0.0
cache_read = 0.0
cache_write_5m = 0.0
cache_write_1h = 0.0
```

The package exposes plain structs and knows nothing about Lip Gloss; `cmd`
translates them into `tui.Options`.

## `internal/tui`

One root model that owns size, theme, key map, the store subscription and the
active view; views are sub-models that receive sessions and render into the
rectangle they are given. Store events arrive through a command that waits on
the subscription channel and re-arms itself. Never block in `Update`; never
call the store's heavier methods (`Entries`, `Daily`, `SearchPrompts`) outside
a `tea.Cmd`.

**Views**

1. **Sessions** (`1`): list on the left, preview on the right. Each row: status
   mark, title, project, age, model, a small context gauge. Preview: metadata,
   usage, cost, subagents, and the tail of the conversation.
   `enter` resume (or focus when live), `f` fork, `n` new session in that
   project, `o` open the full transcript, `/` fuzzy filter, `p` prompt search,
   `s` cycle sort (recent, project, context, cost), `g` group by project,
   `a` cycle scope (all, live, ended).
2. **Live** (`2`): a grid of panes for live sessions, most recently active
   first. Pane header: status badge with `waitingFor`, title, model, context
   gauge, cost. Body: the conversation tail, following. `tab`/`shift+tab` move
   focus, `z` zoom a pane, `+`/`-` change the pane count, `enter` focus the
   session. A session that starts waiting for approval must stand out.
3. **Usage** (`3`): totals for today and the last 7 days, per-model breakdown,
   a per-session table with context gauges and a burn-rate sparkline built
   from `Session.Recent`.
4. **Transcript** (from `o`): full-height scrollable conversation, tool calls
   folded to one line, `/` to search within it, follows when at the bottom.

Global: `?` help overlay listing the active bindings, `T` cycle theme, `q` or
`ctrl+c` quit, `esc` back. Mouse: wheel scrolls, click selects.

**Theme.** A theme is a struct of semantic colour tokens (background, surface,
border, focused border, text, muted text, accent, one colour per status,
error, three gauge steps) from which all styles are derived in one place. No
colour literals in view code. Ship at least four built-ins with original
names, including a light one and a high-contrast one, and pick light or dark
by default from the terminal background. Status must never be carried by
colour alone: pair it with a glyph or word. Degrade cleanly on 256-colour and
no-colour terminals.

**Layout.** Works from 80×24 up. Below ~100 columns the Sessions preview
moves under the list or hides; the Live grid drops to fewer panes. Wide
characters and long titles are truncated with an ellipsis by display width,
never by byte length.

## `cmd`

- `ccdeck` — the TUI. Flags: `--claude-dir`, `--config`, `--theme`, `--demo`.
- `ccdeck list [--json] [--live]` — sessions as a table or JSON (`core.Session`).
- `ccdeck show <id> [--json]` — one session's summary and recent entries. Ids
  may be abbreviated to a unique prefix.
- `ccdeck doctor` — what was found: directories, counts, skipped lines, parse
  time, live sessions, tmux, config path, colour profile. The first thing to
  run when Claude Code changes a format.
- `ccdeck version`.

## Conventions

Follow `~/cli_projects/go_cli` (read its `Makefile`, `.golangci.yml`,
`.goreleaser.yaml`, `cmd/root.go`, `.github/workflows/`): `NewRootCmd()`
factory, version and commit injected with ldflags, `make check` = fmt, vet,
lint, test; golangci-lint v2 config; goimports with the module as local prefix.

Tests use the standard library only (`t.Run` tables, `t.TempDir`, `t.Helper`),
live next to the code, and never read the real `~/.claude`. Fixtures are
synthetic and are built to mirror the formats above, including the awkward
cases: a three-line split message, a 500 KB line, a partial trailing line, a
shrunken file, a stale registry file, an unknown line type, a malformed line.
UI logic is tested by driving `Update` with messages and asserting on model
state and on `View().Content`; the experimental `teatest` package is not used.

Toolchains are not on `PATH` in a fresh shell:
`export PATH=$HOME/.local/go/bin:$HOME/go/bin:$HOME/.local/bin:$PATH`.

To look at the running UI without a human, use tmux with a session name unique
to you, and always kill it afterwards:

```sh
tmux new-session -d -s ccdeck-<yourname> -x 140 -y 40 './bin/ccdeck --demo'
tmux send-keys -t ccdeck-<yourname> 2
tmux capture-pane -t ccdeck-<yourname> -p        # add -e to see colours
tmux kill-session -t ccdeck-<yourname>
```
