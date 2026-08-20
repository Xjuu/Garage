package web

import (
	"bytes"
	"io"
	"log"
	"testing"
)

func TestLogBufferKeepsOnlyTheNewestLines(t *testing.T) {
	buf := newLogBuffer(3)
	for _, line := range []string{"one", "two", "three", "four", "five"} {
		buf.Write([]byte(line + "\n"))
	}
	got := buf.Lines()
	want := []string{"three", "four", "five"}
	if len(got) != len(want) {
		t.Fatalf("Lines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Lines() = %v, want %v", got, want)
		}
	}
}

func TestLogBufferIgnoresBlankWrites(t *testing.T) {
	buf := newLogBuffer(10)
	buf.Write([]byte("\n"))
	buf.Write([]byte(""))
	if len(buf.Lines()) != 0 {
		t.Fatalf("blank writes should not produce stored lines, got %v", buf.Lines())
	}
}

// This is the actual wiring used in New(): the standard logger's output goes
// to a MultiWriter of the real destination and the ring buffer, so every
// log.Printf anywhere in the program — including the "scheduled mailbox sync
// finished" line — ends up readable from the buffer without changing how any
// of those call sites log.
func TestLoggerOutputReachesTheBuffer(t *testing.T) {
	buf := newLogBuffer(10)
	var stderr bytes.Buffer
	logger := log.New(io.MultiWriter(&stderr, buf), "", 0)

	logger.Printf("scheduled mailbox sync finished in 217ms: scanned 19, new 0")
	logger.Printf("ALERT: scheduled sync failed (1 in a row): imap: timeout")

	lines := buf.Lines()
	if len(lines) != 2 {
		t.Fatalf("got %d buffered line(s), want 2: %v", len(lines), lines)
	}
	if lines[0] != "scheduled mailbox sync finished in 217ms: scanned 19, new 0" {
		t.Fatalf("first line = %q", lines[0])
	}
	if lines[1] != "ALERT: scheduled sync failed (1 in a row): imap: timeout" {
		t.Fatalf("second line = %q", lines[1])
	}
	// The real destination must still receive everything too — the buffer is
	// an addition, not a replacement for what actually gets logged.
	if stderr.String() == "" {
		t.Fatalf("stderr received nothing; MultiWriter is not fanning out")
	}
}

// recentLogs is what the Admin page's "Server log" panel polls. It needs
// nothing but the buffer, which is what makes it safe to wire up without
// touching the database or config in the handler itself.
func TestRecentLogsHandlerReturnsBufferedLines(t *testing.T) {
	buf := newLogBuffer(10)
	buf.Write([]byte("mailbox sync scheduled every 1h0m0s\n"))
	s := &Server{logs: buf}

	v, err := s.recentLogs(nil)
	if err != nil {
		t.Fatalf("recentLogs: %v", err)
	}
	body, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("recentLogs returned %T, want map[string]any", v)
	}
	lines, ok := body["lines"].([]string)
	if !ok || len(lines) != 1 || lines[0] != "mailbox sync scheduled every 1h0m0s" {
		t.Fatalf("lines = %#v", body["lines"])
	}
}
