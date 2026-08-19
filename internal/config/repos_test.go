package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempYAML(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadRepos_ValidYAML_DefaultsBranchToMain(t *testing.T) {
	path := writeTempYAML(t, "repos.yaml", `
repos:
  - repo: example/repo-a
    tasks: [agents-refresh]
  - repo: example/repo-b
    tasks: [agents-refresh]
    branch: develop
`)

	cfg, err := LoadRepos(path)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(cfg.Repos))
	}
	if cfg.Repos[0].Branch != "main" {
		t.Errorf("Repos[0].Branch = %q, want %q (default)", cfg.Repos[0].Branch, "main")
	}
	if cfg.Repos[1].Branch != "develop" {
		t.Errorf("Repos[1].Branch = %q, want %q (explicit, not overridden)", cfg.Repos[1].Branch, "develop")
	}
}

func TestLoadRepos_MissingRepoField_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "repos.yaml", `
repos:
  - tasks: [agents-refresh]
`)

	_, err := LoadRepos(path)
	if err == nil {
		t.Fatal("expected error for missing repo field, got nil")
	}
}

func TestLoadRepos_NoTasksAssigned_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "repos.yaml", `
repos:
  - repo: example/repo-a
    tasks: []
`)

	_, err := LoadRepos(path)
	if err == nil {
		t.Fatal("expected error for empty tasks list, got nil")
	}
}

func TestLoadRepos_FileNotFound_ReturnsError(t *testing.T) {
	_, err := LoadRepos(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadRepos_InvalidYAML_ReturnsError(t *testing.T) {
	path := writeTempYAML(t, "repos.yaml", "repos: [this is not valid: yaml: at all")

	_, err := LoadRepos(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestLoadRepos_EmptyFile_ReturnsNoRepos(t *testing.T) {
	path := writeTempYAML(t, "repos.yaml", "")

	cfg, err := LoadRepos(path)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("len(Repos) = %d, want 0", len(cfg.Repos))
	}
}
