//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// fileOwner names who owns p and whether it is the account fxship and the game servers run as.
// Staff editing dev over the SMB share own the files they create; FXServer's caches and logs are
// owned by the service account.
func fileOwner(p string) (string, bool) {
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", true
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", true
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", true
	}
	if windows.EqualSid(owner, tu.User.Sid) {
		return "", true
	}
	// Files made by an administrator are owned by the Administrators group, so name the account.
	acct, dom, _, err := owner.LookupAccount("")
	if err != nil {
		return owner.String(), false
	}
	// The agent runs as SYSTEM and each game server as NT SERVICE\fxship-<name>: their files are
	// fxship's own, like everything the fivem user writes on Linux.
	if owner.IsWellKnown(windows.WinLocalSystemSid) || strings.EqualFold(dom, "NT SERVICE") && strings.HasPrefix(strings.ToLower(acct), "fxship") {
		return "", true
	}
	if h, _ := os.Hostname(); strings.EqualFold(dom, h) {
		return acct, false // a local account: staff from "fxship staff add"
	}
	if dom != "" && dom != "BUILTIN" {
		return dom + `\` + acct, false
	}
	return acct, false
}
