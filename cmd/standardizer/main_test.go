package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/runner"
)

// --- pure helpers ---

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		vals []string
		want string
	}{
		{"first wins", []string{"a", "b"}, "a"},
		{"skips leading empties", []string{"", "", "c"}, "c"},
		{"all empty returns empty", []string{"", ""}, ""},
		{"no args returns empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.vals...); got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.vals, got, tc.want)
			}
		})
	}
}

func TestResolveModel_FlagTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("LLM_MODEL", "env-model")
	if got := resolveModel("flag-model"); got != "flag-model" {
		t.Errorf("resolveModel = %q, want %q", got, "flag-model")
	}
}

func TestResolveModel_FallsBackToEnv(t *testing.T) {
	t.Setenv("LLM_MODEL", "env-model")
	if got := resolveModel(""); got != "env-model" {
		t.Errorf("resolveModel = %q, want %q", got, "env-model")
	}
}

func TestResolveModel_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("LLM_MODEL", "")
	if got := resolveModel(""); got != "claude-sonnet-4-6" {
		t.Errorf("resolveModel = %q, want default %q", got, "claude-sonnet-4-6")
	}
}

func TestResolveBaseURL_FlagTakesPrecedence(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "https://env.example.com")
	if got := resolveBaseURL("https://flag.example.com"); got != "https://flag.example.com" {
		t.Errorf("resolveBaseURL = %q, want flag value", got)
	}
}

func TestResolveBaseURL_FallsBackToEnv(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "https://env.example.com")
	if got := resolveBaseURL(""); got != "https://env.example.com" {
		t.Errorf("resolveBaseURL = %q, want env value", got)
	}
}

func TestResolveBaseURL_EmptyWhenUnset(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "")
	if got := resolveBaseURL(""); got != "" {
		t.Errorf("resolveBaseURL = %q, want empty string", got)
	}
}

// --- tasks list / validate ---

func writeTaskFixture(t *testing.T, dir, name string) {
	t.Helper()
	content := fmt.Sprintf(`
name: %s
description: a test task
model: test-model
context: [readme]
output:
  type: stdout
`, name)
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write task fixture: %v", err)
	}
}

func TestListTasks_ValidDir_ReturnsNilError(t *testing.T) {
	dir := t.TempDir()
	writeTaskFixture(t, dir, "task-a")
	writeTaskFixture(t, dir, "task-b")

	if err := listTasks(dir); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
}

func TestListTasks_InvalidTaskFile_StillReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("context: []\n"), 0644); err != nil {
		t.Fatalf("write broken.yaml: %v", err)
	}

	// listTasks prints per-file errors but only fails outright if the
	// directory itself can't be read.
	if err := listTasks(dir); err != nil {
		t.Fatalf("listTasks: %v, want nil (per-file errors are printed, not returned)", err)
	}
}

func TestListTasks_DirNotFound_ReturnsError(t *testing.T) {
	if err := listTasks(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for missing tasks dir, got nil")
	}
}

func TestValidateTasks_ValidDir_ReturnsNilError(t *testing.T) {
	dir := t.TempDir()
	writeTaskFixture(t, dir, "task-a")

	if err := validateTasks(dir); err != nil {
		t.Fatalf("validateTasks: %v", err)
	}
}

func TestValidateTasks_InvalidTaskFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("context: []\n"), 0644); err != nil {
		t.Fatalf("write broken.yaml: %v", err)
	}

	err := validateTasks(dir)
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("err = %v, want it to mention 'validation failed'", err)
	}
}

func TestValidateTasks_DirNotFound_ReturnsError(t *testing.T) {
	if err := validateTasks(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for missing tasks dir, got nil")
	}
}

// --- writeStepSummary ---

func TestWriteStepSummary_EnvUnset_NoOp(t *testing.T) {
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	// Must not panic; nothing to write to, nothing to assert beyond survival.
	writeStepSummary([]runner.Result{{Repo: "r", Task: "t", Status: "no_changes"}})
}

func TestWriteStepSummary_WritesMarkdownTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", path)

	results := []runner.Result{
		{Repo: "example/repo", Task: "agents-refresh", Status: "pr_opened", Detail: "https://github.com/example/repo/pull/1"},
		{Repo: "example/repo2", Task: "agents-refresh", Status: "error", Detail: "boom"},
	}
	writeStepSummary(results)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read step summary: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "example/repo") || !strings.Contains(got, "pr_opened") {
		t.Errorf("step summary = %q, want it to mention the pr_opened result", got)
	}
	if !strings.Contains(got, "1 pr_opened") || !strings.Contains(got, "1 errors") {
		t.Errorf("step summary = %q, want counts line with 1 pr_opened and 1 errors", got)
	}
}

func TestWriteStepSummary_UnwritablePath_DoesNotPanic(t *testing.T) {
	// Parent directory does not exist, so OpenFile fails; writeStepSummary
	// must swallow the error rather than panic.
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "no-such-dir", "summary.md"))
	writeStepSummary([]runner.Result{{Repo: "r", Task: "t", Status: "no_changes"}})
}

// --- runCmdRunE integration ---

func fakeLLMServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": response}},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func setupGitRepoWithReadme(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available in PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		// scan-fix(golangci:noctx): exec.CommandContext, not exec.Command —
		// test helper; context.Background() is a no-op for these
		// short-lived local git commands.
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test Runner")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}

func writeTaskWithPrompt(t *testing.T, tasksDir, name string) {
	t.Helper()
	writeTaskFixture(t, tasksDir, name)
	if err := os.WriteFile(filepath.Join(tasksDir, name+".md"), []byte("Context: {{.readme}}\n"), 0644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

func TestRunCmdRunE_NoAPIKeyAndNotDryRun_ReturnsError(t *testing.T) {
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")

	err := runCmdRunE(nil, runCmdFlags{dryRun: false})
	if err == nil || !strings.Contains(err.Error(), "no LLM API key") {
		t.Fatalf("err = %v, want it to mention 'no LLM API key'", err)
	}
}

func TestRunCmdRunE_DryRun_LocalMode_Succeeds(t *testing.T) {
	repoDir := setupGitRepoWithReadme(t)
	tasksDir := t.TempDir()
	writeTaskWithPrompt(t, tasksDir, "task-a")

	err := runCmdRunE(nil, runCmdFlags{
		tasksDir: tasksDir,
		dryRun:   true,
		localDir: repoDir,
		repoSlug: "example/repo",
	})
	if err != nil {
		t.Fatalf("runCmdRunE: %v", err)
	}
}

func TestRunCmdRunE_LLMFailure_ReturnsTaskFailedError(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	repoDir := setupGitRepoWithReadme(t)
	tasksDir := t.TempDir()
	writeTaskWithPrompt(t, tasksDir, "task-a")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"message":"boom"}}`)
	}))
	t.Cleanup(server.Close)

	err := runCmdRunE(nil, runCmdFlags{
		tasksDir: tasksDir,
		localDir: repoDir,
		repoSlug: "example/repo",
		baseURL:  server.URL,
	})
	if err == nil || !strings.Contains(err.Error(), "task(s) failed") {
		t.Fatalf("err = %v, want it to mention 'task(s) failed'", err)
	}
}

func TestRunCmdRunE_OutputDirSet_WritesSummaryJSON(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	repoDir := setupGitRepoWithReadme(t)
	tasksDir := t.TempDir()
	writeTaskWithPrompt(t, tasksDir, "task-a")
	server := fakeLLMServer(t, "<action>update</action><content>done</content>")
	outputDir := t.TempDir()

	err := runCmdRunE(nil, runCmdFlags{
		tasksDir:  tasksDir,
		localDir:  repoDir,
		repoSlug:  "example/repo",
		baseURL:   server.URL,
		outputDir: outputDir,
	})
	if err != nil {
		t.Fatalf("runCmdRunE: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outputDir, "summary.json")); statErr != nil {
		t.Errorf("expected summary.json to be written: %v", statErr)
	}
}

// --- cobra command wiring ---

func TestRootCmd_Execute_RunDryRun(t *testing.T) {
	repoDir := setupGitRepoWithReadme(t)
	tasksDir := t.TempDir()
	writeTaskWithPrompt(t, tasksDir, "task-a")

	cmd := rootCmd()
	cmd.SetArgs([]string{
		"run",
		"--dry-run",
		"--tasks-dir", tasksDir,
		"--local-dir", repoDir,
		"--repo-slug", "example/repo",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestRootCmd_Execute_TasksListAndValidate(t *testing.T) {
	tasksDir := t.TempDir()
	writeTaskFixture(t, tasksDir, "task-a")

	for _, sub := range [][]string{
		{"tasks", "list", "--tasks-dir", tasksDir},
		{"tasks", "validate", "--tasks-dir", tasksDir},
	} {
		cmd := rootCmd()
		cmd.SetArgs(sub)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute(%v): %v", sub, err)
		}
	}
}
