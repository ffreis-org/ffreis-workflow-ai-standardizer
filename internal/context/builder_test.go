package context

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// setupGitRepo creates a fresh git repo in a temp dir with a local identity
// configured, so commits work regardless of the host's global git config.
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
	// helper, but the linter doesn't distinguish; context.Background() is a
	// no-op here since these are short-lived local git commands.
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

func TestBuild_UnknownContextKey_ReturnsError(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hi", "init")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	_, err := b.Build(context.Background(), []string{"not_a_real_key"}, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "unknown context key") {
		t.Fatalf("err = %v, want it to mention 'unknown context key'", err)
	}
}

func TestBuild_AgentsMD_ReturnsCommittedContent(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents\n\nBe helpful.", "add AGENTS.md")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"agents_md"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if data["agents_md"] != "# Agents\n\nBe helpful." {
		t.Errorf("agents_md = %q, want committed content", data["agents_md"])
	}
}

func TestBuild_AgentsMD_MissingFile_ReturnsEmptyStringNoError(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hi", "init")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"agents_md"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if data["agents_md"] != "" {
		t.Errorf("agents_md = %q, want empty string for a repo with no AGENTS.md", data["agents_md"])
	}
}

func TestBuild_DiffSinceAgentsUpdate_NoHistory_ReturnsPlaceholder(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hi", "init") // AGENTS.md never committed
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"diff_since_agents_update"}, []string{"*.go"}, 3000)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "(AGENTS.md has no history — diff not available)"
	if data["diff_since_agents_update"] != want {
		t.Errorf("diff_since_agents_update = %q, want %q", data["diff_since_agents_update"], want)
	}
}

func TestBuild_DiffSinceAgentsUpdate_MatchingChange_ReturnsDiff(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	writeAndCommit(t, dir, "main.go", "package main\n", "add main.go")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"diff_since_agents_update"}, []string{"*.go"}, 3000)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(data["diff_since_agents_update"], "package main") {
		t.Errorf("diff_since_agents_update = %q, want it to contain the new file's content", data["diff_since_agents_update"])
	}
}

func TestBuild_DiffSinceAgentsUpdate_NoMatchingChange_ReturnsPlaceholder(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	writeAndCommit(t, dir, "notes.txt", "unrelated change", "add notes")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"diff_since_agents_update"}, []string{"*.go"}, 3000)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "(no source file changes since AGENTS.md was last updated)"
	if data["diff_since_agents_update"] != want {
		t.Errorf("diff_since_agents_update = %q, want %q", data["diff_since_agents_update"], want)
	}
}

func TestBuild_DiffSinceAgentsUpdate_TruncatesToTokenBudget(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	writeAndCommit(t, dir, "main.go", strings.Repeat("x", 500)+"\n", "add large main.go")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"diff_since_agents_update"}, []string{"*.go"}, 1) // maxChars = 4
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(data["diff_since_agents_update"], "truncated to fit context window") {
		t.Errorf("diff_since_agents_update = %q, want truncation suffix", data["diff_since_agents_update"])
	}
}

func TestBuild_ChangedFilesList_NoHistory_ReturnsPlaceholder(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "README.md", "hi", "init")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"changed_files_list"}, []string{"*.go"}, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "(AGENTS.md has no history)"
	if data["changed_files_list"] != want {
		t.Errorf("changed_files_list = %q, want %q", data["changed_files_list"], want)
	}
}

func TestBuild_ChangedFilesList_DeduplicatesRepeatedFile(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	writeAndCommit(t, dir, "main.go", "package main\n", "add main.go")
	writeAndCommit(t, dir, "main.go", "package main\n\nfunc main() {}\n", "update main.go")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"changed_files_list"}, []string{"*.go"}, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if data["changed_files_list"] != "main.go" {
		t.Errorf("changed_files_list = %q, want deduplicated %q", data["changed_files_list"], "main.go")
	}
}

func TestBuild_Readme_FindsReadmeMD(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "README.md", "# Hello", "add readme")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"readme"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if data["readme"] != "# Hello" {
		t.Errorf("readme = %q, want %q", data["readme"], "# Hello")
	}
}

func TestBuild_Readme_MissingFile_ReturnsPlaceholder(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"readme"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "(no README found)"
	if data["readme"] != want {
		t.Errorf("readme = %q, want %q", data["readme"], want)
	}
}

func TestBuild_DirectoryTree_ListsFilesExcludingGitContents(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "main.go", "package main\n", "add main.go")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"directory_tree"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tree := data["directory_tree"]
	if !strings.Contains(tree, "main.go") {
		t.Errorf("directory_tree = %q, want it to list main.go", tree)
	}
	// The -not -path "*/.git/*" filter excludes .git's *contents* (e.g.
	// .git/HEAD, .git/objects/...); the top-level ".git" entry itself is not
	// matched by that pattern and is expected to still appear as one line.
	if strings.Contains(tree, "/.git/") {
		t.Errorf("directory_tree = %q, want .git contents excluded", tree)
	}
}

func TestBuild_MultipleKeys_PopulatesAllRequested(t *testing.T) {
	dir := setupGitRepo(t)
	writeAndCommit(t, dir, "AGENTS.md", "# Agents", "add AGENTS.md")
	b := NewBuilder(dir, "owner", "repo", testLogger())

	data, err := b.Build(context.Background(), []string{"agents_md", "readme", "directory_tree"}, nil, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, key := range []string{"agents_md", "readme", "directory_tree"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data missing requested key %q", key)
		}
	}
}

func TestTruncateToTokens_ShortStringUnchanged(t *testing.T) {
	got := truncateToTokens("short string", 100)
	if got != "short string" {
		t.Errorf("truncateToTokens() = %q, want unchanged input", got)
	}
}

func TestTruncateToTokens_LongStringTruncatedWithSuffix(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := truncateToTokens(s, 5) // maxChars = 20

	wantPrefix := strings.Repeat("a", 20)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("truncateToTokens() = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.Contains(got, "[... diff truncated to fit context window ...]") {
		t.Errorf("truncateToTokens() = %q, want truncation suffix", got)
	}
}
