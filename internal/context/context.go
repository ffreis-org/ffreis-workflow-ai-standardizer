package context

import (
	// stdctx: this package is itself named "context", so the stdlib package
	// needs an alias to avoid shadowing.
	stdctx "context"
	"fmt"
	"log/slog"
)

// Data holds the gathered context variables for a single repo.
type Data map[string]string

// Builder gathers context for a cloned repo directory.
type Builder struct {
	repoDir string
	owner   string
	name    string
	logger  *slog.Logger
}

func NewBuilder(repoDir, owner, name string, logger *slog.Logger) *Builder {
	return &Builder{repoDir: repoDir, owner: owner, name: name, logger: logger}
}

// Build gathers the requested context keys and returns a Data map.
func (b *Builder) Build(ctx stdctx.Context, keys []string, globs []string, maxDiffTokens int) (Data, error) {
	d := make(Data)
	for _, key := range keys {
		var val string
		var err error
		switch key {
		case "agents_md":
			val, err = b.agentsMD(ctx)
		case "diff_since_agents_update":
			val, err = b.diffSinceAgentsUpdate(ctx, globs, maxDiffTokens)
		case "changed_files_list":
			val, err = b.changedFilesList(ctx, globs)
		case "readme":
			val, err = b.readme(ctx)
		case "directory_tree":
			val, err = b.directoryTree(ctx)
		default:
			return nil, fmt.Errorf("unknown context key: %s", key)
		}
		if err != nil {
			return nil, fmt.Errorf("build context key %q: %w", key, err)
		}
		d[key] = val
	}
	return d, nil
}
