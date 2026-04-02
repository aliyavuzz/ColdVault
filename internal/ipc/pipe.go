package ipc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
)

// HunterResult represents a single stale directory found by the hunter.
type HunterResult struct {
	Path    string
	ModDays int // days since last modification
}

// RunHunter launches the hunter subprocess and streams results back via stdout pipe.
// The hunter outputs one line per stale directory: "<days>\t<path>"
// This is the core IPC mechanism between the Hunter and the Heavy Lifter.
func RunHunter(ctx context.Context, hunterExe string, args []string) ([]HunterResult, error) {
	cmd := exec.CommandContext(ctx, hunterExe, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hunter: %w", err)
	}

	// Drain stderr in background for logging
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[HUNTER-ERR] %s", scanner.Text())
		}
	}()

	var results []HunterResult
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var days int
		var path string
		// Protocol: "<days>\t<absolute_path>"
		n, _ := fmt.Sscanf(line, "%d\t", &days)
		if n == 1 {
			// Extract path after the tab
			idx := strings.IndexByte(line, '\t')
			if idx >= 0 {
				path = line[idx+1:]
			}
		}
		if path != "" {
			results = append(results, HunterResult{Path: path, ModDays: days})
		}
	}

	if err := cmd.Wait(); err != nil {
		// Non-zero exit is not fatal — the hunter may have partial results
		log.Printf("[IPC] hunter exited with: %v", err)
	}

	return results, scanner.Err()
}

// ParseHunterOutput reads hunter output from any reader (useful for testing
// or if the hunter is embedded and writes to a buffer).
func ParseHunterOutput(r io.Reader) ([]HunterResult, error) {
	var results []HunterResult
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var days int
		n, _ := fmt.Sscanf(line, "%d\t", &days)
		if n == 1 {
			idx := strings.IndexByte(line, '\t')
			if idx >= 0 {
				path := line[idx+1:]
				results = append(results, HunterResult{Path: path, ModDays: days})
			}
		}
	}
	return results, scanner.Err()
}
