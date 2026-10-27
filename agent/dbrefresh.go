package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// mysqlDefaults writes a client option file so passwords never show up in the process list.
func mysqlDefaults(host, port, user, pass string) (string, error) {
	f, err := os.CreateTemp("", "fxship-my-*.cnf")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_, err = fmt.Fprintf(f, "[client]\nhost=%s\nport=%s\nuser=%s\npassword=%s\n", host, port, q(user), q(pass))
	return f.Name(), err
}

// reDefiner strips DEFINER clauses: views and triggers made by prod's user can't be created by dev's
// user without SUPER, and they work the same with the importing user as definer.
var reDefiner = regexp.MustCompile("DEFINER=(`[^`]*`|'[^']*'|[^ @]*)@(`[^`]*`|'[^']*'|[^ ]*)\\s*")

func stripDefiners(dst io.Writer, src io.Reader) error {
	r := bufio.NewReaderSize(src, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.Contains(line, []byte("DEFINER=")) {
				line = reDefiner.ReplaceAll(line, nil)
			}
			if _, werr := dst.Write(line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// RefreshDevDB replaces the dev database with a copy of prod's. Dev is stopped for the copy, so it
// never runs against a half imported database, and started again afterwards.
func (a *Agent) RefreshDevDB(actor string) (string, error) {
	db := a.cfg.DB
	if !a.cfg.DevEnabled {
		return "", fmt.Errorf("this box has no dev server")
	}
	if !db.Configured() {
		return "", fmt.Errorf("the database settings (DB_PROD_*, DB_DEV_*) aren't in the config")
	}
	if db.ProdName == db.DevName {
		return "", fmt.Errorf("prod and dev use the same database name, refusing")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	start := time.Now()
	log.Printf("dev db refresh from %s to %s, started by %s", db.ProdName, db.DevName, actor)

	prodCnf, err := mysqlDefaults(db.Host, db.Port, db.ProdUser, db.ProdPassword)
	if err != nil {
		return "", err
	}
	defer os.Remove(prodCnf)
	devCnf, err := mysqlDefaults(db.Host, db.Port, db.DevUser, db.DevPassword)
	if err != nil {
		return "", err
	}
	defer os.Remove(devCnf)

	wasOnline := a.dev.Online()
	if wasOnline {
		if err := a.dev.Control("stop"); err != nil {
			return "", fmt.Errorf("stopping dev: %w", err)
		}
		for i := 0; i < 30 && a.dev.Online(); i++ {
			time.Sleep(2 * time.Second)
		}
	}
	startDev := func() string {
		if !wasOnline {
			return "Dev was off before, so it stays off."
		}
		if err := a.dev.Control("start"); err != nil {
			return "Dev did not start again: " + err.Error()
		}
		if !a.dev.WaitOnline(5 * time.Minute) {
			return "Dev was started but isn't answering after 5 minutes. Check txAdmin."
		}
		return "Dev is back up."
	}

	esc := "`" + strings.ReplaceAll(db.DevName, "`", "``") + "`"
	reset := exec.Command("mariadb", "--defaults-extra-file="+devCnf, "-e",
		"DROP DATABASE IF EXISTS "+esc+"; CREATE DATABASE "+esc+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;")
	if out, err := reset.CombinedOutput(); err != nil {
		return "", fmt.Errorf("recreating the dev database: %v: %s (%s)", err, strings.TrimSpace(string(out)), startDev())
	}

	dump := exec.Command("mariadb-dump", "--defaults-extra-file="+prodCnf, "--single-transaction", "--quick",
		"--hex-blob", "--triggers", "--no-tablespaces", "--skip-lock-tables", db.ProdName)
	load := exec.Command("mariadb", "--defaults-extra-file="+devCnf, "--max-allowed-packet=1G", db.DevName)
	var dumpErr, loadErr bytes.Buffer
	dump.Stderr, load.Stderr = &dumpErr, &loadErr
	dumpOut, err := dump.StdoutPipe()
	if err != nil {
		return "", err
	}
	loadIn, err := load.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := load.Start(); err != nil {
		return "", err
	}
	if err := dump.Start(); err != nil {
		loadIn.Close()
		load.Wait()
		return "", err
	}
	copyErr := stripDefiners(loadIn, dumpOut)
	loadIn.Close()
	dErr, lErr := dump.Wait(), load.Wait()
	switch {
	case dErr != nil:
		return "", fmt.Errorf("dumping prod: %v: %s (%s)", dErr, clip(strings.TrimSpace(dumpErr.String()), 400), startDev())
	case lErr != nil:
		return "", fmt.Errorf("loading into dev: %v: %s (%s)", lErr, clip(strings.TrimSpace(loadErr.String()), 400), startDev())
	case copyErr != nil:
		return "", fmt.Errorf("copying: %v (%s)", copyErr, startDev())
	}

	scrubbed := ""
	if f, err := os.Open(db.ScrubSQL); err == nil {
		scrub := exec.Command("mariadb", "--defaults-extra-file="+devCnf, db.DevName)
		scrub.Stdin = f
		out, err := scrub.CombinedOutput()
		f.Close()
		if err != nil {
			return "", fmt.Errorf("prod copied, but the scrub script %s failed: %v: %s (%s)", db.ScrubSQL, err, clip(strings.TrimSpace(string(out)), 400), startDev())
		}
		scrubbed = " and scrubbed with " + db.ScrubSQL
	}
	// Errors from the old database are meaningless against the new data.
	delete(a.state.KnownErrors, a.cfg.Dev.Name)
	a.save()
	took := time.Since(start).Round(time.Second)
	return fmt.Sprintf("Dev database `%s` is now a copy of prod%s (took %s). %s", db.DevName, scrubbed, took, startDev()), nil
}
