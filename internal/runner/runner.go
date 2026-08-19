package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/config"
	gocontext "github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/context"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/llm"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/output"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/tmpl"
	"github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

// Options configures a run.
type Options struct {
	// Central mode: read repos from this config.
	ReposConfig string

	TasksDir  string
	LLMConfig llm.Config
	GHToken   string
	OutputDir string
	DryRun    bool

	// Filters (both modes).
	TaskFilter string
	RepoFilter string // owner/name; filters central mode, required for local mode

	// Local mode: use a pre-cloned directory instead of cloning.
	// When set, RepoSlug (or GITHUB_REPOSITORY) provides owner/name.
	LocalDir string
	RepoSlug string // owner/name, used when LocalDir is set

	Logger *slog.Logger
}

// Result is the outcome for one repo x task pair.
type Result struct {
	Repo   string
	Task   string
	Status string // skipped | pr_opened | no_changes | error
	Detail string
}

// processRepoParams groups the non-options parameters for processRepo.
type processRepoParams struct {
	repoEntry config.RepoEntry
	owner     string
	name      string
	repoDir   string
	task      config.TaskConfig
	llmClient *llm.Client
	prHandler *output.PRHandler
}

// Run executes all configured repo x task pairs and returns results.
func Run(ctx context.Context, opts Options) ([]Result, error) {
	// Load task configs.
	taskCfgs, err := loadTaskConfigs(opts.TasksDir)
	if err != nil {
		return nil, err
	}

	ghClient := buildGHClient(ctx, opts.GHToken)
	llmClient := llm.New(opts.LLMConfig, opts.Logger)
	prHandler := &output.PRHandler{GHClient: ghClient, Logger: opts.Logger}

	// Local mode: single repo, pre-cloned directory.
	if opts.LocalDir != "" {
		return runLocalMode(ctx, opts, taskCfgs, llmClient, prHandler)
	}

	// Central mode: iterate repos from config.
	reposCfg, err := config.LoadRepos(opts.ReposConfig)
	if err != nil {
		return nil, fmt.Errorf("load repos config: %w", err)
	}
	return runCentralMode(ctx, opts, reposCfg, taskCfgs, llmClient, prHandler)
}

func runLocalMode(
	ctx context.Context, opts Options, taskCfgs map[string]config.TaskConfig,
	llmClient *llm.Client, prHandler *output.PRHandler,
) ([]Result, error) {
	slug := opts.RepoSlug
	if slug == "" {
		slug = os.Getenv("GITHUB_REPOSITORY")
	}
	if slug == "" {
		return nil, fmt.Errorf("local mode requires --repo-slug or GITHUB_REPOSITORY env var")
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo slug %q — expected owner/name", slug)
	}
	owner, name := parts[0], parts[1]

	entry := config.RepoEntry{Repo: slug, Branch: "main"}

	var results []Result
	for taskName, task := range taskCfgs {
		if opts.TaskFilter != "" && taskName != opts.TaskFilter {
			continue
		}
		opts.Logger.Info("processing (local mode)", "repo", slug, "task", taskName)
		params := processRepoParams{
			repoEntry: entry,
			owner:     owner,
			name:      name,
			repoDir:   opts.LocalDir,
			task:      task,
			llmClient: llmClient,
			prHandler: prHandler,
		}
		r := processRepo(ctx, opts, params)
		results = append(results, r)
	}
	return results, nil
}

func runCentralMode(
	ctx context.Context, opts Options,
	reposCfg config.ReposConfig, taskCfgs map[string]config.TaskConfig,
	llmClient *llm.Client, prHandler *output.PRHandler,
) ([]Result, error) {
	var results []Result

	for _, repoEntry := range reposCfg.Repos {
		if opts.RepoFilter != "" && repoEntry.Repo != opts.RepoFilter {
			continue
		}
		repoResults, tmpDir, err := processRepoEntry(ctx, opts, repoEntry, taskCfgs, llmClient, prHandler)
		results = append(results, repoResults...)
		if tmpDir != "" {
			if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
				opts.Logger.Warn("cleanup failed", "dir", tmpDir, "error", removeErr)
			}
		}
		if err != nil {
			// err here means clone failed; result already appended inside processRepoEntry
			continue
		}
	}
	return results, nil
}

// processRepoEntry clones one repo and runs all its tasks. Returns results and the tmp dir to clean up.
func processRepoEntry(
	ctx context.Context, opts Options, repoEntry config.RepoEntry,
	taskCfgs map[string]config.TaskConfig,
	llmClient *llm.Client, prHandler *output.PRHandler,
) ([]Result, string, error) {
	parts := strings.SplitN(repoEntry.Repo, "/", 2)
	if len(parts) != 2 {
		opts.Logger.Warn("invalid repo slug, skipping", "repo", repoEntry.Repo)
		return nil, "", nil
	}
	owner, name := parts[0], parts[1]

	tmpDir, err := os.MkdirTemp("", "standardizer-*")
	if err != nil {
		return []Result{{Repo: repoEntry.Repo, Status: "error", Detail: err.Error()}}, "", err
	}

	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
	if err := cloneRepo(ctx, repoURL, tmpDir); err != nil {
		opts.Logger.Warn("clone failed", "repo", repoEntry.Repo, "error", err)
		return []Result{{Repo: repoEntry.Repo, Status: "error", Detail: "clone failed: " + err.Error()}}, tmpDir, err
	}

	var results []Result
	for _, taskName := range repoEntry.Tasks {
		if opts.TaskFilter != "" && taskName != opts.TaskFilter {
			continue
		}
		task, ok := taskCfgs[taskName]
		if !ok {
			opts.Logger.Warn("task config not found", "task", taskName)
			results = append(results, Result{Repo: repoEntry.Repo, Task: taskName, Status: "error", Detail: "task config not found"})
			continue
		}
		// Apply per-repo model override.
		if repoEntry.ModelOverride != "" {
			task.Model = repoEntry.ModelOverride
		}
		opts.Logger.Info("processing", "repo", repoEntry.Repo, "task", taskName)
		params := processRepoParams{
			repoEntry: repoEntry,
			owner:     owner,
			name:      name,
			repoDir:   tmpDir,
			task:      task,
			llmClient: llmClient,
			prHandler: prHandler,
		}
		r := processRepo(ctx, opts, params)
		results = append(results, r)
	}
	return results, tmpDir, nil
}

// processRepo runs a single task against an already-available repo directory.
func processRepo(ctx context.Context, opts Options, p processRepoParams) Result {
	base := Result{Repo: p.repoEntry.Repo, Task: p.task.Name}
	logger := opts.Logger.With("repo", p.repoEntry.Repo, "task", p.task.Name)

	builder := gocontext.NewBuilder(p.repoDir, p.owner, p.name, logger)
	data, err := builder.Build(ctx, p.task.Context, p.task.SourceGlobs, p.task.MaxDiffTokens)
	if err != nil {
		return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "context: " + err.Error()}
	}

	if agentsMD, ok := data["agents_md"]; ok && agentsMD == "" {
		logger.Info("skipping: no AGENTS.md")
		return Result{Repo: base.Repo, Task: base.Task, Status: "skipped", Detail: "no AGENTS.md"}
	}

	promptPath := filepath.Join(opts.TasksDir, p.task.Name+".md")
	prompt, err := tmpl.Render(promptPath, data)
	if err != nil {
		return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "render: " + err.Error()}
	}

	if opts.DryRun {
		logger.Info("[dry-run] prompt rendered", "len", len(prompt))
		fmt.Printf("=== [DRY RUN] %s × %s ===\n%s\n\n", p.repoEntry.Repo, p.task.Name, prompt)
		return Result{Repo: base.Repo, Task: base.Task, Status: "skipped", Detail: "[dry-run]"}
	}

	response, err := p.llmClient.WithModel(p.task.Model).Complete(
		ctx,
		"You are a precise documentation reviewer. Follow the output format exactly.",
		prompt,
	)
	if err != nil {
		return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "llm: " + err.Error()}
	}

	_, content, ok := output.ParseResponse(response, p.task.Output.SkipMarker)
	if !ok {
		logger.Info("no changes needed")
		return Result{Repo: base.Repo, Task: base.Task, Status: "no_changes"}
	}

	switch p.task.Output.Type {
	case "pr":
		if p.prHandler.GHClient == nil {
			return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "GH_TOKEN not set"}
		}
		prURL, err := p.prHandler.CreatePR(ctx, p.repoDir, p.owner, p.name,
			p.repoEntry.Branch, p.task.Output.BranchPrefix, p.task.Name, content, opts.DryRun)
		if err != nil {
			return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "create PR: " + err.Error()}
		}
		logger.Info("PR opened", "url", prURL)
		return Result{Repo: base.Repo, Task: base.Task, Status: "pr_opened", Detail: prURL}
	case "stdout":
		fmt.Printf("=== %s × %s ===\n%s\n\n", p.repoEntry.Repo, p.task.Name, content)
		return Result{Repo: base.Repo, Task: base.Task, Status: "no_changes"}
	default:
		return Result{Repo: base.Repo, Task: base.Task, Status: "error", Detail: "unknown output type: " + p.task.Output.Type}
	}
}

func loadTaskConfigs(tasksDir string) (map[string]config.TaskConfig, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	cfgs := map[string]config.TaskConfig{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		tc, err := config.LoadTask(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("load task %s: %w", e.Name(), err)
		}
		cfgs[tc.Name] = tc
	}
	return cfgs, nil
}

// cloneRepo is a package-level var (not a plain func call to gocontext.Clone)
// so tests can substitute a fake that materializes a local fixture directory
// instead of performing a real network clone.
var cloneRepo = gocontext.Clone

func buildGHClient(ctx context.Context, token string) *github.Client {
	if token == "" {
		return nil
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return github.NewClient(oauth2.NewClient(ctx, ts))
}
