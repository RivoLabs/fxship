package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

func TestCfxToLua(t *testing.T) {
	cases := map[string]string{
		"local h = `adder`":             "local h = 0",
		"count += 1":                    "count = count + 1",
		"self.x -= dt * 2":              "self.x = self.x - dt * 2",
		"t[i] *= 3":                     "t[i] = t[i] * 3",
		"if a == b then end":            "if a == b then end",
		"if a <= b and c >= d then end": "if a <= b and c >= d then end",
		"if a ~= b then end":            "if a ~= b then end",
		"local v = p?.coords":           "local v = p.coords",
	}
	for in, want := range cases {
		if got := string(cfxToLua([]byte(in))); got != want {
			t.Errorf("cfxToLua(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLintNeedsLuac(t *testing.T) {
	if _, err := exec.LookPath("luac5.4"); err != nil {
		t.Skip("luac5.4 not installed")
	}
	if is := lintFile("a.lua", []byte("local x = `hash`\nx += 1\nprint(x)\n")); is != nil {
		t.Fatalf("valid CfxLua flagged: %+v", is)
	}
	is := lintFile("b.lua", []byte("local x = 1\nif x then\nprint(x)\n"))
	if is == nil || !strings.Contains(is.Msg, "line") {
		t.Fatalf("missing end not caught: %+v", is)
	}
	if is := lintFile("c.json", []byte(`{"a":1,}`)); is == nil {
		t.Fatal("bad json not caught")
	}
}

func testCfg() *Config {
	loc, _ := time.LoadLocation("America/New_York")
	return &Config{
		Location: loc, RestartTimes: []string{"06:00", "18:00"}, ApplyLead: 90 * time.Second,
		ColdPrefixes:  []string{"[maps]/", "[eup]/", "[assets]/"},
		CoreResources: map[string]bool{"qbx_core": true},
	}
}

func TestNextRestart(t *testing.T) {
	a := &Agent{cfg: testCfg()}
	loc := a.cfg.Location
	cases := []struct{ now, want string }{
		{"2026-10-20 05:00", "2026-10-20 06:00"},
		{"2026-10-20 05:59", "2026-10-20 18:00"}, // inside the apply lead: too late for 06:00
		{"2026-10-20 12:00", "2026-10-20 18:00"},
		{"2026-10-20 23:00", "2026-10-21 06:00"},
	}
	for _, c := range cases {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.now, loc)
		if got := a.nextRestart(now).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("nextRestart(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitFlowAndClassification(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	sh(t, src, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(src, "[qbx]/qbx_core/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "[scripts]/shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "[scripts]/shop/server.lua"), "print('a')\n")
	write(t, filepath.Join(src, "[maps]/bank/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "[scripts]/old/fxmanifest.lua"), "fx_version 'cerulean'\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "base")
	base := sh(t, src, "git", "rev-parse", "HEAD")

	write(t, filepath.Join(src, "[scripts]/shop/server.lua"), "print('b')\n")
	write(t, filepath.Join(src, "[scripts]/shop/stream/x.ytd"), "bin")
	write(t, filepath.Join(src, "[qbx]/qbx_core/config.lua"), "return {}\n")
	write(t, filepath.Join(src, "[scripts]/newthing/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "[scripts]/newthing/client.lua"), "print(1)\n")
	write(t, filepath.Join(src, ".gitignore"), "x\n")
	os.RemoveAll(filepath.Join(src, "[scripts]/old"))
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "change things")
	head := sh(t, src, "git", "rev-parse", "HEAD")

	g := &Git{Dir: filepath.Join(root, "repo.git"), URL: src, Branch: "main"}
	if err := g.Ensure(); err != nil {
		t.Fatal(err)
	}
	got, err := g.Fetch()
	if err != nil || got != head {
		t.Fatalf("fetch = %s, %v; want %s", got, err, head)
	}
	wt := filepath.Join(root, "dev", "resources")
	os.MkdirAll(wt, 0o755)
	if err := g.Checkout(wt, base); err != nil {
		t.Fatal(err)
	}
	if err := g.Checkout(wt, head); err != nil {
		t.Fatal(err)
	}
	if g.Head(wt) != head {
		t.Fatal("worktree not at head")
	}
	if _, err := os.Stat(filepath.Join(wt, "[scripts]/old")); !os.IsNotExist(err) {
		t.Fatal("removed resource still on disk")
	}

	files, err := g.Diff(base, head)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.Resources(testCfg(), base, head, files)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ResourceChange{}
	for _, r := range res {
		by[r.Name] = r
	}
	expect := map[string]struct {
		kind string
		cold bool
	}{
		"shop":     {"changed", true}, // stream folder touched
		"qbx_core": {"changed", true},
		"newthing": {"added", false},
		"old":      {"removed", true},
	}
	if len(by) != len(expect) {
		t.Fatalf("resources = %+v", res)
	}
	for n, e := range expect {
		if by[n].Kind != e.kind || by[n].Cold != e.cold {
			t.Errorf("%s = %+v, want kind %s cold %v", n, by[n], e.kind, e.cold)
		}
	}
	cs, _ := g.Commits(base, head)
	if len(cs) != 1 || cs[0].Subject != "change things" {
		t.Fatalf("commits = %+v", cs)
	}
}

func TestConsoleErrors(t *testing.T) {
	log := "[ script:shop] ^1SCRIPT ERROR: @shop/server.lua:3: attempt to index a nil value^7\nall good\n[ citizen-server-impl] Couldn't find resource old_hair.\n[ script:other] SCRIPT ERROR: boom\n"
	errs := ConsoleErrors(log, []string{"shop"})
	if len(errs) != 3 || !strings.Contains(errs[0], "shop") {
		t.Fatalf("errs = %q", errs)
	}
}

// Discord rejects a V2 message over 40 components or 4000 characters of text, so a worst case
// release (many commits, resources, lint issues and a long dev log) must stay inside both.
func TestCardLimits(t *testing.T) {
	d := &Discord{cfg: &Config{WebURL: "https://github.com/example/server"}, icon: "https://cdn.discordapp.com/icons/1/a.png"}
	ok := false
	r := &Release{ID: "abcdef1", From: "1111111", To: "2222222", Status: StatusPending, DevOK: &ok,
		DevReport: "restarted, 3 console errors\n" + strings.Repeat("SCRIPT ERROR: @x/server.lua:1: boom\n", 80), CreatedAt: time.Now()}
	for i := 0; i < 40; i++ {
		r.Commits = append(r.Commits, Commit{Sha: strings.Repeat("a", 40), Author: "alex", Subject: strings.Repeat("long subject ", 20)})
		r.Resources = append(r.Resources, ResourceChange{Name: fmt.Sprintf("resource_number_%d", i), Kind: "changed", Cold: i%2 == 0, Reason: "streamed assets"})
		r.Lint = append(r.Lint, LintIssue{Path: "[scripts]/x/client.lua", Msg: strings.Repeat("unexpected symbol ", 10)})
	}
	for _, st := range []string{StatusPending, StatusQueued, StatusApplied, StatusRejected} {
		r.Status = st
		checkCard(t, st, d.card(r))
	}
}

// The dev edits card at its worst: hundreds of files, a lint issue in each, a wall of errors.
func TestEditCardLimits(t *testing.T) {
	d := &Discord{cfg: &Config{WebURL: "https://github.com/example/server"}, icon: "https://cdn.discordapp.com/icons/1/a.png"}
	ok := false
	e := &DevEdit{ID: "edit-1021-030000", Actor: "sam", DevOK: &ok, ShippedSha: strings.Repeat("b", 40),
		Note:   strings.Repeat("GitHub won't take files over 100 MB: [maps]/[big]/x/stream/huge.ytd (140 MB). ", 20),
		Reload: "restarted dev (400 resources changed), new errors:\n" + strings.Repeat("SCRIPT ERROR: @x/server.lua:1: boom\n", 80),
		Since:  time.Now(), UpdatedAt: time.Now()}
	for i := 0; i < 400; i++ {
		e.Files = append(e.Files, EditFile{Path: fmt.Sprintf("[standalone]/[custom]/some_long_resource_name_%d/client/main.lua", i), Code: "?", Owner: "alex"})
		e.Lint = append(e.Lint, LintIssue{Path: e.Files[i].Path, Msg: strings.Repeat("unexpected symbol ", 10)})
	}
	for _, st := range []string{EditOpen, EditShipped, EditDiscarded} {
		e.Status = st
		checkCard(t, st, d.editCard(e))
	}
}

func checkCard(t *testing.T, label string, card []discordgo.MessageComponent) {
	t.Helper()
	comps, text := 0, 0
	var walk func(c discordgo.MessageComponent)
	walk = func(c discordgo.MessageComponent) {
		comps++
		switch v := c.(type) {
		case discordgo.Container:
			for _, x := range v.Components {
				walk(x)
			}
		case discordgo.Section:
			for _, x := range v.Components {
				walk(x)
			}
			walk(v.Accessory)
		case discordgo.ActionsRow:
			for _, x := range v.Components {
				walk(x)
			}
		case discordgo.TextDisplay:
			text += utf8.RuneCountInString(v.Content)
		}
	}
	for _, c := range card {
		walk(c)
	}
	if comps > 40 || text > 4000 {
		t.Fatalf("%s: %d components, %d chars", label, comps, text)
	}
	if _, err := json.Marshal(card); err != nil {
		t.Fatal(err)
	}
}

// txAdmin's session cookie has colons in its name, which net/http refuses to parse. A fake txAdmin
// that only honours that exact cookie proves the client carries it from login to the next call.
func TestTxAdminSessionCookie(t *testing.T) {
	const sess = "txa:sess:635db7f0ad05"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/password":
			w.Header().Add("Set-Cookie", sess+"=good; Path=/; HttpOnly; SameSite=Lax")
			fmt.Fprint(w, `{"csrfToken":"tok"}`)
		default:
			if !strings.Contains(r.Header.Get("Cookie"), sess+"=good") || r.Header.Get("X-TxAdmin-CsrfToken") != "tok" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"logout":true}`)
				return
			}
			fmt.Fprint(w, `{"type":"success","msg":"The server is now restarting."}`)
		}
	}))
	defer srv.Close()
	x := NewTxAdmin(serverTarget{Name: "dev", TxURL: srv.URL, TxUser: "u", TxPassword: "p"})
	if err := x.Restart(); err != nil {
		t.Fatal(err)
	}
}

// Checkout must keep runtime data the server wrote into tracked files, unless the new commit
// changes that same file, and must clear an untracked file the new commit adds at the same path.
func TestCheckoutKeepsLocalChanges(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	sh(t, src, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(src, "a/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "a/stock.json"), "{}\n")
	write(t, filepath.Join(src, "a/config.lua"), "x = 1\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "base")
	base := sh(t, src, "git", "rev-parse", "HEAD")
	write(t, filepath.Join(src, "a/config.lua"), "x = 2\n")
	write(t, filepath.Join(src, "a/new.lua"), "print('repo')\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "next")
	head := sh(t, src, "git", "rev-parse", "HEAD")

	g := &Git{Dir: filepath.Join(root, "repo.git"), URL: src, Branch: "main"}
	if err := g.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Fetch(); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if err := g.Checkout(wt, base); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, "a/stock.json"), "{\"stock\":3}\n") // runtime data, untouched by head
	write(t, filepath.Join(wt, "a/config.lua"), "x = 99\n")        // local edit head also changes
	write(t, filepath.Join(wt, "a/new.lua"), "print('local')\n")   // untracked, head adds it
	clash, err := g.Clashes(wt, head)
	if err != nil || len(clash) != 2 {
		t.Fatalf("clashes = %v, %v", clash, err)
	}
	if err := g.Checkout(wt, head); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, _ := os.ReadFile(filepath.Join(wt, p))
		return strings.ReplaceAll(string(b), "", "")
	} // autocrlf on Windows
	if g.Head(wt) != head || read("a/stock.json") != "{\"stock\":3}\n" || read("a/config.lua") != "x = 2\n" || read("a/new.lua") != "print('repo')\n" {
		t.Fatalf("head %s stock %q config %q new %q", g.Head(wt), read("a/stock.json"), read("a/config.lua"), read("a/new.lua"))
	}
}
