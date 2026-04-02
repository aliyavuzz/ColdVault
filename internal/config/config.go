package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type CleanerRule struct {
	Language string   `json:"language"`
	Markers  []string `json:"markers"`
	Kill     []string `json:"kill"`
}

type CleanerRules struct {
	Version     int           `json:"version"`
	Description string        `json:"description"`
	Rules       []CleanerRule `json:"rules"`
}

type AppConfig struct {
	ScanDirs       []string `json:"scan_dirs"`
	CloudDest      string   `json:"cloud_dest"`
	InactivityDays int      `json:"inactivity_days"`
	Redlist        []string `json:"redlist"`
	ScheduleHour   int      `json:"schedule_hour"`
	ScheduleMinute int      `json:"schedule_minute"`
	LogFile        string   `json:"log_file"`
	DryRun         bool     `json:"dry_run"`
}

func ConfigDir() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "configs")
}

func UserConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".coldvault")
}

func LoadAppConfig() (*AppConfig, error) {
	cfg := &AppConfig{
		InactivityDays: 30,
		ScheduleHour:   11,
		ScheduleMinute: 0,
	}

	// Try user config first, then fall back to exe-relative
	paths := []string{
		filepath.Join(UserConfigDir(), "coldvault.json"),
		filepath.Join(ConfigDir(), "coldvault.json"),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	return cfg, nil
}

func SaveAppConfig(cfg *AppConfig) error {
	dir := UserConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "coldvault.json"), data, 0644)
}

func LoadCleanerRules() (*CleanerRules, error) {
	rules := &CleanerRules{}

	paths := []string{
		filepath.Join(UserConfigDir(), "cleaner_rules.json"),
		filepath.Join(ConfigDir(), "cleaner_rules.json"),
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, rules); err != nil {
			return nil, err
		}
		return rules, nil
	}

	return rules, nil
}

// AllKillTargets returns a deduplicated flat list of all directory names to nuke.
func (r *CleanerRules) AllKillTargets() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, rule := range r.Rules {
		for _, k := range rule.Kill {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				out = append(out, k)
			}
		}
	}
	return out
}
