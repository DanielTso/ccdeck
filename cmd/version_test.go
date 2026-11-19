package cmd

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	rev := func(r string, modified ...string) []debug.BuildSetting {
		s := []debug.BuildSetting{{Key: "vcs.revision", Value: r}}
		for _, m := range modified {
			s = append(s, debug.BuildSetting{Key: "vcs.modified", Value: m})
		}
		return s
	}
	tests := []struct {
		name         string
		v, c         string
		info         debug.BuildInfo
		wantV, wantC string
	}{
		{
			name: "go install module@version uses the module version",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{Main: debug.Module{Version: "v0.1.1"}},
			wantV: "v0.1.1", wantC: "none",
		},
		{
			name: "local build uses the vcs revision",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: rev("abc1234def5678")},
			wantV: "dev", wantC: "abc1234",
		},
		{
			name: "uncommitted changes are marked dirty",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: rev("abc1234def5678", "true")},
			wantV: "dev", wantC: "abc1234-dirty",
		},
		{
			name: "modified false is clean",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: rev("abc1234def5678", "false")},
			wantV: "dev", wantC: "abc1234",
		},
		{
			name: "a short revision is kept whole",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{Settings: rev("abc")},
			wantV: "dev", wantC: "abc",
		},
		{
			name: "no build information changes nothing",
			v:    "dev", c: "none",
			info:  debug.BuildInfo{},
			wantV: "dev", wantC: "none",
		},
		{
			name: "ldflags values win",
			v:    "1.2.3", c: "f00baa1",
			info:  debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}, Settings: rev("deadbeefcafe", "true")},
			wantV: "1.2.3", wantC: "f00baa1",
		},
		{
			name: "an ldflags version alone still lets the revision fill in the commit",
			v:    "1.2.3", c: "none",
			info:  debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}, Settings: rev("deadbeefcafe")},
			wantV: "1.2.3", wantC: "deadbee",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotV, gotC := fromBuildInfo(tt.v, tt.c, &tt.info)
			if gotV != tt.wantV || gotC != tt.wantC {
				t.Errorf("got (%q, %q), want (%q, %q)", gotV, gotC, tt.wantV, tt.wantC)
			}
		})
	}
}

func TestVersionString(t *testing.T) {
	old, oldCommit := version, commit
	t.Cleanup(func() { version, commit = old, oldCommit })
	tests := []struct{ v, c, want string }{
		{"1.2.3", "abc1234", "1.2.3 (abc1234)"},
		{"1.2.3", "none", "1.2.3"},
	}
	for _, tt := range tests {
		version, commit = tt.v, tt.c
		if got := versionString(); got != tt.want {
			t.Errorf("versionString() with %q, %q = %q, want %q", tt.v, tt.c, got, tt.want)
		}
	}
}
