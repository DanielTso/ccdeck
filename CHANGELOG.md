# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-11-19

### Fixed

Found by running against a real Claude Code 2.1.328 session.

- The status line was fitted to `COLUMNS`, but Claude Code draws it with two cells of padding on each side and cuts
  what does not fit, so a line that just fitted lost its end. It is now fitted to `COLUMNS - 4`.
- After a compaction the dashboard showed the context from before it (from the transcript), or 0% (from the
  status-line meter, which Claude Code feeds a used percentage of 0 with no tokens until its next request). The
  context is now unknown, shown as `—`, until the next reply, and the status line leaves it out. `ccdeck show`
  prints an unknown context as `-` instead of `0 (window unknown)`.

### Changed

- The index cache format changed (accumulator state version 4); the first start after upgrading reads every
  transcript again.

## [0.1.0] - 2026-11-19

First version.

### Added

- `ccdeck`, a terminal dashboard for Claude Code sessions. It reads the files Claude Code keeps under
  `~/.claude` (or `--claude-dir`, or `$CLAUDE_CONFIG_DIR`). It never sends prompts and never writes to the
  Claude directory.
- Sessions view: every session in every project with status, title, project, context fill and age; a preview
  with metadata, usage, cost, subagents and the end of the conversation. Fuzzy filter, prompt search over
  `history.jsonl`, sort by recent, project, context or cost, group by project, and scope all, live or ended.
- Live view: a grid of panes, each following one running session. A session waiting for approval gets a pane
  first, with a double border and a `WAITING` badge showing what it waits for. Zoom, focus movement, 1 to 16 panes.
- Usage view: tokens and estimated cost for today and the last seven days, a bar per day, a per-model table, and
  a per-session table with context gauges and last-hour burn-rate sparklines.
- Transcript view: the full conversation with tool calls and thinking folded to one line, search within it, and
  following of new entries.
- Resume, fork and new session through `claude --resume <id>`, `claude --resume <id> --fork-session` and
  `claude`, started in the session's project directory. Modes `suspend`, `tmux-window`, `tmux-split` and `auto`
  (`[resume] mode`). A running session is never resumed twice; inside tmux, `enter` focuses its pane instead.
  `claude` is started with `CLAUDE_CONFIG_DIR` set to the Claude directory ccdeck reads when that is not the
  default `~/.claude`.
- Cost and context figures derived from the transcripts: usage counted once per API response, subagents included;
  cost reported by Claude Code when present, otherwise estimated from `[prices]` in the config file and from
  Claude Code's own price cache and marked with a tilde; a dash when a model has no known price; context
  percentage from Claude Code's model catalog.
- Four built-in themes (`ember`, `tidepool`, `paper`, `contrast`), custom themes with colour token overrides on a
  base that is a built-in or another custom theme (up to five custom themes in a chain; a cycle, an unknown base or
  a chain that is too deep is an error naming the config key), light or dark picked from the terminal background, and `NO_COLOR` support. Status is always
  shown with a glyph and a word as well as a colour.
- Configuration file at `$XDG_CONFIG_HOME/ccdeck/config.toml` (`~/.config/ccdeck/config.toml`): `theme`,
  `[layout]`, `[resume]`, `[keys]`, `[themes.<name>]` and `[prices."<model>"]`. Unknown keys and bad values are
  errors that name the key.
- Commands `ccdeck list [--json] [--live]`, `ccdeck show <id> [--json] [-n N | --entries N]`, `ccdeck doctor`,
  `ccdeck version` (also `-v` and `--version`) and `ccdeck completion <shell>`; flags `--claude-dir`, `--config`,
  `--theme` and `--demo`.
- The version and commit come from the build flags that `make install` and the release archives set, and from the
  build information Go records in the binary otherwise, so a plain `go install` reports its module version, or the
  commit it was built from (marked `-dirty` for uncommitted changes), instead of `dev (none)`.
- `ccdeck doctor` reports the Claude directory, index scan results including skipped lines, live sessions, tmux,
  the `claude` binary, the config file and the colour profile, and prints an example config. It is the first thing
  to run when numbers look wrong.
- `ccdeck --demo` runs the dashboard on invented, moving data and reads nothing from the Claude directory.
- A Claude directory that does not exist or cannot be read is an error naming it, not an empty list.
- A cache of derived numbers (no conversation text) in `$XDG_CACHE_HOME/ccdeck` makes later start-ups fast.
- GoReleaser archives for Linux and macOS (amd64 and arm64) containing the license, README and changelog.
- Opt-in status-line meter. `ccdeck statusline` is a Claude Code status-line command: it records Claude Code's own
  context figure, running cost and the account's rate limits (5-hour, 7-day and spend windows) in
  `$XDG_STATE_HOME/ccdeck/meter/<session id>.json` (numbers only, mode 0600, replaced atomically) and prints a
  status line fitted to `COLUMNS`. `--wrap '<command>'` keeps an existing status line; a wrapped command that
  fails, prints nothing or outlives `--wrap-timeout` is stopped with everything it started and ccdeck's own line is
  printed, and one that succeeds keeps its background jobs and its output, even when a job holds stdout open. The helper always
  exits 0 with a line and takes a few milliseconds. ccdeck never edits Claude Code's settings; `ccdeck doctor`
  reports whether the meter is feeding and prints the setting when it is not.
- With the meter on, a session's context and cost are Claude Code's own when its report is at least as new as the
  latest reply (cost kind `claude`, shown as "Claude Code's estimate"); a cost recorded at the end of the session
  still wins. The top bar shows the tightest rate-limit window with its reset time, and the Usage view lists every
  window; a window at 80% or more is marked with `!`. Rate limits come only from the reports of sessions in the
  Claude directory being read, and each window shows the highest use reported for it (or a later window), not the
  newest report's figure, which an idle session re-reports long after it was current. Without the meter nothing changes. Reports of sessions no
  longer running are deleted after 30 days. `ccdeck show` prints the last report; `list --json` includes it.
- Estimated cost respects the price tiers in Claude Code's price cache. A response served in fast mode
  (`usage.speed` `fast`) is priced at the model's fast rates, and a response whose prompt (input plus cache reads
  and writes) is above the model's `longPrompt.abovePromptTokens` at its long-prompt rates, request by request when
  the response lists its requests. Usage is kept per tier under keys such as `claude-opus-5-5+fast`, shown as
  `opus 5.5 fast` in the Usage view's model table and as `claude-opus-5-5 (fast)` by `ccdeck show`; the session's
  model stays the plain id. A tier with no rates of its own, a response both fast and long, and a speed ccdeck does
  not know have no price, so such a session shows tokens only rather than a figure at the base rate. `[prices]` may
  name `+fast`, `+long` and `+fast+long` keys. `ccdeck doctor` counts the models with tier rates and lists the
  tiers used and whether each is priced. The index cache is rebuilt once, and again whenever a long-prompt
  threshold changes. Not modelled: Claude Code's 10% surcharge on US-only inference.
- `--demo` includes invented meter figures and rate limits.
- Text inputs (filter, prompt search, find) of ccdeck's own, so that every ccdeck process, the status-line helper
  included, starts in a few milliseconds on WSL: the clipboard package behind Bubbles' input searched `PATH`,
  Windows directories included, for clipboard tools at start-up, which took up to half a second. Pasting works
  through the terminal's bracketed paste; `ctrl+v` does not read the system clipboard itself.
