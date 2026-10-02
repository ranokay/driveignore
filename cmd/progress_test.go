package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProgressDisabledWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	p := newProgress(&buf, false)

	p.Tick("one.txt")
	p.Tick("two.txt")
	p.Done()

	require.Empty(t, buf.String())
}

func TestProgressPrintsFirstTickThrottledUpdatesAndSummary(t *testing.T) {
	var buf bytes.Buffer
	now := time.Unix(0, 0)
	p := newProgress(&buf, true)
	p.now = func() time.Time { return now }

	p.Tick("first.txt")
	now = now.Add(200 * time.Millisecond)
	p.Tick("second.txt")
	now = now.Add(900 * time.Millisecond)
	p.Tick("third.txt")
	now = now.Add(2 * time.Second)
	p.Done()

	want := "processed 1 entries — first.txt\n" +
		"processed 3 entries — third.txt\n" +
		"done: 3 entries in 3.1s\n"
	require.Equal(t, want, buf.String())
}

func TestIsTerminalRejectsNonFileWriters(t *testing.T) {
	require.False(t, isTerminal(&bytes.Buffer{}))
}
