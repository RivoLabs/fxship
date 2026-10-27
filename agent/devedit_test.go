package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTx stands in for txAdmin and the game server's info.json, recording every console command.
type fakeTx struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fakeTx) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/password":
			w.Header().Add("Set-Cookie", "txa:sess:test=ok; Path=/")
			fmt.Fprint(w, `{"csrfToken":"t"}`)
		case "/info.json":
			fmt.Fprint(w, `{}`)
		default:
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			f.mu.Lock()
			f.cmds = append(f.cmds, strings.TrimSpace(b["action"]+" "+b["parameter"]))
			f.mu.Unlock()
			fmt.Fprint(w, `{"type":"success","msg":"ok"}`)
		}
	})
}

func (f *fakeTx) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	return out
}

func TestDevEditsFlow(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	sh(t, root, "git", "init", "-q", "--bare", "-b", "main", origin)
	other := filepath.Join(root, "other") // a teammate's clone on their PC
	sh(t, root, "git", "clone", "-q", origin, other)
	sh(t, other, "git", "checkout", "-q", "-b", "main")
	write(t, filepath.Join(other, "[scripts]/shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(other, "[scripts]/shop/server.lua"), "local price = 10\n")
	write(t, filepath.Join(other, "[scripts]/bank/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(other, "[scripts]/bank/server.lua"), "local rate = 1\n")
	write(t, filepath.Join(other, "[criminal]/burglary/stock.json"), "{}\n")
	sh(t, other, "git", "add", "-A")
	sh(t, other, "git", "-c", "user.name=kool", "-c", "user.email=k@x", "commit", "-qm", "base")
	sh(t, other, "git", "push", "-q", "origin", "main")
	teammate := func(file, body, msg string) {
		sh(t, other, "git", "pull", "-q", "--rebase", "origin", "main")
		write(t, filepath.Join(other, file), body)
		sh(t, other, "git", "add", "-A")
		sh(t, other, "git", "-c", "user.name=kool", "-c", "user.email=k@x", "commit", "-qm", msg)
		sh(t, other, "git", "push", "-q", "origin", "main")
	}

	tx := &fakeTx{}
	srv := httptest.NewServer(tx.handler())
	defer srv.Close()
	logPath := filepath.Join(root, "fxserver.log")
	os.WriteFile(logPath, nil, 0o644)
	target := func(name string) serverTarget {
		return serverTarget{Name: name, Worktree: filepath.Join(root, name), TxURL: srv.URL, TxUser: "u", TxPassword: "p", InfoURL: srv.URL + "/info.json", LogPath: logPath}
	}
	cfg := &Config{Branch: "main", WebURL: "https://example/repo", Location: time.UTC, Dev: target("dev"), Prod: target("prod"), DevEnabled: true, Layout: "resources", ShipEmailDomain: "users.noreply.test",
		CoreResources: map[string]bool{}, DevIgnore: []*regexp.Regexp{globRe("[criminal]/burglary/stock.json"), globRe("**/output/**")}}
	st, _ := LoadState(filepath.Join(root, "state.json"))
	a := &Agent{cfg: cfg, git: &Git{Dir: filepath.Join(root, "repo.git"), URL: origin, Branch: "main"}, dev: NewTxAdmin(cfg.Dev), prod: NewTxAdmin(cfg.Prod), state: st, note: consoleNotifier{}}
	if err := a.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	w := newDevWatch()
	tick := func() { a.devTick(w); a.devTick(w) } // first sees the change, second (debounce 0) reloads
	dev := func(p string) string {
		b, _ := os.ReadFile(filepath.Join(cfg.Dev.Worktree, p))
		return strings.ReplaceAll(string(b), "\r", "")
	}
	originHead := func() string { return sh(t, root, "git", "--git-dir", origin, "rev-parse", "main") }

	// 1. The server writing runtime data is not an edit.
	write(t, filepath.Join(cfg.Dev.Worktree, "[criminal]/burglary/stock.json"), "{\"stock\":4}\n")
	tick()
	if a.state.DevEdit != nil {
		t.Fatalf("runtime file counted as an edit: %+v", a.state.DevEdit)
	}

	// 2. A staff edit on dev reloads that resource and opens the card.
	write(t, filepath.Join(cfg.Dev.Worktree, "[scripts]/shop/server.lua"), "local price = 12\n")
	tick()
	e := a.state.DevEdit
	if e == nil || e.Status != EditOpen || len(e.Files) != 1 || e.Resources[0] != "[scripts]/shop" {
		t.Fatalf("edit = %+v", e)
	}
	if got := strings.Join(tx.take(), ","); got != "refresh_res,ensure_res shop" {
		t.Fatalf("dev commands = %s", got)
	}
	tick() // nothing changed: no second reload
	if got := tx.take(); len(got) != 0 {
		t.Fatalf("reloaded again without a change: %v", got)
	}

	// 3. Ship commits as the presser, pushes, and opens a release already tested on dev.
	if _, _, err := a.Ship(e.ID, "sam", "  "); err == nil {
		t.Fatal("shipped without a message")
	}
	e, rel, err := a.Ship(e.ID, "sam", "Shop price to 12")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != EditShipped || originHead() != a.git.Head(cfg.Dev.Worktree) || rel == nil || rel.Status != StatusPending {
		t.Fatalf("ship: edit %+v release %+v", e, rel)
	}
	if author := sh(t, root, "git", "--git-dir", origin, "log", "-1", "--format=%an %s", "main"); author != "sam Shop price to 12" {
		t.Fatalf("commit = %q", author)
	}
	if !strings.HasPrefix(rel.DevReport, "tested live on dev") || dev("[criminal]/burglary/stock.json") != "{\"stock\":4}\n" {
		t.Fatalf("release dev report %q, runtime data %q", rel.DevReport, dev("[criminal]/burglary/stock.json"))
	}
	if err := a.Poll(); err != nil { // GitHub now has our own commit: no second release
		t.Fatal(err)
	}
	if n := len(a.state.Releases); n != 1 {
		t.Fatalf("releases = %d", n)
	}

	// 4. A teammate pushes a change to the same file while dev holds a new unshipped edit:
	// dev keeps the edit and the release says why dev didn't update.
	tick()
	write(t, filepath.Join(cfg.Dev.Worktree, "[scripts]/shop/server.lua"), "local price = 15\n")
	tick()
	teammate("[scripts]/shop/server.lua", "local price = 20\n", "price 20")
	if err := a.Poll(); err != nil {
		t.Fatal(err)
	}
	last := a.state.Releases[len(a.state.Releases)-1]
	if dev("[scripts]/shop/server.lua") != "local price = 15\n" || !strings.HasPrefix(last.DevReport, "dev not updated") {
		t.Fatalf("dev file %q, report %q", dev("[scripts]/shop/server.lua"), last.DevReport)
	}

	// 5. Discard drops the edit and dev catches up to the teammate's push.
	tx.take()
	if _, err := a.Discard(a.state.DevEdit.ID, "sam"); err != nil {
		t.Fatal(err)
	}
	if dev("[scripts]/shop/server.lua") != "local price = 20\n" || a.git.Head(cfg.Dev.Worktree) != originHead() || a.state.DevEdit.Status != EditDiscarded {
		t.Fatalf("after discard: file %q head %s origin %s", dev("[scripts]/shop/server.lua"), a.git.Head(cfg.Dev.Worktree), originHead())
	}

	// 6. Shipping after a teammate pushed something else replays the edit on top of their work.
	tick()
	write(t, filepath.Join(cfg.Dev.Worktree, "[scripts]/bank/server.lua"), "local rate = 2\n")
	tick()
	teammate("[scripts]/shop/server.lua", "local price = 25\n", "price 25")
	if _, _, err := a.Ship(a.state.DevEdit.ID, "sam", "Bank rate 2"); err != nil {
		t.Fatal(err)
	}
	if originHead() != a.git.Head(cfg.Dev.Worktree) || dev("[scripts]/shop/server.lua") != "local price = 25\n" || dev("[scripts]/bank/server.lua") != "local rate = 2\n" {
		t.Fatalf("after rebase ship: head %s origin %s shop %q bank %q", a.git.Head(cfg.Dev.Worktree), originHead(), dev("[scripts]/shop/server.lua"), dev("[scripts]/bank/server.lua"))
	}

	// 7. A big batch (400 files over 40 resources) waits out a slow copy, then restarts dev once
	// instead of reloading 40 resources, and the card line stays short.
	settle := func() { a.devTick(w); w.lastChange = time.Now().Add(-time.Minute); a.devTick(w) }
	// The 40 resources arrive from GitHub first, so the batch edits tracked files: new files the
	// agent's own user makes are ignored on purpose, and in this test that user writes everything.
	sh(t, other, "git", "pull", "-q", "--rebase", "origin", "main")
	for r := 0; r < 40; r++ {
		for f := 0; f < 10; f++ {
			write(t, filepath.Join(other, fmt.Sprintf("[batch]/res%02d/file%d.lua", r, f)), "return 1\n")
		}
		write(t, filepath.Join(other, fmt.Sprintf("[batch]/res%02d/fxmanifest.lua", r)), "fx_version 'cerulean'\n")
	}
	write(t, filepath.Join(other, "[scripts]/bank/data.bin"), "small\n")
	sh(t, other, "git", "add", "-A")
	sh(t, other, "git", "-c", "user.name=kool", "-c", "user.email=k@x", "commit", "-qm", "batch")
	sh(t, other, "git", "push", "-q", "origin", "main")
	if err := a.Poll(); err != nil {
		t.Fatal(err)
	}
	settle()
	tx.take()
	for r := 0; r < 40; r++ {
		for f := 0; f < 10; f++ {
			write(t, filepath.Join(cfg.Dev.Worktree, fmt.Sprintf("[batch]/res%02d/file%d.lua", r, f)), "return 2\n")
		}
	}
	a.devTick(w)
	a.devTick(w) // stable, but under the 15s a big batch waits for
	if got := tx.take(); len(got) != 0 {
		t.Fatalf("big batch loaded before the copy settled: %v", got)
	}
	w.lastChange = time.Now().Add(-time.Minute)
	a.devTick(w)
	e = a.state.DevEdit
	if got := strings.Join(tx.take(), ","); got != "restart" || len(e.Files) != 400 || !strings.HasPrefix(e.Reload, "restarted dev (40 resources changed)") {
		t.Fatalf("big batch: commands %q, files %d, reload %q", got, len(e.Files), e.Reload)
	}
	if n := len((&Discord{cfg: cfg}).editCard(e)); n != 1 {
		t.Fatal("card did not render")
	}
	if _, err := a.Discard(e.ID, "sam"); err != nil {
		t.Fatal(err)
	}
	settle()
	tx.take()

	// 8. Editing a core script restarts dev rather than hot-reloading it under players.
	cfg.CoreResources["bank"] = true
	write(t, filepath.Join(cfg.Dev.Worktree, "[scripts]/bank/server.lua"), "local rate = 3\n")
	settle()
	if got := strings.Join(tx.take(), ","); got != "restart" || !strings.Contains(a.state.DevEdit.Reload, "core resource: bank") {
		t.Fatalf("core edit: commands %q, reload %q", got, a.state.DevEdit.Reload)
	}

	// 9. Shipping a file GitHub would refuse fails up front and keeps the edit.
	big := filepath.Join(cfg.Dev.Worktree, "[scripts]/bank/data.bin")
	fh, _ := os.Create(big)
	fh.Truncate(101 << 20)
	fh.Close()
	settle()
	if _, _, err := a.Ship(a.state.DevEdit.ID, "sam", "huge"); err == nil || !strings.Contains(err.Error(), "over 100 MB") {
		t.Fatalf("huge file ship: %v", err)
	}
	if a.state.DevEdit.Status != EditOpen || originHead() != a.git.Head(cfg.Dev.Worktree) {
		t.Fatal("refused ship changed something")
	}
}
