package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeCfg(t *testing.T) {
	src := `endpoint_add_tcp "0.0.0.0:30120"
  endpoint_add_udp "0.0.0.0:30120"
sv_licenseKey "cfxk_abc"
set sv_licenseKey cfxk_abc
set mysql_connection_string "mysql://root:pw@localhost/db"
sv_hostname "My Server"
sets sv_hostname "x"
set sv_listingIpOverride 1.2.3.4
set sv_master1 ""
rcon_password hunter2
set sv_tebexSecret abc
sets sv_projectName "My Server"
set mysql_slow_query_warning 1500
ensure oxmysql
exec resources.cfg
`
	out, removed := SanitizeCfg([]byte(src))
	s := string(out)
	for _, gone := range []string{"0.0.0.0:30120", "cfxk_abc", "mysql://", "hunter2", "tebexSecret abc", "1.2.3.4", `sv_hostname "My`} {
		if strings.Contains(s, gone) {
			t.Errorf("sanitized cfg still has %q:\n%s", gone, s)
		}
	}
	for _, kept := range []string{`sets sv_projectName "My Server"`, "set mysql_slow_query_warning 1500", "ensure oxmysql", "exec resources.cfg"} {
		if !strings.Contains(s, kept+"\n") {
			t.Errorf("sanitized cfg lost %q", kept)
		}
	}
	want := "endpoint_add_tcp,endpoint_add_udp,sv_licensekey,mysql_connection_string,sv_hostname,sv_listingipoverride,sv_master1,rcon_password,sv_tebexsecret"
	if got := strings.Join(removed, ","); got != want {
		t.Errorf("removed = %s, want %s", got, want)
	}
}

func TestSyncCfgRegeneratesOnChange(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "server.cfg"), "endpoint_add_tcp \"0.0.0.0:30120\"\nensure a\n")
	if wrote, err := SyncCfg(dir); err != nil || !wrote {
		t.Fatalf("first sync wrote=%v err=%v", wrote, err)
	}
	if wrote, _ := SyncCfg(dir); wrote {
		t.Fatal("unchanged server.cfg rewritten")
	}
	time.Sleep(10 * time.Millisecond)
	write(t, filepath.Join(dir, "server.cfg"), "ensure a\nensure b\n")
	if wrote, _ := SyncCfg(dir); !wrote {
		t.Fatal("changed server.cfg not picked up")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "server.fxship.cfg"))
	if !strings.Contains(string(b), "ensure b\n") {
		t.Fatalf("server.fxship.cfg = %q", b)
	}
}

// Streaming is detected from the repo itself: a stream/ folder anywhere in the resource or a
// manifest that loads game data, wherever the resource sits. Folder names don't matter.
func TestAutoColdDetection(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	sh(t, root, "git", "init", "-q", "-b", "main", src)
	write(t, filepath.Join(src, "cars/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "cars/stream/car.yft"), "bin")
	write(t, filepath.Join(src, "cars/client.lua"), "print(1)\n")
	write(t, filepath.Join(src, "mlo/fxmanifest.lua"), "fx_version 'cerulean'\nthis_is_a_map 'yes'\n")
	write(t, filepath.Join(src, "mlo/client.lua"), "print(1)\n")
	write(t, filepath.Join(src, "meta/fxmanifest.lua"), "fx_version 'cerulean'\ndata_file 'HANDLING_FILE' 'handling.meta'\n")
	write(t, filepath.Join(src, "meta/handling.meta"), "<x/>\n")
	write(t, filepath.Join(src, "shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "shop/server.lua"), "print(1)\n")
	write(t, filepath.Join(src, "ox_lib/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "ox_lib/init.lua"), "print(1)\n")
	write(t, filepath.Join(src, "hotcore/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "hotcore/a.lua"), "print(1)\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "base")
	base := sh(t, src, "git", "rev-parse", "HEAD")
	for _, f := range []string{"cars/client.lua", "mlo/client.lua", "meta/handling.meta", "shop/server.lua", "ox_lib/init.lua", "hotcore/a.lua"} {
		write(t, filepath.Join(src, f), "print(2)\n")
	}
	sh(t, src, "git", "commit", "-qam", "touch all")
	head := sh(t, src, "git", "rev-parse", "HEAD")

	g := &Git{Dir: filepath.Join(root, "repo.git"), URL: src, Branch: "main"}
	if err := g.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Fetch(); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Layout: "resources", CoreResources: set(DefaultCoreResources + ",hotcore"), HotResources: set("hotcore")}
	files, _ := g.Diff(base, head)
	res, err := g.Resources(cfg, base, head, files)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"cars": "streamed assets", "mlo": "streamed assets", "meta": "streamed assets", "shop": "", "ox_lib": "core resource", "hotcore": ""}
	for _, r := range res {
		if r.Reason != want[r.Name] || r.Cold != (want[r.Name] != "") {
			t.Errorf("%s: cold=%v reason=%q, want %q", r.Name, r.Cold, r.Reason, want[r.Name])
		}
	}
	if len(res) != len(want) {
		t.Fatalf("resources = %+v", res)
	}
}

// In the server-data layout the repo holds server.cfg; a change to it needs a restart, and the
// installer's local files in the same folder never show up as changes.
func TestServerDataLayout(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	sh(t, root, "git", "init", "-q", "-b", "main", src)
	write(t, filepath.Join(src, "server.cfg"), "ensure shop\n")
	write(t, filepath.Join(src, "resources/shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(src, "README.md"), "hi\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "base")
	base := sh(t, src, "git", "rev-parse", "HEAD")
	write(t, filepath.Join(src, "server.cfg"), "ensure shop\nset x 1\n")
	write(t, filepath.Join(src, "README.md"), "hello\n")
	sh(t, src, "git", "commit", "-qam", "cfg")
	head := sh(t, src, "git", "rev-parse", "HEAD")

	g := &Git{Dir: filepath.Join(root, "repo.git"), URL: src, Branch: "main"}
	if err := g.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Fetch(); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Layout: "server-data", CoreResources: map[string]bool{}}
	files, _ := g.Diff(base, head)
	res, _ := g.Resources(cfg, base, head, files)
	if len(res) != 1 || res[0].Kind != "config" || !res[0].Cold || res[0].Name != "server.cfg" {
		t.Fatalf("resources = %+v", res)
	}

	// The worktree goes into a folder that already holds the installer's files.
	wt := filepath.Join(root, "server-data")
	write(t, filepath.Join(wt, "secrets.cfg"), "sv_licenseKey cfxk_x\n")
	write(t, filepath.Join(wt, "instance.cfg"), "sv_hostname x\n")
	write(t, filepath.Join(wt, "fxship.cfg"), "exec instance.cfg\n")
	if err := g.Checkout(wt, head); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "secrets.cfg")); string(b) != "sv_licenseKey cfxk_x\n" {
		t.Fatalf("secrets.cfg after worktree add = %q", b)
	}
	SyncCfg(wt)
	st, err := g.Status(wt)
	if err != nil || len(st) != 0 {
		t.Fatalf("status shows local files: %+v %v", st, err)
	}
	// Anything else in the way is refused, not overwritten.
	other := filepath.Join(root, "other")
	write(t, filepath.Join(other, "precious.txt"), "x")
	if err := g.Checkout(other, head); err == nil {
		t.Fatal("checkout over a folder with unknown files should fail")
	}
}

// Prod-only boxes get releases straight away, marked untested, and never touch a dev worktree.
func TestProdOnlyStage(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	sh(t, root, "git", "init", "-q", "-b", "main", src)
	write(t, filepath.Join(src, "shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "base")

	tx := &fakeTx{}
	srv := httptest.NewServer(tx.handler())
	defer srv.Close()
	tg := func(n string) serverTarget {
		return serverTarget{Name: n, Worktree: filepath.Join(root, n), ServerData: filepath.Join(root, n), TxURL: srv.URL, TxUser: "u", TxPassword: "p", InfoURL: srv.URL + "/info.json", LogPath: filepath.Join(root, "log")}
	}
	cfg := &Config{Branch: "main", Location: time.UTC, Layout: "resources", Prod: tg("prod"), Dev: tg("dev"), CoreResources: map[string]bool{}, RestartTimes: []string{"06:00"}}
	st, _ := LoadState(filepath.Join(root, "state.json"))
	a := &Agent{cfg: cfg, git: &Git{Dir: filepath.Join(root, "repo.git"), URL: src, Branch: "main"}, dev: NewTxAdmin(cfg.Dev), prod: NewTxAdmin(cfg.Prod), state: st, note: consoleNotifier{}}
	if err := a.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Dev.Worktree); !os.IsNotExist(err) {
		t.Fatal("prod-only bootstrap created a dev worktree")
	}
	write(t, filepath.Join(src, "shop/server.lua"), "print(1)\n")
	sh(t, src, "git", "add", "-A")
	sh(t, src, "git", "commit", "-qm", "shop")
	if err := a.Poll(); err != nil {
		t.Fatal(err)
	}
	r := a.state.Latest(StatusPending)
	if r == nil || r.DevOK != nil || !strings.Contains(r.DevReport, "no dev server") {
		t.Fatalf("release = %+v", r)
	}
	if _, err := a.Approve(r.ID, "sam"); err != nil {
		t.Fatal(err)
	}
	if a.state.ProdSha != r.To || a.git.Head(cfg.Prod.Worktree) != r.To {
		t.Fatal("prod not moved to the release")
	}
}

func TestStripDefiners(t *testing.T) {
	in := "/*!50017 DEFINER=`prod_user`@`%`*/ /*!50003 TRIGGER t1 BEFORE INSERT ON x FOR EACH ROW SET NEW.a = 1 */;\n" +
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */\nINSERT INTO `players` VALUES ('DEFINER is a word');\n"
	var out bytes.Buffer
	if err := stripDefiners(&out, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if strings.Contains(s, "prod_user") || strings.Contains(s, "`root`@") {
		t.Fatalf("definers left: %s", s)
	}
	if !strings.Contains(s, "SQL SECURITY DEFINER") || !strings.Contains(s, "'DEFINER is a word'") {
		t.Fatalf("too much removed: %s", s)
	}
}

func TestShipIdentity(t *testing.T) {
	a := &Agent{cfg: &Config{ShipEmailDomain: "users.noreply.test", ShipAuthors: map[string]string{"sam": "Sam Lee <sam@example.com>"}}}
	if n, e := a.shipIdentity("sam"); n != "Sam Lee" || e != "sam@example.com" {
		t.Errorf("mapped = %s <%s>", n, e)
	}
	if n, e := a.shipIdentity("Alex.B!"); n != "Alex.B!" || e != "alex.b@users.noreply.test" {
		t.Errorf("default = %s <%s>", n, e)
	}
	if _, e := a.shipIdentity("!!!"); e != "staff@users.noreply.test" {
		t.Errorf("empty local part = %s", e)
	}
}

func TestLoadConfigFromEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fxship.env")
	write(t, p, "# comment\nAPI_TOKEN=tok\nREPO_URL=\"git@github.com:acme/server.git\"\nexport PROD_TX_PASSWORD='p w'\nDEV_ENABLED=false\nPOWERED_BY=false\nTZ=UTC\n")
	for _, k := range []string{"API_TOKEN", "REPO_URL", "PROD_TX_PASSWORD", "DEV_ENABLED", "POWERED_BY", "TZ", "DEV_TX_PASSWORD", "REPO_WEB_URL", "DISCORD_TOKEN", "DISCORD_CHANNEL_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := loadEnvFile(p); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.WebURL != "https://github.com/acme/server" || c.Prod.TxPassword != "p w" || c.DevEnabled || c.PoweredBy {
		t.Fatalf("config = web %q pass %q dev %v powered %v", c.WebURL, c.Prod.TxPassword, c.DevEnabled, c.PoweredBy)
	}
	if !c.CoreResources["qb-core"] || !c.CoreResources["es_extended"] {
		t.Fatal("default core list missing frameworks")
	}
	os.Setenv("DEV_ENABLED", "true")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "DEV_TX_PASSWORD") {
		t.Fatalf("dev enabled without a password: %v", err)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "server.cfg"), "endpoint_add_tcp \"0.0.0.0:30120\"\nsv_licenseKey cfxk_x\nensure qb-core\n")
	write(t, filepath.Join(dir, "resources/[qb]/qb-core/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(dir, "resources/[maps]/bank/fxmanifest.lua"), "this_is_a_map 'yes'\n")
	write(t, filepath.Join(dir, "resources/[cars]/car/fxmanifest.lua"), "fx_version 'cerulean'\n")
	write(t, filepath.Join(dir, "resources/[cars]/car/stream/a.yft"), "x")
	write(t, filepath.Join(dir, "resources/shop/fxmanifest.lua"), "fx_version 'cerulean'\n")
	d, err := Detect(dir, set(DefaultCoreResources))
	if err != nil {
		t.Fatal(err)
	}
	if d.Layout != "server-data" || d.Framework != "QBCore" || d.Resources != 4 {
		t.Fatalf("detect = %+v", d)
	}
	if strings.Join(d.Core, ",") != "qb-core" || strings.Join(d.Streaming, ",") != "bank,car" {
		t.Fatalf("core %v streaming %v", d.Core, d.Streaming)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "license key") {
		t.Fatalf("warnings = %v", d.Warnings)
	}
	d, _ = Detect(filepath.Join(dir, "resources"), set(DefaultCoreResources))
	if d.Layout != "resources" || d.Resources != 4 {
		t.Fatalf("resources folder detect = %+v", d)
	}
}

func TestWebURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/server.git":       "https://github.com/acme/server",
		"ssh://git@github.com/acme/server.git": "https://github.com/acme/server",
		"https://github.com/acme/server.git":   "https://github.com/acme/server",
		"/srv/local/origin":                    "",
	} {
		if got := webURL(in); got != want {
			t.Errorf("webURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPoweredByFooter(t *testing.T) {
	on := &Discord{cfg: &Config{PoweredBy: true}}
	off := &Discord{cfg: &Config{PoweredBy: false}}
	if f := on.footer("-# x"); f != "-# x\n-# Powered by [Rivo](https://rivo.tools)" {
		t.Errorf("on = %q", f)
	}
	if f := off.footer("-# x"); f != "-# x" {
		t.Errorf("off = %q", f)
	}
	r := &Release{ID: "a", Status: StatusPending, CreatedAt: time.Now()}
	found := false
	for _, c := range on.card(r) {
		found = found || strings.Contains(stringify(c), "rivo.tools")
	}
	if !found {
		t.Error("release card has no powered-by line")
	}
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
