package main

import (
	"os"
	"strings"
	"testing"
)

// The load-bearing S1 claim: ONE subprocess serves several user turns, with
// memory across them and no respawn. If this fails we need a process per turn,
// which changes latency and cost characteristics substantially.
func TestOneProcessServesMultipleTurns(t *testing.T) {
	if os.Getenv("RUN_SPIKE") == "" {
		t.Skip("set RUN_SPIKE=1 (costs real subscription usage)")
	}

	workdir, err := os.MkdirTemp("", "s1-multiturn-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workdir)

	// No --max-turns cap here: that flag bounds the whole process, so a low
	// value ends the session after the first turn.
	p, err := spawnUncapped(workdir)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer p.close()

	r1, err := p.turn("Remember the number 47. Reply with just: stored")
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn1: %q (cost $%.4f, cache_creation=%d)", strings.TrimSpace(r1.text), r1.cost, r1.cacheCreation)

	r2, err := p.turn("What number did I ask you to remember? Reply with just the number.")
	if err != nil {
		t.Fatalf("turn 2 on the SAME process: %v", err)
	}
	t.Logf("turn2: %q (cost $%.4f, cache_creation=%d)", strings.TrimSpace(r2.text), r2.cost, r2.cacheCreation)

	if !strings.Contains(r2.text, "47") {
		t.Errorf("second turn lost memory of the first: %q", r2.text)
	}

	r3, err := p.turn("Add 3 to it. Reply with just the number.")
	if err != nil {
		t.Fatalf("turn 3 on the SAME process: %v", err)
	}
	t.Logf("turn3: %q (cost $%.4f, cache_creation=%d)", strings.TrimSpace(r3.text), r3.cost, r3.cacheCreation)

	if !strings.Contains(r3.text, "50") {
		t.Errorf("third turn lost context: want 50, got %q", r3.text)
	}

	// Later turns should ride the cache, not rebuild it.
	if r2.cacheCreation >= r1.cacheCreation {
		t.Logf("NOTE: turn 2 cache_creation (%d) not lower than turn 1 (%d) — prefix may not be reused",
			r2.cacheCreation, r1.cacheCreation)
	}
}
