package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/config"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/llm"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/output"
	"github.com/felipefuhr/ffreis-workflow-ai-standardizer/internal/runner"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "standardizer",
		Short: "AI-assisted repo documentation maintenance",
	}
	root.AddCommand(runCmd(), tasksCmd())
	return root
}

func runCmd() *cobra.Command {
	var (
		reposConfig string
		tasksDir    string
		outputDir   string
		taskFilter  string
		repoFilter  string
		model       string
		baseURL     string
		dryRun      bool
		localDir    string
		repoSlug    string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run configured tasks across repos",
		Long: `Run tasks in one of two modes:

  Central mode (default): reads config/repos.yaml and iterates all configured repos.
    standardizer run [--task <name>] [--repo owner/name]

  Local mode: operates on an already-cloned directory (for use inside GitHub Actions).
    standardizer run --local-dir <path> --repo-slug owner/name [--task <name>]`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCmdRunE(cmd, runCmdFlags{
				reposConfig: reposConfig,
				tasksDir:    tasksDir,
				outputDir:   outputDir,
				taskFilter:  taskFilter,
				repoFilter:  repoFilter,
				model:       model,
				baseURL:     baseURL,
				dryRun:      dryRun,
				localDir:    localDir,
				repoSlug:    repoSlug,
			})
		},
	}

	cmd.Flags().StringVar(&reposConfig, "repos-config", "config/repos.yaml", "path to repos.yaml (central mode)")
	cmd.Flags().StringVar(&tasksDir, "tasks-dir", "tasks", "path to tasks directory")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "write summary JSON here")
	cmd.Flags().StringVar(&taskFilter, "task", "", "run only this task")
	cmd.Flags().StringVar(&repoFilter, "repo", "", "filter to one repo slug (central mode)")
	cmd.Flags().StringVar(&model, "model", "", "model override (default: claude-sonnet-4-6)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "LLM base URL (any OpenAI-compatible endpoint)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print rendered prompts, skip API calls and git writes")
	cmd.Flags().StringVar(&localDir, "local-dir", "", "local repo directory to use instead of cloning (local mode)")
	cmd.Flags().StringVar(&repoSlug, "repo-slug", "", "owner/name of the local repo (required with --local-dir)")

	return cmd
}

// runCmdFlags holds all flags for the run subcommand.
type runCmdFlags struct {
	reposConfig string
	tasksDir    string
	outputDir   string
	taskFilter  string
	repoFilter  string
	model       string
	baseURL     string
	dryRun      bool
	localDir    string
	repoSlug    string
}

func resolveModel(flagModel string) string {
	return firstNonEmpty(flagModel, os.Getenv("LLM_MODEL"), "claude-sonnet-4-6")
}

func resolveBaseURL(flagBaseURL string) string {
	if flagBaseURL != "" {
		return flagBaseURL
	}
	return os.Getenv("LLM_BASE_URL")
}

func runCmdRunE(_ *cobra.Command, flags runCmdFlags) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	apiKey := firstNonEmpty(
		os.Getenv("LLM_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
	)
	if apiKey == "" && !flags.dryRun {
		return fmt.Errorf("no LLM API key: set LLM_API_KEY, ANTHROPIC_API_KEY, or OPENAI_API_KEY")
	}

	opts := runner.Options{
		ReposConfig: flags.reposConfig,
		TasksDir:    flags.tasksDir,
		LLMConfig: llm.Config{
			BaseURL: resolveBaseURL(flags.baseURL), APIKey: apiKey, Model: resolveModel(flags.model),
			MaxRetries: 3, Timeout: 120 * time.Second,
		},
		GHToken:    os.Getenv("GH_TOKEN"),
		OutputDir:  flags.outputDir,
		DryRun:     flags.dryRun,
		TaskFilter: flags.taskFilter,
		RepoFilter: flags.repoFilter,
		LocalDir:   flags.localDir,
		RepoSlug:   flags.repoSlug,
		Logger:     logger,
	}

	start := time.Now()
	results, err := runner.Run(context.Background(), opts)
	if err != nil {
		return err
	}

	sum := output.RunSummary{StartedAt: start, FinishedAt: time.Now()}
	for _, r := range results {
		sum.Results = append(sum.Results, output.RepoResult{
			Repo: r.Repo, Task: r.Task, Status: r.Status, Detail: r.Detail,
		})
	}
	if flags.outputDir != "" {
		if err := output.WriteSummary(flags.outputDir, sum); err != nil {
			logger.Warn("write summary failed", "error", err)
		}
	}

	fmt.Println("\n── Run Summary ──────────────────────────────────")
	for _, r := range results {
		fmt.Printf("  %-40s  %-20s  %-12s  %s\n", r.Repo, r.Task, r.Status, r.Detail)
	}

	writeStepSummary(results)

	var errCount int
	for _, r := range results {
		if r.Status == "error" {
			errCount++
		}
	}
	if errCount > 0 && !flags.dryRun {
		return fmt.Errorf("%d task(s) failed — see summary above", errCount)
	}
	return nil
}

func tasksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tasks", Short: "Manage task configurations"}

	var tasksDir string

	list := &cobra.Command{
		Use:   "list",
		Short: "List all configured tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listTasks(tasksDir)
		},
	}

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate all task configs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return validateTasks(tasksDir)
		},
	}

	for _, sub := range []*cobra.Command{list, validate} {
		sub.Flags().StringVar(&tasksDir, "tasks-dir", "tasks", "path to tasks directory")
		cmd.AddCommand(sub)
	}
	return cmd
}

func listTasks(tasksDir string) error {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		tc, err := config.LoadTask(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			fmt.Printf("  %-30s  ERROR: %v\n", e.Name(), err)
			continue
		}
		fmt.Printf("  %-20s  %-40s  output=%-10s  model=%s\n",
			tc.Name, tc.Description, tc.Output.Type, tc.Model)
	}
	return nil
}

func validateTasks(tasksDir string) error {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return err
	}
	ok := true
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if _, err := config.LoadTask(filepath.Join(tasksDir, e.Name())); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", e.Name(), err)
			ok = false
		} else {
			fmt.Printf("OK   %s\n", e.Name())
		}
	}
	if !ok {
		return fmt.Errorf("validation failed")
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// writeStepSummary writes a markdown results table to $GITHUB_STEP_SUMMARY when running
// inside GitHub Actions, so findings are visible in the workflow quality view without
// downloading the artifact.
func writeStepSummary(results []runner.Result) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	// scan-fix(gosec:G302,G703): 0600 — the step summary is written for this
	// job's own eyes only, no reason to grant group/other read. #nosec G703 —
	// path is $GITHUB_STEP_SUMMARY, a file path GitHub Actions itself creates
	// and injects into every job's environment (not user/network input); this
	// function is only ever called from a GitHub Actions runner.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) //nolint:gosec // G703: path is the GitHub-Actions-provided GITHUB_STEP_SUMMARY, not external input
	if err != nil {
		return
	}
	defer func() {
		// scan-fix(golangci:errcheck): name+log the close error — best-effort,
		// nothing meaningful to do differently on a summary-file close failure.
		if cerr := f.Close(); cerr != nil {
			slog.Warn("close step summary file", "error", cerr)
		}
	}()

	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}

	var sb strings.Builder
	sb.WriteString("## Standardizer Run\n\n")
	sb.WriteString("| Repo | Task | Status | Detail |\n")
	sb.WriteString("|------|------|--------|--------|\n")
	for _, r := range results {
		// scan-fix(staticcheck:QF1012): fmt.Fprintf(&sb, ...) instead of
		// sb.WriteString(fmt.Sprintf(...))
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n", r.Repo, r.Task, r.Status, r.Detail)
	}
	fmt.Fprintf(&sb, "\n**%d pr_opened / %d no_changes / %d skipped / %d errors**\n",
		counts["pr_opened"], counts["no_changes"], counts["skipped"], counts["error"])

	// scan-fix(golangci:errcheck): check the write error — log rather than
	// silently drop it.
	if _, werr := fmt.Fprint(f, sb.String()); werr != nil {
		slog.Warn("write step summary", "error", werr)
	}
}
