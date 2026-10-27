//go:build windows

package main

// Windows has no IANA zone database; TZ=Europe/London must still work.
import _ "time/tzdata"
