//go:build windows

package main

// "fxship backup" on Windows, the counterpart of the fxship-backup script on Linux: database dumps
// plus everything on the box that isn't in git, into a restic repository. The fxship-backup
// scheduled task runs "fxship backup run" daily.

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const backupUsage = `usage: fxship backup <command>
  run                               back up now (the daily task runs this)
  list                              list snapshots
  restore-db <prod|dev> [snapshot]  load a database from a snapshot (default: latest)
  restore-files <snapshot> <dir>    extract a snapshot's files into dir to copy back by hand`

func backupCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, backupUsage)
		return 2
	}
	if os.Getenv("BACKUP_REPO") == "" || os.Getenv("RESTIC_PASSWORD") == "" {
		fmt.Fprintf(os.Stderr, "backups aren't set up (BACKUP_REPO and RESTIC_PASSWORD in %s): re-run the installer and turn them on\n", envFile())
		return 1
	}
	os.Setenv("RESTIC_REPOSITORY", os.Getenv("BACKUP_REPO"))
	switch args[0] {
	case "run":
		return backupRun()
	case "list":
		return restic("snapshots", "--tag", "fxship")
	case "restore-db":
		if len(args) < 2 || (args[1] != "prod" && args[1] != "dev") {
			fmt.Fprintln(os.Stderr, "usage: fxship backup restore-db <prod|dev> [snapshot]")
			return 2
		}
		snap := "latest"
		if len(args) > 2 {
			snap = args[2]
		}
		return backupRestoreDB(args[1], snap)
	case "restore-files":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: fxship backup restore-files <snapshot> <dir>")
			return 2
		}
		if restic("restore", args[1], "--target", args[2]) != 0 {
			return 1
		}
		fmt.Printf("files are in %s; copy back what you need\n", args[2])
		return 0
	}
	fmt.Fprintln(os.Stderr, backupUsage)
	return 2
}

func restic(args ...string) int {
	cmd := exec.Command("restic", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return 1
	}
	return 0
}

func backupStage() string {
	return filepath.Join(env("ProgramData", `C:\ProgramData`), "fxship-backup-stage")
}

func backupRun() (code int) {
	mark := paths().BackupMark
	logf, _ := os.OpenFile(filepath.Join(filepath.Dir(paths().EnvFile), "logs", "backup.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	var out io.Writer = os.Stdout
	if logf != nil {
		defer logf.Close()
		out = io.MultiWriter(os.Stdout, logf)
		fmt.Fprintf(logf, "--- backup %s\n", time.Now().Format(time.RFC3339))
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(out, "backup failed: "+format+"\n", a...)
		os.WriteFile(mark, []byte(fmt.Sprintf("%d failed\n", time.Now().Unix())), 0o644)
		return 1
	}
	rs := func(args ...string) error {
		cmd := exec.Command("restic", args...)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}

	stage := backupStage()
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return fail("%v", err)
	}
	// Database dumps sit here for a moment: SYSTEM and Administrators only.
	exec.Command("icacls", stage, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "/grant:r", "*S-1-5-32-544:(OI)(CI)F", "/Q").Run()
	defer os.RemoveAll(stage)

	repo := os.Getenv("RESTIC_REPOSITORY")
	if filepath.IsAbs(repo) {
		os.MkdirAll(repo, 0o700)
	}
	if exec.Command("restic", "cat", "config").Run() != nil {
		if err := rs("init"); err != nil {
			return fail("can't open or create the restic repository %s: %v", repo, err)
		}
	}

	for _, which := range []string{"PROD", "DEV"} {
		name := os.Getenv("DB_" + which + "_NAME")
		if name == "" {
			continue
		}
		if err := dumpDB(os.Getenv("DB_"+which+"_USER"), os.Getenv("DB_"+which+"_PASSWORD"), name, filepath.Join(stage, name+".sql.gz")); err != nil {
			return fail("dumping %s: %v", name, err)
		}
		fi, _ := os.Stat(filepath.Join(stage, name+".sql.gz"))
		fmt.Fprintf(out, "dumped %s (%d KB)\n", name, fi.Size()/1024)
	}

	root := env("FIVEM_ROOT", paths().FivemRoot)
	inc := []string{stage, filepath.Dir(paths().EnvFile)}
	exc := []string{filepath.Join(filepath.Dir(paths().EnvFile), "logs"), filepath.Join(filepath.Dir(paths().EnvFile), "releases")}
	for _, i := range []string{"prod", "dev"} {
		data := filepath.Join(root, i, "server-data")
		if !fileExists(data) {
			continue
		}
		// The repo is in git; back up what isn't: cfgs, secrets, txAdmin's data, and files resources
		// change at runtime (git sees them as modified or untracked).
		inc = append(inc, filepath.Join(root, i, "txData"))
		exc = append(exc, filepath.Join(root, i, "txData", "*", "logs"))
		os.MkdirAll(filepath.Join(stage, i), 0o700)
		cfgs, _ := filepath.Glob(filepath.Join(data, "*.cfg"))
		for _, f := range cfgs {
			copyFile(f, filepath.Join(stage, i, filepath.Base(f)))
		}
		wt := data
		if !fileExists(filepath.Join(data, ".git")) {
			wt = filepath.Join(data, "resources")
		}
		if fileExists(filepath.Join(wt, ".git")) {
			if err := zipRuntimeFiles(wt, filepath.Join(stage, i+"-runtime-files.zip")); err != nil {
				fmt.Fprintf(out, "runtime files of %s not saved: %v\n", i, err)
			}
		}
	}
	args := []string{"backup", "--tag", "fxship", "--host", hostname()}
	for _, e := range exc {
		args = append(args, "--exclude", e)
	}
	if err := rs(append(args, inc...)...); err != nil {
		return fail("restic backup: %v", err)
	}
	if err := rs("forget", "--tag", "fxship", "--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "6", "--prune"); err != nil {
		return fail("restic forget: %v", err)
	}
	os.WriteFile(mark, []byte(fmt.Sprintf("%d ok\n", time.Now().Unix())), 0o644)
	fmt.Fprintln(out, "backup ok")
	return 0
}

func dumpDB(user, pass, name, dest string) error {
	cnf, err := mysqlDefaults(env("DB_HOST", "127.0.0.1"), env("DB_PORT", "3306"), user, pass)
	if err != nil {
		return err
	}
	defer os.Remove(cnf)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	cmd := exec.Command("mariadb-dump", "--defaults-extra-file="+cnf, "--single-transaction", "--quick", "--hex-blob",
		"--triggers", "--no-tablespaces", "--skip-lock-tables", name)
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = gz, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, clip(stderr.String(), 400))
	}
	return gz.Close()
}

// zipRuntimeFiles saves the files git sees as changed or new in a worktree.
func zipRuntimeFiles(wt, dest string) error {
	cmd := exec.Command("git", "-c", "safe.directory=*", "-C", wt, "ls-files", "-m", "-o", "--exclude-standard", "-z")
	b, err := cmd.Output()
	if err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	for _, rel := range strings.Split(string(b), "\x00") {
		if rel == "" {
			continue
		}
		src, err := os.Open(filepath.Join(wt, filepath.FromSlash(rel)))
		if err != nil {
			continue // removed since git looked
		}
		w, err := z.Create(rel)
		if err == nil {
			_, err = io.Copy(w, src)
		}
		src.Close()
		if err != nil {
			return err
		}
	}
	return z.Close()
}

func backupRestoreDB(which, snap string) int {
	up := strings.ToUpper(which)
	name, user, pass := os.Getenv("DB_"+up+"_NAME"), os.Getenv("DB_"+up+"_USER"), os.Getenv("DB_"+up+"_PASSWORD")
	if name == "" {
		fmt.Fprintf(os.Stderr, "no %s database configured\n", which)
		return 1
	}
	tmp, err := os.MkdirTemp("", "fxship-restore-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(tmp)
	// Dumps are always named after their database; restic keeps the drive in the path, so the file
	// is found by name rather than by a guessed path.
	if exec.Command("restic", "restore", snap, "--target", tmp, "--include", "*/"+name+".sql.gz").Run() != nil {
		fmt.Fprintf(os.Stderr, "restoring from snapshot %s failed: fxship backup list shows the snapshots\n", snap)
		return 1
	}
	var dump string
	filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name+".sql.gz" {
			dump = p
		}
		return nil
	})
	if dump == "" {
		fmt.Fprintf(os.Stderr, "snapshot %s has no dump of %s\n", snap, name)
		return 1
	}
	fmt.Printf("This replaces database %s with the copy from snapshot %s.\n", name, snap)
	fmt.Printf("Stop the %s server first (txAdmin, or: Stop-Service fxship-%s).\n", which, which)
	fmt.Print("Type the database name to go ahead: ")
	ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(ans) != name {
		fmt.Println("cancelled")
		return 1
	}
	cnf, err := mysqlDefaults(env("DB_HOST", "127.0.0.1"), env("DB_PORT", "3306"), user, pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.Remove(cnf)
	esc := "`" + strings.ReplaceAll(name, "`", "``") + "`"
	if out, err := exec.Command("mariadb", "--defaults-extra-file="+cnf, "-e",
		"DROP DATABASE IF EXISTS "+esc+"; CREATE DATABASE "+esc+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "recreating %s: %v %s\n", name, err, out)
		return 1
	}
	f, err := os.Open(dump)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	load := exec.Command("mariadb", "--defaults-extra-file="+cnf, "--max-allowed-packet=1G", name)
	load.Stdin = gz
	if out, err := load.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "loading %s: %v %s\n", name, err, clip(string(out), 600))
		return 1
	}
	fmt.Printf("restored %s from %s\n", name, snap)
	return 0
}
