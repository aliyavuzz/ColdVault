package stubber

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// StubInfo is written as a JSON manifest inside the stub folder.
type StubInfo struct {
	OriginalPath string `json:"original_path"`
	ArchivePath  string `json:"archive_path"`
	ArchivedAt   string `json:"archived_at"`
	ProjectName  string `json:"project_name"`
}

// We use Option B: a stub folder with a manifest + a .lnk shortcut + a restore.cmd script.
// The stub folder keeps the original name and contains:
//   - .coldvault.json   (manifest for the daemon to read)
//   - RESTORE.cmd       (double-clickable script that invokes the daemon's restore command)
//   - desktop.ini       (custom folder icon to signal it's archived)

// CreateStub replaces the original project directory with a lightweight stub folder.
// The original directory must already be deleted before calling this.
func CreateStub(originalPath, archivePath, archivedAt string) error {
	projectName := filepath.Base(originalPath)

	// Create the stub directory
	if err := os.MkdirAll(originalPath, 0755); err != nil {
		return fmt.Errorf("create stub dir: %w", err)
	}

	// 1. Write the manifest
	info := StubInfo{
		OriginalPath: originalPath,
		ArchivePath:  archivePath,
		ArchivedAt:   archivedAt,
		ProjectName:  projectName,
	}
	manifestData, _ := json.MarshalIndent(info, "", "  ")
	manifestPath := filepath.Join(originalPath, ".coldvault.json")
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// 2. Write the RESTORE.cmd — a tiny batch script that calls the daemon
	daemonExe, _ := os.Executable()
	// If we're not running as the daemon, try to find it next to us
	if !strings.HasSuffix(strings.ToLower(daemonExe), "coldvault-daemon.exe") {
		daemonExe = filepath.Join(filepath.Dir(daemonExe), "coldvault-daemon.exe")
	}

	restoreScript := fmt.Sprintf(`@echo off
echo ======================================
echo   ColdVault - Restoring Project...
echo ======================================
echo.
echo Restoring: %s
echo From: %s
echo.
"%s" restore --path "%s"
if %%ERRORLEVEL%% EQU 0 (
    echo.
    echo Project restored successfully!
    echo Opening folder...
    explorer "%s"
) else (
    echo.
    echo ERROR: Restoration failed. Check that the archive exists.
    echo Archive: %s
    pause
)
`, projectName, archivePath, daemonExe, originalPath, originalPath, archivePath)

	restorePath := filepath.Join(originalPath, "RESTORE.cmd")
	if err := os.WriteFile(restorePath, []byte(restoreScript), 0644); err != nil {
		return fmt.Errorf("write restore script: %w", err)
	}

	// 3. Write desktop.ini for custom folder appearance
	desktopIni := fmt.Sprintf(`[.ShellClassInfo]
IconResource=%%SystemRoot%%\System32\imageres.dll,3
InfoTip=ColdVault Archive - Double-click RESTORE.cmd to bring this project back
[ViewState]
Mode=
Vid=
FolderType=Generic
`)
	iniPath := filepath.Join(originalPath, "desktop.ini")
	if err := os.WriteFile(iniPath, []byte(desktopIni), 0644); err != nil {
		return fmt.Errorf("write desktop.ini: %w", err)
	}

	// 4. Set folder attributes: System + ReadOnly (required for desktop.ini to work)
	setFileAttributes(originalPath, windows.FILE_ATTRIBUTE_READONLY|windows.FILE_ATTRIBUTE_SYSTEM)
	// Hide the helper files
	setFileAttributes(manifestPath, windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM)
	setFileAttributes(iniPath, windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_ATTRIBUTE_SYSTEM)

	return nil
}

// IsStub checks if a directory is a ColdVault stub.
func IsStub(dirPath string) bool {
	_, err := os.Stat(filepath.Join(dirPath, ".coldvault.json"))
	return err == nil
}

// ReadStub reads the manifest from a stub directory.
func ReadStub(dirPath string) (*StubInfo, error) {
	data, err := os.ReadFile(filepath.Join(dirPath, ".coldvault.json"))
	if err != nil {
		return nil, err
	}
	var info StubInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RemoveStub deletes the stub folder entirely.
func RemoveStub(dirPath string) error {
	// Remove system/readonly attributes first so we can delete
	setFileAttributes(dirPath, windows.FILE_ATTRIBUTE_NORMAL)
	entries, _ := os.ReadDir(dirPath)
	for _, e := range entries {
		p := filepath.Join(dirPath, e.Name())
		setFileAttributes(p, windows.FILE_ATTRIBUTE_NORMAL)
	}
	return os.RemoveAll(dirPath)
}

func setFileAttributes(path string, attrs uint32) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	windows.SetFileAttributes(pathPtr, attrs)
	_ = unsafe.Sizeof(pathPtr) // suppress unused import
}
