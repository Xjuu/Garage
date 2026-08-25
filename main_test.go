package main

import (
	"strings"
	"testing"
)

func TestRandomPasswordLengthAndCharset(t *testing.T) {
	pw, err := randomPassword(256)
	if err != nil {
		t.Fatalf("randomPassword: %v", err)
	}
	if got := len([]rune(pw)); got != 256 {
		t.Fatalf("length = %d, want 256", got)
	}
	for _, r := range pw {
		if !strings.ContainsRune(passwordCharset, r) {
			t.Fatalf("generated character %q is not in passwordCharset", r)
		}
	}
}

// A weak generator that always picked the same handful of characters would
// still pass the charset check above — this is the check that actually
// catches that: across every letter/digit/special class, at least one
// generated password uses more than a token few of them.
func TestRandomPasswordUsesTheWholeCharset(t *testing.T) {
	seen := map[rune]bool{}
	for i := 0; i < 20; i++ {
		pw, err := randomPassword(256)
		if err != nil {
			t.Fatalf("randomPassword: %v", err)
		}
		for _, r := range pw {
			seen[r] = true
		}
	}
	if len(seen) < len([]rune(passwordCharset))/2 {
		t.Fatalf("only saw %d distinct characters across 20 passwords of 256 chars each — "+
			"generation looks biased toward a narrow subset", len(seen))
	}
}

func TestRandomPasswordCallsAreNotIdentical(t *testing.T) {
	a, err := randomPassword(32)
	if err != nil {
		t.Fatalf("randomPassword: %v", err)
	}
	b, err := randomPassword(32)
	if err != nil {
		t.Fatalf("randomPassword: %v", err)
	}
	if a == b {
		t.Fatalf("two independent calls produced the same password — not actually random")
	}
}
