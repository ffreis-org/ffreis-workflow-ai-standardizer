package output

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-github/v62/github"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// withFakeRunGit substitutes runGit for the duration of the test and
// restores the real implementation afterward.
func withFakeRunGit(t *testing.T, fake func(dir string, args ...string) (string, error)) {
	t.Helper()
	orig := runGit
	runGit = fake
	t.Cleanup(func() { runGit = orig })
}

func TestCreatePR_DryRun_SkipsGitAndAPI(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		t.Fatalf("runGit should not be called in dry-run mode, got args=%v", args)
		return "", nil
	})

	h := &PRHandler{Logger: testLogger()}
	got, err := h.CreatePR(context.Background(), "/repo", "owner", "repo", "main", "chore/", "my-task", "content", true)
	if err != nil {
		t.Fatalf("CreatePR dry-run: unexpected error: %v", err)
	}
	if !strings.Contains(got, "dry-run") {
		t.Errorf("dry-run result = %q, want it to mention dry-run", got)
	}
}

func TestCreatePR_CheckoutFails_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		if args[0] == "checkout" {
			return "fatal: could not checkout", errors.New("exit status 128")
		}
		t.Fatalf("unexpected git call after checkout failure: %v", args)
		return "", nil
	})

	h := &PRHandler{Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "git checkout -b") {
		t.Fatalf("err = %v, want it to mention 'git checkout -b'", err)
	}
}

func TestCreatePR_WriteAgentsMDFails_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		if args[0] == "checkout" {
			return "", nil
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	})

	// repoDir does not exist, so os.WriteFile(repoDir/AGENTS.md, ...) fails
	// with ENOENT — checkout is faked, so nothing actually creates the dir.
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	h := &PRHandler{Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), missingDir, "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "write AGENTS.md") {
		t.Fatalf("err = %v, want it to mention 'write AGENTS.md'", err)
	}
}

func TestCreatePR_GitAddFails_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "checkout":
			return "", nil
		case "add":
			return "", errors.New("add failed")
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	})

	h := &PRHandler{Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "git add") {
		t.Fatalf("err = %v, want it to mention 'git add'", err)
	}
}

func TestCreatePR_GitCommitFails_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "checkout", "add":
			return "", nil
		case "commit":
			return "nothing to commit", errors.New("commit failed")
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	})

	h := &PRHandler{Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "git commit") {
		t.Fatalf("err = %v, want it to mention 'git commit'", err)
	}
}

func TestCreatePR_GitPushFails_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		switch args[0] {
		case "checkout", "add", "commit":
			return "", nil
		case "push":
			return "rejected", errors.New("push failed")
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	})

	h := &PRHandler{Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "git push") {
		t.Fatalf("err = %v, want it to mention 'git push'", err)
	}
}

// fakeGHClient points a go-github client at an httptest server so CreatePR's
// PullRequests.Create call can be exercised without hitting the real API.
func fakeGHClient(t *testing.T, handler http.HandlerFunc) *github.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := github.NewClient(nil)
	base, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client.BaseURL = base
	client.UploadURL = base
	return client
}

func TestCreatePR_Success_ReturnsPRURL(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		return "", nil
	})

	wantURL := "https://github.com/owner/repo/pull/42"
	ghClient := fakeGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repos/owner/repo/pulls") {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
		var body github.NewPullRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.GetBase() != "main" {
			t.Errorf("base = %q, want %q", body.GetBase(), "main")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(github.PullRequest{HTMLURL: github.String(wantURL)})
	})

	h := &PRHandler{GHClient: ghClient, Logger: testLogger()}
	got, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err != nil {
		t.Fatalf("CreatePR: unexpected error: %v", err)
	}
	if got != wantURL {
		t.Errorf("PR URL = %q, want %q", got, wantURL)
	}
}

func TestCreatePR_GitHubAPIError_ReturnsWrappedError(t *testing.T) {
	withFakeRunGit(t, func(dir string, args ...string) (string, error) {
		return "", nil
	})

	ghClient := fakeGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"message":"boom"}`)
	})

	h := &PRHandler{GHClient: ghClient, Logger: testLogger()}
	_, err := h.CreatePR(context.Background(), t.TempDir(), "owner", "repo", "main", "chore/", "my-task", "content", false)
	if err == nil || !strings.Contains(err.Error(), "create PR") {
		t.Fatalf("err = %v, want it to mention 'create PR'", err)
	}
}

// TestRunGit_DefaultImplementation_ExecutesRealGit covers the real (non-faked)
// runGit body: it must be a thin exec.Command wrapper scoped to dir.
func TestRunGit_DefaultImplementation_ExecutesRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available in PATH")
	}
	dir := t.TempDir()
	out, err := runGit(dir, "init")
	if err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
		t.Fatalf("expected %s/.git to exist: %v", dir, statErr)
	}
}
