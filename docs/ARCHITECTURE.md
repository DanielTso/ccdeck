# ccdeck architecture

`ccdeck` is a terminal dashboard for Claude Code sessions. It is a companion to
Claude Code, not a replacement: it reads the files Claude Code already keeps
under `~/.claude`, shows every session across every project, follows the live
ones, and hands the terminal to `claude` when you want to resume one. Chatting
happens in Claude Code.

Module `github.com/DanielTso/ccdeck`, binary `ccdeck`, Go 1.27.4 or newer, Linux first
(WSL2 and Windows Terminal are the primary target), cross-compiles to macOS.

## What it must do

1. **Session browser.** Every session in every project: filter, sort, group by
   project, search past prompts, preview, read the transcript, resume or fork.
2. **Live usage.** For running sessions: status (busy, idle, waiting and what
   for), model, how full the context window is, tokens, cost, burn rate.
   Plus totals per day.
3. **Multi-session view.** A grid of panes, each following one live session's
   conversation as it happens, with quick switching and a jump to the session.
4. **Custom look and layout.** Themes, layout settings and key bindings, all
   overridable from a config file.

Non-goals for now: sending prompts into a session, approving permissions,
embedding a terminal emulator, any network access, and changing Claude Code's
settings. The status-line meter below is opt-in: ccdeck ships the helper and
documents the one setting that enables it, and never edits that setting itself.

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
  written with mode 0600 (directory 0700) under `$XDG_CACHE_HOME/ccdeck`
  (default `~/.cache/ccdeck`). The only other thing ccdeck writes is the
  status-line meter's state, numbers only, under `$XDG_STATE_HOME/ccdeck`; see
  "Status-line meter".
- **The on-disk formats are undocumented and will change.** Decode tolerantly:
  unknown line types and unknown fields are ignored, a malformed line is
  skipped and counted, and nothing panics on surprising input.
- **No hardcoded prices.** They go stale. See "Cost".

## Packages

```
main.go              thin: calls cmd.Execute
cmd/                 cobra commands: root (the TUI), list, show, doctor, statusline,
                     version; completion is cobra's own
internal/core/       FROZEN contract: Session, Entry, Store, Launcher. Stdlib only.
                     Changed once, for the meter (see "Status-line meter").
internal/claude/     parsers for everything under ~/.claude. No UI imports.
internal/meter/      status-line meter: input decoding, state files. Imports core only.
internal/index/      core.Store implementation: scan, cache, watch, live updates
internal/demo/       core.Store with synthetic, moving data (ccdeck --demo, tests)
internal/config/     config file: theme, layout, keys, resume mode, prices
internal/launch/     core.Launcher implementation: suspend-and-exec, tmux
internal/tui/        Bubble Tea v2 application
```

Dependency direction, strictly: `core` ← `claude` ← `index`; `core` ← `meter`
← `index`; `core` ← `demo`; `core` ← `launch`; `core` ← `tui`; `config`
imports nothing internal; `cmd` wires them together, and imports `meter`
itself to write reports. `tui` must not import `claude`, `index`, `config` or
`launch`: it receives a `core.Store`, a `core.Launcher` and a plain
`tui.Options` value. This is what lets the packages be built in parallel and
tested with fakes.

`internal/core/core.go` is already written. Read it first. Do not change it; if
you believe it is wrong, say so in your report instead. Its types carry `json`
tags, because `ccdeck list --json` prints them.

## Libraries

```
charm.land/bubbletea/v2                  v2.1.2
charm.land/lipgloss/v2                   v2.1.1
charm.land/bubbles/v2                    v2.2.1   key bindings only
github.com/charmbracelet/colorprofile    v0.4.3   colour profile detection
github.com/charmbracelet/x/ansi          v0.11.9  width and truncation of styled text
github.com/fsnotify/fsnotify             v1.10.1
github.com/sahilm/fuzzy                  v0.1.3
github.com/spf13/cobra                   v1.10.2  (same as ~/cli_projects/go_cli)
github.com/BurntSushi/toml               v1.6.0
```

The text inputs are ccdeck's own (`internal/tui/input.go`), not Bubbles': the
clipboard package behind Bubbles' text input searches `PATH` for clipboard tools
when the process starts, which took half a second on WSL, where `PATH` holds
the Windows directories, in every ccdeck process, the status-line helper
included. Paste arrives as the terminal's bracketed paste, routed to the input
that has focus; ctrl+v does not read the system clipboard.

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
| `user` | A human prompt when `message.content` is a string (or text and image blocks) and `isMeta`, `isCompactSummary` and a non-human `origin.kind` are absent; text that opens with a plumbing tag (`<system-reminder>`, `<task-notification>`, …) or `[Request interrupted` is not a prompt. A tool result when `message.content` is an array of `{type:"tool_result", tool_use_id, content, is_error}` (`content` is a string or an array of blocks). Slash commands show up as text containing `<command-name>/x</command-name>`, shell escapes as `<bash-input>`; both are shown as commands. |
| `assistant` | Exactly one content block per line in `message.content[0]`: `thinking`, `text`, or `tool_use {id, name, input}`. `message.id`, `message.model`, `message.usage`, `message.stop_reason`. |
| `ai-title` | `aiTitle`. Re-appended every turn: the last one wins. |
| `custom-title` | `customTitle`. Beats `aiTitle`. (Seen in the binary, not in data.) |
| `last-prompt` | `lastPrompt`. Last one wins. |
| `cost-state` | Written when a session ends: `totalCostUSD`, `modelUsage{<model>:{inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens, costUSD,…}}`. Often 0 on subscription plans. Only `totalCostUSD` is read. |
| `system` | `subtype` `turn_duration` (`durationMs`), `local_command`, `compact_boundary` (`compactMetadata.preTokens`; seen in the binary, not in data). Only `compact_boundary` is used: it becomes a "compacted" entry. |
| everything else | Ignore. In particular the plumbing types listed under "Hard rules". |

Common fields on `user`, `assistant` and `system` lines: `uuid`, `parentUuid`,
`timestamp` (RFC 3339 UTC with milliseconds), `cwd`, `gitBranch`, `version`,
`sessionId`, `isSidechain`. Roughly 40% of lines (the metadata ones) have no
timestamp.

**Usage** is at `message.usage`: `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`,
`cache_creation.{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}`,
`output_tokens_details.thinking_tokens`, `server_tool_use.web_search_requests`,
`speed` and `iterations`. When the `cache_creation` object is absent, count all
of `cache_creation_input_tokens` as 5-minute writes. `speed` is `standard` or
`fast`; `iterations` lists the requests a response took (each with the usage
fields above, `type` `message`), and always has one entry in the data seen.
`service_tier` and `inference_geo` are not read.

**Count usage once per `message.id`.** One API response is split over up to
three lines (thinking, text, tool_use) and every one of them repeats the same
final usage. Summing per line triples the numbers. A later line of the message
replaces the earlier line's usage, so the message is counted once.

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
 "name":"api-12","status":"busy","waitingFor":"approve Bash: …",
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
- Claude Code's global config file: `<dir>/.claude.json` when the directory
  comes from `--claude-dir` or `CLAUDE_CONFIG_DIR` and that file exists,
  otherwise `~/.claude.json`. `additionalModelCostsCache` maps model ids to
  `{inputTokens, outputTokens, promptCacheWriteTokens, promptCacheWrite1hTokens,
  promptCacheReadTokens, webSearchRequests}` in dollars per million tokens
  (per request for web searches). An entry may also hold `fast`, the same five
  rates for responses served in fast mode, and `longPrompt`, the five rates plus
  `abovePromptTokens` for responses whose prompt is larger than that. Read only
  that key.

## Cost

In order: (1) `cost-state.totalCostUSD` when it is above zero and no assistant
message follows it → `CostReported`. The total covers the session as it ended:
once the session is resumed and answers again, the figure is estimated again;
(2) with the status-line meter on, Claude Code's own figure from a report at
least as new as the latest reply → `CostClaude`; (3) tokens × prices →
`CostEstimated`, where prices come from `[prices]` in the config file, then
`additionalModelCostsCache`; (4) otherwise `CostNone`, and the UI shows tokens
only. Everything but `CostReported` is labelled as an estimate in the UI.

**Price tiers.** Usage is filed under a usage key, not a bare model id: the
model id, then `+fast` when the response's `speed` is `fast`, then `+long` when
its prompt (input + cache reads + cache writes) is strictly above the model's
`longPrompt.abovePromptTokens`: `m`, `m+fast`, `m+long`, `m+fast+long`. A speed
other than `standard` or `fast` files under `m+unknown`. `Session.Usage` and
`DayUsage.Usage` are keyed this way; `Session.Model` stays the plain id. Prices
are keyed the same way and matched by exact key, then by prefix of the model
id. A tier with no price of its own has none: it never falls back to the base
rate, and a session that used it shows tokens only. So `+fast+long` and
`+unknown` have a price only when `[prices]` gives one. A long response is
priced entirely at the long rates, output included, web searches excepted; one
whose `iterations` list several requests adding up to its totals is judged
request by request, as Claude Code does. Which responses are long depends on
the thresholds, so they are part of the cache's validity: a cache folded under
other thresholds is ignored, and a running store reads every transcript again
when they change. Not modelled: the 10% Claude Code adds for `inference_geo`
`us`, and Claude Code's use of the 5-minute rate for 1-hour cache writes when a
price has no 1-hour rate (ccdeck counts them at 0).

## `internal/claude`

Pure functions and small types over the formats above. No goroutines, no
watching. Roughly:

- `Paths` — resolves the directory layout, including which global config file
  holds the prices; lists transcript and registry files.
- `Tailer` — incremental reader for one transcript: remembers its offset, holds
  back a partial last line, resets if the file shrank or, by a fingerprint of
  what precedes the offset, was replaced rather than grown, and feeds complete
  lines to a `Sink`.
- `Accumulator` — a `Sink` that folds lines into a `core.Session` summary
  (deduplicating usage by message id and filing it by price tier). Its numeric
  `State` is serialisable so the index can cache it and `RestoreAccumulator`
  continue from an offset; it includes when the latest reply arrived
  (`ContextAt`), which the meter's override rule compares against.
- `Conversation` — the other `Sink`: lines into `[]core.Entry`.
- `ReadRegistry` — registry files → live info, with the liveness check.
- `ReadHistory`, `ReadCatalog` (`Window` ignores a tier suffix), and
  `ReadPrices(configFile) (Prices, Thresholds, error)`: tier rates under
  suffixed keys, and the long-prompt thresholds.

Entry text rules: prompts and assistant text verbatim; tool_use as a one-line
summary of the input (the command for Bash, the path for file tools, the
description for Agent, otherwise compact JSON truncated to ~200 runes);
tool_result truncated to ~2,000 runes; thinking as a marker with whatever text
is present.

## `internal/index`

Implements `core.Store`. Options: the Claude directory, the cache directory,
price overrides (the config's `[prices]`) and the meter directory, where empty
means the meter is off, so tests and `--demo` never touch the real state
directory.

- **Start-up:** list transcripts, live sessions first, then the most recently
  modified; for each, reuse the cached accumulator state when path, size and
  mtime allow (continue from the cached offset if the file only grew), otherwise
  parse from the start. The cache is ignored when its format version, state
  version, Claude directory or long-prompt thresholds differ. Parse in a
  bounded worker pool. First paint must not wait for a full parse of large
  histories: publish sessions as they complete; `Ready` closes when all are.
- **Watching:** fsnotify on the Claude directory, `projects/`, each project
  directory, each `<sessionId>/subagents/` directory, `sessions/`, and the meter
  directory once it exists. Watches are not recursive: add new directories on
  `Create` and rescan them after adding the watch. Debounce bursts (100 ms). A
  poll every 2 seconds notices dead pids and registry and meter changes; every
  30 seconds a sweep rereads the price cache and model catalog and revisits
  every transcript, which is the safety net for missed events (and the only
  mechanism when fsnotify is unavailable). The sweep also deletes old meter
  files.
- **Entries** are not kept in memory for every session: parse on demand, with a
  small LRU of conversations (24) for the sessions currently on screen,
  extended incrementally as the file grows.
- **Daily** usage comes from per-session usage kept in 15-minute buckets, so a
  day's totals are right in whatever time zone `Daily` runs; its cost is an
  estimate, and 0 unless every model used that day has a price.
- **Subscribers** get buffered, coalesced, non-blocking events.

## `internal/demo`

A `core.Store` with a dozen invented sessions across invented projects, some
live and changing on a timer (status flips, tokens climbing, entries
appending). Two of them carry meter figures (cost kind `claude`) and the store
reports a 5-hour and a 7-day rate-limit window, so the meter's UI shows in
`--demo`. Deterministic given a seed and a clock, so views can be tested and
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
offers Focus, and otherwise explains where the session is running. When ccdeck
reads a Claude directory other than the default `~/.claude` (from `--claude-dir`
or `CLAUDE_CONFIG_DIR`), `claude` is started with `CLAUDE_CONFIG_DIR` set to it,
in every mode, so it finds the same sessions and account.

The `claude` binary is found on `PATH` (overridable in config). Everything that
shells out goes through one small interface so tests never run tmux or claude.

## `internal/config`

`$XDG_CONFIG_HOME/ccdeck/config.toml` (default `~/.config/ccdeck/config.toml`;
`XDG_CONFIG_HOME` counts only when it is an absolute path). Only the default
path may be missing, which means defaults: a file named with `--config` that
does not exist is an error. A bad file is a clear error naming the key.
`ccdeck config init` is not needed: `ccdeck doctor` prints the path and an
example.

```toml
theme = "ember"            # a built-in name, or a key under [themes]; unset follows the terminal

[layout]
preview_ratio = 0.55       # share of width for the preview pane
live_panes = 4             # panes in the live grid
group_by_project = false

[resume]
mode = "auto"              # auto | suspend | tmux-window | tmux-split
claude = "claude"

[keys]                     # action = ["key", ...]; unlisted actions keep defaults
quit = ["q", "ctrl+c"]

[themes.mine]              # custom theme: start from a built-in or custom theme
base = "ember"             # override tokens
accent = "#ff8800"

[prices."claude-opus-5-5"] # dollars per million tokens
input = 0.0
output = 0.0
cache_read = 0.0
cache_write_5m = 0.0
cache_write_1h = 0.0
```

A custom theme's `base` is a built-in theme or another custom theme, resolved
through the chain; unset means `ember`. A chain may hold at most five custom
themes (itself included). A cycle, a chain that deep, a base that is neither,
and a custom theme named like a built-in are errors that name the key
(`themes.<name>.base`). `config` can check cycles and depth but does not know
the built-in names, so an unknown base is the UI's error, reported with the
config key. A `[prices]` model id may end in `+fast`, `+long` or `+fast+long`
to price that tier (see "Price tiers"); any other `+` suffix is an error, and a
table replaces the whole price of its key, the fields left out counting as 0.

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
   mark, title, project, age, and as the width allows model and a small context
   gauge; group headers when grouped. Preview: metadata, usage, cost (and
   which source: ccdeck's estimate or Claude Code's), subagents, and the tail
   of the conversation.
   `enter` resume (or focus when live), `f` fork, `n` new session in that
   project, `o` open the full transcript, `/` fuzzy filter, `p` prompt search,
   `s` cycle sort (recent, project, context, cost), `g` group by project,
   `a` cycle scope (all, live, ended).
2. **Live** (`2`): a grid of panes, one per live session, never more panes than
   sessions (`live_panes` wanted, `+`/`-` change it, 1 to 16). A pane keeps its
   session for as long as the session exists, live or ended, so panes never
   trade places; a live session without a pane takes a free one, else the pane
   of an ended session (never the focused one), waiting sessions first. Live
   sessions that still do not fit are listed in a strip under the grid. A
   smaller window hides panes but never the focused one, and they come back
   where they were when it grows. Pane header: status badge and how long it has
   held, title, project, model, context gauge, cost. Body: the conversation
   tail, following; scrolling a pane (keys or wheel) holds it still until it is
   back at the bottom. `tab`/`shift+tab` move focus, `z` zooms a pane,
   `enter` focuses the session (resumes it when ended), `esc` unzooms and then
   goes back to Sessions. A session that starts waiting for approval must stand
   out: it gets a pane first, a double border, a `WAITING` badge and what it
   waits for. With nothing live, one message says so.
3. **Usage** (`3`): totals for today and the last 7 days, the account's rate
   limits when the meter reports them, a bar per day, a per-model breakdown (a
   price tier is its own row: `opus 5.5 fast`), and a per-session table, live
   sessions first, with context gauges, a burn-rate sparkline built from
   `Session.Recent`, tokens per hour, fresh tokens and cost. Daily figures are
   estimates from tokens, never the reported totals.
4. **Transcript** (from `o`): full-height scrollable conversation, tool calls
   and thinking folded to one line (`tab`/`shift+tab` select one, `enter` or
   `space` unfolds it, `e` all), `/` to search within it with `n`/`N` for the
   next and previous match, follows when at the bottom.

Global: `?` help overlay: the global and move bindings plus those of the current
view, as bound now (config overrides included); `?` or `esc` closes it, and `T`
and `q` still work while it is open. `T` cycles the theme (built-ins first, then
custom ones, and stops following the terminal background), `q` or `ctrl+c`
quits (`ctrl+c` even when `quit` is rebound), `esc` goes back: from Transcript
to where it was opened, from Live and Usage to Sessions, and it clears a
filter. The terminal title is `ccdeck · N waiting` while any session waits.
Mouse: wheel scrolls, click selects (a click on a tool call folds it).

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

- `ccdeck` — the TUI. Flags, shared by every command except `statusline`, which
  takes none of them: `--claude-dir`, `--config`, `--theme`, `--demo`, and
  `-v`/`--version`. `--theme` means something only to the TUI and is ignored by
  the other commands. `--demo` swaps in the demo store for the TUI, `list` and
  `show`; `doctor` ignores it and says so, because it exists to inspect the real
  directory. A `--config` file that is missing is an error; only the default
  path may be missing.
- `ccdeck list [--json] [--live]` — sessions as a table or JSON (`core.Session`,
  meter report included).
- `ccdeck show <id> [--json] [-n N | --entries N]` — one session's summary and
  recent entries (20 by default, `0` for all). Ids may be abbreviated to a
  unique prefix; an ambiguous or unknown one is an error.
- `ccdeck doctor` — what was found: directories, counts, skipped lines, parse
  time, price tiers in use, live sessions, the status-line meter (whether it is
  feeding, the limits last reported, the setting that enables it), tmux, config
  path, colour profile, and an example config. The first thing to run when
  Claude Code changes a format.
- `ccdeck statusline [--wrap '<command>'] [--wrap-timeout D]` — the meter's
  helper, below. It parses its own arguments and never fails.
- `ccdeck version`, also `ccdeck --version`.
- `ccdeck completion <shell>` — completion script, generated by cobra.

`list`, `show` and `doctor` load the config file for `[prices]`; a bad config is
an error for them as for the TUI.

## Conventions

Follow `~/cli_projects/go_cli` (read its `Makefile`, `.golangci.yml`,
`.goreleaser.yaml`, `cmd/root.go`, `.github/workflows/`): `NewRootCmd()`
factory, version and commit injected with ldflags (a plain `go install` has
none, so the module version and VCS revision from `runtime/debug.ReadBuildInfo`
fill in whatever the flags left unset), `make check` = fmt, vet, lint, test;
`make snapshot` builds the release archives locally; golangci-lint v2 config;
goimports with the module as local prefix.

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

## Status-line meter (opt-in)

Files cannot give three things Claude Code knows: its own context-window
percentage and size, its own running cost figure, and the account's rate-limit
status. Claude Code passes all three to a status-line command on stdin. `ccdeck
statusline` is such a command: it records the numbers for the dashboard and
prints a status line.

The user enables it by adding to Claude Code's `settings.json`:

```json
{ "statusLine": { "type": "command", "command": "ccdeck statusline", "refreshInterval": 30 } }
```

ccdeck never writes this. `ccdeck doctor` reports whether the meter is feeding
(a live session has a report written since it started) and prints the snippet
when no live session is being fed or the helper has never run.

**Input** (documented by Claude Code; every field optional, decode tolerantly):
`session_id`, `model.id`, `model.display_name` (for the printed line only),
`cost.{total_cost_usd,total_lines_added,total_lines_removed}`,
`context_window.{context_window_size,used_percentage,total_input_tokens}`
(`used_percentage` may be null early on and after a compaction),
`rate_limits.{five_hour,seven_day}.{used_percentage,resets_at}` and
`rate_limits.spend_limit.{used_percentage,resets_at,used_usd,limit_usd,period}`
(`resets_at` is epoch seconds; each window may be absent). Numbers are clamped
to a sane range and control characters are dropped from names. Nothing else is
read, and nothing else is stored: in particular no prompt, title, path or model
name.

**`ccdeck statusline`**

- Reads stdin, writes `$XDG_STATE_HOME/ccdeck/meter/<session_id>.json` (default
  `~/.local/state/ccdeck/meter/`), directory 0700, file 0600, temp file then
  rename. The state directory is ccdeck's own, never the Claude directory.
- Prints one line: model, context percentage, cost, and the tightest rate-limit
  window with its reset time; a window or context at 80% or more gets a `!`.
  Claude Code captures the output, so stdout is never a terminal: the line is
  coloured unless `NO_COLOR` is set or `TERM` is `dumb`. Claude Code sets
  `COLUMNS`, so fit it.
- `--wrap '<command>'` runs the user's existing status-line command (`sh -c`,
  the same stdin and environment, its stderr discarded) and prints its output
  instead of ccdeck's, so enabling the meter does not cost them their status
  line. When the command fails, prints nothing, prints more than 64 KB, or runs
  longer than `--wrap-timeout` (default 2s), it is stopped with everything it
  started (its process group) and ccdeck's own line is printed. A command that
  succeeds keeps its background jobs (a status-line script may start a cache
  refresh); one that still holds stdout is not waited for beyond 100 ms, and
  what the command printed by then is its output.
- **It must never disturb the session:** always exit 0, always print a line
  (empty input, malformed JSON, an unwritable state directory and a failing
  wrapped command included), finish in a few milliseconds, never read the
  transcript, never start the index. Claude Code cancels an in-flight run when
  a new one starts, so a kill at any point must leave either the old file or
  the new one, never a partial.
- A `session_id` is used as a file name only after checking it is a plain id
  (letters, digits, `-`, `_`); anything else is dropped.

**`internal/meter`** owns the input decoding, the state file format (versioned)
and reading it back. It imports only `core`. `cmd` uses it to write, `index`
to read.

**Contract additions (`internal/core`)**

```go
// CostClaude: Claude Code's own running figure, from the status-line meter.
// Still an estimate at list price, but it knows the price tiers.
const CostClaude CostKind = "claude"

// Meter is what Claude Code last reported about a session through the
// status-line helper. Nil on Session when the helper is not enabled.
type Meter struct {
	At             time.Time // when the report was written
	ContextPercent float64   // 0–100; valid only when HasContext
	HasContext     bool
	ContextWindow  int64
	ContextTokens  int64
	CostUSD        float64
	LinesAdded     int
	LinesRemoved   int
}

// RateLimit is one usage window of the account.
type RateLimit struct {
	Window      string    // "5h", "7d" or "spend"
	UsedPercent float64   // may exceed 100 for spend
	ResetsAt    time.Time
	UsedUSD     float64   // spend only; 0 when unknown
	LimitUSD    float64
	Period      string    // spend only: daily, weekly, monthly
}
```

`Session` gains `Meter *Meter`. `Store` gains
`Limits() (limits []RateLimit, at time.Time)`: the windows of the newest report
that has rate limits, each with the freshest figure any report gives for it
(`meter.Merge`), with windows whose reset time has passed dropped; `at` is when
the newest report those figures came from was written. Empty when the helper is
not enabled or has never run. A report without limits (a session's first,
written before its first API response) says nothing about the account and does
not blank them. Only the reports of the store's own sessions count (tracked or
live in its Claude directory): the meter directory is shared by every Claude
directory on the machine, and another one may be another account.

The newest report is not the freshest figure. Claude Code sets its rate-limit
figures from API response headers, and with `refreshInterval` every live
process, an idle one included, re-runs the status line and re-stamps the figures
it last saw. So within a window, the figure is the highest use any report gives
for the same reset time (use only rises within a window; for a spend limit the
most dollars, since raising the limit lowers the percentage); a later reset time
is a later window and wins; when either reset time is unknown, the newer report
wins. The core.go comment on `Limits` ("the most recent status-line report")
predates this and is kept, core being frozen.

**How the index uses it.** Watch the meter directory like the registry. When a
session has a meter report at least as new as its latest main-thread assistant
message, the report wins: `ContextTokens` and `ContextWindow` come from it when
it has a context figure and a window size, and `CostUSD` with `CostKind =
CostClaude` when its cost is above zero. An older report is kept on
`Session.Meter` for display (`ccdeck show` prints it; the TUI does not) but
does not override figures computed from the transcript, because the transcript
has moved on. A reported end-of-session cost (`CostReported`) still beats both.
Meter files of sessions that are no longer live and are older than 30 days are
deleted (this is ccdeck's own directory).

**How the UI uses it.** Wherever context and cost are shown, nothing changes
in layout: the figures are simply Claude Code's own when available, and the
Sessions preview says which source is in use. Rate limits get one compact
element in the top bar (the tightest window, as a percentage with a gauge
glyph, and its reset time) and a short block in the Usage view listing each
window. When limits are absent, neither is shown: no empty placeholders.
A window at or above 80% must stand out without relying on colour alone: it
gets a `!`. In a narrow top bar the badge gives way before the counts: first the
counts drop their words, then the badge drops its reset time, then the gap
shrinks, then the badge goes; the counts keep the right edge.
