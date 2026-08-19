package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/llm"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- git fixture helpers (mirrors internal/context's test helpers; this is
// a different package so it needs its own copy) ---

func setupGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available in PATH")
	}
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-q")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test Runner")
	runGitCmd(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// scan-fix(golangci:noctx): exec.CommandContext, not exec.Command — test
	// helper; context.Background() is a no-op for these short-lived local
	// git commands.
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeAndCommit(t *testing.T, dir, relPath, content, msg string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
	runGitCmd(t, dir, "add", relPath)
	runGitCmd(t, dir, "commit", "-q", "-m", msg)
}

// --- task fixture helpers ---

// writeTask writes tasks/<name>.yaml (+ tasks/<name>.md unless skipPrompt).
func writeTask(t *testing.T, tasksDir, name, contextKey, outputType string, skipPrompt bool) {
	t.Helper()
	yamlContent := fmt.Sprintf(`
name: %s
description: test task
model: test-model
context: [%s]
output:
  type: %s
`, name, contextKey, outputType)
	if err := os.WriteFile(filepath.Join(tasksDir, name+".yaml"), []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write task yaml: %v", err)
	}
	if !skipPrompt {
		if err := os.WriteFile(filepath.Join(tasksDir, name+".md"), []byte("Context: {{index . \""+contextKey+"\"}}\n"), 0644); err != nil {
			t.Fatalf("write task prompt: %v", err)
		}
	}
}

// fakeLLMServer returns an httptest server that always answers chat
// completions with the given response content.
func fakeLLMServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "test",
			"object": "chat.completion",
			"model":  "test-model",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": response}},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func baseOptions(tasksDir string, llmServer *httptest.Server) Options {
	return Options{
		TasksDir: tasksDir,
		LLMConfig: llm.Config{
			BaseURL:    llmServer.URL,
			APIKey:     "test-key",
			Model:      "test-model",
			MaxRetries: 1,
		},
		Logger: testLogger(),
	}
}

// --- loadTaskConfigs ---

func TestLoadTaskConfigs_ValidDir_ReturnsAllTasksKeyedByName(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	writeTask(t, dir, "task-b", "readme", "stdout", false)

	cfgs, err := loadTaskConfigs(dir)
	if err != nil {
		t.Fatalf("loadTaskConfigs: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("len(cfgs) = %d, want 2", len(cfgs))
	}
	if _, ok := cfgs["task-a"]; !ok {
		t.Error("missing task-a")
	}
	if _, ok := cfgs["task-b"]; !ok {
		t.Error("missing task-b")
	}
}

func TestLoadTaskConfigs_SkipsNonYAMLFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir.yaml"), 0755); err != nil {
		t.Fatalf("mkdir subdir.yaml: %v", err)
	}

	cfgs, err := loadTaskConfigs(dir)
	if err != nil {
		t.Fatalf("loadTaskConfigs: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("len(cfgs) = %d, want 1 (non-yaml file and dir should be skipped)", len(cfgs))
	}
}

func TestLoadTaskConfigs_InvalidTaskFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("context: []\n"), 0644); err != nil {
		t.Fatalf("write broken.yaml: %v", err)
	}

	_, err := loadTaskConfigs(dir)
	if err == nil {
		t.Fatal("expected error for invalid task config, got nil")
	}
}

func TestLoadTaskConfigs_DirNotFound_ReturnsError(t *testing.T) {
	_, err := loadTaskConfigs(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected error for missing tasks dir, got nil")
	}
}

// --- buildGHClient ---

func TestBuildGHClient_EmptyToken_ReturnsNil(t *testing.T) {
	if got := buildGHClient(context.Background(), ""); got != nil {
		t.Errorf("buildGHClient(\"\") = %v, want nil", got)
	}
}

func TestBuildGHClient_NonEmptyToken_ReturnsClient(t *testing.T) {
	if got := buildGHClient(context.Background(), "ghp_test"); got == nil {
		t.Error("buildGHClient(token) = nil, want non-nil client")
	}
}

// --- Run: local mode ---

func TestRun_LocalMode_NoRepoSlugOrEnv_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	// scan-fix(go:ci-env-leak): blank GITHUB_REPOSITORY — it's ambient in every
	// GitHub Actions job, so without this the test silently picked up the real
	// repo slug from the CI environment and failed only in CI, never locally.
	t.Setenv("GITHUB_REPOSITORY", "")

	opts := baseOptions(dir, server)
	opts.LocalDir = setupGitRepo(t)

	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "--repo-slug") {
		t.Fatalf("err = %v, want it to mention --repo-slug", err)
	}
}

func TestRun_LocalMode_RepoSlugFromEnvVar(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "<action>update</action><content>done</content>")

	t.Setenv("GITHUB_REPOSITORY", "example/repo-from-env")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.DryRun = true // keep it simple: just confirm the slug parses and the run proceeds

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Repo != "example/repo-from-env" {
		t.Fatalf("results = %+v, want one result for example/repo-from-env", results)
	}
}

func TestRun_LocalMode_InvalidSlugFormat_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.LocalDir = setupGitRepo(t)
	opts.RepoSlug = "no-slash-here"

	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "invalid repo slug") {
		t.Fatalf("err = %v, want it to mention 'invalid repo slug'", err)
	}
}

func TestRun_LocalMode_DryRun_SkipsLLMCall(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "should not be called")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"
	opts.DryRun = true

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Status != "skipped" || results[0].Detail != "[dry-run]" {
		t.Errorf("results[0] = %+v, want status=skipped detail=[dry-run]", results[0])
	}
}

func TestRun_LocalMode_TaskFilter_RunsOnlyMatchingTask(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	writeTask(t, dir, "task-b", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"
	opts.DryRun = true
	opts.TaskFilter = "task-a"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Task != "task-a" {
		t.Fatalf("results = %+v, want exactly one result for task-a", results)
	}
}

func TestRun_LocalMode_NoAgentsMD_SkipsWithReason(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init") // no AGENTS.md
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "agents_md", "stdout", false)
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "skipped" || results[0].Detail != "no AGENTS.md" {
		t.Fatalf("results = %+v, want status=skipped detail='no AGENTS.md'", results)
	}
}

func TestRun_LocalMode_UnknownContextKey_ReturnsErrorResult(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "not_a_real_key", "stdout", false)
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || !strings.HasPrefix(results[0].Detail, "context: ") {
		t.Fatalf("results = %+v, want status=error detail starting with 'context: '", results)
	}
}

func TestRun_LocalMode_MissingPromptTemplate_ReturnsRenderErrorResult(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", true) // skipPrompt=true: no .md file
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || !strings.HasPrefix(results[0].Detail, "render: ") {
		t.Fatalf("results = %+v, want status=error detail starting with 'render: '", results)
	}
}

func TestRun_LocalMode_LLMSuccess_StdoutOutput_ReturnsNoChanges(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "<action>update</action><content>new stuff</content>")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "no_changes" {
		t.Fatalf("results = %+v, want status=no_changes (stdout output type never opens a PR)", results)
	}
}

func TestRun_LocalMode_LLMReturnsSkipMarker_ReturnsNoChanges(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "NO_CHANGES_NEEDED")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "no_changes" {
		t.Fatalf("results = %+v, want status=no_changes", results)
	}
}

func TestRun_LocalMode_LLMError_ReturnsErrorResult(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"message":"boom","type":"server_error"}}`)
	}))
	t.Cleanup(server.Close)

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || !strings.HasPrefix(results[0].Detail, "llm: ") {
		t.Fatalf("results = %+v, want status=error detail starting with 'llm: '", results)
	}
}

func TestRun_LocalMode_OutputTypePR_NoGHToken_ReturnsErrorResult(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "pr", false)
	server := fakeLLMServer(t, "<action>update</action><content>c</content>")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"
	opts.GHToken = "" // no token: prHandler.GHClient will be nil

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || results[0].Detail != "GH_TOKEN not set" {
		t.Fatalf("results = %+v, want status=error detail='GH_TOKEN not set'", results)
	}
}

func TestRun_LocalMode_UnknownOutputType_ReturnsErrorResult(t *testing.T) {
	repoDir := setupGitRepo(t)
	writeAndCommit(t, repoDir, "README.md", "hello", "init")
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "carrier-pigeon", false)
	server := fakeLLMServer(t, "<action>update</action><content>c</content>")

	opts := baseOptions(dir, server)
	opts.LocalDir = repoDir
	opts.RepoSlug = "example/repo"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || results[0].Detail != "unknown output type: carrier-pigeon" {
		t.Fatalf("results = %+v, want status=error detail='unknown output type: carrier-pigeon'", results)
	}
}

func TestRun_TasksDirNotFound_ReturnsError(t *testing.T) {
	server := fakeLLMServer(t, "unused")
	opts := baseOptions(filepath.Join(t.TempDir(), "missing-tasks"), server)
	opts.LocalDir = setupGitRepo(t)
	opts.RepoSlug = "example/repo"

	_, err := Run(context.Background(), opts)
	if err == nil {
		t.Fatal("expected error for missing tasks dir, got nil")
	}
}

// --- Run: central mode (cloneRepo faked to avoid real network calls) ---

// withFakeClone substitutes cloneRepo for the duration of the test.
func withFakeClone(t *testing.T, fake func(ctx context.Context, repoURL, destDir string) error) {
	t.Helper()
	orig := cloneRepo
	cloneRepo = fake
	t.Cleanup(func() { cloneRepo = orig })
}

func TestRun_CentralMode_ReposConfigNotFound_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	opts := baseOptions(dir, server)
	opts.ReposConfig = filepath.Join(t.TempDir(), "missing-repos.yaml")

	_, err := Run(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "load repos config") {
		t.Fatalf("err = %v, want it to mention 'load repos config'", err)
	}
}

func TestRun_CentralMode_InvalidRepoSlug_SkipsEntrySilently(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(reposPath, []byte("repos:\n  - repo: no-slash-here\n    tasks: [task-a]\n"), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		t.Fatal("cloneRepo should not be called for a malformed repo slug")
		return nil
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none (malformed slug is skipped, not an error result)", results)
	}
}

func TestRun_CentralMode_CloneFails_ReturnsErrorResult(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(reposPath, []byte("repos:\n  - repo: example/repo\n    tasks: [task-a]\n"), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		return errors.New("network unreachable")
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || !strings.HasPrefix(results[0].Detail, "clone failed: ") {
		t.Fatalf("results = %+v, want status=error detail starting with 'clone failed: '", results)
	}
}

func TestRun_CentralMode_CloneSucceeds_ProcessesTaskAndCleansUpTmpDir(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(reposPath, []byte("repos:\n  - repo: example/repo\n    tasks: [task-a]\n"), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	var clonedInto string
	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		if repoURL != "https://github.com/example/repo.git" {
			t.Errorf("repoURL = %q, want https://github.com/example/repo.git", repoURL)
		}
		clonedInto = destDir
		fakeRepo := setupGitRepo(t)
		writeAndCommit(t, fakeRepo, "README.md", "hello from fake clone", "init")
		// processRepoEntry expects the repo materialized directly at destDir
		// (mktemp already created destDir; git init needs an empty dir).
		if err := os.RemoveAll(destDir); err != nil {
			return err
		}
		return os.Rename(fakeRepo, destDir)
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath
	opts.DryRun = true

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "skipped" || results[0].Detail != "[dry-run]" {
		t.Fatalf("results = %+v, want status=skipped detail=[dry-run]", results)
	}
	if clonedInto == "" {
		t.Fatal("cloneRepo was never called")
	}
	if _, statErr := os.Stat(clonedInto); !os.IsNotExist(statErr) {
		t.Errorf("expected tmp clone dir %s to be cleaned up after Run, stat err = %v", clonedInto, statErr)
	}
}

func TestRun_CentralMode_RepoFilter_SkipsNonMatchingRepos(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	yaml := "repos:\n" +
		"  - repo: example/repo-a\n    tasks: [task-a]\n" +
		"  - repo: example/repo-b\n    tasks: [task-a]\n"
	if err := os.WriteFile(reposPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		fakeRepo := setupGitRepo(t)
		writeAndCommit(t, fakeRepo, "README.md", "hi", "init")
		if err := os.RemoveAll(destDir); err != nil {
			return err
		}
		return os.Rename(fakeRepo, destDir)
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath
	opts.DryRun = true
	opts.RepoFilter = "example/repo-b"

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Repo != "example/repo-b" {
		t.Fatalf("results = %+v, want exactly one result for example/repo-b", results)
	}
}

func TestRun_CentralMode_TaskNotFoundInTasksDir_ReturnsErrorResult(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)
	server := fakeLLMServer(t, "unused")

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	if err := os.WriteFile(reposPath, []byte("repos:\n  - repo: example/repo\n    tasks: [task-does-not-exist]\n"), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		fakeRepo := setupGitRepo(t)
		writeAndCommit(t, fakeRepo, "README.md", "hi", "init")
		if err := os.RemoveAll(destDir); err != nil {
			return err
		}
		return os.Rename(fakeRepo, destDir)
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath

	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 1 || results[0].Status != "error" || results[0].Detail != "task config not found" {
		t.Fatalf("results = %+v, want status=error detail='task config not found'", results)
	}
}

func TestRun_CentralMode_ModelOverride_UsedInLLMRequest(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "task-a", "readme", "stdout", false)

	var capturedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			capturedModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "NO_CHANGES_NEEDED"}},
			},
		})
	}))
	t.Cleanup(server.Close)

	reposPath := filepath.Join(t.TempDir(), "repos.yaml")
	yamlContent := "repos:\n  - repo: example/repo\n    tasks: [task-a]\n    model_override: override-model\n"
	if err := os.WriteFile(reposPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}

	withFakeClone(t, func(ctx context.Context, repoURL, destDir string) error {
		fakeRepo := setupGitRepo(t)
		writeAndCommit(t, fakeRepo, "README.md", "hi", "init")
		if err := os.RemoveAll(destDir); err != nil {
			return err
		}
		return os.Rename(fakeRepo, destDir)
	})

	opts := baseOptions(dir, server)
	opts.ReposConfig = reposPath

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if capturedModel != "override-model" {
		t.Errorf("captured model = %q, want %q (per-repo model_override)", capturedModel, "override-model")
	}
}
