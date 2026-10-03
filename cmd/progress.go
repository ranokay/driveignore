package cmd

import (
	"fmt"
	"io"
	"os"
	"time"
)

// progress prints liveness lines while a command walks a source or drive
// tree. It is enabled only for interactive terminals, so piped and CI output
// stays unchanged.
type progress struct {
	w        io.Writer
	enabled  bool
	interval time.Duration
	now      func() time.Time

	start   time.Time
	last    time.Time
	entries int
}

func newProgress(w io.Writer, enabled bool) *progress {
	return &progress{w: w, enabled: enabled, interval: time.Second, now: time.Now}
}

// isTerminal reports whether w is an interactive terminal. Anything that is
// not an *os.File (test buffers, pipes) is not.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Tick records one walked entry and prints a status line at most once per
// interval. The first entry prints immediately so a run never starts silent.
func (p *progress) Tick(rel string) {
	if !p.enabled {
		return
	}
	now := p.now()
	if p.entries == 0 {
		p.start = now
	}
	p.entries++
	if p.entries == 1 || now.Sub(p.last) >= p.interval {
		p.last = now
		_, _ = fmt.Fprintf(p.w, "processed %d entries — %s\n", p.entries, rel)
	}
}

// Done prints the closing summary, unless nothing was walked.
func (p *progress) Done() {
	if !p.enabled || p.entries == 0 {
		return
	}
	elapsed := p.now().Sub(p.start).Round(10 * time.Millisecond)
	_, _ = fmt.Fprintf(p.w, "done: %d entries in %s\n", p.entries, elapsed)
}
