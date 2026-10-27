//go:build !windows

package main

// asService is Windows only; systemd runs the daemon as a plain process.
func asService(string, func(), func()) bool { return false }

func fxserverCmd([]string) int {
	println("fxship fxserver is for Windows; on Linux the fivem@ systemd units run FXServer")
	return 2
}
