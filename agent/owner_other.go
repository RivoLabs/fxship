//go:build !unix && !windows

package main

// fileOwner elsewhere: every new file counts as someone else's.
func fileOwner(p string) (string, bool) { return "", false }
