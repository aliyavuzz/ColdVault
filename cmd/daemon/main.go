// ColdVault Daemon — The Heavy Lifter
//
// Orchestrates the full archive pipeline:
//   1. Launches the Hunter subprocess to find stale projects
//   2. Reads results via stdout pipe (IPC)
//   3. Nukes dependency folders (node_modules, .venv, target, etc.)
//   4. Zips the remaining source code
//   5. Moves the archive to the cloud-synced folder
//   6. Creates a magic stub in the original location
//
// Commands:
//   run       — Execute the full scan-and-archive pipeline
//   restore   — Restore a single project from its stub
//   install   — Register with Windows Task Scheduler
//   uninstall — Remove from Task Scheduler
//   list      — Show all currently archived (stubbed) projects

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"coldvault/internal/archiver"
	"coldvault/internal/config"
	"coldvault/internal/ipc"
	"coldvault/internal/nuker"
	"coldvault/internal/scheduler"
	"coldvault/internal/stubber"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Setup logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.SetPrefix("[COLDVAULT] ")

	cmd := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...) // shift for flag parsing

	switch cmd {
	case "run":
		cmdRun()
	case "restore":
		cmdRestore()
	case "install":
		cmdInstall()
	case "uninstall":
		cmdUninstall()
	case "list":
		cmdList()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `ColdVault Daemon — Automated Cold Storage for Developer Projects

Commands:
  run         Execute the full scan → nuke → zip → archive pipeline
  restore     Restore a project from its stub (--path <stub_dir>)
  install     Register daily task with Windows Task Scheduler
  uninstall   Remove from Task Scheduler
  list        List all archived projects`)
}

// ──────────────────────────────────────────────
// RUN: The main pipeline
// ──────────────────────────────────────────────

func cmdRun() {
	dryRun := flag.Bool("dry-run", false, "Preview what would be archived without making changes")
	flag.Parse()

	cfg, err := config.LoadAppConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *dryRun {
		cfg.DryRun = true
	}

	rules, err := config.LoadCleanerRules()
	if err != nil {
		log.Printf("warning: could not load cleaner rules: %v (proceeding without nuking)", err)
		rules = &config.CleanerRules{}
	}

	if len(cfg.ScanDirs) == 0 {
		log.Fatal("no scan directories configured")
	}
	if cfg.CloudDest == "" {
		log.Fatal("no cloud destination configured")
	}

	// Ensure cloud destination exists
	if err := os.MkdirAll(cfg.CloudDest, 0755); err != nil {
		log.Fatalf("create cloud dest: %v", err)
	}

	// Setup file logging if configured
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}

	log.Printf("=== ColdVault Run Started ===")
	log.Printf("Scan dirs: %v", cfg.ScanDirs)
	log.Printf("Cloud dest: %s", cfg.CloudDest)
	log.Printf("Inactivity threshold: %d days", cfg.InactivityDays)
	log.Printf("Dry run: %v", cfg.DryRun)

	// Phase 1: Launch the Hunter
	hunterResults, err := launchHunter(cfg)
	if err != nil {
		log.Fatalf("hunter failed: %v", err)
	}

	log.Printf("Hunter found %d stale projects", len(hunterResults))

	if len(hunterResults) == 0 {
		log.Println("Nothing to archive. Exiting.")
		return
	}

	killTargets := rules.AllKillTargets()

	var totalFreed int64
	var archived int

	for _, result := range hunterResults {
		projectPath := result.Path
		projectName := filepath.Base(projectPath)

		// Skip if it's already a stub
		if stubber.IsStub(projectPath) {
			log.Printf("SKIP (already a stub): %s", projectPath)
			continue
		}

		log.Printf("──── Processing: %s (inactive %d days) ────", projectName, result.ModDays)

		if cfg.DryRun {
			log.Printf("[DRY-RUN] Would archive: %s", projectPath)
			continue
		}

		// Phase 2: Nuke dependencies
		freed, err := nuker.Nuke(projectPath, killTargets, false)
		if err != nil {
			log.Printf("nuker error on %s: %v (continuing)", projectPath, err)
		}
		totalFreed += freed
		if freed > 0 {
			log.Printf("Nuked %s of dependencies from %s", humanSize(freed), projectName)
		}

		// Phase 3: Zip the remaining source
		archiveName := projectName + ".zip"
		archivePath := filepath.Join(cfg.CloudDest, archiveName)

		// Handle name collision: append timestamp
		if _, err := os.Stat(archivePath); err == nil {
			ts := time.Now().Format("20060102-150405")
			archiveName = projectName + "_" + ts + ".zip"
			archivePath = filepath.Join(cfg.CloudDest, archiveName)
		}

		log.Printf("Zipping %s → %s", projectPath, archivePath)
		zipSize, err := archiver.ZipDirectory(projectPath, archivePath)
		if err != nil {
			log.Printf("ERROR zipping %s: %v (skipping)", projectPath, err)
			continue
		}
		log.Printf("Archive created: %s (%s)", archivePath, humanSize(zipSize))

		// Phase 4: Delete original and create stub
		log.Printf("Removing original: %s", projectPath)
		if err := os.RemoveAll(projectPath); err != nil {
			log.Printf("ERROR removing original %s: %v (archive saved, stub not created)", projectPath, err)
			continue
		}

		archivedAt := time.Now().Format(time.RFC3339)
		if err := stubber.CreateStub(projectPath, archivePath, archivedAt); err != nil {
			log.Printf("ERROR creating stub for %s: %v", projectPath, err)
			continue
		}

		log.Printf("✓ %s → archived and stubbed", projectName)
		archived++
	}

	log.Printf("=== Run Complete: %d projects archived, %s freed ===", archived, humanSize(totalFreed))
}

// launchHunter either runs the external hunter binary or uses the embedded scanner.
func launchHunter(cfg *config.AppConfig) ([]ipc.HunterResult, error) {
	// Look for the hunter binary next to the daemon
	exe, _ := os.Executable()
	hunterExe := filepath.Join(filepath.Dir(exe), "coldvault-hunter.exe")

	scanArg := strings.Join(cfg.ScanDirs, ",")
	redlistArg := strings.Join(cfg.Redlist, ",")

	args := []string{
		"--days", fmt.Sprintf("%d", cfg.InactivityDays),
		"--scan", scanArg,
	}
	if redlistArg != "" {
		args = append(args, "--redlist", redlistArg)
	}

	if _, err := os.Stat(hunterExe); err == nil {
		// External hunter binary exists — use IPC via stdout pipe
		log.Printf("Launching external hunter: %s", hunterExe)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		return ipc.RunHunter(ctx, hunterExe, args)
	}

	// Fallback: run the embedded Go scanner (same logic, just in-process)
	log.Println("Hunter binary not found, using embedded scanner")
	return embeddedScan(cfg)
}

// embeddedScan is a pure-Go fallback scanner when the Hunter binary isn't present.
// Less optimized than the raw Win32 hunter but still functional.
func embeddedScan(cfg *config.AppConfig) ([]ipc.HunterResult, error) {
	var results []ipc.HunterResult
	now := time.Now()

	redlist := make(map[string]bool)
	for _, r := range cfg.Redlist {
		redlist[strings.ToLower(r)] = true
	}

	for _, scanDir := range cfg.ScanDirs {
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			log.Printf("cannot read scan dir %s: %v", scanDir, err)
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			fullPath := filepath.Join(scanDir, entry.Name())

			if redlist[strings.ToLower(fullPath)] {
				continue
			}

			latestMod := findLatestMod(fullPath, 3)
			daysSince := int(now.Sub(latestMod).Hours() / 24)

			if daysSince >= cfg.InactivityDays {
				results = append(results, ipc.HunterResult{
					Path:    fullPath,
					ModDays: daysSince,
				})
			}
		}
	}

	return results, nil
}

func findLatestMod(dir string, depth int) time.Time {
	if depth <= 0 {
		return time.Time{}
	}
	var latest time.Time

	entries, err := os.ReadDir(dir)
	if err != nil {
		return latest
	}

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		if e.IsDir() && depth > 1 {
			child := findLatestMod(filepath.Join(dir, e.Name()), depth-1)
			if child.After(latest) {
				latest = child
			}
		}
	}
	return latest
}

// ──────────────────────────────────────────────
// RESTORE: Bring a project back from cold storage
// ──────────────────────────────────────────────

func cmdRestore() {
	path := flag.String("path", "", "Path to the stub directory to restore")
	flag.Parse()

	if *path == "" {
		log.Fatal("--path is required")
	}

	if !stubber.IsStub(*path) {
		log.Fatalf("%s is not a ColdVault stub", *path)
	}

	info, err := stubber.ReadStub(*path)
	if err != nil {
		log.Fatalf("read stub: %v", err)
	}

	log.Printf("Restoring project: %s", info.ProjectName)
	log.Printf("Archive: %s", info.ArchivePath)
	log.Printf("Original location: %s", info.OriginalPath)

	// Verify archive exists
	if _, err := os.Stat(info.ArchivePath); err != nil {
		log.Fatalf("archive not found: %s (is your cloud drive synced?)", info.ArchivePath)
	}

	// Remove the stub
	if err := stubber.RemoveStub(*path); err != nil {
		log.Fatalf("remove stub: %v", err)
	}

	// Extract archive to original location
	if err := archiver.Unzip(info.ArchivePath, info.OriginalPath); err != nil {
		log.Fatalf("unzip failed: %v", err)
	}

	log.Printf("Project restored to: %s", info.OriginalPath)

	// Optionally delete the archive from cloud storage
	// (keeping it for safety — user can delete manually)
	log.Printf("Archive retained at: %s (delete manually if desired)", info.ArchivePath)
}

// ──────────────────────────────────────────────
// INSTALL / UNINSTALL: Task Scheduler
// ──────────────────────────────────────────────

func cmdInstall() {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("get executable path: %v", err)
	}

	if err := scheduler.Install(exe, cfg.ScheduleHour, cfg.ScheduleMinute); err != nil {
		log.Fatalf("install failed: %v", err)
	}

	log.Printf("Task scheduled: daily at %02d:%02d (with catch-up enabled)", cfg.ScheduleHour, cfg.ScheduleMinute)
}

func cmdUninstall() {
	if err := scheduler.Uninstall(); err != nil {
		log.Fatalf("uninstall failed: %v", err)
	}
	log.Println("Task removed from scheduler")
}

// ──────────────────────────────────────────────
// LIST: Show archived projects
// ──────────────────────────────────────────────

func cmdList() {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	fmt.Println("ColdVault — Archived Projects")
	fmt.Println("═══════════════════════════════════════════════════")

	found := 0
	for _, scanDir := range cfg.ScanDirs {
		entries, err := os.ReadDir(scanDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			fullPath := filepath.Join(scanDir, entry.Name())
			if stubber.IsStub(fullPath) {
				info, err := stubber.ReadStub(fullPath)
				if err != nil {
					continue
				}
				// Check if archive still exists
				status := "OK"
				if _, err := os.Stat(info.ArchivePath); err != nil {
					status = "MISSING ARCHIVE"
				}
				fmt.Printf("  %-30s  archived: %s  [%s]\n", info.ProjectName, info.ArchivedAt[:10], status)
				fmt.Printf("    stub:    %s\n", fullPath)
				fmt.Printf("    archive: %s\n", info.ArchivePath)
				fmt.Println()
				found++
			}
		}
	}

	if found == 0 {
		fmt.Println("  No archived projects found.")
	} else {
		fmt.Printf("Total: %d archived projects\n", found)
	}
}

func humanSize(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
