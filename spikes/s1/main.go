// SPIKE S1 — persistent `claude -p` pipe (planv2.md §0.5).
//
// Questions this answers, on THIS machine and THIS claude version:
//  1. Can one subprocess serve multiple user turns over its lifetime?
//  2. Does the event schema match what we'd parse in production?
//  3. Does --resume restore memory after the process is killed?
//  4. How much per-turn overhead does config isolation remove?
//
// Throwaway by design: no abstractions, prints findings, exits.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type event struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
	Result    string          `json:"result"`
	Usage     json.RawMessage `json:"usage"`
	TotalCost float64         `json:"total_cost_usd"`
	DurationMs int            `json:"duration_ms"`
}

type assistantMessage struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// turnResult is what one user turn produced.
type turnResult struct {
	text      string
	sessionID string
	cost      float64
	durationMs int
	cacheCreation int
}

// pipe wraps a long-lived claude subprocess.
type pipe struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *bufio.Scanner
}

func spawn(isolated bool, resumeID string, workdir string) (*pipe, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "1",
	}
	if isolated {
		// Everything that keeps the whiteboard turn from dragging in the
		// user's global Claude Code world.
		args = append(args,
			"--strict-mcp-config",
			"--setting-sources", "",
			"--append-system-prompt", "You are a whiteboard thinking partner. Answer in one short sentence.",
		)
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}

	cmd := exec.Command("claude", args...)
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	return &pipe{cmd: cmd, stdin: stdinPipe.(*os.File), stdout: scanner}, nil
}

// turn writes one user message and reads until the result event.
func (p *pipe) turn(text string) (turnResult, error) {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	}
	line, _ := json.Marshal(msg)
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return turnResult{}, fmt.Errorf("write turn: %w", err)
	}

	var out turnResult
	for p.stdout.Scan() {
		var ev event
		if err := json.Unmarshal(p.stdout.Bytes(), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			out.sessionID = ev.SessionID
		}
		switch ev.Type {
		case "assistant":
			var am assistantMessage
			if err := json.Unmarshal(ev.Message, &am); err == nil {
				for _, b := range am.Content {
					if b.Type == "text" {
						out.text += b.Text
					}
				}
			}
		case "result":
			out.cost = ev.TotalCost
			out.durationMs = ev.DurationMs
			var u struct {
				CacheCreation int `json:"cache_creation_input_tokens"`
			}
			_ = json.Unmarshal(ev.Usage, &u)
			out.cacheCreation = u.CacheCreation
			return out, nil
		}
	}
	if err := p.stdout.Err(); err != nil {
		return out, fmt.Errorf("scan: %w", err)
	}
	return out, fmt.Errorf("stream ended before result")
}

func (p *pipe) close() {
	p.stdin.Close()
	p.cmd.Wait()
}

func main() {
	workdir, err := os.MkdirTemp("", "s1-workdir-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(workdir)

	fmt.Println("=== S1: persistent claude pipe ===")
	fmt.Printf("workdir: %s (empty — no CLAUDE.md)\n\n", workdir)

	// --- Q1/Q2: multi-turn over one process ---
	fmt.Println("--- multi-turn over ONE process ---")
	start := time.Now()
	p, err := spawn(true, "", workdir)
	if err != nil {
		fmt.Println("SPAWN FAILED:", err)
		os.Exit(1)
	}

	r1, err := p.turn("Remember this number: 47. Reply with just: stored")
	if err != nil {
		fmt.Println("TURN 1 FAILED:", err)
		p.close()
		os.Exit(1)
	}
	fmt.Printf("turn1: %-30q cost=$%.4f %dms cache_creation=%d\n",
		trim(r1.text), r1.cost, r1.durationMs, r1.cacheCreation)
	sessionID := r1.sessionID
	p.close()

	fmt.Printf("session_id captured: %s\n", sessionID)
	fmt.Printf("total elapsed: %s\n\n", time.Since(start).Round(time.Millisecond))

	// --- Q3: does --resume restore memory? ---
	fmt.Println("--- respawn with --resume ---")
	if sessionID == "" {
		fmt.Println("VERDICT: no session_id captured — cannot test resume")
		os.Exit(1)
	}
	p2, err := spawn(true, sessionID, workdir)
	if err != nil {
		fmt.Println("RESUME SPAWN FAILED:", err)
		os.Exit(1)
	}
	r2, err := p2.turn("What number did I ask you to remember? Reply with just the number.")
	if err != nil {
		fmt.Println("RESUME TURN FAILED:", err)
		p2.close()
		os.Exit(1)
	}
	p2.close()

	remembered := strings.Contains(r2.text, "47")
	fmt.Printf("turn2: %-30q cost=$%.4f %dms\n", trim(r2.text), r2.cost, r2.durationMs)
	fmt.Printf("remembered 47: %v\n\n", remembered)

	// --- Q4: what does isolation buy? ---
	fmt.Println("--- isolation cost comparison (single turn each) ---")
	pIso, _ := spawn(true, "", workdir)
	iso, errIso := pIso.turn("Reply with just: ok")
	pIso.close()

	pBare, _ := spawn(false, "", workdir)
	bare, errBare := pBare.turn("Reply with just: ok")
	pBare.close()

	if errIso == nil && errBare == nil {
		fmt.Printf("isolated : $%.4f  %5dms  cache_creation=%d\n", iso.cost, iso.durationMs, iso.cacheCreation)
		fmt.Printf("bare     : $%.4f  %5dms  cache_creation=%d\n", bare.cost, bare.durationMs, bare.cacheCreation)
		if bare.cost > 0 {
			fmt.Printf("isolation saves: %.0f%% cost, %.0f%% latency\n",
				(1-iso.cost/bare.cost)*100, (1-float64(iso.durationMs)/float64(bare.durationMs))*100)
		}
	} else {
		fmt.Println("comparison incomplete:", errIso, errBare)
	}

	fmt.Println()
	fmt.Println("=== VERDICT ===")
	fmt.Printf("multi-turn pipe works : %v\n", r1.text != "")
	fmt.Printf("session_id captured   : %v\n", sessionID != "")
	fmt.Printf("--resume restores mem : %v\n", remembered)
}

// spawnUncapped starts an isolated session with no --max-turns cap, so one
// process can serve many user turns. --max-turns bounds the whole process, not
// a single exchange, so a low value would end the session after turn one.
func spawnUncapped(workdir string) (*pipe, error) {
	cmd := exec.Command("claude",
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--append-system-prompt", "You are a whiteboard thinking partner. Answer in one short sentence.",
	)
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	return &pipe{cmd: cmd, stdin: stdinPipe.(*os.File), stdout: scanner}, nil
}

func trim(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 28 {
		return s[:28]
	}
	return s
}
