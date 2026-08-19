package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunSummary is written to output/summary.json after a run.
type RunSummary struct {
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Results    []RepoResult `json:"results"`
}

type RepoResult struct {
	Repo   string `json:"repo"`
	Task   string `json:"task"`
	Status string `json:"status"` // skipped | pr_opened | no_changes | error
	Detail string `json:"detail"` // PR URL or error message
}

// WriteSummary writes the summary JSON to outputDir/summary.json.
func WriteSummary(outputDir string, summary RunSummary) error {
	// scan-fix(gosec:G301): 0750 — no reason to grant world access to the run's
	// own output directory.
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(outputDir, "summary.json")
	// scan-fix(gosec:G306): 0600 — summary.json has no reason to be group/other
	// readable.
	return os.WriteFile(path, data, 0600)
}
