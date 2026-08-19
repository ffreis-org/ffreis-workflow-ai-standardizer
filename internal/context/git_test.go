package context

import (
	"context"
	"strings"
	"testing"
)

// TestValidateRepoURL_Table pins the allowlist behaviour explicitly. Each
// case names a real-world category of unsafe input that motivated the
// allowlist. If the regex is loosened in a refactor, the relevant case
// flips from "rejected" to "accepted" and this test catches it.
func TestValidateRepoURLTable(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ok    bool
	}{
		{"happy https", "https://github.com/example/repo", true},
		{"happy https .git", "https://github.com/example/repo.git", true},
		{"hyphen in repo", "https://github.com/example-org/some-repo", true},
		{"dot in repo", "https://github.com/example/repo.test", true},

		{"empty", "", false},
		{"leading dash (flag injection)", "-upload-pack=evil", false},
		{"double dash", "--", false},
		{"http (not https)", "http://github.com/a/b", false},
		{"file scheme", "file:///etc/passwd", false},
		{"ext::scheme", "ext::sh -c id", false},
		{"ssh scheme", "git@github.com:example/repo", false},
		{"different host", "https://gitlab.com/a/b", false},
		{"path traversal", "https://github.com/../../../etc/passwd", false},
		{"trailing whitespace", "https://github.com/a/b ", false},
		{"newline", "https://github.com/a/b\n", false},
		{"missing repo segment", "https://github.com/example", false},
		{"trailing slash", "https://github.com/example/repo/", false},
		{"query string", "https://github.com/example/repo?branch=main", false},
		{"fragment", "https://github.com/example/repo#main", false},
		{"userinfo", "https://user:pass@github.com/example/repo", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepoURL(tc.input)
			gotOK := err == nil
			if gotOK != tc.ok {
				t.Fatalf("ValidateRepoURL(%q): got err=%v (ok=%v), want ok=%v", tc.input, err, gotOK, tc.ok)
			}
		})
	}
}

// FuzzRepoURL is the open-ended guard: it asks "does the validator ever
// accept a string that contains a shell-meaningful character or scheme we
// didn't anticipate?" The seed corpus encodes the obvious attack shapes;
// `go test -fuzz=.` will explore mutations.
func FuzzRepoURL(f *testing.F) {
	for _, seed := range []string{
		"https://github.com/a/b",
		"https://github.com/a/b.git",
		"-upload-pack=evil",
		"--",
		"file://",
		"ext::sh",
		"ssh://",
		"\thttps://github.com/a/b",
		"https://github.com/../../../",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, u string) {
		err := ValidateRepoURL(u)
		if err != nil {
			return // rejected — fine
		}
		// Accepted: the allowlist is in force, so u must satisfy the
		// hard invariants below. If fuzzing finds a counter-example, that's
		// a bug.
		if strings.HasPrefix(u, "-") {
			t.Fatalf("validator accepted leading-dash URL %q", u)
		}
		if strings.ContainsAny(u, " \t\n\r") {
			t.Fatalf("validator accepted whitespace-containing URL %q", u)
		}
		if !strings.HasPrefix(u, "https://github.com/") {
			t.Fatalf("validator accepted non-github HTTPS URL %q", u)
		}
		if strings.Contains(u, "..") {
			t.Fatalf("validator accepted path-traversal URL %q", u)
		}
	})
}

// TestClone_RejectsInvalidURLBeforeExec confirms the allowlist short-circuits
// before reaching exec.Command, so a malicious URL never becomes argv at all.
func TestCloneRejectsInvalidURLBeforeExec(t *testing.T) {
	err := Clone(context.Background(), "-upload-pack=touch /tmp/owned", t.TempDir())
	if err == nil {
		t.Fatal("Clone accepted a leading-dash repoURL; allowlist did not fire")
	}
	if !strings.Contains(err.Error(), "git clone") {
		t.Errorf("error %q does not mention 'git clone'", err.Error())
	}
}
