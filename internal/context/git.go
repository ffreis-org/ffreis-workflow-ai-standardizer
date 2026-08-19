package context

import (
	// stdctx: this package is itself named "context", so the stdlib package
	// needs an alias to avoid shadowing.
	stdctx "context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// repoURLPattern restricts repoURL inputs to HTTPS GitHub URLs. Anything
// else — file://, ssh://, ext::, a leading dash — is rejected before exec.
// Even though Clone is invoked from a config file (not a user prompt), an
// unvalidated argv element starting with `-` would be interpreted by git as
// a flag, potentially escalating into things like --upload-pack=<command>.
// This is defense in depth.
var repoURLPattern = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:\.git)?$`)

// ValidateRepoURL returns nil if u is a safe-looking GitHub HTTPS URL and an
// error otherwise. Exposed as a package symbol so callers (and fuzz tests)
// can validate without invoking the full Clone.
func ValidateRepoURL(u string) error {
	if u == "" {
		return fmt.Errorf("repo URL is empty")
	}
	if strings.ContainsAny(u, " \t\n\r") {
		return fmt.Errorf("repo URL contains whitespace: %q", u)
	}
	if !repoURLPattern.MatchString(u) {
		return fmt.Errorf("repo URL %q is not a recognised https://github.com/<org>/<repo> URL", u)
	}
	return nil
}

// Clone performs a shallow clone of the given HTTPS URL into destDir.
//
// repoURL is validated against an allowlist before being passed to git. A
// `--` separator is also emitted before the URL so git treats a value
// starting with `-` as a positional argument rather than a flag, even if the
// allowlist is later relaxed.
func Clone(ctx stdctx.Context, repoURL, destDir string) error {
	if err := ValidateRepoURL(repoURL); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	// scan-fix(golangci:noctx): exec.CommandContext — lets a caller-provided
	// deadline/cancellation abort a hung clone instead of blocking forever.
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=200", "--no-single-branch", "--", repoURL, destDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %w\n%s", repoURL, err, out)
	}
	return nil
}

func (b *Builder) run(ctx stdctx.Context, args ...string) (string, error) {
	// scan-fix(golangci:noctx): exec.CommandContext — same deadline/cancellation
	// rationale as Clone above.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = b.repoDir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func (b *Builder) agentsMD(ctx stdctx.Context) (string, error) {
	content, err := b.run(ctx, "git", "show", "HEAD:AGENTS.md")
	if err != nil {
		// File may not exist yet.
		return "", nil
	}
	return content, nil
}

// agentsMDCommitSHA returns the SHA of the last commit that touched AGENTS.md.
// Returns "" if AGENTS.md has never been committed.
func (b *Builder) agentsMDCommitSHA(ctx stdctx.Context) (string, error) {
	sha, err := b.run(ctx, "git", "log", "-1", "--format=%H", "--", "AGENTS.md")
	if err != nil || sha == "" {
		return "", nil
	}
	return sha, nil
}

func (b *Builder) diffSinceAgentsUpdate(ctx stdctx.Context, globs []string, maxDiffTokens int) (string, error) {
	sha, err := b.agentsMDCommitSHA(ctx)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "(AGENTS.md has no history — diff not available)", nil
	}

	args := []string{"git", "diff", sha + "..HEAD", "--"}
	args = append(args, globs...)
	diff, err := b.run(ctx, args...)
	if err != nil {
		// No diff output is fine (exit 0 with empty output).
		diff = ""
	}
	if diff == "" {
		return "(no source file changes since AGENTS.md was last updated)", nil
	}
	return truncateToTokens(diff, maxDiffTokens), nil
}

func (b *Builder) changedFilesList(ctx stdctx.Context, globs []string) (string, error) {
	sha, err := b.agentsMDCommitSHA(ctx)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "(AGENTS.md has no history)", nil
	}
	args := []string{"git", "log", sha + "..HEAD", "--name-only", "--format=", "--"}
	args = append(args, globs...)
	out, err := b.run(ctx, args...)
	if err != nil || out == "" {
		return "(none)", nil
	}
	// Deduplicate lines.
	seen := map[string]bool{}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] {
			seen[l] = true
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func (b *Builder) readme(ctx stdctx.Context) (string, error) {
	for _, name := range []string{"README.md", "README", "readme.md"} {
		content, err := b.run(ctx, "git", "show", "HEAD:"+name)
		if err == nil {
			return content, nil
		}
	}
	return "(no README found)", nil
}

func (b *Builder) directoryTree(ctx stdctx.Context) (string, error) {
	// scan-fix(golangci:noctx): exec.CommandContext — same deadline/cancellation
	// rationale as Clone above.
	out, err := exec.CommandContext(ctx, "find", b.repoDir,
		"-maxdepth", "3",
		"-not", "-path", "*/.git/*",
		"-not", "-path", "*/vendor/*",
		"-not", "-path", "*/.venv/*",
		"-not", "-path", "*/node_modules/*",
	).Output()
	if err != nil {
		return "", err
	}
	// Strip repo dir prefix.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var cleaned []string
	for _, l := range lines {
		l = strings.TrimPrefix(l, b.repoDir+"/")
		if l != b.repoDir && l != "." {
			cleaned = append(cleaned, l)
		}
	}
	return strings.Join(cleaned, "\n"), nil
}

// truncateToTokens approximates token count as chars/4 and truncates.
func truncateToTokens(s string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "\n\n[... diff truncated to fit context window ...]"
}
