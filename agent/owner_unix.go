//go:build unix

package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// fileOwner names who owns p and whether it is the agent's own user (the server's, fivem).
func fileOwner(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", true
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		return "", true
	}
	id := strconv.Itoa(int(st.Uid))
	if u, err := user.LookupId(id); err == nil {
		return u.Username, false
	}
	return "uid " + id, false
}
