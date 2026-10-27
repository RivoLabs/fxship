package main

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestConfRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fxship.env")
	os.WriteFile(p, []byte("# kept out\nBRAND_NAME=\"Test City\"\nexport TZ='Europe/London'\nPOWERED_BY=true\n"), 0o600)
	c, err := loadConf(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.get("BRAND_NAME") != "Test City" || c.get("TZ") != "Europe/London" || !c.is("POWERED_BY") {
		t.Fatalf("read wrong: %v", c.vals)
	}
	c.set("BRAND_NAME", "Other City")
	c.set("API_TOKEN", "abc")
	b, _ := os.ReadFile(p)
	want := "BRAND_NAME=\"Other City\"\nTZ=\"Europe/London\"\nPOWERED_BY=\"true\"\nAPI_TOKEN=\"abc\"\n"
	if string(b) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", b, want)
	}
	// The agent reads what the installer writes.
	os.Unsetenv("API_TOKEN")
	t.Cleanup(func() { os.Unsetenv("API_TOKEN") })
	if err := loadEnvFile(p); err != nil || os.Getenv("API_TOKEN") != "abc" {
		t.Fatalf("agent can't read it: %v %q", err, os.Getenv("API_TOKEN"))
	}

	o := &conf{path: filepath.Join(t.TempDir(), "o.env"), vals: map[string]string{}}
	o.set("TOPOLOGY", "two-ip")
	c.merge(o)
	if c2, _ := loadConf(p); c2.get("TOPOLOGY") != "two-ip" || c2.get("BRAND_NAME") != "Other City" {
		t.Fatalf("merge lost something: %v", c2.vals)
	}
}

func TestConfRejectsQuotes(t *testing.T) {
	c := &conf{path: filepath.Join(t.TempDir(), "x.env"), vals: map[string]string{}}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("a value with a double quote was accepted")
		}
	}()
	c.set("BRAND_NAME", `The "Best" City`)
}

func TestManagedBlock(t *testing.T) {
	first := managedBlock("", "sv_licenseKey \"a\"\n")
	if first != "# BEGIN fxship (managed by the installer: re-run it to change these)\nsv_licenseKey \"a\"\n# END fxship\n" {
		t.Fatalf("new file:\n%s", first)
	}
	owner := first + "set my_api_key \"keep me\"\n"
	again := managedBlock(owner, "sv_licenseKey \"b\"\n")
	if !strings.Contains(again, "sv_licenseKey \"b\"") || strings.Contains(again, "\"a\"") || !strings.HasSuffix(again, "set my_api_key \"keep me\"\n") {
		t.Fatalf("re-run:\n%s", again)
	}
	if managedBlock(again, "sv_licenseKey \"b\"\n") != again {
		t.Fatal("same block twice changed the file")
	}
	if crlf := managedBlock(strings.ReplaceAll(owner, "\n", "\r\n"), "x\n"); strings.Contains(crlf, "\"a\"") {
		t.Fatalf("CRLF file kept the old block:\n%s", crlf)
	}
}

func testConf(t *testing.T, kv ...string) *conf {
	c := &conf{path: filepath.Join(t.TempDir(), "fxship.env"), vals: map[string]string{}}
	for i := 0; i < len(kv); i += 2 {
		c.set(kv[i], kv[i+1])
	}
	return c
}

func TestInstanceAndSecretsCfg(t *testing.T) {
	c := testConf(t, "TOPOLOGY", "two-ip", "PROD_IP", "1.2.3.4", "DEV_IP", "1.2.3.5", "PROD_GAME_PORT", "30120", "DEV_GAME_PORT", "30120",
		"BRAND_NAME", "Test City", "PROD_LICENSE_KEY", "cfxk_p", "DEV_LICENSE_KEY", "cfxk_d", "DB_INSTALL", "true",
		"DB_DEV_USER", "tc_dev", "DB_DEV_PASSWORD", "pw", "DB_DEV_NAME", "tc_dev")
	dev := instanceCfg(c, "dev")
	for _, want := range []string{`endpoint_add_udp "1.2.3.5:30120"`, `set sv_listingIpOverride "1.2.3.5"`, `sv_hostname "Test City [DEV]"`, `set sv_master1 ""`} {
		if !strings.Contains(dev, want) {
			t.Errorf("dev instance.cfg lacks %s:\n%s", want, dev)
		}
	}
	c.set("TOPOLOGY", "one-ip")
	c.set("DEV_GAME_PORT", "30130")
	if dev = instanceCfg(c, "dev"); !strings.Contains(dev, `endpoint_add_tcp "0.0.0.0:30130"`) || strings.Contains(dev, "Override") {
		t.Errorf("one-ip dev:\n%s", dev)
	}
	if prod := instanceCfg(c, "prod"); strings.Contains(prod, "sv_master1") || !strings.Contains(prod, `"0.0.0.0:30120"`) {
		t.Errorf("prod:\n%s", prod)
	}
	s := secretsCfg(c, "dev")
	if s != "sv_licenseKey \"cfxk_d\"\nset mysql_connection_string \"mysql://tc_dev:pw@127.0.0.1:3306/tc_dev?charset=utf8mb4\"\n" {
		t.Errorf("dev secrets.cfg:\n%s", s)
	}
}

func TestTxAdminConfig(t *testing.T) {
	b, err := txAdminConfig(nil, "Test City", "C:/fxship/prod/server-data/", []string{"06:00", "18:00"}, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	srv := v["server"].(map[string]any)
	if srv["cfgPath"] != "fxship.cfg" || srv["onesync"] != "legacy" || srv["autoStart"] != true {
		t.Fatalf("new config: %s", b)
	}
	// The owner changed onesync and Discord settings in txAdmin; a re-run keeps them.
	owned := strings.Replace(string(b), `"legacy"`, `"on"`, 1)
	owned = strings.Replace(owned, `"version": 2`, `"version": 2, "discordBot": {"enabled": true}`, 1)
	b2, err := txAdminConfig([]byte(owned), "x", "C:/fxship/prod/server-data/", nil, "off")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b2, &v)
	if v["server"].(map[string]any)["onesync"] != "on" || v["discordBot"] == nil || len(v["restarter"].(map[string]any)["schedule"].([]any)) != 0 {
		t.Fatalf("re-run config: %s", b2)
	}
	if _, err := txAdminConfig([]byte("{broken"), "x", "y", nil, ""); err == nil {
		t.Fatal("broken JSON accepted")
	}
}

func TestTxPasswordHash(t *testing.T) {
	h, err := txPasswordHash("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2b$11$") {
		t.Fatalf("hash %s isn't bcrypt $2b$ cost 11 like htpasswd's", h)
	}
	if bcrypt.CompareHashAndPassword([]byte("$2a$"+strings.TrimPrefix(h, "$2b$")), []byte("hunter22")) != nil {
		t.Fatal("hash doesn't verify")
	}
}

func TestSlugMatchesBash(t *testing.T) {
	for in, want := range map[string]string{"Test City Two": "test_city_two", "Rivo's  RP!": "rivo_s_rp", "A Very Long Server Name For Testing": "a_very_long_server_name"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSumFromList(t *testing.T) {
	sums := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  restic_0.19.1_windows_amd64.zip\n"
	if sumFromList(sums, "restic_0.19.1_windows_amd64.zip") == "" {
		t.Fatal("SHA256SUMS line not found")
	}
	table := "Filename | SHA-256\n-------- | -------\nGit-2.56.0.4-64-bit.exe | ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789\n"
	if got := sumFromList(table, "Git-2.56.0.4-64-bit.exe"); got != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Fatalf("markdown table: %q", got)
	}
	if sumFromList(table, "Git-2.56.0.4-32-bit.exe") != "" {
		t.Fatal("found a sum for a file that isn't listed")
	}
}

func TestUnzipRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "a.zip")
	f, _ := os.Create(zp)
	z := zip.NewWriter(f)
	w, _ := z.Create("ok/file.txt")
	w.Write([]byte("hi"))
	w, _ = z.Create("../escape.txt")
	w.Write([]byte("no"))
	z.Close()
	f.Close()
	if err := unzip(zp, filepath.Join(dir, "out")); err == nil {
		t.Fatal("an entry outside the folder was accepted")
	}
	if fileExists(filepath.Join(dir, "escape.txt")) {
		t.Fatal("escape.txt was written")
	}
}

func TestPrompter(t *testing.T) {
	c := testConf(t, "TX_USER", "admin")
	p := &prompter{in: bufio.NewReader(strings.NewReader("\nnewname\n\n7\n2\ny\n1,3\n"))}
	stdout = &strings.Builder{}
	defer func() { stdout = os.Stdout }()
	if got := p.ask(c, "TX_USER", "user", "x"); got != "admin" {
		t.Errorf("Enter should keep the saved value, got %q", got)
	}
	if got := p.ask(c, "BRAND_NAME", "name", "My FiveM Server"); got != "newname" {
		t.Errorf("typed answer, got %q", got)
	}
	opts := []string{"one-ip  a", "two-ip  b", "prod-only  c"}
	if got := p.choose(c, "TOPOLOGY", "q", "one-ip", opts); got != "one-ip" {
		t.Errorf("Enter on a menu should take the default, got %q", got)
	}
	if got := p.choose(c, "TOPOLOGY", "q", "one-ip", opts); got != "two-ip" {
		t.Errorf("7 is out of range and should be asked again, then 2: got %q", got)
	}
	if !p.askYes(c, "X", "q", false) {
		t.Error("y should be yes")
	}
	if got := p.pick("roles", 3, true); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("pick 1,3 = %v", got)
	}
	yes := &prompter{yes: true}
	if yes.ask(c, "BRAND_NAME", "q", "def") != "def" || yes.choose(c, "TOPOLOGY", "q", "one-ip", opts) != "one-ip" {
		t.Error("--yes should take saved values or defaults without reading")
	}
}

// Live lookups against Cfx's servers: FXSHIP_NET_TESTS=1 go test -run Artifact
func TestFxArtifactLive(t *testing.T) {
	if os.Getenv("FXSHIP_NET_TESTS") == "" {
		t.Skip("set FXSHIP_NET_TESTS=1")
	}
	for _, c := range []struct{ goos, build, suffix string }{
		{"windows", "recommended", "/server.zip"}, {"windows", "35245", "/server.zip"},
		{"linux", "recommended", "/fx.tar.xz"}, {"linux", "35245", "/fx.tar.xz"},
	} {
		url, build, err := fxArtifact(c.goos, c.build)
		if err != nil || !strings.HasSuffix(url, c.suffix) || build == "" {
			t.Errorf("%s %s: %s %s %v", c.goos, c.build, url, build, err)
		}
		t.Logf("%s %s -> %s (%s)", c.goos, c.build, build, url)
	}
	if _, _, err := fxArtifact("windows", "1"); err == nil {
		t.Error("build 1 found")
	}
}

// FiveM starts a new command at every ; even inside a # comment, and quotes change how a line is
// split, so nothing fxship generates may contain either outside real values.
func TestGeneratedCfgIsSafe(t *testing.T) {
	src := "sv_licenseKey x\nendpoint_add_tcp x\nendpoint_add_udp x\nsv_hostname x\nset sv_listingIpOverride x\nset sv_master1 x\n" +
		"set mysql_connection_string x\nrcon_password x\nset sv_tebexSecret x\nset onesync on\n"
	out, _ := SanitizeCfg([]byte(src))
	for _, text := range []string{string(out), fxshipCfg} {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "#") && strings.ContainsAny(line, ";'\"") {
				t.Errorf("generated comment would run as a command: %q", line)
			}
		}
	}
}

func TestAccountPass(t *testing.T) {
	for i := 0; i < 2000; i++ {
		p := accountPass(20, "abc")
		if len(p) != 20 || !strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || !strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") ||
			!strings.ContainsAny(p, "0123456789") || strings.Contains(strings.ToLower(p), "abc") {
			t.Fatalf("accountPass gave %q", p)
		}
	}
}
