// Package logfixture locates the committed vendor log fixtures so tests
// never hardcode repo-relative paths or session UUIDs directly.
package logfixture

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Dir returns the absolute path of this package's directory, resolved via
// runtime.Caller so callers work regardless of their own package location.
func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("logfixture: unable to determine caller info")
	}
	return filepath.Dir(file)
}

// ClaudeProjectsDir returns Dir()/claude/projects, mirroring ~/.claude/projects.
func ClaudeProjectsDir() string {
	return filepath.Join(Dir(), "claude", "projects")
}

// PathologicalRoot returns Dir()/pathological.
func PathologicalRoot() string {
	return filepath.Join(Dir(), "pathological")
}

// Fixture project directory names under ClaudeProjectsDir(). Named
// constants so tests never hardcode the encoded-cwd directory name.
const (
	// ClaudeSidechainProject spawns a subagent: parent session + a nested
	// <session-uuid>/subagents/agent-<agentId>.jsonl sidechain transcript.
	ClaudeSidechainProject = "-home-user-fixture-sidechain"
	// ClaudeToolErrorProject contains a failed Bash tool call (is_error: true).
	ClaudeToolErrorProject = "-home-user-fixture-tool-error"
)

// SessionFile returns the single top-level *.jsonl file in the named
// project directory under ClaudeProjectsDir(). Errors if there is not
// exactly one.
func SessionFile(project string) (string, error) {
	dir := filepath.Join(ClaudeProjectsDir(), project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("logfixture: reading %s: %w", dir, err)
	}

	var found []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".jsonl" {
			found = append(found, e.Name())
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("logfixture: expected exactly one top-level *.jsonl in %s, found %d", dir, len(found))
	}
	return filepath.Join(dir, found[0]), nil
}

// SubagentFiles returns the *.jsonl files under <session-uuid>/subagents/
// for the named project directory. Returns an empty slice (not an error)
// if the project has no sidechain transcripts.
func SubagentFiles(project string) ([]string, error) {
	dir := filepath.Join(ClaudeProjectsDir(), project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("logfixture: reading %s: %w", dir, err)
	}

	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(dir, e.Name(), "subagents")
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("logfixture: reading %s: %w", subDir, err)
		}
		for _, se := range subEntries {
			if !se.IsDir() && filepath.Ext(se.Name()) == ".jsonl" {
				out = append(out, filepath.Join(subDir, se.Name()))
			}
		}
	}
	return out, nil
}
