package nuker

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Nuke walks the project directory one level deep and removes any subdirectory
// whose name matches a kill target. Returns total bytes freed.
func Nuke(projectDir string, killTargets []string, dryRun bool) (int64, error) {
	killSet := make(map[string]struct{}, len(killTargets))
	for _, t := range killTargets {
		killSet[strings.ToLower(t)] = struct{}{}
	}

	var freed int64

	err := nukeRecursive(projectDir, killSet, dryRun, &freed, 0)
	return freed, err
}

func nukeRecursive(dir string, killSet map[string]struct{}, dryRun bool, freed *int64, depth int) error {
	// Safety: don't descend more than 10 levels to avoid runaway traversal
	if depth > 10 {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		fullPath := filepath.Join(dir, entry.Name())

		if _, kill := killSet[name]; kill {
			size := dirSize(fullPath)
			if dryRun {
				log.Printf("[DRY-RUN] would nuke: %s (%s)", fullPath, humanSize(size))
			} else {
				log.Printf("[NUKE] removing: %s (%s)", fullPath, humanSize(size))
				if err := os.RemoveAll(fullPath); err != nil {
					log.Printf("[NUKE] error removing %s: %v", fullPath, err)
					continue
				}
			}
			*freed += size
		} else {
			// Recurse into subdirectories to catch nested kill targets
			// e.g., packages/sub-app/node_modules in a monorepo
			_ = nukeRecursive(fullPath, killSet, dryRun, freed, depth+1)
		}
	}
	return nil
}

func dirSize(path string) int64 {
	var size int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors, keep counting
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
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
