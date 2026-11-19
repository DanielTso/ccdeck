package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Invented model ids and window sizes. acmeModel has no known window and no
// rates, so the UI must cope with an unknown context window and no cost.
const (
	opus      = "claude-opus-5-5"
	sonnet    = "claude-sonnet-5"
	haiku     = "claude-haiku-4-5-20251001"
	acmeModel = "acme-research-preview"
	window    = 200_000
)

// rate is an invented price per million tokens. The figures are synthetic
// so the demo can show estimated costs; they are not real prices.
type rate struct{ input, output, cacheRead, write5m, write1h float64 }

var rates = map[string]rate{
	opus:   {4, 20, 0.4, 5, 8},
	sonnet: {2, 10, 0.2, 2.5, 4},
	haiku:  {0.8, 4, 0.08, 1, 1.6},
}

func costOf(usage map[string]core.Usage) float64 {
	var c float64
	for m, u := range usage {
		r, ok := rates[m]
		if !ok {
			continue
		}
		c += (float64(u.Input)*r.input + float64(u.Output)*r.output +
			float64(u.CacheRead)*r.cacheRead + float64(u.CacheWrite5m)*r.write5m +
			float64(u.CacheWrite1h)*r.write1h) / 1e6
	}
	return c
}

// line is one scripted conversation step.
type line struct {
	kind core.EntryKind
	tool string
	text string
	err  bool
}

func prompt(s string) line                    { return line{kind: core.EntryPrompt, text: s} }
func say(s string) line                       { return line{kind: core.EntryText, text: s} }
func think(s string) line                     { return line{kind: core.EntryThinking, text: s} }
func use(tool, s string) line                 { return line{kind: core.EntryToolUse, tool: tool, text: s} }
func result(s string) line                    { return line{kind: core.EntryToolResult, text: s} }
func failed(s string) line                    { return line{kind: core.EntryToolResult, text: s, err: true} }
func command(s string) line                   { return line{kind: core.EntryCommand, text: s} }
func compacted(s string) line                 { return line{kind: core.EntryCompact, text: s} }
func agent(typ, desc, model string) agentSpec { return agentSpec{typ, desc, model} }

type agentSpec struct{ typ, desc, model string }

// spec describes one invented session.
type spec struct {
	id, title, project, branch, model string
	ago, length                       time.Duration // since last activity; session length
	status                            core.Status   // StatusEnded when not live
	waitingFor                        string
	pid                               int
	name                              string // registry name of a live session
	context                           int64
	window                            int64
	cost                              core.CostKind
	reported                          float64
	turns                             int
	active                            bool
	agents                            []agentSpec
	convo                             []line
}

const root = "/home/demo/src/"

var specs = []spec{
	{
		id: "5b0e2c4a-91d3-4f6e-a7b8-0c1d2e3f4a51", project: root + "orbit-api", branch: "feat/tenant-rate-limit",
		title: "Refactor the rate limiter to use a token bucket per tenant and add metrics for rejected requests",
		model: opus, ago: 40 * time.Second, length: 95 * time.Minute,
		status: core.StatusBusy, pid: 41207, name: "orbit-api-3",
		context: 124_300, window: window, cost: core.CostEstimated, turns: 6, active: true,
		agents: []agentSpec{
			agent("Explore", "Find every caller of Limiter.Allow", haiku),
			agent("general-purpose", "Draft metric names that follow the existing conventions", sonnet),
			agent("code-reviewer", "Review the tenant limiter diff for races", opus),
		},
		convo: []line{
			prompt("The rate limiter is global right now. Refactor it to use a token bucket per tenant, and add metrics for rejected requests so we can alert on them."),
			think("The limiter lives in internal/ratelimit; tenants come from the request context."),
			say("I'll start by reading the current limiter and where it is wired into the middleware chain."),
			use("Grep", "Limiter in internal/"),
			result("internal/ratelimit/limiter.go\ninternal/http/middleware.go\ninternal/http/middleware_test.go"),
			use("Read", "internal/ratelimit/limiter.go"),
			result("package ratelimit\n\n// Limiter allows n requests per second across all tenants.\ntype Limiter struct {"),
			use("Agent", "Find every caller of Limiter.Allow"),
			result("3 callers: http/middleware.go:42, admin/handler.go:88, jobs/worker.go:120"),
			say("There are three callers. The admin handler and the worker should keep a global budget, so I'll keep the old type as `GlobalLimiter` and add `TenantLimiter` beside it."),
			use("Write", "internal/ratelimit/tenant.go"),
			result("Created internal/ratelimit/tenant.go (+84 lines)"),
			use("Bash", "go test ./internal/ratelimit/..."),
			failed("--- FAIL: TestTenantBurst (0.00s)\n    tenant_test.go:31: got 11 allowed, want 10\nFAIL"),
			say("The burst test is off by one: the bucket starts full and refills in the same tick. I'll refill before taking a token."),
		},
	},
	{
		id: "8c71f0d2-3a4b-4c5d-9e6f-1a2b3c4d5e62", project: root + "orbit-api", branch: "fix/flaky-refund",
		title: "Fix the flaky refund test in payments",
		model: sonnet, ago: 3 * time.Minute, length: 22 * time.Minute,
		status: core.StatusWaiting, pid: 41388, name: "orbit-api-4",
		waitingFor: "approve Bash: go test ./internal/payments/... -run TestRefund -count=200 -race",
		context:    58_900, window: window, cost: core.CostEstimated, turns: 2, active: true,
		convo: []line{
			prompt("TestRefund fails about one run in twenty on CI. Find out why and fix it properly, no retries."),
			say("Let me reproduce it locally by running the test many times."),
			use("Bash", "go test ./internal/payments/ -run TestRefund -count=50"),
			failed("--- FAIL: TestRefund (0.01s)\n    refund_test.go:77: refund amount 0, want 1250"),
			think(""),
			say("The test reads the ledger before the async writer flushes. I'll give the writer a Flush method and call it in the test instead of sleeping."),
			use("Edit", "internal/payments/ledger.go"),
			result("Updated internal/payments/ledger.go (+12 −3)"),
			say("Now I want to run it 200 times with the race detector to be sure."),
		},
	},
	{
		id: "c3d4e5f6-0718-4293-a4b5-c6d7e8f90a73", project: root + "lattice", branch: "build/bazel",
		title: "Migrate the build from Make to Bazel, one package at a time",
		model: opus, ago: 11 * time.Minute, length: 5 * time.Hour,
		status: core.StatusIdle, pid: 39920, name: "lattice-1",
		context: 191_400, window: window, cost: core.CostEstimated, turns: 23,
		agents: []agentSpec{
			agent("Explore", "List packages with no internal dependencies", haiku),
			agent("general-purpose", "Write BUILD files for the proto packages", sonnet),
		},
		convo: []line{
			prompt("Start migrating the build to Bazel. Begin with the leaf packages and keep make working until everything is ported."),
			use("Agent", "List packages with no internal dependencies"),
			result("14 leaf packages: proto/v1, util/clock, util/backoff, …"),
			command("/compact"),
			compacted("Conversation compacted (182k → 31k tokens)"),
			prompt("Keep going with the remaining leaf packages."),
			use("Bash", "bazel build //util/... //proto/..."),
			result("INFO: Build completed successfully, 212 total actions"),
			use("Bash", "make all"),
			result("ok"),
			say("All 14 leaf packages now build under Bazel and `make` still works. The context window is nearly full, so this is a good moment to compact before porting the services."),
		},
	},
	{
		id: "d4e5f607-1829-43a4-b5c6-d7e8f90a1b84", project: root + "tide-ui", branch: "main",
		title: "認証フローのリファクタリングとエラーメッセージの改善",
		model: sonnet, ago: 47 * time.Minute, length: 38 * time.Minute,
		status: core.StatusEnded, context: 71_000, window: window, cost: core.CostReported, reported: 3.18, turns: 4,
		convo: []line{
			prompt("ログイン画面のエラーメッセージを分かりやすくして、認証フローを整理してください。"),
			say("認証フローを三つの段階に分けました：入力の検証、トークンの取得、リダイレクト。"),
			use("Edit", "src/auth/LoginForm.tsx"),
			result("Updated src/auth/LoginForm.tsx (+41 −58)"),
			use("Bash", "npm test -- auth"),
			result("Tests: 18 passed, 18 total"),
			say("エラーメッセージは原因と次の操作が分かる文に書き直しました。"),
		},
	},
	{
		id: "e5f60718-293a-44b5-c6d7-e8f90a1b2c95", project: root + "tide-ui", branch: "feat/dark-mode",
		title: "Add a dark mode toggle 🌙 to the settings page",
		model: haiku, ago: 3 * time.Hour, length: 25 * time.Minute,
		status: core.StatusEnded, context: 33_500, window: window, cost: core.CostEstimated, turns: 3,
		convo: []line{
			prompt("Add a dark mode toggle to the settings page. Persist the choice and respect the system setting by default."),
			use("Read", "src/settings/SettingsPage.tsx"),
			result("export function SettingsPage() { … } (120 lines)"),
			use("Edit", "src/settings/SettingsPage.tsx"),
			result("Updated src/settings/SettingsPage.tsx (+27 −2)"),
			say("The toggle stores `theme` in local storage and falls back to `prefers-color-scheme`."),
		},
	},
	{
		id: "f60718a2-3a4b-45c6-d7e8-f90a1b2c3da6", project: root + "field-notes", branch: "",
		title: "Draft the Q3 architecture review from the meeting notes",
		model: acmeModel, ago: 5 * time.Hour, length: 50 * time.Minute,
		status: core.StatusEnded, context: 88_200, cost: core.CostNone, turns: 5,
		convo: []line{
			prompt("Turn notes/2026-q3/*.md into a draft architecture review: decisions, open questions, risks."),
			use("Glob", "notes/2026-q3/*.md"),
			result("notes/2026-q3/week-01.md\nnotes/2026-q3/week-02.md\nnotes/2026-q3/week-03.md"),
			say("Here is a first draft with three decisions, five open questions and a risk table."),
		},
	},
	{
		id: "0718a2b3-4b5c-46d7-e8f9-0a1b2c3d4eb7", project: root + "lattice", branch: "main",
		title: "Investigate memory growth in the job scheduler",
		model: opus, ago: 26 * time.Hour, length: 70 * time.Minute,
		status: core.StatusEnded, context: 102_000, window: window, cost: core.CostEstimated, turns: 6,
		convo: append([]line{
			prompt("The scheduler's RSS grows by about 40 MB an hour. Find the leak."),
			use("Bash", "go tool pprof -top heap.out"),
			result("flat  flat%   sum%\n 310MB 61.2% 61.2%  scheduler.(*queue).push"),
		}, investigation(36)...),
	},
	{
		id: "18a2b3c4-5c6d-47e8-f90a-1b2c3d4e5fc8", project: root + "kiln", branch: "toml-config",
		title: "Port the config loader to TOML",
		model: haiku, ago: 2 * time.Minute, length: 15 * time.Minute,
		status: core.StatusUnknown, pid: 42010, name: "kiln-6",
		context: 21_000, window: window, cost: core.CostEstimated, turns: 1,
		convo: []line{
			prompt("Port the config loader from YAML to TOML and keep reading the old file for one release."),
			use("Read", "internal/config/load.go"),
			result("package config … (96 lines)"),
		},
	},
	{
		id: "2a3b4c5d-6e7f-48a9-b0c1-d2e3f4a5b6d9", project: root + "orbit-api", branch: "docs/adr-audit",
		title: "Write an ADR comparing event sourcing with plain CRUD for the audit log",
		model: opus, ago: 3*24*time.Hour + 2*time.Hour, length: 40 * time.Minute,
		status: core.StatusEnded, context: 64_000, window: window, cost: core.CostReported, reported: 6.42, turns: 5,
		convo: []line{
			prompt("Write an ADR comparing event sourcing with plain CRUD plus an audit table for the audit log."),
			say("I recommend CRUD with an append-only audit table: the replay benefits do not justify the operational cost yet."),
		},
	},
	{
		id: "3b4c5d6e-7f80-49ba-c1d2-e3f4a5b6c7ea", project: root + "ledger-sync", branch: "deps",
		title: "Bump dependencies and fix the two breaking API changes",
		model: sonnet, ago: 12 * 24 * time.Hour, length: 30 * time.Minute,
		status: core.StatusEnded, context: 45_000, window: window, cost: core.CostEstimated, turns: 3,
		convo: []line{
			prompt("Bump all dependencies and fix whatever breaks."),
			use("Bash", "go get -u ./... && go build ./..."),
			failed("client.go:52: undefined: retry.WithMax"),
			say("Two APIs changed: retry options were renamed and the HTTP client takes a context. Both are fixed."),
		},
	},
	{
		id: "4c5d6e7f-8091-4acb-d2e3-f4a5b6c7d8fb", project: root + "dotfiles", branch: "main",
		title: "Tidy shell aliases",
		model: haiku, ago: 40 * 24 * time.Hour, length: 8 * time.Minute,
		status: core.StatusEnded, context: 9_000, window: window, cost: core.CostEstimated, turns: 1,
		convo: []line{
			prompt("Remove aliases I never use and group the rest by tool."),
			say("Removed 23 unused aliases and grouped the rest under git, docker and kubectl headings."),
		},
	},
	{
		id: "5d6e7f80-91a2-4bdc-e3f4-a5b6c7d8e90c", project: root + "kiln", title: "kiln-7",
		ago: 30 * time.Second, length: 30 * time.Second,
		status: core.StatusIdle, pid: 42315, name: "kiln-7", cost: core.CostNone,
	},
}

// investigation is a long, varied stretch of debugging, for a transcript
// worth scrolling and searching: rounds rounds of narrowing down, ending
// with the fix.
func investigation(rounds int) []line {
	suspects := []string{"queue", "retry index", "timer wheel", "lease table", "metrics registry", "worker pool"}
	var out []line
	for i := range rounds {
		s := suspects[i%len(suspects)]
		if i%6 == 0 && i > 0 {
			out = append(out, prompt(fmt.Sprintf("Still growing after round %d. Keep going, and check the %s next.", i, s)))
		}
		out = append(out,
			think(fmt.Sprintf("If the %s holds references after a job finishes, the heap grows with every run.", s)),
			say(fmt.Sprintf("Round %d: checking whether the %s releases finished jobs.\n\nI'll take two heap profiles a minute apart and compare them.", i+1, s)),
			use("Bash", fmt.Sprintf("go test ./scheduler/ -run TestSoak -count=1 -memprofile round%02d.out", i+1)),
		)
		if i%5 == 3 {
			out = append(out, failed(fmt.Sprintf("--- FAIL: TestSoak (31.4s)\n    soak_test.go:58: heap grew by %d MB over 10000 jobs\nFAIL\nexit status 1", 12+i)))
		} else {
			out = append(out, result(fmt.Sprintf("ok  \texample.com/scheduler\t%d.%ds\nheap after: %d MB\nobjects retained by the %s: %d", 30+i%7, i%10, 180+3*i, s, 1000+97*i)))
		}
		out = append(out,
			use("Read", fmt.Sprintf("scheduler/%s.go", strings.ReplaceAll(s, " ", "_"))),
			result(fmt.Sprintf("package scheduler\n\n// %s … (%d lines)", s, 80+11*i)),
		)
	}
	return append(out,
		say("Finished jobs are never removed from the retry index. I added a cleanup on completion and a test that checks the index is empty after a run."),
	)
}

// workScript is what a busy session does, one step per tick.
var workScript = []line{
	think("Checking how this interacts with the existing tests."),
	say("Next I'll look at the callers before changing the signature."),
	use("Grep", "func New in internal/"),
	result("4 matches in 3 files"),
	use("Read", "internal/service/service.go"),
	result("package service … (212 lines)"),
	use("Edit", "internal/service/service.go"),
	result("Updated internal/service/service.go (+18 −6)"),
	use("Bash", "go test ./..."),
	result("ok  \texample.com/service\t0.412s"),
	say("Tests pass. Moving on to the documentation for the new option."),
}

var followUps = []string{
	"Looks good. Now add a test for the empty case.",
	"Can you also update the README section about configuration?",
	"Run the linter and fix whatever it reports.",
	"Why a mutex here instead of a channel?",
}

var waits = []string{
	"approve Bash: make lint",
	"approve Edit: internal/service/service.go",
	"approve Bash: go test -race ./...",
	"answer: Which config format should the loader keep supporting?",
}
