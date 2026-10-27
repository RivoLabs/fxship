package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths fxship uses outside the server folders. Linux follows the FHS; Windows keeps everything
// under one folder in ProgramData, the place Windows services keep their data.
type platformPaths struct {
	EnvFile     string // the config file the installer writes
	FivemRoot   string // prod/, dev/, repo.git, deploy/
	Releases    string // unpacked release bundles, for upgrade
	BackupMark  string // written by the backup job after every run
	DevScrubSQL string
}

func paths() platformPaths {
	if runtime.GOOS == "windows" {
		base := filepath.Join(env("ProgramData", `C:\ProgramData`), "fxship")
		return platformPaths{
			EnvFile:     filepath.Join(base, "fxship.env"),
			FivemRoot:   env("FXSHIP_DATA", `C:\fxship`),
			Releases:    filepath.Join(base, "releases"),
			BackupMark:  filepath.Join(base, "backup.last"),
			DevScrubSQL: filepath.Join(base, "dev-db-scrub.sql"),
		}
	}
	return platformPaths{
		EnvFile:     "/etc/fxship/fxship.env",
		FivemRoot:   "/srv/fivem",
		Releases:    "/opt/fxship/releases",
		BackupMark:  "/var/lib/fxship/backup.last",
		DevScrubSQL: "/etc/fxship/dev-db-scrub.sql",
	}
}

// envFile is the config file this run reads: FXSHIP_ENV, or the platform default.
func envFile() string { return env("FXSHIP_ENV", paths().EnvFile) }

// exe adds .exe on Windows, for tools looked up or run by name.
func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func isAdmin() bool { return os.Geteuid() == 0 || runtime.GOOS == "windows" && windowsElevated() }
