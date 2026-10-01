package driveignore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIgnoreMatchesGitCheckIgnore cross-checks the matcher against the real
// git implementation for the same pattern set and paths as
// TestIgnoreMatchesGitignoreSemantics. It is skipped when git is unavailable.
func TestIgnoreMatchesGitCheckIgnore(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	local := t.TempDir()
	patterns := strings.Join([]string{
		"*.log",
		"!keep.log",
		"/rooted",
		"node_modules/",
		"**/generated",
		"a/**/b",
		"foo/**",
		"!foo/keep.txt",
		"q?.log",
		`\#literal`,
	}, "\n") + "\n"
	write(t, filepath.Join(local, ".driveignore"), patterns)
	write(t, filepath.Join(local, ".gitignore"), patterns)
	nested := "build/\n!q1.log\n"
	write(t, filepath.Join(local, "sub", ".driveignore"), nested)
	write(t, filepath.Join(local, "sub", ".gitignore"), nested)

	cases := []struct {
		path  string
		isDir bool
	}{
		{"app.log", false},
		{"keep.log", false},
		{"x/app.log", false},
		{"x/keep.log", false},
		{"rooted", false},
		{"x/rooted", false},
		{"node_modules", true},
		{"x/node_modules", true},
		{"node_modules/pkg/index.js", false},
		{"x/generated/g.js", false},
		{"a/b/f.txt", false},
		{"a/x/y/b/f.txt", false},
		{"foo", true},
		{"foo/bar", false},
		{"foo/keep.txt", false},
		{"q1.log", false},
		{"#literal", false},
		{"README.md", false},
		{"sub/build", true},
		{"sub/q1.log", false},
		{"sub/q2.log", false},
		{"sub/app.log", false},
		{"build", true},
	}
	for _, c := range cases {
		full := filepath.Join(local, filepath.FromSlash(c.path))
		if c.isDir {
			require.NoError(t, os.MkdirAll(full, 0o755))
			continue
		}
		write(t, full, "")
	}

	matcher, err := LoadIgnore(missingGlobal(t), local, false)
	require.NoError(t, err)

	gitCheck := func(path string) (bool, error) {
		cmd := exec.Command(git, "-C", local, "check-ignore", "-q", "--no-index", "--", path)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
		err := cmd.Run()
		if err == nil {
			return true, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("git check-ignore %s: %w", path, err)
	}

	require.NoError(t, exec.Command(git, "-C", local, "init", "-q").Run())
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			gitIgnored, err := gitCheck(c.path)
			require.NoError(t, err)
			require.Equal(t, gitIgnored, matcher.Match(filepath.Join(local, filepath.FromSlash(c.path)), c.isDir),
				"matcher disagrees with git for %s", c.path)
		})
	}
}
