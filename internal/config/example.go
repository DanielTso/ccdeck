package config

// Example returns a commented example config file. Everything active in it is
// the default; the commented-out lines show what can be changed.
func Example() string {
	return exampleFile
}

// exampleFile keeps its commented-out settings in the form "# key = value" or
// "# [table]" and its prose in any other form, so that a test can uncomment
// the settings and check they load.
const exampleFile = `# ccdeck configuration.
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

# A custom theme starts from a built-in or custom theme and overrides colour
# tokens. Colours are "#rgb", "#rrggbb" or an ANSI number from 0 to 255.
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
`
