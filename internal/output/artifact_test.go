package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteSummary_ValidDir_WritesReadableJSON(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	finished := started.Add(5 * time.Minute)
	summary := RunSummary{
		StartedAt:  started,
		FinishedAt: finished,
		Results: []RepoResult{
			{Repo: "example/repo", Task: "agents-refresh", Status: "pr_opened", Detail: "https://github.com/example/repo/pull/1"},
		},
	}

	if err := WriteSummary(dir, summary); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatalf("read summary.json: %v", err)
	}

	var got RunSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal summary.json: %v", err)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if !got.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
	if len(got.Results) != 1 || got.Results[0] != summary.Results[0] {
		t.Errorf("Results = %+v, want %+v", got.Results, summary.Results)
	}
}

func TestWriteSummary_CreatesNestedOutputDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "output")

	if err := WriteSummary(dir, RunSummary{}); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatalf("expected summary.json to exist: %v", err)
	}
}

func TestWriteSummary_OutputDirIsBlockedByFile_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("setup: write blocker file: %v", err)
	}
	// MkdirAll must fail: "blocker" already exists as a regular file, so it
	// cannot be traversed as a directory component.
	outputDir := filepath.Join(blocker, "sub")

	err := WriteSummary(outputDir, RunSummary{})
	if err == nil {
		t.Fatal("expected error when output dir is blocked by a file, got nil")
	}
}

func TestWriteSummary_WriteFileFails_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit based read-only dirs behave differently on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission bits")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatalf("setup: chmod read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0755) // restore so t.TempDir() cleanup can remove it
	})

	err := WriteSummary(dir, RunSummary{})
	if err == nil {
		t.Fatal("expected error writing summary.json into a read-only dir, got nil")
	}
}
