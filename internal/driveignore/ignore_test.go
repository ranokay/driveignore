package driveignore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIgnoreMatchesGitignoreSemantics pins the matcher to the same results
// `git check-ignore` produces for this pattern set: globs, `**`, `?`,
// negation, anchoring, directory-only patterns and escaped leading hashes.
func TestIgnoreMatchesGitignoreSemantics(t *testing.T) {
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

	matcher, err := LoadIgnore(missingGlobal(t), local, false)
	require.NoError(t, err)

	tests := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"app.log", false, true},
		{"keep.log", false, false},
		{"x/app.log", false, true},
		{"x/keep.log", false, false},
		{"rooted", false, true},
		{"x/rooted", false, false},
		{"node_modules", true, true},
		{"x/node_modules", true, true},
		{"node_modules/pkg/index.js", false, true},
		{"x/generated/g.js", false, true},
		{"a/b/f.txt", false, true},
		{"a/x/y/b/f.txt", false, true},
		{"foo", true, false},
		{"foo", false, false},
		{"foo/bar", false, true},
		{"foo/keep.txt", false, false},
		{"q1.log", false, true},
		{"#literal", false, true},
		{"README.md", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, matcher.Match(filepath.Join(local, filepath.FromSlash(tt.path)), tt.isDir))
		})
	}
}

func TestIgnoreSkipsCommentsAndBlankLines(t *testing.T) {
	local := t.TempDir()
	write(t, filepath.Join(local, ".driveignore"), "# comment\n\n*.tmp\n\\#hashed.txt\n")

	matcher, err := LoadIgnore(missingGlobal(t), local, false)
	require.NoError(t, err)
	require.True(t, matcher.Match(filepath.Join(local, "x.tmp"), false))
	require.True(t, matcher.Match(filepath.Join(local, "#hashed.txt"), false))
	require.False(t, matcher.Match(filepath.Join(local, "comment"), false))
}

func TestIgnoreHandlesLinesLongerThanScannerLimit(t *testing.T) {
	local := t.TempDir()
	long := strings.Repeat("x", 70_000)
	write(t, filepath.Join(local, ".driveignore"), long+"\n*.tmp\n")

	matcher, err := LoadIgnore(missingGlobal(t), local, false)
	require.NoError(t, err)
	require.True(t, matcher.Match(filepath.Join(local, "file.tmp"), false), "patterns after a long line must still apply")
}
