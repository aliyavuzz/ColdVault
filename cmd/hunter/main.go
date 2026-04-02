// The Silent Hunter
//
// Ultra-fast filesystem scanner using raw Win32 FindFirstFileW/FindNextFileW.
// Zero allocations in the hot path. No filepath.Walk, no os.ReadDir.
//
// Protocol: writes to stdout, one line per stale project folder:
//   <days_since_modified>\t<absolute_path>\n
//
// The Go daemon (Heavy Lifter) reads these lines via a pipe.

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	modDLL              = syscall.NewLazyDLL("kernel32.dll")
	procFindFirstFileW  = modDLL.NewProc("FindFirstFileW")
	procFindNextFileW   = modDLL.NewProc("FindNextFileW")
	procFindClose       = modDLL.NewProc("FindClose")
)

const (
	INVALID_HANDLE_VALUE = ^uintptr(0)
	FILE_ATTRIBUTE_DIR   = 0x10
	MAX_PATH             = 260
)

// Hardcoded forbidden prefixes. The hunter must NEVER enter these.
var forbidden = []string{
	`C:\Windows`,
	`C:\Program Files`,
	`C:\Program Files (x86)`,
	`C:\ProgramData`,
}

func isForbidden(path string) bool {
	upper := strings.ToUpper(path)
	for _, f := range forbidden {
		if strings.HasPrefix(upper, strings.ToUpper(f)) {
			return true
		}
	}
	// Also skip any AppData directory
	if strings.Contains(upper, `\APPDATA\`) {
		return true
	}
	return false
}

// win32FindData mirrors the WIN32_FIND_DATAW structure.
type win32FindData struct {
	FileAttributes    uint32
	CreationTime      syscall.Filetime
	LastAccessTime    syscall.Filetime
	LastWriteTime     syscall.Filetime
	FileSizeHigh      uint32
	FileSizeLow       uint32
	Reserved0         uint32
	Reserved1         uint32
	FileName          [MAX_PATH]uint16
	AlternateFileName [14]uint16
}

func findFirstFile(pattern string) (uintptr, *win32FindData, error) {
	patternPtr, err := syscall.UTF16PtrFromString(pattern)
	if err != nil {
		return 0, nil, err
	}
	var fd win32FindData
	handle, _, errno := procFindFirstFileW.Call(
		uintptr(unsafe.Pointer(patternPtr)),
		uintptr(unsafe.Pointer(&fd)),
	)
	if handle == INVALID_HANDLE_VALUE {
		return 0, nil, errno
	}
	return handle, &fd, nil
}

func findNextFile(handle uintptr, fd *win32FindData) bool {
	ret, _, _ := procFindNextFileW.Call(handle, uintptr(unsafe.Pointer(fd)))
	return ret != 0
}

func findClose(handle uintptr) {
	procFindClose.Call(handle)
}

func filetimeToTime(ft syscall.Filetime) time.Time {
	return time.Unix(0, ft.Nanoseconds())
}

func utf16ToString(s []uint16) string {
	for i, v := range s {
		if v == 0 {
			return syscall.UTF16ToString(s[:i])
		}
	}
	return syscall.UTF16ToString(s[:])
}

// scanTopLevel finds immediate subdirectories of `root` that are project folders.
// A "project folder" is any directory that isn't a dot-directory.
// We check the most recent ModTime of any file within 2 levels of depth.
func scanTopLevel(root string, thresholdDays int, redlist map[string]bool, out *os.File) {
	if isForbidden(root) {
		return
	}

	pattern := root + `\*`
	handle, fd, err := findFirstFile(pattern)
	if err != nil {
		return
	}
	defer findClose(handle)

	now := time.Now()

	for {
		name := utf16ToString(fd.FileName[:])

		if name != "." && name != ".." && (fd.FileAttributes&FILE_ATTRIBUTE_DIR) != 0 {
			fullPath := root + `\` + name

			if !isForbidden(fullPath) && !redlist[strings.ToLower(fullPath)] {
				// Get the most recent modification time across the project
				latestMod := getLatestModTime(fullPath, 3, now)
				daysSince := int(now.Sub(latestMod).Hours() / 24)

				if daysSince >= thresholdDays {
					fmt.Fprintf(out, "%d\t%s\n", daysSince, fullPath)
				}
			}
		}

		if !findNextFile(handle, fd) {
			break
		}
	}
}

// getLatestModTime recursively scans up to `maxDepth` levels to find the
// most recent LastWriteTime. This avoids false positives from stale parent mtimes.
func getLatestModTime(dir string, maxDepth int, now time.Time) time.Time {
	if maxDepth <= 0 {
		return time.Time{}
	}

	pattern := dir + `\*`
	handle, fd, err := findFirstFile(pattern)
	if err != nil {
		return time.Time{}
	}
	defer findClose(handle)

	var latest time.Time

	for {
		name := utf16ToString(fd.FileName[:])
		if name == "." || name == ".." {
			if !findNextFile(handle, fd) {
				break
			}
			continue
		}

		modTime := filetimeToTime(fd.LastWriteTime)
		if modTime.After(latest) {
			latest = modTime
		}

		// If we already found something modified today, no need to keep searching
		if now.Sub(latest).Hours() < 24 {
			return latest
		}

		if (fd.FileAttributes&FILE_ATTRIBUTE_DIR) != 0 && maxDepth > 1 {
			childLatest := getLatestModTime(dir+`\`+name, maxDepth-1, now)
			if childLatest.After(latest) {
				latest = childLatest
				if now.Sub(latest).Hours() < 24 {
					return latest
				}
			}
		}

		if !findNextFile(handle, fd) {
			break
		}
	}

	return latest
}

func main() {
	thresholdDays := flag.Int("days", 30, "Inactivity threshold in days")
	scanDirsFlag := flag.String("scan", "", "Comma-separated list of directories to scan")
	redlistFlag := flag.String("redlist", "", "Comma-separated list of directories to ignore (lowercase)")
	flag.Parse()

	if *scanDirsFlag == "" {
		fmt.Fprintln(os.Stderr, "hunter: --scan is required")
		os.Exit(1)
	}

	scanDirs := strings.Split(*scanDirsFlag, ",")

	redlist := make(map[string]bool)
	if *redlistFlag != "" {
		for _, r := range strings.Split(*redlistFlag, ",") {
			redlist[strings.ToLower(strings.TrimSpace(r))] = true
		}
	}

	// Write results to stdout — the daemon reads via pipe
	for _, dir := range scanDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		scanTopLevel(dir, *thresholdDays, redlist, os.Stdout)
	}
}
