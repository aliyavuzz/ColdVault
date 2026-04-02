// ColdVault Stub Restorer
//
// This is a tiny (~2KB after UPX) standalone executable that can be dropped
// alongside .coldvault.json in a stub folder. When double-clicked, it reads
// the manifest and invokes the daemon's restore command.
//
// Build: go build -ldflags="-s -w" -o restore.exe ./cmd/stub

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type StubInfo struct {
	OriginalPath string `json:"original_path"`
	ArchivePath  string `json:"archive_path"`
	ProjectName  string `json:"project_name"`
}

func main() {
	// Find the .coldvault.json manifest in the same directory as this exe
	exe, err := os.Executable()
	if err != nil {
		fatal("cannot determine exe path: " + err.Error())
	}
	stubDir := filepath.Dir(exe)
	manifestPath := filepath.Join(stubDir, ".coldvault.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal("cannot read manifest: " + err.Error())
	}

	var info StubInfo
	if err := json.Unmarshal(data, &info); err != nil {
		fatal("corrupt manifest: " + err.Error())
	}

	// Find the daemon executable
	daemonExe := findDaemon(stubDir)
	if daemonExe == "" {
		fatal("coldvault-daemon.exe not found. Please ensure it's in your PATH or next to this file.")
	}

	fmt.Printf("ColdVault — Restoring: %s\n", info.ProjectName)
	fmt.Printf("From: %s\n\n", info.ArchivePath)

	cmd := exec.Command(daemonExe, "restore", "--path", info.OriginalPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fatal("restore failed: " + err.Error())
	}

	fmt.Println("\nProject restored successfully!")

	// Open the restored folder in Explorer
	exec.Command("explorer", info.OriginalPath).Start()
}

func findDaemon(stubDir string) string {
	// Check common locations
	candidates := []string{
		filepath.Join(stubDir, "coldvault-daemon.exe"),
	}

	// Check PATH
	if p, err := exec.LookPath("coldvault-daemon.exe"); err == nil {
		candidates = append(candidates, p)
	}

	// Check Program Files
	pf := os.Getenv("ProgramFiles")
	if pf != "" {
		candidates = append(candidates, filepath.Join(pf, "ColdVault", "coldvault-daemon.exe"))
	}

	// Check next to the user's home
	home, _ := os.UserHomeDir()
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".coldvault", "coldvault-daemon.exe"))
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func fatal(msg string) {
	// If not running from a terminal, use a message box approach via echo + pause
	if !isTerminal() {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", msg)
		// Keep the window open so user sees the error
		fmt.Println("\nPress Enter to close...")
		fmt.Scanln()
	} else {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", msg)
	}
	os.Exit(1)
}

func isTerminal() bool {
	// Check if stdout is attached to a console
	return strings.Contains(os.Getenv("TERM"), "xterm") ||
		os.Getenv("ConEmuPID") != "" ||
		os.Getenv("WT_SESSION") != ""
}
