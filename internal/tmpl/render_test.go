package tmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempTemplate(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return path
}

func TestRender_SubstitutesDataKeys(t *testing.T) {
	path := writeTempTemplate(t, "Repo: {{.repo}}\nTask: {{.task}}\n")

	got, err := Render(path, map[string]string{"repo": "example/repo", "task": "agents-refresh"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "Repo: example/repo\nTask: agents-refresh\n"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRender_IndexFunctionForNonIdentifierKeys(t *testing.T) {
	path := writeTempTemplate(t, `{{index . "diff-since-agents-update"}}`)

	got, err := Render(path, map[string]string{"diff-since-agents-update": "diff content"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "diff content" {
		t.Errorf("Render() = %q, want %q", got, "diff content")
	}
}

func TestRender_MissingKey_RendersNoValuePlaceholder(t *testing.T) {
	path := writeTempTemplate(t, "Value: {{.missing}}")

	got, err := Render(path, map[string]string{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "<no value>") {
		t.Errorf("Render() = %q, want it to contain %q for a missing key", got, "<no value>")
	}
}

func TestRender_TemplateFileNotFound_ReturnsError(t *testing.T) {
	_, err := Render(filepath.Join(t.TempDir(), "missing.md"), map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "read template") {
		t.Fatalf("err = %v, want it to mention 'read template'", err)
	}
}

func TestRender_InvalidTemplateSyntax_ReturnsParseError(t *testing.T) {
	path := writeTempTemplate(t, "{{ .unterminated ")

	_, err := Render(path, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "parse template") {
		t.Fatalf("err = %v, want it to mention 'parse template'", err)
	}
}

func TestRender_ExecutionError_IndexOutOfRange(t *testing.T) {
	// "ab" has length 2; indexing at 5 is a runtime execution error, not a
	// parse error, so this exercises the tmpl.Execute error path distinctly
	// from the parse-error path above.
	path := writeTempTemplate(t, `{{index .key 5}}`)

	_, err := Render(path, map[string]string{"key": "ab"})
	if err == nil || !strings.Contains(err.Error(), "execute template") {
		t.Fatalf("err = %v, want it to mention 'execute template'", err)
	}
}
