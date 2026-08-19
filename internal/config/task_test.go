package config

import (
	"path/filepath"
	"testing"
)

func TestLoadTask_ValidYAML_AppliesDefaults(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", `
name: agents-refresh
description: Keep AGENTS.md current
model: claude-sonnet-4-6
context: [agents_md, diff_since_agents_update]
source_globs: ["**/*.go"]
output:
  type: pr
  branch_prefix: chore/
  skip_marker: NO_CHANGES_NEEDED
`)

	cfg, err := LoadTask(path)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if cfg.Name != "agents-refresh" {
		t.Errorf("Name = %q, want %q", cfg.Name, "agents-refresh")
	}
	if cfg.Output.Type != "pr" {
		t.Errorf("Output.Type = %q, want %q (explicit, not overridden)", cfg.Output.Type, "pr")
	}
	if cfg.MaxDiffTokens != 3000 {
		t.Errorf("MaxDiffTokens = %d, want 3000 (default)", cfg.MaxDiffTokens)
	}
}

func TestLoadTask_OutputTypeDefaultsToStdout(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", `
name: agents-refresh
context: [agents_md]
`)

	cfg, err := LoadTask(path)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if cfg.Output.Type != "stdout" {
		t.Errorf("Output.Type = %q, want %q (default)", cfg.Output.Type, "stdout")
	}
}

func TestLoadTask_ExplicitMaxDiffTokens_NotOverridden(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", `
name: agents-refresh
context: [agents_md]
max_diff_tokens: 500
`)

	cfg, err := LoadTask(path)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}
	if cfg.MaxDiffTokens != 500 {
		t.Errorf("MaxDiffTokens = %d, want 500 (explicit)", cfg.MaxDiffTokens)
	}
}

func TestLoadTask_MissingName_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", `
context: [agents_md]
`)

	_, err := LoadTask(path)
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
}

func TestLoadTask_EmptyContext_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", `
name: agents-refresh
context: []
`)

	_, err := LoadTask(path)
	if err == nil {
		t.Fatal("expected error for empty context list, got nil")
	}
}

func TestLoadTask_FileNotFound_ReturnsError(t *testing.T) {
	_, err := LoadTask(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadTask_InvalidYAML_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "task.yaml", "name: [this is not: valid yaml")

	_, err := LoadTask(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}
