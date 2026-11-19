# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- Four built-in themes (`ember`, `tidepool`, `paper`, `contrast`), custom themes with a built-in base and colour
  token overrides, light or dark picked from the terminal background, and `NO_COLOR` support. Status is always
  shown with a glyph and a word as well as a colour.
- Configuration file at `$XDG_CONFIG_HOME/ccdeck/config.toml` (`~/.config/ccdeck/config.toml`): `theme`,
  `[layout]`, `[resume]`, `[keys]`, `[themes.<name>]` and `[prices."<model>"]`. Unknown keys and bad values are
  errors that name the key.
- Commands `ccdeck list [--json] [--live]`, `ccdeck show <id> [--json] [-n N]`, `ccdeck doctor`,
  `ccdeck version` and shell completion; flags `--claude-dir`, `--config`, `--theme` and `--demo`.
- `ccdeck doctor` reports the Claude directory, index scan results including skipped lines, live sessions, tmux,
  the `claude` binary, the config file and the colour profile, and prints an example config. It is the first thing
  to run when numbers look wrong.
- `ccdeck --demo` runs the dashboard on invented, moving data and reads nothing from the Claude directory.
- A Claude directory that does not exist or cannot be read is an error naming it, not an empty list.
- A cache of derived numbers (no conversation text) in `$XDG_CACHE_HOME/ccdeck` makes later start-ups fast.
- GoReleaser archives for Linux and macOS (amd64 and arm64) containing the license, README and changelog.
