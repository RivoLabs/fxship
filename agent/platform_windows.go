//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func windowsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// serviceActive reports whether a Windows service is running.
func serviceActive(name string) bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	defer s.Close()
	st, err := s.Query()
	return err == nil && st.State == windows.SERVICE_RUNNING
}

func diskFree(p string) (float64, float64, bool) {
	var free, total, totalFree uint64
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil || windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree) != nil || total == 0 {
		return 0, 0, false
	}
	return float64(free) / float64(total) * 100, float64(free) / (1 << 30), true
}

func clockSynced() (synced, ok bool) {
	out, err := exec.Command("w32tm", "/query", "/status").CombinedOutput()
	if err != nil {
		return false, false
	}
	s := string(out)
	return !strings.Contains(s, "Local CMOS Clock") && !strings.Contains(s, "Free-running System Clock"), true
}

// runAs runs the command as the current account: on Windows the service account holds the deploy
// key in its own profile, and the installer points GIT_SSH_COMMAND at it explicitly.
func runAs(_ string, extra []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), extra...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
