package claude

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Paths is the layout of a Claude directory.
type Paths struct {
	Dir        string // the Claude directory, normally ~/.claude
	ConfigFile string // Claude Code's global config file, normally ~/.claude.json
}

// ResolvePaths finds the Claude directory: dir when not empty, else
// $CLAUDE_CONFIG_DIR, else ~/.claude. The global config file is
// <dir>/.claude.json when the directory was given explicitly (by dir or
// the environment) and that file exists, else ~/.claude.json. Nothing is
// required to exist.
func ResolvePaths(dir string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil && dir == "" && os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		return Paths{}, fmt.Errorf("locate home directory: %w", err)
	}
	return resolvePaths(dir, os.Getenv("CLAUDE_CONFIG_DIR"), home), nil
}

func resolvePaths(dir, env, home string) Paths {
	explicit := true
	switch {
	case dir != "":
	case env != "":
		dir = env
	default:
		dir, explicit = filepath.Join(home, ".claude"), false
	}
	p := Paths{Dir: filepath.Clean(dir), ConfigFile: filepath.Join(home, ".claude.json")}
	if explicit {
		inside := filepath.Join(p.Dir, ".claude.json")
		if _, err := os.Stat(inside); err == nil || home == "" {
			p.ConfigFile = inside
		}
	}
	return p
}

// ProjectsDir holds one directory per project, each with the transcripts
// of its sessions.
func (p Paths) ProjectsDir() string { return filepath.Join(p.Dir, "projects") }

// SessionsDir holds the live session registry.
func (p Paths) SessionsDir() string { return filepath.Join(p.Dir, "sessions") }

// HistoryFile is the prompt history.
func (p Paths) HistoryFile() string { return filepath.Join(p.Dir, "history.jsonl") }

// CatalogDir holds the model catalog.
func (p Paths) CatalogDir() string { return filepath.Join(p.Dir, "cache", "model-catalog") }

// TranscriptFile is the main transcript of one session.
type TranscriptFile struct {
	SessionID string
	Path      string
}

// Transcripts lists every session transcript, projects/<slug>/<id>.jsonl.
// A missing projects directory means no transcripts.
func (p Paths) Transcripts() ([]TranscriptFile, error) {
	projects, err := os.ReadDir(p.ProjectsDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []TranscriptFile
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		dir := filepath.Join(p.ProjectsDir(), proj.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue // vanished or unreadable: skip the project
		}
		for _, e := range ents {
			name := e.Name()
			if e.Type().IsRegular() && strings.HasSuffix(name, ".jsonl") && len(name) > len(".jsonl") {
				out = append(out, TranscriptFile{
					SessionID: strings.TrimSuffix(name, ".jsonl"),
					Path:      filepath.Join(dir, name),
				})
			}
		}
	}
	return out, nil
}

// SessionOf maps a path under ProjectsDir to the session it belongs to: a
// transcript projects/<slug>/<id>.jsonl, or anything under
// projects/<slug>/<id>/ such as a subagent transcript. ok is false for any
// other path.
func (p Paths) SessionOf(path string) (id string, ok bool) {
	rel, err := filepath.Rel(p.ProjectsDir(), path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case len(parts) == 2 && strings.HasSuffix(parts[1], ".jsonl"):
		id = strings.TrimSuffix(parts[1], ".jsonl")
	case len(parts) > 2:
		id = parts[1]
	}
	return id, id != ""
}

// SubagentFile is one subagent transcript of a session.
type SubagentFile struct {
	ID       string // agent id
	Path     string // agent-<id>.jsonl
	MetaPath string // agent-<id>.meta.json beside it; may not exist
}

// SubagentsDir is where a session's subagent transcripts live, given the
// session's transcript path.
func SubagentsDir(transcript string) string {
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
}

// SubagentFiles lists the subagent transcripts of the session whose main
// transcript is at transcript. They are found anywhere under SubagentsDir,
// including nested directories such as workflows/<run>/. A session with no
// subagents yields none.
func SubagentFiles(transcript string) ([]SubagentFile, error) {
	root := SubagentsDir(transcript)
	var out []SubagentFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			if d != nil && d.IsDir() && path != root {
				return filepath.SkipDir // unreadable subdirectory
			}
			return err
		}
		name := d.Name()
		if !d.Type().IsRegular() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		base := strings.TrimSuffix(path, ".jsonl")
		id := strings.TrimPrefix(strings.TrimSuffix(name, ".jsonl"), "agent-")
		if id != "" {
			out = append(out, SubagentFile{ID: id, Path: path, MetaPath: base + ".meta.json"})
		}
		return nil
	})
	return out, err
}

// SubagentMeta describes a subagent, from its .meta.json file.
type SubagentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Model       string `json:"model"` // as requested, often an alias such as "sonnet"
	ToolUseID   string `json:"toolUseId"`
}

// ReadSubagentMeta reads a subagent's .meta.json file.
func ReadSubagentMeta(path string) (SubagentMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SubagentMeta{}, err
	}
	var m SubagentMeta
	if err := decodeTolerant(data, &m); err != nil {
		return SubagentMeta{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return m, nil
}
