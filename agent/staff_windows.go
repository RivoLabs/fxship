//go:build windows

package main

// Staff on Windows: a local account in the fxship-staff group, which can only open the dev
// server's files through the fxship-dev share (reachable over Tailscale or from the admin IPs).
// What they save there is loaded on dev at once and shows up in Discord to Ship or Discard, the
// same as an SSH edit on Linux.

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func staffCmd(args []string) int {
	usage := "usage: fxship staff add <name> | remove <name> | list"
	if !windowsElevated() {
		fmt.Fprintln(os.Stderr, "run it from an elevated PowerShell")
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if !isTrue(env("DEV_ENABLED", "true")) {
		fmt.Fprintln(os.Stderr, "this box has no dev server, so there is nothing for staff to edit")
		return 1
	}
	switch {
	case args[0] == "list":
		out, err := run("net", "localgroup", staffGrp)
		if err != nil {
			fmt.Fprintln(os.Stderr, "no staff yet")
			return 0
		}
		fmt.Println(out)
		return 0
	case len(args) == 2 && args[0] == "add":
		return staffAdd(args[1])
	case len(args) == 2 && args[0] == "remove":
		if out, err := run("net", "user", args[1], "/delete"); err != nil {
			fmt.Fprintf(os.Stderr, "removing %s: %s\n", args[1], out)
			return 1
		}
		fmt.Printf("removed %s; files they made on dev keep their name in the history\n", args[1])
		return 0
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

func staffAdd(name string) int {
	if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,19}$`).MatchString(name) {
		fmt.Fprintln(os.Stderr, "name: 2-20 lowercase letters, digits, _ . - (it becomes their Windows login and git name)")
		return 2
	}
	pass := genPass(20)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$ErrorActionPreference='Stop'
$pw = ConvertTo-SecureString $env:FXSHIP_STAFF_PW -AsPlainText -Force
if (Get-LocalUser -Name $env:FXSHIP_STAFF_NAME -ErrorAction SilentlyContinue) {
  Set-LocalUser -Name $env:FXSHIP_STAFF_NAME -Password $pw
} else {
  New-LocalUser -Name $env:FXSHIP_STAFF_NAME -Password $pw -PasswordNeverExpires -UserMayNotChangePassword -Description 'fxship staff: edits the dev server' | Out-Null
}
Add-LocalGroupMember -Group '`+staffGrp+`' -Member $env:FXSHIP_STAFF_NAME -ErrorAction SilentlyContinue`)
	// The password goes through the environment, never a command line other processes can read.
	cmd.Env = append(os.Environ(), "FXSHIP_STAFF_PW="+pass, "FXSHIP_STAFF_NAME="+name)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "creating %s: %v\n%s\nrun the installer first so the %s group and share exist\n", name, err, strings.TrimSpace(string(out)), staffGrp)
		return 1
	}
	host := firstNonEmpty(os.Getenv("TS_NAME"), os.Getenv("PUBLIC_IP"), hostname())
	fmt.Printf(`%s can now edit dev. Send them, privately:

  Folder:    \\%s\fxship-dev   (File Explorer > This PC > Map network drive)
  User:      %s\%s
  Password:  %s

Edits load on dev within seconds and show up in Discord to Ship or Discard.
They can't see prod, the database passwords or this machine's settings.
`, name, host, hostname(), name, pass)
	if os.Getenv("TAILSCALE") == "true" {
		fmt.Println("They need to be on your tailnet: invite them in the Tailscale admin console.")
	}
	return 0
}
