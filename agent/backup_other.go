//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// backupCmd hands over to the fxship-backup script the Linux installer puts in place, so
// "fxship backup ..." works the same on both systems.
func backupCmd(args []string) int {
	script, err := exec.LookPath("fxship-backup")
	if err != nil {
		script = "/usr/local/sbin/fxship-backup"
	}
	if !fileExists(script) {
		fmt.Fprintln(os.Stderr, "fxship-backup isn't installed: re-run the installer and turn backups on")
		return 1
	}
	cmd := exec.Command(script, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}
