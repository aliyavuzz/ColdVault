package scheduler

import (
	"fmt"
	"os"
	"os/exec"
)

const taskName = "ColdVault-DailyArchive"

// Install registers a daily task with Windows Task Scheduler.
// The task runs the daemon with "run" argument at the specified hour/minute.
// "Run task as soon as possible after a scheduled start is missed" is enabled.
func Install(daemonExe string, hour, minute int) error {
	// Remove any existing task first (ignore errors)
	Uninstall()

	scheduleTime := fmt.Sprintf("%02d:%02d", hour, minute)

	// schtasks /create with all the flags we need:
	//   /SC DAILY          - run once per day
	//   /ST HH:MM          - start time
	//   /RL HIGHEST        - run with highest privileges
	//   /F                 - force overwrite
	//   HKEY flags via XML are not available in schtasks, so we use an XML import approach.

	// We'll use the XML method because schtasks CLI doesn't support
	// "StartWhenAvailable" (the catch-up feature) directly.
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>ColdVault - Automated Cold Storage Daemon. Archives inactive projects to free SSD space.</Description>
  </RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>2024-01-01T%s:00</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay>
        <DaysInterval>1</DaysInterval>
      </ScheduleByDay>
    </CalendarTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT4H</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>run</Arguments>
    </Exec>
  </Actions>
</Task>`, scheduleTime, daemonExe)

	// Write temp XML
	tmpFile, err := os.CreateTemp("", "coldvault-task-*.xml")
	if err != nil {
		return fmt.Errorf("create temp xml: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write as UTF-16LE with BOM for schtasks compatibility
	if _, err := tmpFile.WriteString(xml); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	cmd := exec.Command("schtasks", "/Create", "/TN", taskName, "/XML", tmpFile.Name(), "/F")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks create failed: %s: %w", string(output), err)
	}
	return nil
}

// Uninstall removes the ColdVault task from Task Scheduler.
func Uninstall() error {
	cmd := exec.Command("schtasks", "/Delete", "/TN", taskName, "/F")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks delete: %s: %w", string(output), err)
	}
	return nil
}

// RunNow triggers the scheduled task immediately.
func RunNow() error {
	cmd := exec.Command("schtasks", "/Run", "/TN", taskName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks run: %s: %w", string(output), err)
	}
	return nil
}
