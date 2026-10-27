//go:build !windows

package main

import (
	"fmt"
	"os"
)

func installCmd([]string) int {
	fmt.Fprintln(os.Stderr, "fxship install is the Windows installer; on Linux run install.sh")
	return 2
}

func staffCmd([]string) int {
	fmt.Fprintln(os.Stderr, "on Linux, staff are SSH logins: fxship-add-staff <name> '<ssh public key>'")
	return 2
}
