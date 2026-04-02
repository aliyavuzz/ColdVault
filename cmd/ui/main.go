// ColdVault UI — Native Win32 Configuration Panel
//
// Minimal, "Everything-style" window using lxn/walk.
// Zero web technologies. Pure Win32 under the hood.
//
// Build: go build -ldflags="-s -w -H windowsgui" -o coldvault-ui.exe ./cmd/ui

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"coldvault/internal/config"
	"coldvault/internal/scheduler"

	"github.com/lxn/walk"
	"github.com/lxn/win"
	. "github.com/lxn/walk/declarative"
)

// ──────────────────────────────────────────────
// Modern IFileOpenDialog folder picker via COM.
// The legacy SHBrowseForFolder doesn't show shell namespace
// extensions (Yandex Disk, OneDrive, Google Drive, etc.).
// IFileOpenDialog with FOS_PICKFOLDERS uses the same dialog
// as Explorer and shows everything.
// ──────────────────────────────────────────────

var (
	ole32              = syscall.NewLazyDLL("ole32.dll")
	shell32            = syscall.NewLazyDLL("shell32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
)

var (
	CLSID_FileOpenDialog = syscall.GUID{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	IID_IFileOpenDialog  = syscall.GUID{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	IID_IShellItem       = syscall.GUID{0x43826D1E, 0xE718, 0x42EE, [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	FOS_PICKFOLDERS       = 0x20
	FOS_FORCEFILESYSTEM   = 0x40
	COINIT_APARTMENTTHREADED = 0x2
)

// IFileOpenDialog vtable offsets (IUnknown=3, IModalWindow=1, IFileDialog=12, IFileOpenDialog=2)
const (
	vtRelease      = 2
	vtShow         = 3  // IModalWindow::Show
	vtSetOptions   = 9  // IFileDialog::SetOptions
	vtGetOptions   = 10 // IFileDialog::GetOptions
	vtGetResult    = 20 // IFileDialog::GetResult
)

// IShellItem vtable offsets
const (
	vtShellItemRelease       = 2
	vtShellItemGetDisplayName = 5
)

const SIGDN_FILESYSPATH = 0x80058000

// pickFolderModern opens the modern Vista+ folder picker that shows cloud drives.
// Returns the selected path or "" if cancelled.
func pickFolderModern(owner win.HWND) string {
	procCoInitializeEx.Call(0, COINIT_APARTMENTTHREADED)
	defer procCoUninitialize.Call()

	var dialogPtr uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&CLSID_FileOpenDialog)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&IID_IFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialogPtr)),
	)
	if hr != 0 || dialogPtr == 0 {
		return ""
	}
	vtable := *(*[64]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(dialogPtr))))
	defer syscall.SyscallN(vtable[vtRelease], dialogPtr)

	// Get current options and add FOS_PICKFOLDERS
	var options uint32
	syscall.SyscallN(vtable[vtGetOptions], dialogPtr, uintptr(unsafe.Pointer(&options)))
	options |= FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM
	syscall.SyscallN(vtable[vtSetOptions], dialogPtr, uintptr(options))

	// Show the dialog
	hr2, _, _ := syscall.SyscallN(vtable[vtShow], dialogPtr, uintptr(owner))
	if hr2 != 0 {
		return "" // user cancelled
	}

	// Get the result IShellItem
	var itemPtr uintptr
	hr3, _, _ := syscall.SyscallN(vtable[vtGetResult], dialogPtr, uintptr(unsafe.Pointer(&itemPtr)))
	if hr3 != 0 || itemPtr == 0 {
		return ""
	}
	itemVtable := *(*[16]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(itemPtr))))
	defer syscall.SyscallN(itemVtable[vtShellItemRelease], itemPtr)

	// Get the filesystem path
	var pathPtr uintptr
	hr4, _, _ := syscall.SyscallN(itemVtable[vtShellItemGetDisplayName], itemPtr, SIGDN_FILESYSPATH, uintptr(unsafe.Pointer(&pathPtr)))
	if hr4 != 0 || pathPtr == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pathPtr)

	// Read the UTF-16 string
	return utf16PtrToString((*uint16)(unsafe.Pointer(pathPtr)))
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	// Find null terminator
	ptr := unsafe.Pointer(p)
	var utf16Chars []uint16
	for i := 0; ; i++ {
		ch := *(*uint16)(unsafe.Pointer(uintptr(ptr) + uintptr(i*2)))
		if ch == 0 {
			break
		}
		utf16Chars = append(utf16Chars, ch)
	}
	return syscall.UTF16ToString(utf16Chars)
}

var (
	mainWindow  *walk.MainWindow
	scanDirsTE  *walk.TextEdit
	cloudDestLE *walk.LineEdit
	redlistTE   *walk.TextEdit
	daysCB      *walk.ComboBox
	statusBar   *walk.StatusBarItem
)

func main() {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		cfg = &config.AppConfig{
			InactivityDays: 30,
			ScheduleHour:   11,
			ScheduleMinute: 0,
		}
	}

	daysOptions := []string{"15 days", "30 days", "60 days", "90 days", "120 days", "180 days"}
	daysValues := []int{15, 30, 60, 90, 120, 180}
	selectedDaysIdx := 1 // default 30
	for i, v := range daysValues {
		if v == cfg.InactivityDays {
			selectedDaysIdx = i
			break
		}
	}

	var sbi *walk.StatusBarItem

	err = MainWindow{
		AssignTo: &mainWindow,
		Title:    "ColdVault — Cold Storage Configuration",
		MinSize:  Size{Width: 520, Height: 480},
		Size:     Size{Width: 520, Height: 480},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 8}},
		StatusBarItems: []StatusBarItem{
			{AssignTo: &sbi, Text: "Ready", Width: 500},
		},
		Children: []Widget{
			// Header
			Label{Text: "ColdVault — Automated Cold Storage for Projects", Font: Font{PointSize: 11, Bold: true}},
			VSpacer{Size: 4},

			// Scan Directories
			Label{Text: "Scan Directories (one per line):", Font: Font{PointSize: 9}},
			TextEdit{
				AssignTo: &scanDirsTE,
				Text:     strings.Join(cfg.ScanDirs, "\r\n"),
				MinSize:  Size{Height: 60},
				VScroll:  true,
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{
						Text:    "Browse...",
						MaxSize: Size{Width: 80},
						OnClicked: func() {
							if path := pickFolderModern(win.HWND(mainWindow.Handle())); path != "" {
								current := strings.TrimSpace(scanDirsTE.Text())
								if current != "" {
									current += "\r\n"
								}
								scanDirsTE.SetText(current + path)
							}
						},
					},
				},
			},

			VSpacer{Size: 4},

			// Cloud Destination
			Label{Text: "Cloud Sync Destination:", Font: Font{PointSize: 9}},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					LineEdit{AssignTo: &cloudDestLE, Text: cfg.CloudDest},
					PushButton{
						Text:    "Browse...",
						MaxSize: Size{Width: 80},
						OnClicked: func() {
							if path := pickFolderModern(win.HWND(mainWindow.Handle())); path != "" {
								cloudDestLE.SetText(path)
							}
						},
					},
				},
			},

			VSpacer{Size: 4},

			// Inactivity Threshold
			Label{Text: "Inactivity Threshold:", Font: Font{PointSize: 9}},
			ComboBox{
				AssignTo:     &daysCB,
				Model:        daysOptions,
				CurrentIndex: selectedDaysIdx,
				MaxSize:      Size{Width: 120},
			},

			VSpacer{Size: 4},

			// Redlist
			Label{Text: "Redlist — Never Archive (one path per line):", Font: Font{PointSize: 9}},
			TextEdit{
				AssignTo: &redlistTE,
				Text:     strings.Join(cfg.Redlist, "\r\n"),
				MinSize:  Size{Height: 60},
				VScroll:  true,
			},

			VSpacer{Size: 8},

			// Action Buttons
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{
						Text: "Save Configuration",
						OnClicked: func() {
							saveConfig(cfg, daysValues, sbi)
						},
					},
					PushButton{
						Text: "Install Scheduler",
						OnClicked: func() {
							if err := installScheduler(cfg); err != nil {
								walk.MsgBox(mainWindow, "Error", err.Error(), walk.MsgBoxIconError)
							} else {
								sbi.SetText(fmt.Sprintf("Scheduled: daily at %02d:%02d", cfg.ScheduleHour, cfg.ScheduleMinute))
								walk.MsgBox(mainWindow, "Success",
									fmt.Sprintf("Task scheduled: daily at %02d:%02d\nCatch-up enabled.", cfg.ScheduleHour, cfg.ScheduleMinute),
									walk.MsgBoxIconInformation)
							}
						},
					},
					PushButton{
						Text: "Run Now",
						OnClicked: func() {
							sbi.SetText("Running archive scan...")
							go runDaemonNow(sbi)
						},
					},
					PushButton{
						Text: "Dry Run",
						OnClicked: func() {
							sbi.SetText("Running dry-run scan...")
							go runDaemonDryRun(sbi)
						},
					},
				},
			},
		},
	}.Create()

	if err != nil {
		log.Fatal(err)
	}

	statusBar = sbi
	mainWindow.Run()
}

func saveConfig(cfg *config.AppConfig, daysValues []int, sbi *walk.StatusBarItem) {
	// Parse scan dirs
	scanText := strings.TrimSpace(scanDirsTE.Text())
	var scanDirs []string
	for _, line := range strings.Split(scanText, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
		if line != "" {
			scanDirs = append(scanDirs, line)
		}
	}

	// Parse redlist
	redlistText := strings.TrimSpace(redlistTE.Text())
	var redlist []string
	for _, line := range strings.Split(redlistText, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\r", ""))
		if line != "" {
			redlist = append(redlist, line)
		}
	}

	cfg.ScanDirs = scanDirs
	cfg.CloudDest = strings.TrimSpace(cloudDestLE.Text())
	cfg.Redlist = redlist

	idx := daysCB.CurrentIndex()
	if idx >= 0 && idx < len(daysValues) {
		cfg.InactivityDays = daysValues[idx]
	}

	if err := config.SaveAppConfig(cfg); err != nil {
		walk.MsgBox(mainWindow, "Error", "Failed to save: "+err.Error(), walk.MsgBoxIconError)
		return
	}

	sbi.SetText("Configuration saved to " + config.UserConfigDir())
}

// findDaemonExe searches for coldvault-daemon.exe in multiple locations:
//   1. Next to the current executable (normal installed setup)
//   2. The project's bin/ directory (development)
//   3. The user's .coldvault directory
//   4. System PATH
func findDaemonExe() (string, error) {
	const name = "coldvault-daemon.exe"

	// 1. Next to current exe
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// 2. Project bin/ directory — walk up from the working directory looking for bin/
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 5; i++ {
			candidate := filepath.Join(dir, "bin", name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 3. ~/.coldvault/
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".coldvault", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// 4. System PATH
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return "", fmt.Errorf("%s not found — build it first with: go build -o bin\\%s .\\cmd\\daemon", name, name)
}

func installScheduler(cfg *config.AppConfig) error {
	daemonExe, err := findDaemonExe()
	if err != nil {
		return err
	}
	return scheduler.Install(daemonExe, cfg.ScheduleHour, cfg.ScheduleMinute)
}

func runDaemonNow(sbi *walk.StatusBarItem) {
	daemonExe, err := findDaemonExe()
	if err != nil {
		mainWindow.Synchronize(func() {
			sbi.SetText("Daemon not found")
			walk.MsgBox(mainWindow, "Error", err.Error(), walk.MsgBoxIconError)
		})
		return
	}

	cmd := exec.Command(daemonExe, "run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		mainWindow.Synchronize(func() {
			sbi.SetText("Run failed: " + err.Error())
			walk.MsgBox(mainWindow, "Run Failed", string(output), walk.MsgBoxIconError)
		})
		return
	}
	mainWindow.Synchronize(func() {
		sbi.SetText("Run completed successfully")

		// Count archived projects from output
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, "Run Complete") {
				sbi.SetText(strings.TrimSpace(line))
				break
			}
		}
	})
}

func runDaemonDryRun(sbi *walk.StatusBarItem) {
	daemonExe, err := findDaemonExe()
	if err != nil {
		mainWindow.Synchronize(func() {
			sbi.SetText("Daemon not found")
			walk.MsgBox(mainWindow, "Error", err.Error(), walk.MsgBoxIconError)
		})
		return
	}

	cmd := exec.Command(daemonExe, "run", "--dry-run")
	output, err := cmd.CombinedOutput()
	if err != nil {
		mainWindow.Synchronize(func() {
			sbi.SetText("Dry run failed")
			walk.MsgBox(mainWindow, "Dry Run Failed", string(output), walk.MsgBoxIconError)
		})
		return
	}
	mainWindow.Synchronize(func() {
		sbi.SetText("Dry run complete")
		walk.MsgBox(mainWindow, "Dry Run Results", string(output), walk.MsgBoxIconInformation)
	})
}

// Unused but required for walk manifest embedding
var _ = strconv.Itoa
