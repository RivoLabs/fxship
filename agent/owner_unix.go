//go:build unix

package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// devUser is the account the dev server runs as on Linux, kept apart from prod's fivem so a
// resource on dev can't read prod's secrets.
const devUser = "fivem-dev"

// fileOwner names who owns p and whether it is fxship's own: the agent's user (fivem, which prod
// also runs as) or fivem-dev, which the dev server runs as and owns its caches and logs.
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
		return u.Username, u.Username == devUser
	}
	return "uid " + id, false
}
