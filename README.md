# ccdeck

`ccdeck` is a terminal dashboard for Claude Code sessions. It lists every session in every project, follows the
running ones (status, context fill, tokens, cost), shows several live conversations side by side, and hands the
terminal to `claude` when you want to resume one.

It is a companion that reads the files Claude Code already keeps under `~/.claude`. It never sends prompts, never
approves permission requests, and never writes to the Claude directory: you chat and approve in Claude Code, and
use ccdeck to see what is going on and to get back into a session.

## What it looks like

These captures come from `ccdeck --demo`, which runs on invented data. Colours are not shown; every status also
has its own glyph and word (`●` busy, `○` idle, `◆` waiting, `?` unknown, `·` ended).

**Sessions** (key `1`): the list on the left, the selected session on the right.

```text
 ccdeck  1 Sessions  2 Live  3 Usage                        ◆ 1 waiting  ● 1 busy  ○ 2 idle  ? 1 unknown  12 sessions
 Sessions                          all · recent  12 │ Refactor the rate limiter to use a token bucket per tenant and
▌● Refactor the rate limiter … orbit-api   63%  now │ add metrics for rejected requests
 ○ kiln-7                      kiln          —  now │ ● busy for 2m · orbit-api-3
 ? Port the config loader to … kiln        10%   2m │
 ◆ Fix the flaky refund test … orbit-api   29%   3m │ project  orbit-api  /home/demo/src/orbit-api
 ○ Migrate the build from Mak… lattice     95%  11m │ branch   feat/tenant-rate-limit
 · 認証フローのリファクタリン… tide-ui     35%  47m │ model    opus 5.5  v2.1.3
 · Add a dark mode toggle 🌙 … tide-ui     16%   3h │ started  1h ago · Nov 18 20:27
 · Draft the Q3 architecture … field-not…  88k   5h │ context  ━━━━━━━━━━━━━━━━─────────  63%  126k / 200k
 · Investigate memory growth … lattice     51%   1d │ tokens   3.42M total · 328k fresh · 26k out
 · Write an ADR comparing eve… orbit-api   32%   3d │ cost     ~$2.76  estimate
 · Bump dependencies and fix … ledger-sy…  22%  12d │ turns    6 prompts · 43 replies · 25 tool calls
 · Tidy shell aliases          dotfiles     4%   5w │
                                                    │ agents   3
                                                    │          Explore       Find every caller of Limit…    52k   2m
                                                    │          general-pur…  Draft metric names that fo…    52k   9m
                                                    │          code-review…  Review the tenant limiter …    41k  16m
                                                    │
                                                    │ ──────────────────────────────────────────────────────────────
                                                    │   should keep a global budget, so I'll keep the old type as
                                                    │   `GlobalLimiter` and add `TenantLimiter` beside it.
                                                    │   ▸ Write internal/ratelimit/tenant.go
                                                    │     ↳ Created internal/ratelimit/tenant.go (+84 lines)
                                                    │   ▸ Bash go test ./internal/ratelimit/...
                                                    │     ✗ --- FAIL: TestTenantBurst (0.00s)
                                                    │   The burst test is off by one: the bucket starts full and
                                                    │   refills in the same tick. I'll refill before taking a token.
                                                    │   ∴ thinking: Checking how this interacts with the existing t…
 enter focus  f fork  n new  o transcript  / filter  p search  s sort  g group  a scope                ? help  q quit
```

**Live** (key `2`): one pane per running session, each following its conversation. A session that is waiting for
you (here, to approve a command) gets a double border and a `WAITING` badge.

```text
 ccdeck  1 Sessions  2 Live  3 Usage                        ◆ 1 waiting  ● 1 busy  ○ 2 idle  ? 1 unknown  12 sessions
╔═ ▶1  ◆ WAITING  3m ═════════════════════════════════════╗╭─ 2 ○ idle 1m ───────────────────────────────────────────╮
║ Fix the flaky refund test in payments                   ║│ kiln-7                                                  │
║ orbit-api · sonnet 5 · ━───  29% · ~$0.40               ║│ kiln · — · —                                            │
║ ▶ needs you: approve Bash: go test ./internal/payments… ║│ ─────────────────────────────────────────────────────── │
║ ─────────────────────────────────────────────────────── ║│ Nothing said yet.                                       │
║   The test reads the ledger before the async writer     ║│                                                         │
║   flushes. I'll give the writer a Flush method and call ║│                                                         │
║   it in the test instead of sleeping.                   ║│                                                         │
║   ▸ Edit internal/payments/ledger.go                    ║│                                                         │
║     ↳ Updated internal/payments/ledger.go (+12 −3)      ║│                                                         │
║   Now I want to run it 200 times with the race detector ║│                                                         │
║   to be sure.                                           ║│                                                         │
╚═══════════════════════════════════════════ orbit-api-4 ═╝╰──────────────────────────────────────────────── kiln-7 ─╯
╭─ 3 ● busy 2m ───────────────────────────────────────────╮╭─ 4 ? unknown 2m ────────────────────────────────────────╮
│ Refactor the rate limiter to use a token bucket per te… ││ Port the config loader to TOML                          │
│ orbit-api · opus 5.5 · ━━━─  65% · ~$2.91               ││ kiln · haiku 4.5 · ━───  10% · ~$0.06                   │
│ ─────────────────────────────────────────────────────── ││ ─────────────────────────────────────────────────────── │
│   ▸ Bash go test ./internal/ratelimit/...               ││ › Port the config loader from YAML to TOML and keep     │
│     ✗ --- FAIL: TestTenantBurst (0.00s)                 ││   reading the old file for one release.                 │
│   The burst test is off by one: the bucket starts full  ││   ▸ Read internal/config/load.go                        │
│   and refills in the same tick. I'll refill before      ││     ↳ package config … (96 lines)                       │
│   taking a token.                                       ││                                                         │
│   ∴ thinking: Checking how this interacts with the exi… ││                                                         │
│   Next I'll look at the callers before changing the     ││                                                         │
│   signature.                                            ││                                                         │
│   ▸ Grep func New in internal/                          ││                                                         │
╰─────────────────────────────────────────── orbit-api-3 ─╯╰──────────────────────────────────────────────── kiln-6 ─╯
 4 panes · 5 live · 1 without a pane:  ○ Migrate the build from …  + adds a pane
 enter focus  tab next pane  z zoom  + more panes  - fewer panes  o transcript                         ? help  q quit
```

## Install

Requires Go 1.27.4 or newer (see `go.mod`).

```sh
make install                                      # from a checkout; embeds version and commit
go install github.com/DanielTso/ccdeck@latest     # once the repository is published
```

Both put `ccdeck` in `$GOBIN` (default `~/go/bin`). `make build` writes `./bin/ccdeck` instead. A build made with
plain `go install` reports its version as `dev (none)`; `make install` embeds `git describe`.

Tagged releases (`vX.Y.Z`) publish `tar.gz` archives for Linux and macOS (amd64 and arm64) via GoReleaser;
`make snapshot` builds them locally without publishing. Linux (WSL2 included) is the primary target; see
[Platforms](#platforms).

Optional runtime dependencies: `claude` on `PATH` to resume, fork or start sessions, and `tmux` for the tmux
resume modes and for focusing a running session.

## Quick start

```sh
ccdeck --demo      # try it on invented data first; q quits
ccdeck doctor      # check that ccdeck can read your Claude directory
ccdeck             # the real thing
```

In the dashboard: arrow keys (or `j`/`k`) move through sessions, `enter` resumes the selected one, `2` shows the
live grid, `3` shows usage, `o` opens the full transcript, and `?` lists every key.

## Views

Four views. `1`, `2` and `3` switch between the first three; the transcript opens from any of them with `o`. The
header shows the view tabs and a count of sessions by status. The terminal window title becomes
`ccdeck · N waiting` while any session is waiting for you.

### Keys that work everywhere

| Key | Action |
| --- | --- |
| `1` `2` `3` | Sessions, Live, Usage |
| `o` | Open the selected session's transcript (from Sessions, Live or Usage) |
| `?` | Help overlay listing the bindings active in the current view; `?` or `esc` closes it |
| `T` | Switch to the next theme (built-ins first, then your own) |
| `esc` | Back: from the transcript to where you came from, from Live and Usage to Sessions |
| `q`, `ctrl+c` | Quit |
| `↑`/`k` `↓`/`j` | Move or scroll one line |
| `PgUp`/`ctrl+u` `PgDn`/`ctrl+d` | Move or scroll a page |
| `home` `end` | First or last item (in Live, top or bottom of the focused pane) |

Mouse: the wheel scrolls, a click selects a row or pane, and a click on a tool call in the transcript folds or
unfolds it. `ctrl+c` always quits, even if you rebind `quit`.

### Sessions (`1`)

Every session in every project. Each row shows a status mark, title, project, how full the context window is (or
the token count when the window size is unknown) and time since last activity. The preview shows project, branch,
model, start time, context, tokens, cost, turns, subagents and the tail of the conversation. Below 100 columns
the preview moves under the list, and a short window hides it.

| Key | Action |
| --- | --- |
| `enter` | Resume the session; for a running session, focus it (needs tmux, see [below](#resume-fork-new-and-focus)) |
| `f` | Fork: resume into a new session id and leave the original untouched (ended sessions only) |
| `n` | New session in the selected session's project directory |
| `/` | Fuzzy filter on title, project and branch; `enter` keeps it, `esc` clears it |
| `p` | Search past prompts; `enter` jumps to the session that prompt belongs to, `esc` closes |
| `s` | Cycle sort: recent, project, context, cost |
| `g` | Group by project |
| `a` | Cycle scope: all, live, ended |

The filter is a fuzzy (subsequence) match, so short filters can match more than you expect. Prompt search is a
plain case-insensitive substring search over Claude Code's `history.jsonl`, newest first.

### Live (`2`)

A grid of panes, one per running session, each following its conversation as it grows. A pane header shows the
status badge, title, project, model, context gauge and cost. A session that starts waiting for approval gets a
pane first, with a double border, a `WAITING` badge and the text of what it is waiting for.

| Key | Action |
| --- | --- |
| `tab` `shift+tab` | Move focus to the next or previous pane |
| `z` | Zoom the focused pane to the full view; again to unzoom |
| `+` `-` | Show more or fewer panes (1 to 16, as many as fit the window) |
| `enter` | Focus the pane's session (or resume it, if it has ended) |
| `↑` `↓` `PgUp` `PgDn` `home` `end` | Scroll the focused pane; a scrolled pane holds still until you return to the bottom |
| `esc` | Unzoom, then back to Sessions |

Panes are filled with running sessions only. A pane keeps its session even after it ends, until the room is needed for
a running one. The grid never shows empty panes: with fewer sessions than panes it shows one pane per session, and
with nothing running a single message says so. Running sessions that do not fit are listed in the strip under the
grid, waiting ones first. The default pane count is 4 (`live_panes`); small windows show fewer, always keeping the
focused session on screen, and the other panes come back where they were when the window grows again.

### Usage (`3`)

Today and the last seven days: total, fresh and output tokens, estimated cost, session count; a bar per day; a
per-model table (fresh tokens today and over 7 days, output, cache reads, share of the week); and a per-session
table, running sessions first, with context gauge, a last-hour burn-rate sparkline, tokens per hour, fresh
tokens and cost. "Fresh" means tokens that were not served from the prompt cache; see
[Cost and context](#cost-and-context-figures).

| Key | Action |
| --- | --- |
| `↑` `↓` `PgUp` `PgDn` `home` `end` | Select a session row |
| `enter` | Focus (running) or resume (ended) the selected session |
| `o` | Open its transcript |
| `esc` | Back to Sessions |

### Transcript (`o`)

The whole conversation of one session, scrollable. Prompts and replies are shown as written; each tool call is
folded to one line with its result; thinking blocks are folded too. When you are at the bottom it follows new
entries (the header says `following`); `end` returns to the bottom.

| Key | Action |
| --- | --- |
| `tab` `shift+tab` | Select the next or previous tool call or thinking block |
| `enter` `space` | Expand or fold the selected one |
| `e` | Expand all, or fold all if none is folded |
| `/` | Find in the transcript; `enter` keeps the search, `esc` clears it |
| `n` `N` | Next or previous match |
| `esc` | Clear the search; if there is none, go back |

## Commands

```text
ccdeck                       the dashboard
ccdeck list [--json] [--live]
ccdeck show <id> [--json] [-n N]
ccdeck doctor
ccdeck version               also: ccdeck --version
ccdeck completion <shell>    shell completion script (generated by cobra)
```

Flags that every command accepts:

| Flag | Meaning |
| --- | --- |
| `--claude-dir DIR` | Claude Code directory. Default: `$CLAUDE_CONFIG_DIR`, else `~/.claude`. A directory that does not exist or cannot be read is an error (`doctor` still reports on it) |
| `--config FILE` | Config file. Default: `$XDG_CONFIG_HOME/ccdeck/config.toml`. A file named here must exist |
| `--theme NAME` | Colour theme, overriding the config file (dashboard only) |
| `--demo` | Use invented data and read nothing from the Claude directory (`doctor` ignores it and says so) |

### `ccdeck list`

Prints sessions, most recently active first, as a table. `--live` keeps only sessions a running process owns;
`--json` prints the sessions as a JSON array instead (the fields of `core.Session`: `id`, `title`, `projectDir`,
`model`, `usage`, `contextTokens`, `contextWindow`, `costUSD`, `costKind` (`none`, `estimated` or `reported`),
`live`, and so on).

```text
ID        STATUS   ACTIVE  MODEL                      CONTEXT  TOKENS  COST    PROJECT                     TITLE
5b0e2c4a  busy     now     claude-opus-5-5            62%      3.3M    ~$2.69  /home/demo/src/orbit-api    Refactor the rate limiter to use a token bucket per tenant …
8c71f0d2  waiting  3m      claude-sonnet-5            29%      561k    ~$0.40  /home/demo/src/orbit-api    Fix the flaky refund test in payments
f60718a2  ended    5h      acme-research-preview      88.2k    1.9M    -       /home/demo/src/field-notes  Draft the Q3 architecture review from the meeting notes
```

`ID` is the first 8 characters; `ACTIVE` is the time since last activity (`now`, `5m`, `3h`, `2d`); `CONTEXT` is
a percentage, or a token count when the model's window size is unknown; `TOKENS` counts everything, cache reads
included; `COST` is `$1.50` when Claude Code reported it, `~$0.22` when estimated, `-` when unknown.

### `ccdeck show <id>`

One session's summary (status, process, project, branch, model, start and last activity, turns, context, cost,
tokens per model, subagents) followed by its most recent conversation entries. `<id>` may be any prefix that
matches only one session; an ambiguous or unknown prefix is an error that says so. `-n` sets how many entries to
show (default 20, `0` for all). `--json` prints `{"session": …, "entries": […]}`.

### `ccdeck doctor`

Reports what ccdeck finds, then prints an example config file. Run it first when something looks wrong. It shows:

- the Claude directory and what is in it: project directories and transcripts, registry files and how many have a
  live process, prompts in `history.jsonl`, models with a known context window, models with a known price (from
  Claude Code's price cache and from your config);
- the result of one index scan: files read, how many were continued from the cache, lines read, **malformed lines
  skipped**, scan time, how many directories are watched (or "polling only"), the cache file, and any warnings;
- the live sessions with pid, status, model and context;
- `tmux` (version, and whether you are inside it), the `claude` binary, the effective resume mode, the config file
  and whether it exists, and the terminal's colour profile.

## Configuration

ccdeck works without a config file. To customise it, save the example below as
`$XDG_CONFIG_HOME/ccdeck/config.toml` (usually `~/.config/ccdeck/config.toml`; `ccdeck doctor` prints the exact
path it will read and the same example). Every setting is optional, and the active lines below are the defaults.

```toml
# ccdeck configuration.
# Every setting is optional. Unknown keys are an error, so typos are caught.

# Colour theme: a built-in name, or the name of a table under [themes].
# Unset, ccdeck picks a light or dark theme from your terminal background.
# theme = "ember"

[layout]
# Share of the screen width given to the preview pane, from 0.2 to 0.8.
preview_ratio = 0.55
# Panes in the live grid, from 1 to 16.
live_panes = 4
# Group the session list by project.
group_by_project = false

[resume]
# How a session is opened: auto, suspend, tmux-window or tmux-split.
# auto uses a new tmux window inside tmux and suspends ccdeck otherwise.
mode = "auto"
# The claude binary: a name looked up on PATH, or a full path.
claude = "claude"

# Key bindings. Each action takes a list of keys; actions you leave out keep
# their defaults. Press ? in ccdeck to see the actions.
# [keys]
# quit = ["q", "ctrl+c"]

# A custom theme starts from a built-in theme and overrides colour tokens.
# Colours are "#rgb", "#rrggbb" or an ANSI number from 0 to 255.
# [themes.mine]
# base = "ember"
# accent = "#ff8800"

# Price overrides in dollars per million tokens, used to estimate cost when
# Claude Code did not record one. Add one table per model id.
# [prices."example-model"]
# input = 3.0
# output = 15.0
# cache_read = 0.3
# cache_write_5m = 3.75
# cache_write_1h = 6.0
```

Rules:

- A missing file means defaults. A file named with `--config` must exist.
- Unknown keys, a bad value or an out-of-range number are errors that name the key (for example
  `key layout.live_panes: 20 is outside 1 to 16`), so typos are caught rather than ignored.
- `layout.preview_ratio` is 0.2 to 0.8; `layout.live_panes` is 1 to 16.
- `theme` unset picks `ember` or `paper` from the terminal's background colour. `--theme` overrides the file.
- `XDG_CONFIG_HOME` is used only when it is an absolute path.

### Key bindings

`[keys]` maps an action to a list of keys, spelled as Bubble Tea spells them: `q`, `T`, `ctrl+c`, `enter`,
`space`, `tab`, `shift+tab`, `esc`, `up`, `down`, `pgup`, `pgdown`, `home`, `end`. Listing an action **replaces**
its keys; actions you leave out keep their defaults. The footer and the `?` overlay show your bindings.

| Group | Action (default keys) |
| --- | --- |
| global | `sessions` (1), `live` (2), `usage` (3), `open` (o), `help` (?), `theme` (T), `back` (esc), `quit` (q, ctrl+c) |
| move | `up` (up, k), `down` (down, j), `page_up` (pgup, ctrl+u), `page_down` (pgdown, ctrl+d), `top` (home), `bottom` (end) |
| sessions | `resume` (enter), `fork` (f), `new` (n), `filter` (/), `search` (p), `sort` (s), `group` (g), `scope` (a) |
| live | `next_pane` (tab), `prev_pane` (shift+tab), `zoom` (z), `more_panes` (+), `fewer_panes` (-), and `resume` (enter) |
| transcript | `next_fold` (tab), `prev_fold` (shift+tab), `expand` (enter, space), `expand_all` (e), `find` (/), `next_match` (n), `prev_match` (N) |

An unknown action, an empty key list, or one key bound to two actions that are active in the same view is an error
at start-up that names the keys involved (for example `key "n" is bound to both "quit" and "new"`). The same key
may be used for different actions in different views. `ctrl+c` always quits.

## Themes

Four built-in themes:

| Name | Style |
| --- | --- |
| `ember` | dark, warm; the default on a dark terminal |
| `tidepool` | dark, cool |
| `paper` | light; the default on a light terminal |
| `contrast` | black and white with saturated status colours |

Press `T` to cycle through them (the status line names the theme), or set `theme = "paper"` in the config, or run
`ccdeck --theme contrast`. With no theme set, ccdeck asks the terminal for its background colour at start-up and
uses `ember` or `paper`; pressing `T` stops it following the terminal.

Status is never carried by colour alone: each status has its own glyph and word. With `NO_COLOR=1` set, ccdeck
draws without colour, keeps bold text and glyphs, and leaves the terminal's own default colours alone.

### Defining a theme

Add a `[themes.<name>]` table. `base` names the built-in theme to start from (default `ember`); every other key
overrides one colour token. Your theme appears after the built-ins in the `T` cycle and can be selected with
`theme = "<name>"` or `--theme <name>`.

```toml
theme = "dusk"

[themes.dusk]
base = "paper"
accent = "#b0520e"       # "#rgb" or "#rrggbb"
waiting = 208            # or an ANSI colour number, 0 to 255 (bare or quoted)
gauge_high = "#c23a12"
```

| Token | Used for |
| --- | --- |
| `background`, `text`, `muted` | the page, normal text, secondary text |
| `surface` | background of the selected row |
| `border` | rules, separators and empty gauge cells |
| `border_focused` | the focused pane's border and the help box |
| `accent` | the app name, key hints, the selection bar, the `›` before your prompts, highlights |
| `busy`, `idle`, `waiting`, `ended`, `unknown` | one colour per session status |
| `error` | errors and failed tool results |
| `gauge_low`, `gauge_mid`, `gauge_high` | the three steps of the context gauge |

The base must be a built-in theme: a custom theme cannot be based on another custom theme (the example file
printed by `ccdeck doctor` says otherwise, but ccdeck refuses it at start-up). A custom theme cannot reuse a
built-in's name. An unknown token or a bad colour is an error that names the theme and the token.

## Resume, fork, new and focus

Resuming runs `claude --resume <session id>` in the session's project directory; forking adds `--fork-session`;
a new session runs `claude` in the project directory of the selected session. ccdeck passes no other flags. When
ccdeck reads a Claude directory other than the default `~/.claude` (from `--claude-dir` or `CLAUDE_CONFIG_DIR`),
`claude` is started with `CLAUDE_CONFIG_DIR` set to it, in every mode, so it finds the same sessions and account.
One launch runs at a time: a second `enter`, `f` or `n` while `claude` is starting is ignored.
The `claude` binary is looked up on `PATH` (set `[resume] claude` for another name or a full path). Where the
command runs is `[resume] mode`:

| Mode | Behaviour |
| --- | --- |
| `auto` (default) | `tmux-window` when ccdeck runs inside tmux (`$TMUX` is set), otherwise `suspend` |
| `suspend` | ccdeck steps aside and gives its terminal to `claude`; when `claude` exits, the dashboard returns and refreshes |
| `tmux-window` | Opens `claude` in a new tmux window; ccdeck keeps running. Needs tmux; outside tmux this is an error |
| `tmux-split` | Splits the current tmux window and runs `claude` in the new pane. Same requirements |

Without tmux, only `suspend` works, and `auto` means `suspend`. With tmux, `auto` gives you a window per session
and keeps the dashboard on screen.

**A running session is never resumed a second time.** On a running session, `enter` focuses it and `f` tells you
to fork once it has ended. **Focus** works inside tmux only: ccdeck asks tmux for its panes, finds the one whose
process tree contains the session's process, and switches the client to that session, window and pane. It works
whatever the resume mode. When it cannot find the pane (you are not in tmux, or the session runs in a terminal
tmux does not know), the status line says the session is already running, as which process, and that it cannot be
reached from here. Focus reads `/proc`, so it is Linux-only.

Resume, fork and new fail with a message in the status line, and nothing is started, when the session's project
directory no longer exists, when `claude` is not found, or when a tmux mode is configured outside tmux. In
`--demo` the projects do not exist, so these actions fail by design.

## Cost and context figures

Everything here is derived from Claude Code's transcripts. The formats are observed, not documented, and may
change with any Claude Code release (the layout was observed with Claude Code 2.1.328). If numbers look wrong, run
`ccdeck doctor` first: a changed format usually shows up as many skipped lines, zero tokens, missing sessions, or
dashes where figures used to be.

**Tokens.** One API response is written to the transcript as up to three lines that repeat the same usage; ccdeck
counts each response once. Usage from subagents is added to the parent session's tokens and cost. *Total* includes
cache reads; *fresh* is input, output and cache writes, which is the better measure of work done. Thinking tokens
are part of output.

**Context.** The context size is input plus cache-read plus cache-creation tokens of the latest main-thread
message (subagents do not count). The percentage is against the model's window from Claude Code's model catalog
(`cache/model-catalog/*.json`), matched by exact model id, then by prefix. An unknown model has no window, so you
see the token count without a percentage. The gauge stops at 100%.

**Cost.** In order:

1. The total Claude Code recorded when the session ended, shown plainly (`$1.50`, "reported"). It is used only
   while nothing has been added since: once you resume the session and a new response arrives, ccdeck goes back
   to estimating.
2. Otherwise tokens multiplied by prices, shown with a tilde (`~$0.22`, "estimate"). Prices come from `[prices]`
   in your config file, then from the price cache Claude Code keeps in `~/.claude.json`
   (`additionalModelCostsCache`; if `--claude-dir` or `CLAUDE_CONFIG_DIR` is set and that directory has its own
   `.claude.json`, that file). ccdeck contains no price list of its own. Model ids match exactly, then by prefix
   (`claude-x-1` prices `claude-x-1-20260101`). Cache writes with no 5-minute/1-hour split count as 5-minute
   writes. Web searches are priced only when Claude Code's cache has a per-request price.
3. If any model used in the session, subagents included, has no price: a dash (`-` on the command line, `—` in
   the dashboard), and tokens only.

Limits to keep in mind:

- An estimate is tokens at list price. It is not your bill: subscription plans, discounts and anything Claude Code
  charges differently are not modelled. Claude Code often records `0` on subscription plans, which is why most
  figures are estimates.
- A `[prices."model"]` table replaces that model's whole price, and the fields you leave out count as `0`. Give
  all five.
- The Usage view's daily and seven-day figures are always estimates from tokens (never the reported totals), so
  they need not equal the sum of the session figures. A day on which any model has no price shows "no price
  known"; the seven-day figure then leaves that day out and says so, as `≥ ~$X partial estimate`. The seven-day
  session count counts each session once, however many days it was active.
- Claude Code's price cache has higher rates for some models in fast mode and for long prompts. ccdeck prices
  every message at the base rate, so sessions that use fast mode or very long prompts on such a model are
  underestimated.
- Times and "today" use your local time zone.

## Privacy

ccdeck runs locally. It makes no network connections, sends no telemetry and fetches no prices; the only
programs it starts are `claude` and `tmux`, when you ask.

- **Reads**: under the Claude directory, session transcripts and subagent transcripts (`projects/`), the live
  session registry (`sessions/*.json`), `history.jsonl`, and the model catalog; plus `~/.claude.json`, of which it
  uses only the price cache. It never opens `sessions/*.key` or the sockets, and it skips the transcript line
  types that carry prompt plumbing and account details.
- **Writes**: nothing under the Claude directory. Its only file is a cache of derived numbers at
  `$XDG_CACHE_HOME/ccdeck/` (default `~/.cache/ccdeck/`), one `index-<hash>.json` per Claude directory, mode 0600
  in a 0700 directory. It holds, per transcript: file size and modification time, how far ccdeck has read, line
  counts, the project directory path, git branch, model ids, message ids, timestamps and token counts per model,
  and byte offsets. It holds no conversation text, titles or prompts. Delete it whenever you like; the next start
  re-reads the transcripts.
- Conversation text is read from the transcripts when a view needs it and kept in memory only while ccdeck runs.

## Platforms

Linux first; it was built and tested on WSL2. Release archives are built for Linux and macOS. On
macOS there is no `/proc`: ccdeck can only check that a registered pid exists (so a stale registry file can show
as running), and Focus is unavailable. There are no Windows builds. The macOS paths are untested.

## Development

```sh
make check        # fmt, vet, lint (golangci-lint), test
make build        # ./bin/ccdeck
make help         # all targets
```

| Target | Does |
| --- | --- |
| `build` | Build the binary into `./bin` |
| `install` | Install into `$GOBIN` / `$GOPATH/bin` |
| `test` | All tests with the race detector and coverage |
| `test-short` | Fast tests only |
| `fmt`, `vet`, `lint` | `gofmt -s -w .`, `go vet ./...`, `golangci-lint run ./...` |
| `check` | `fmt`, `vet`, `lint`, `test` |
| `snapshot` | Build release archives locally with GoReleaser; nothing is published |
| `clean` | Remove `bin` and `dist` |

Layout: `cmd/` (cobra commands), `internal/core` (the shared types: session, store, launcher),
`internal/claude` (parsers for the Claude directory), `internal/index` (scan, cache, watch),
`internal/demo` (the `--demo` data), `internal/config`, `internal/launch` and `internal/tui` (Bubble Tea v2).
`docs/ARCHITECTURE.md` describes the design, the on-disk formats and the hard rules. The tests use only
synthetic fixtures and never read your real `~/.claude`.

To look at the running UI without a terminal of your own, start it in tmux and read the screen back:

```sh
tmux new-session -d -s ccdeck-test -x 140 -y 40 './bin/ccdeck --demo'
tmux send-keys -t ccdeck-test 2
tmux capture-pane -t ccdeck-test -p
tmux kill-session -t ccdeck-test
```

## License

MIT. See [LICENSE](LICENSE).
