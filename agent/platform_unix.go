//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func windowsElevated() bool { return false }

// serviceActive reports whether a systemd unit is running.
func serviceActive(name string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
}

// diskFree returns the free share (percent) and free gigabytes of the filesystem holding p.
func diskFree(p string) (float64, float64, bool) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil || fs.Blocks == 0 {
		return 0, 0, false
	}
	return float64(fs.Bavail) / float64(fs.Blocks) * 100, float64(fs.Bavail) * float64(fs.Bsize) / (1 << 30), true
}

// clockSynced reports whether NTP has the clock in step; ok is false when it can't tell.
func clockSynced() (synced, ok bool) {
	out, err := exec.Command("timedatectl", "show", "-p", "NTPSynchronized", "--value").Output()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) == "yes", true
}

// runAs runs a command as the server's user when called as root, so git uses that user's deploy key.
func runAs(user string, extra []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if os.Geteuid() == 0 && user != "" && user != "root" {
		cmd = exec.Command("runuser", append([]string{"-u", user, "--", name}, args...)...)
	}
	cmd.Env = append(os.Environ(), extra...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
