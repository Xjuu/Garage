package web

import (
	"strings"
	"sync"
)

// logBuffer keeps the most recent lines written to the standard logger in
// memory, so the dashboard can show what the server has been doing without
// needing shell access to the machine it runs on. It is a ring, not a log
// file: old lines are simply dropped, because "what just happened" is all
// this is for — anything worth keeping longer belongs in the journal.
type logBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newLogBuffer(max int) *logBuffer {
	return &logBuffer{max: max}
}

// Write implements io.Writer so a logBuffer can sit inside an io.MultiWriter
// next to the real output (stderr, and therefore the systemd journal)
// without changing how logging is called anywhere else in the program.
func (b *logBuffer) Write(p []byte) (int, error) {
	// log.Logger calls Write once per line already terminated with "\n";
	// trimming it back off keeps the stored line plain so the front end
	// decides its own formatting.
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		b.mu.Lock()
		b.lines = append(b.lines, line)
		if len(b.lines) > b.max {
			// Reslicing off the front rather than copying down keeps this
			// cheap; the backing array is bounded by max either way.
			b.lines = append([]string(nil), b.lines[len(b.lines)-b.max:]...)
		}
		b.mu.Unlock()
	}
	return len(p), nil
}

// Lines returns the buffered lines, oldest first. A copy, so the caller can
// hand it straight to the JSON encoder without holding the lock.
func (b *logBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
