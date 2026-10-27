//go:build windows

package main

// "fxship install": the Windows Server installer. It asks the same questions as install.sh, writes
// the same settings file, and sets up the same pieces the Windows way:
//
//	fxship        the agent, a service running as LocalSystem
//	fxship-prod   FXServer + txAdmin for prod, as the virtual account NT SERVICE\fxship-prod
//	fxship-dev    the same for dev, capped on CPU and memory by its Job Object
//	fxship-mariadb  MariaDB on 127.0.0.1 only
//
// Each game server can only touch its own folder: a bad resource on dev can't read prod's secrets,
// the deploy key, or the settings file. Re-running it changes only what differs and never restarts a
// server that is up.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"golang.org/x/term"
)

const (
	sidSystem = "*S-1-5-18"
	sidAdmins = "*S-1-5-32-544"
	staffGrp  = "fxship-staff"
)

type winSetup struct {
	c       *conf
	p       *prompter
	bundle  string // folder the installer runs from (the unpacked release)
	prog    string // C:\Program Files\fxship
	data    string // C:\ProgramData\fxship
	root    string // C:\fxship
	creds   string // the root-only credentials file
	upgrade bool
	tsName  string
}

func installCmd(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	cfgFile := fs.String("config", "", "settings file to merge in before installing (answers every question)")
	upgrade := fs.Bool("upgrade", false, "re-apply every step with the saved settings, no questions")
	yes := fs.Bool("yes", false, "use saved settings and defaults, no questions")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		noColor()
	}
	w := &winSetup{
		prog:    filepath.Join(env("ProgramFiles", `C:\Program Files`), "fxship"),
		data:    filepath.Dir(paths().EnvFile),
		root:    paths().FivemRoot,
		upgrade: *upgrade,
	}
	w.creds = filepath.Join(w.data, "credentials.txt")
	self, _ := os.Executable()
	w.bundle = filepath.Dir(self)

	code := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				e, ok := r.(setupError)
				if !ok {
					panic(r)
				}
				failf(e.msg)
				code = 1
			}
		}()
		w.preflight()
		closeLog := w.logToFile()
		defer closeLog()
		c, err := loadConf(paths().EnvFile)
		if err != nil {
			die("reading %s: %v", paths().EnvFile, err)
		}
		w.c = c
		if *cfgFile != "" {
			o, err := loadConf(*cfgFile)
			if err != nil || len(o.keys) == 0 {
				die("can't read settings from %s", *cfgFile)
			}
			c.merge(o)
		}
		w.p = &prompter{in: bufio.NewReader(os.Stdin), yes: *yes || *upgrade || *cfgFile != ""}
		if term.IsTerminal(int(os.Stdin.Fd())) {
			w.p.secret = func() (string, error) {
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				return string(b), err
			}
		}
		w.installBinaries()
		w.wizard()
		w.tools()
		w.access()
		w.database()
		w.fxserver()
		w.servers()
		w.agent()
		w.permissions()
		w.startServers()
		w.backups()
		w.finish()
	}()
	if code != 0 {
		return code
	}
	step("Checking everything (fxship doctor)")
	return doctor(nil)
}

// --- running things ---

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func must(what string, name string, args ...string) string {
	out, err := run(name, args...)
	if err != nil {
		die("%s: %v\n%s", what, err, clip(out, 1500))
	}
	return out
}

func powershell(script string) (string, error) {
	return run("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; "+script)
}

// psq quotes a string for a PowerShell single-quoted literal.
func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func icacls(path string, args ...string) {
	must("setting permissions on "+path, "icacls", append([]string{path}, append(args, "/Q")...)...)
}

// lockDown makes path reachable by SYSTEM and Administrators only, plus the extra grants.
func lockDown(path string, extra ...string) {
	args := []string{"/inheritance:r", "/grant:r", sidSystem + ":(OI)(CI)F", "/grant:r", sidAdmins + ":(OI)(CI)F"}
	for _, g := range extra {
		args = append(args, "/grant:r", g)
	}
	icacls(path, args...)
}

func (w *winSetup) logToFile() func() {
	dir := filepath.Join(w.data, "logs")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "install.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}
	}
	fmt.Fprintf(f, "--- fxship install %s\n", time.Now().Format(time.RFC3339))
	stdout = io.MultiWriter(os.Stdout, &stripANSI{f})
	return func() { stdout = os.Stdout; f.Close() }
}

type stripANSI struct{ w io.Writer }

var reANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

func (s *stripANSI) Write(b []byte) (int, error) {
	_, err := s.w.Write(reANSI.ReplaceAll(b, nil))
	return len(b), err
}

func (w *winSetup) remember(label, value string) {
	old, _ := os.ReadFile(w.creds)
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(string(old), "\r\n", "\n"), "\n") {
		if l != "" && !strings.HasPrefix(l, label+":") {
			keep = append(keep, l)
		}
	}
	keep = append(keep, label+": "+value)
	if err := os.WriteFile(w.creds, []byte(strings.Join(keep, "\r\n")+"\r\n"), 0o600); err != nil {
		die("saving %s: %v", w.creds, err)
	}
}

// --- preflight and files ---

func (w *winSetup) preflight() {
	fmt.Fprintf(stdout, "%sfxship %s%s  Windows installer\n", colB, version, colN)
	if !windowsElevated() {
		die("run this from an elevated PowerShell (right-click > Run as administrator)")
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		die("can't read the Windows version: %v", err)
	}
	defer k.Close()
	build, _, _ := k.GetStringValue("CurrentBuildNumber")
	kind, _, _ := k.GetStringValue("InstallationType")
	product, _, _ := k.GetStringValue("ProductName")
	n, _ := strconv.Atoi(build)
	if (!strings.HasPrefix(kind, "Server") || n < 20348) && os.Getenv("FXSHIP_ANY_OS") != "1" {
		die("fxship supports Windows Server 2022 and 2025; this is %s (build %s). Set FXSHIP_ANY_OS=1 to try anyway.", product, build)
	}
	okf("%s (build %s)", product, build)
	for _, d := range []string{w.data, w.root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			die("creating %s: %v", d, err)
		}
	}
	// Settings, credentials and the deploy key live here: SYSTEM and Administrators only. The game
	// servers get their own folders and files granted later.
	lockDown(w.data)
	lockDown(w.root)
}

// installBinaries copies the release into Program Files. A running fxship.exe (the agent, or a
// game server's supervisor) can't be overwritten but can be renamed, so the old one is moved aside
// and the services pick the new one up at their next start.
func (w *winSetup) installBinaries() {
	if err := os.MkdirAll(w.prog, 0o755); err != nil {
		die("creating %s: %v", w.prog, err)
	}
	if old, _ := filepath.Glob(filepath.Join(w.prog, "*.old-*")); len(old) > 0 {
		for _, f := range old {
			os.Remove(f) // still in use until that service restarts; tried again next run
		}
	}
	if strings.EqualFold(filepath.Clean(w.bundle), filepath.Clean(w.prog)) {
		return
	}
	stamp := time.Now().Format("20060102150405")
	for _, name := range []string{"fxship.exe", "luac.exe", "install.ps1"} {
		src := filepath.Join(w.bundle, name)
		if !fileExists(src) {
			continue
		}
		dst := filepath.Join(w.prog, name)
		if fileExists(dst) {
			os.Rename(dst, dst+".old-"+stamp)
		}
		if err := copyFile(src, dst); err != nil {
			die("installing %s: %v", name, err)
		}
	}
	ex := filepath.Join(w.bundle, "examples")
	if fileExists(ex) {
		// robocopy's exit codes below 8 all mean success.
		if out, err := run("robocopy", ex, filepath.Join(w.prog, "examples"), "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/NP"); err != nil {
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() >= 8 {
				die("copying the examples: %v %s", err, out)
			}
		}
	}
	addToMachinePath(w.prog)
	okf("fxship %s in %s", version, w.prog)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func addToMachinePath(dir string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	cur, _, _ := k.GetStringValue("Path")
	for _, p := range strings.Split(cur, ";") {
		if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
			return
		}
	}
	k.SetExpandStringValue("Path", strings.TrimRight(cur, ";")+";"+dir)
	os.Setenv("PATH", os.Getenv("PATH")+";"+dir)
	// Tell open windows the environment changed, so a new PowerShell finds fxship.
	env, _ := windows.UTF16PtrFromString("Environment")
	windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW").Call(
		0xffff, 0x001A, 0, uintptr(unsafe.Pointer(env)), 0x0002, 5000, 0)
}

// --- the questions ---

func (w *winSetup) wizard() {
	c, p := w.c, w.p
	fmt.Fprintf(stdout, "\n%sfxship setup%s  %s(Enter keeps the value in brackets)%s\n", colB, colN, colD, colN)

	step("Your server")
	c.set("BRAND_NAME", p.ask(c, "BRAND_NAME", "Server name (shown on Discord cards and in txAdmin)", "My FiveM Server"))
	zone, _ := time.Now().Zone()
	info("Restart times use this machine's time zone (%s). Change it in Settings > Time first if that's wrong.", zone)
	fmt.Fprintln(stdout)
	topo := p.choose(c, "TOPOLOGY", "How should prod and dev run?", "one-ip", []string{
		"one-ip     One public IP: prod on port 30120, dev on 30130 (most servers)",
		"two-ip     Two public IPs: each server on its own IP, both on 30120",
		"prod-only  No dev server: approved deploys go straight to prod, untested",
	})
	ips := localIPs()
	first, second := "", ""
	if len(ips) > 0 {
		first = ips[0]
	}
	if len(ips) > 1 {
		second = ips[1]
	}
	info("%sIPs on this machine: %s%s", colD, strings.Join(ips, " "), colN)
	switch topo {
	case "one-ip", "prod-only":
		pub := p.ask(c, "PUBLIC_IP", "Public IP players connect to", first)
		c.set("PUBLIC_IP", pub)
		c.set("PROD_IP", pub)
		c.set("DEV_IP", pub)
		c.set("PROD_GAME_PORT", "30120")
		dp := c.get("DEV_GAME_PORT")
		if dp == "" || dp == "30120" {
			dp = "30130"
		}
		if topo == "one-ip" {
			c.vals["DEV_GAME_PORT"] = dp
			dp = p.ask(c, "DEV_GAME_PORT", "Dev game port", dp)
		}
		c.set("DEV_GAME_PORT", dp)
	case "two-ip":
		pi := p.ask(c, "PROD_IP", "Prod public IP", first)
		di := c.get("DEV_IP")
		if di == "" || di == pi {
			c.vals["DEV_IP"] = second
		}
		di = p.ask(c, "DEV_IP", "Dev public IP (the second IP must already be on this machine)", second)
		if pi == di {
			die("two-ip needs two different IPs")
		}
		c.set("PROD_IP", pi)
		c.set("DEV_IP", di)
		c.set("PUBLIC_IP", pi)
		c.set("PROD_GAME_PORT", "30120")
		c.set("DEV_GAME_PORT", "30120")
	default:
		die("TOPOLOGY must be one-ip, two-ip or prod-only")
	}
	c.set("TOPOLOGY", topo)
	c.set("DEV_ENABLED", boolStr(topo != "prod-only"))
	rt := p.ask(c, "RESTART_TIMES", "Prod scheduled restarts, 24h, comma separated (cold changes go live at these)", "06:00,18:00")
	if !reRestartTimes.MatchString(rt) {
		die("RESTART_TIMES must look like 06:00,18:00")
	}
	c.set("RESTART_TIMES", rt)

	w.wizardRepo()
	w.wizardCfg()

	step("Cfx.re license keys")
	info("Each running server needs a key from https://portal.cfx.re (Servers > Register).")
	key := p.askSecret(c, "PROD_LICENSE_KEY", "Prod license key (cfxk_...)")
	if !strings.HasPrefix(key, "cfxk_") {
		die("a prod license key starting with cfxk_ is required")
	}
	c.set("PROD_LICENSE_KEY", key)
	if c.is("DEV_ENABLED") {
		info("Dev can use its own key or share prod's. A separate key keeps dev off prod's server listing.")
		dk := p.askSecret(c, "DEV_LICENSE_KEY", "Dev license key (Enter to use the prod key)")
		if dk == "" {
			dk = key
		}
		c.set("DEV_LICENSE_KEY", dk)
	}

	step("txAdmin")
	u := p.ask(c, "TX_USER", "txAdmin admin username (you and fxship sign in with it)", "admin")
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{3,20}$`).MatchString(u) {
		die("username: 3-20 letters, digits, _ . -")
	}
	c.set("TX_USER", u)
	for _, n := range w.servers_() {
		k := strings.ToUpper(n) + "_TX_PASSWORD"
		if c.get(k) == "" {
			c.set(k, genPass(20))
		}
		w.remember("txAdmin "+n+" ("+u+")", c.get(k))
	}
	info("Passwords are generated and saved to %s.", w.creds)
	c.set("FX_BUILD", p.ask(c, "FX_BUILD", "FXServer build: recommended, latest, or a build number", "recommended"))

	w.wizardDB()
	w.wizardDiscord()
	w.wizardExtras()
	if c.get("API_TOKEN") == "" {
		c.set("API_TOKEN", genPass(40))
	}
}

func (w *winSetup) servers_() []string {
	if w.c.is("DEV_ENABLED") {
		return []string{"prod", "dev"}
	}
	return []string{"prod"}
}

// localIPs lists this machine's IPv4 addresses, leaving out Tailscale's.
func localIPs() []string {
	var out []string
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || strings.Contains(strings.ToLower(i.Name), "tailscale") {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || cgnat.Contains(ip) {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}

func (w *winSetup) sshDir() string    { return filepath.Join(w.root, ".ssh") }
func (w *winSetup) deployKey() string { return filepath.Join(w.sshDir(), "deploy_key") }

func (w *winSetup) gitSSH() string {
	ssh := filepath.ToSlash(filepath.Join(env("SystemRoot", `C:\Windows`), "System32", "OpenSSH", "ssh.exe"))
	return ssh + " -i " + filepath.ToSlash(w.deployKey()) + " -o IdentitiesOnly=yes -o UserKnownHostsFile=" +
		filepath.ToSlash(filepath.Join(w.sshDir(), "known_hosts")) + " -o StrictHostKeyChecking=yes"
}

func (w *winSetup) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+w.gitSSH(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (w *winSetup) wizardRepo() {
	c, p := w.c, w.p
	step("Your GitHub repository")
	info("fxship deploys from a GitHub repo holding either your resources folder or your whole")
	info("server-data folder (server.cfg plus resources/). Not in git yet? See the guide's")
	info("\"Put your server in git\" chapter, then come back.")
	url := p.ask(c, "REPO_URL", "Repository (owner/name, or its SSH URL)", "")
	if url == "" {
		die("a repository is required")
	}
	if regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(url) {
		url = "git@github.com:" + strings.TrimSuffix(url, ".git") + ".git"
	}
	if m := regexp.MustCompile(`^https://github\.com/(.+)$`).FindStringSubmatch(url); m != nil {
		url = "git@github.com:" + strings.TrimSuffix(m[1], ".git") + ".git"
	}
	if !regexp.MustCompile(`^git@github\.com:.+/.+\.git$`).MatchString(url) {
		die("use owner/name or git@github.com:owner/name.git")
	}
	branch := p.ask(c, "REPO_BRANCH", "Branch to deploy from", "main")
	c.set("REPO_URL", url)
	c.set("REPO_BRANCH", branch)

	w.tool("git", w.installGit)
	if !fileExists(filepath.Join(env("SystemRoot", `C:\Windows`), "System32", "OpenSSH", "ssh.exe")) {
		info("installing the OpenSSH client (a Windows feature)...")
		if out, err := powershell("Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0 | Out-Null"); err != nil {
			die("the OpenSSH client isn't installed and couldn't be added: %v\n%s", err, out)
		}
	}
	os.MkdirAll(w.sshDir(), 0o700)
	w.githubKnownHosts()
	if !fileExists(w.deployKey()) {
		must("making the deploy key", "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "fxship@"+hostname(), "-f", w.deployKey())
	}
	// OpenSSH refuses a key others can read: SYSTEM (the agent) and Administrators only.
	icacls(w.deployKey(), "/inheritance:r", "/grant:r", sidSystem+":F", "/grant:r", sidAdmins+":F")
	c.set("GIT_SSH_COMMAND", w.gitSSH())

	web := "https://github.com/" + strings.TrimSuffix(strings.TrimPrefix(url, "git@github.com:"), ".git")
	for {
		out, err := w.git("ls-remote", url, "refs/heads/"+branch)
		if err == nil && out != "" {
			break
		}
		pub, _ := os.ReadFile(w.deployKey() + ".pub")
		fmt.Fprintln(stdout)
		info("Add this deploy key to the repo: %s", bold(web+"/settings/keys/new"))
		info("Title: fxship %s. Tick %s (staff use it to ship dev edits from Discord).", hostname(), bold("Allow write access"))
		fmt.Fprintf(stdout, "\n      %s%s%s\n\n", colC, strings.TrimSpace(string(pub)), colN)
		if p.yes {
			die("can't read %s (%s) yet: add the key above, then run the installer again", url, branch)
		}
		p.line("  Press Enter once it's added (Ctrl+C to stop)...")
	}
	okf("the deploy key can read %s (%s)", url, branch)

	if c.get("REPO_LAYOUT") != "" && c.get("REPO_DETECTED") == url+"#"+branch {
		okf("layout: %s", c.get("REPO_LAYOUT"))
		return
	}
	tmp, err := os.MkdirTemp("", "fxship-detect-")
	if err != nil {
		die("%v", err)
	}
	defer os.RemoveAll(tmp)
	info("Fetching the repo to look at it (big repos take a while)...")
	if out, err := w.git("clone", "-q", "--depth", "1", "--branch", branch, url, filepath.Join(tmp, "repo")); err != nil {
		die("clone failed: %v\n%s", err, out)
	}
	fmt.Fprintln(stdout)
	d, err := Detect(filepath.Join(tmp, "repo"), set(env("CORE_RESOURCES", DefaultCoreResources)))
	if err != nil {
		die("looking at the repo: %v", err)
	}
	printDetection(d, "    ")
	c.set("REPO_LAYOUT", d.Layout)
	c.set("REPO_DETECTED", url+"#"+branch)
	if d.Layout == "resources" && c.get("CFG_SOURCE") == "" && fileExists(filepath.Join(tmp, "repo", "server.cfg")) {
		c.set("CFG_SOURCE", "repo")
	}
}

func hostname() string { h, _ := os.Hostname(); return h }

func (w *winSetup) githubKnownHosts() {
	b, err := httpGet("https://api.github.com/meta")
	if err != nil {
		die("couldn't fetch GitHub's SSH host keys from api.github.com: %v", err)
	}
	var meta struct {
		Keys []string `json:"ssh_keys"`
	}
	if json.Unmarshal(b, &meta) != nil || len(meta.Keys) == 0 {
		die("api.github.com/meta had no SSH host keys")
	}
	var s strings.Builder
	for _, k := range meta.Keys {
		s.WriteString("github.com " + k + "\n")
	}
	if err := os.WriteFile(filepath.Join(w.sshDir(), "known_hosts"), []byte(s.String()), 0o644); err != nil {
		die("%v", err)
	}
}

func (w *winSetup) wizardCfg() {
	c, p := w.c, w.p
	if c.get("REPO_LAYOUT") == "server-data" {
		c.set("CFG_SOURCE", "repo")
		return
	}
	step("Server config")
	info("Your repo holds the resources, so server.cfg lives on this machine.")
	info("Give the folder of your current server.cfg (the files exec'd from it are copied too),")
	info("or type %s for a minimal one to edit later.", bold("starter"))
	src := p.ask(c, "CFG_SOURCE", "Folder with your server.cfg, or starter", "starter")
	if src != "starter" && src != "repo" && !fileExists(filepath.Join(src, "server.cfg")) {
		die("no server.cfg in %s", src)
	}
	c.set("CFG_SOURCE", src)
}

func (w *winSetup) wizardDB() {
	c, p := w.c, w.p
	step("Database")
	inst := p.askYes(c, "DB_INSTALL", "Run MariaDB on this machine for the servers?", true)
	c.set("DB_INSTALL", boolStr(inst))
	if !inst {
		info("Skipped: set mysql_connection_string in each secrets.cfg yourself.")
		return
	}
	s := slug(c.get("BRAND_NAME"))
	if s == "" {
		s = "fivem"
	}
	c.set("DB_PROD_NAME", p.ask(c, "DB_PROD_NAME", "Prod database name", s+"_prod"))
	users := s
	if len(users) > 20 {
		users = users[:20]
	}
	def := func(k, v string) {
		if c.get(k) == "" {
			c.set(k, v)
		}
	}
	def("DB_PROD_USER", users+"_prod")
	def("DB_PROD_PASSWORD", genPass(24))
	def("MARIADB_ROOT_PASSWORD", genPass(28))
	if c.is("DEV_ENABLED") {
		c.set("DB_DEV_NAME", p.ask(c, "DB_DEV_NAME", "Dev database name", s+"_dev"))
		if c.get("DB_DEV_NAME") == c.get("DB_PROD_NAME") {
			die("dev and prod need different databases")
		}
		def("DB_DEV_USER", users+"_dev")
		def("DB_DEV_PASSWORD", genPass(24))
	}
	imp := p.ask(c, "DB_IMPORT", "Import an existing .sql or .sql.gz dump into prod (path, or blank)", "")
	if imp != "" && !fileExists(imp) {
		die("no such file: %s", imp)
	}
	c.set("DB_IMPORT", imp)
	w.remember("MariaDB root", c.get("MARIADB_ROOT_PASSWORD"))
}

func discordGet(token, path string, v any) error {
	req, _ := http.NewRequest("GET", "https://discord.com/api/v10"+path, nil)
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("discord %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (w *winSetup) wizardDiscord() {
	c, p := w.c, w.p
	step("Discord")
	info("fxship posts release cards with Approve and Reject buttons. Create a bot for your server:")
	info("https://discord.com/developers/applications > New Application > Bot > Reset Token.")
	info("Leave this blank to approve from the command line only.")
	tok := p.askSecret(c, "DISCORD_TOKEN", "Bot token")
	if tok == "" {
		c.set("DISCORD_TOKEN", "")
		c.set("DISCORD_CHANNEL_ID", "")
		return
	}
	var me struct{ ID, Username string }
	if err := discordGet(tok, "/users/@me", &me); err != nil {
		die("Discord rejected that token (%v)", err)
	}
	okf("bot %s", me.Username)
	c.set("DISCORD_TOKEN", tok)
	type named struct{ ID, Name string }
	var guilds []named
	for {
		if err := discordGet(tok, "/users/@me/guilds", &guilds); err != nil {
			die("%v", err)
		}
		if len(guilds) > 0 {
			break
		}
		if p.yes {
			die("the bot isn't in any Discord server yet")
		}
		info("Invite the bot to your server, then press Enter:")
		info("%s", bold("https://discord.com/oauth2/authorize?client_id="+me.ID+"&scope=bot+applications.commands&permissions=117760"))
		p.line("")
	}
	g := guilds[0]
	if len(guilds) > 1 && !p.yes {
		for i, x := range guilds {
			fmt.Fprintf(stdout, "    %d) %s\n", i+1, x.Name)
		}
		g = guilds[p.pick("Which server?", len(guilds), false)[0]]
	}
	if p.yes {
		if c.get("DISCORD_CHANNEL_ID") == "" {
			die("set DISCORD_CHANNEL_ID")
		}
	} else {
		var all []struct {
			ID, Name string
			Type     int
			Position int
		}
		discordGet(tok, "/guilds/"+g.ID+"/channels", &all)
		var chans []named
		for _, ch := range sortedBy(all, func(a, b int) bool { return all[a].Position < all[b].Position }) {
			if all[ch].Type == 0 {
				chans = append(chans, named{all[ch].ID, all[ch].Name})
			}
		}
		for i, ch := range chans {
			fmt.Fprintf(stdout, "    %d) #%s\n", i+1, ch.Name)
		}
		if len(chans) == 0 {
			die("the bot can't see any text channels in %s", g.Name)
		}
		c.set("DISCORD_CHANNEL_ID", chans[p.pick("Channel for release cards (a private #deploys is best):", len(chans), false)[0]].ID)
		var rs []struct {
			ID, Name string
			Position int
			Managed  bool
		}
		discordGet(tok, "/guilds/"+g.ID+"/roles", &rs)
		var roles []named
		for _, r := range sortedBy(rs, func(a, b int) bool { return rs[a].Position > rs[b].Position }) {
			if rs[r].Name != "@everyone" && !rs[r].Managed {
				roles = append(roles, named{rs[r].ID, rs[r].Name})
			}
		}
		for i, r := range roles {
			fmt.Fprintf(stdout, "    %d) @%s\n", i+1, r.Name)
		}
		ids := func(idx []int) string {
			var out []string
			for _, i := range idx {
				out = append(out, roles[i].ID)
			}
			return strings.Join(out, ",")
		}
		c.set("DISCORD_APPROVER_ROLE_IDS", ids(p.pick("Roles that may approve prod releases, e.g. 1,3 (server admins always can):", len(roles), true)))
		if c.is("DEV_ENABLED") {
			c.set("DISCORD_SHIPPER_ROLE_IDS", ids(p.pick("Extra roles that may ship dev edits but not approve (blank for none):", len(roles), true)))
		}
	}
	c.set("POWERED_BY", boolStr(p.askYes(c, "POWERED_BY", "Show a small \"Powered by Rivo\" line on cards?", true)))
}

// sortedBy returns the indexes of a slice in the order less gives.
func sortedBy[T any](xs []T, less func(a, b int) bool) []int {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && less(idx[j], idx[j-1]); j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
	return idx
}

func (w *winSetup) wizardExtras() {
	c, p := w.c, w.p
	step("Backups")
	info("A nightly copy of both databases, configs, txAdmin data and runtime files, kept with restic")
	info("(7 daily, 4 weekly, 6 monthly). Use storage off this machine: a dead disk takes local backups too.")
	on := p.askYes(c, "BACKUP_ENABLED", "Turn on backups?", true)
	c.set("BACKUP_ENABLED", boolStr(on))
	if on {
		info("Where to: %s, %s,", bold("s3:https://s3.amazonaws.com/bucket/fxship"), bold("b2:bucket:fxship"))
		info("%s, or a local folder (not recommended).", bold("sftp:user@host:/backups/fxship"))
		repo := p.ask(c, "BACKUP_REPO", "Backup location", `C:\fxship-backups`)
		c.set("BACKUP_REPO", repo)
		switch {
		case strings.HasPrefix(repo, "s3:"):
			c.set("AWS_ACCESS_KEY_ID", p.ask(c, "AWS_ACCESS_KEY_ID", "S3 access key id", ""))
			c.set("AWS_SECRET_ACCESS_KEY", p.askSecret(c, "AWS_SECRET_ACCESS_KEY", "S3 secret key"))
		case strings.HasPrefix(repo, "b2:"):
			c.set("B2_ACCOUNT_ID", p.ask(c, "B2_ACCOUNT_ID", "B2 key id", ""))
			c.set("B2_ACCOUNT_KEY", p.askSecret(c, "B2_ACCOUNT_KEY", "B2 application key"))
		case filepath.IsAbs(repo):
			warnf("local backups don't survive losing this machine")
		}
		c.set("BACKUP_TIME", p.ask(c, "BACKUP_TIME", "Daily backup time", "04:30"))
		if c.get("RESTIC_PASSWORD") == "" {
			c.set("RESTIC_PASSWORD", genPass(32))
		}
		w.remember("Backup encryption password (without it backups can't be restored, keep a copy elsewhere)", c.get("RESTIC_PASSWORD"))
	}

	step("Admin access")
	info("txAdmin and the dev file share are safest reachable only over Tailscale, a free private")
	info("network for you and your staff. Without it they are limited to the IPs you list.")
	ts := p.askYes(c, "TAILSCALE", "Use Tailscale for admin pages?", true)
	c.set("TAILSCALE", boolStr(ts))
	if !ts {
		c.set("ADMIN_ALLOW_IPS", p.ask(c, "ADMIN_ALLOW_IPS", "IPs allowed to reach admin pages, comma separated", ""))
	}
}

// --- tools ---

// tool makes sure a program is on PATH, installing it if not.
func (w *winSetup) tool(name string, install func()) {
	if _, err := exec.LookPath(name); err == nil {
		return
	}
	install()
	if _, err := exec.LookPath(name); err != nil {
		die("%s was installed but still isn't on PATH", name)
	}
}

func prependPath(dir string) {
	if !strings.Contains(strings.ToLower(os.Getenv("PATH")), strings.ToLower(dir)) {
		os.Setenv("PATH", dir+";"+os.Getenv("PATH"))
	}
}

func (w *winSetup) installGit() {
	info("installing Git for Windows...")
	tag, body, assets, err := githubLatest("git-for-windows/git")
	if err != nil {
		die("finding Git for Windows: %v", err)
	}
	var name string
	for n := range assets {
		if regexp.MustCompile(`^Git-[0-9.]+-64-bit\.exe$`).MatchString(n) {
			name = n
		}
	}
	sum := sumFromList(body, name)
	if name == "" || sum == "" {
		die("Git for Windows %s: no 64-bit installer with a checksum in the release", tag)
	}
	f := filepath.Join(os.TempDir(), name)
	if err := download(assets[name], f, sum); err != nil {
		die("downloading Git: %v", err)
	}
	defer os.Remove(f)
	must("installing Git", f, "/VERYSILENT", "/NORESTART", "/NOCANCEL", "/SP-", "/SUPPRESSMSGBOXES", "/o:PathOption=Cmd")
	prependPath(filepath.Join(env("ProgramFiles", `C:\Program Files`), "Git", "cmd"))
	okf("Git %s", strings.TrimPrefix(tag, "v"))
}

// toolDirs are where the tools fxship runs live. Services only see the PATH Windows had when it
// booted, so the agent and the backup task get them from FXSHIP_PATH instead.
func (w *winSetup) toolDirs() []string {
	dirs := []string{w.prog}
	if p, err := exec.LookPath("git"); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	if d := w.mariadbDir(); d != "" {
		dirs = append(dirs, filepath.Join(d, "bin"))
	}
	return dirs
}

func (w *winSetup) tools() {
	step("Tools")
	if c := w.c; c.is("BACKUP_ENABLED") && !fileExists(filepath.Join(w.prog, "restic.exe")) {
		tag, _, assets, err := githubLatest("restic/restic")
		if err != nil {
			die("finding restic: %v", err)
		}
		ver := strings.TrimPrefix(tag, "v")
		name := "restic_" + ver + "_windows_amd64.zip"
		sums, err := httpGet(assets["SHA256SUMS"])
		if err != nil || assets[name] == "" {
			die("restic %s: no Windows download with checksums", tag)
		}
		zf := filepath.Join(os.TempDir(), name)
		if err := download(assets[name], zf, sumFromList(string(sums), name)); err != nil {
			die("downloading restic: %v", err)
		}
		defer os.Remove(zf)
		tmp, _ := os.MkdirTemp("", "restic-")
		defer os.RemoveAll(tmp)
		if err := unzip(zf, tmp); err != nil {
			die("unpacking restic: %v", err)
		}
		if err := copyFile(filepath.Join(tmp, "restic_"+ver+"_windows_amd64.exe"), filepath.Join(w.prog, "restic.exe")); err != nil {
			die("installing restic: %v", err)
		}
		okf("restic %s", ver)
	}
	if !vcRedistInstalled() {
		info("installing the Visual C++ runtime FXServer needs...")
		f := filepath.Join(os.TempDir(), "vc_redist.x64.exe")
		if err := download("https://aka.ms/vs/17/release/vc_redist.x64.exe", f, ""); err != nil {
			die("downloading the Visual C++ runtime: %v", err)
		}
		defer os.Remove(f)
		if out, err := powershell("$s = Get-AuthenticodeSignature " + psq(f) + "; if ($s.Status -ne 'Valid' -or $s.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') { throw 'bad signature: ' + $s.Status }"); err != nil {
			die("the Visual C++ runtime download isn't signed by Microsoft: %s", out)
		}
		if out, err := run(f, "/install", "/quiet", "/norestart"); err != nil && !strings.Contains(err.Error(), "3010") {
			die("installing the Visual C++ runtime: %v %s", err, out)
		}
	}
	okf("Git, OpenSSH%s and the Visual C++ runtime", map[bool]string{true: ", restic", false: ""}[w.c.is("BACKUP_ENABLED")])
}

func vcRedistInstalled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\X64`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Installed")
	return err == nil && v == 1
}

// --- database ---

func (w *winSetup) mariadbDir() string {
	m, _ := filepath.Glob(filepath.Join(env("ProgramFiles", `C:\Program Files`), "fxship-mariadb", "mariadb-*"))
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

func (w *winSetup) rootSQL(sql string, db ...string) (string, error) {
	cnf, err := mysqlDefaults("127.0.0.1", "3306", "root", w.c.get("MARIADB_ROOT_PASSWORD"))
	if err != nil {
		return "", err
	}
	defer os.Remove(cnf)
	cmd := exec.Command("mariadb", append([]string{"--defaults-extra-file=" + cnf, "-N", "-B"}, db...)...)
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (w *winSetup) database() {
	c := w.c
	if !c.is("DB_INSTALL") {
		return
	}
	step("Database")
	dir := w.mariadbDir()
	if dir == "" {
		b, err := httpGet("https://downloads.mariadb.org/rest-api/mariadb/12.3/latest/")
		if err != nil {
			die("finding MariaDB: %v", err)
		}
		var rel struct {
			Releases map[string]struct {
				Files []struct {
					Name     string            `json:"file_name"`
					URL      string            `json:"file_download_url"`
					Checksum map[string]string `json:"checksum"`
				} `json:"files"`
			} `json:"releases"`
		}
		json.Unmarshal(b, &rel)
		var url, sum, name string
		for _, r := range rel.Releases {
			for _, f := range r.Files {
				if strings.HasSuffix(f.Name, "-winx64.zip") {
					url, sum, name = strings.Replace(f.URL, "http://", "https://", 1), f.Checksum["sha256sum"], f.Name
				}
			}
		}
		if url == "" || sum == "" {
			die("MariaDB's download API has no Windows zip with a checksum")
		}
		info("downloading %s...", name)
		zf := filepath.Join(os.TempDir(), name)
		if err := download(url, zf, sum); err != nil {
			die("downloading MariaDB: %v", err)
		}
		defer os.Remove(zf)
		base := filepath.Join(env("ProgramFiles", `C:\Program Files`), "fxship-mariadb")
		if err := unzip(zf, base); err != nil {
			die("unpacking MariaDB: %v", err)
		}
		dir = w.mariadbDir()
	}
	prependPath(filepath.Join(dir, "bin"))
	datadir := filepath.Join(w.root, "mariadb")
	if !serviceExists("fxship-mariadb") {
		if fileExists(datadir) {
			die("%s exists but the fxship-mariadb service doesn't: move the folder away or recreate the service", datadir)
		}
		must("creating the database", filepath.Join(dir, "bin", "mariadb-install-db.exe"),
			"--datadir="+datadir, "--service=fxship-mariadb", "--port=3306", "--password="+c.get("MARIADB_ROOT_PASSWORD"))
		// Local connections only, and the same defaults as the Linux container.
		ini := filepath.Join(datadir, "my.ini")
		b, _ := os.ReadFile(ini)
		s := strings.Replace(string(b), "[mysqld]", "[mysqld]\r\nbind-address=127.0.0.1\r\ncharacter-set-server=utf8mb4\r\ncollation-server=utf8mb4_unicode_ci\r\nmax_allowed_packet=1G", 1)
		os.WriteFile(ini, []byte(s), 0o600)
		setServiceRecovery("fxship-mariadb")
	}
	startService("fxship-mariadb")
	var err error
	for i := 0; i < 40; i++ {
		if _, err = w.rootSQL("select 1"); err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		out, _ := w.rootSQL("select 1")
		if strings.Contains(out, "Access denied") {
			die("MariaDB is up but refuses MARIADB_ROOT_PASSWORD from %s. Its data was created with another password: put that one back.", paths().EnvFile)
		}
		die("MariaDB isn't answering on 127.0.0.1:3306: %s", out)
	}
	ver, _ := w.rootSQL("select version()")
	okf("MariaDB %s", ver)

	for _, n := range w.servers_() {
		up := strings.ToUpper(n)
		name, user, pass := c.get("DB_"+up+"_NAME"), c.get("DB_"+up+"_USER"), c.get("DB_"+up+"_PASSWORD")
		sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"+
			"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';\nALTER USER '%s'@'localhost' IDENTIFIED BY '%s';\n"+
			"CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY '%s';\nALTER USER '%s'@'127.0.0.1' IDENTIFIED BY '%s';\n"+
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost';\nGRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'127.0.0.1';\nFLUSH PRIVILEGES;\n",
			name, user, pass, user, pass, user, pass, user, pass, name, user, name, user)
		if out, err := w.rootSQL(sql); err != nil {
			die("creating database %s: %v %s", name, err, out)
		}
		okf("database %s, user %s", name, user)
	}

	tables := func(db string) string {
		n, _ := w.rootSQL("select count(*) from information_schema.tables where table_schema='" + db + "'")
		return n
	}
	prod := c.get("DB_PROD_NAME")
	if imp := c.get("DB_IMPORT"); imp != "" && tables(prod) == "0" {
		info("importing %s into %s...", imp, prod)
		if err := w.importSQL(imp, prod); err != nil {
			die("importing %s: %v", imp, err)
		}
		okf("imported %s tables", tables(prod))
	}
	if c.is("DEV_ENABLED") && tables(c.get("DB_DEV_NAME")) == "0" && tables(prod) != "0" {
		info("copying prod's database into dev...")
		if err := w.copyDB(prod, c.get("DB_DEV_NAME")); err != nil {
			die("copying prod's database into dev: %v", err)
		}
		okf("dev starts with a copy of prod (%s tables)", tables(c.get("DB_DEV_NAME")))
	}
}

func (w *winSetup) importSQL(file, db string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(file, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		r = gz
	}
	cnf, err := mysqlDefaults("127.0.0.1", "3306", "root", w.c.get("MARIADB_ROOT_PASSWORD"))
	if err != nil {
		return err
	}
	defer os.Remove(cnf)
	cmd := exec.Command("mariadb", "--defaults-extra-file="+cnf, "--max-allowed-packet=1G", db)
	cmd.Stdin = r
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, clip(string(out), 600))
	}
	return nil
}

func (w *winSetup) copyDB(from, to string) error {
	cnf, err := mysqlDefaults("127.0.0.1", "3306", "root", w.c.get("MARIADB_ROOT_PASSWORD"))
	if err != nil {
		return err
	}
	defer os.Remove(cnf)
	dump := exec.Command("mariadb-dump", "--defaults-extra-file="+cnf, "--single-transaction", "--quick", "--hex-blob",
		"--triggers", "--skip-lock-tables", from)
	load := exec.Command("mariadb", "--defaults-extra-file="+cnf, "--max-allowed-packet=1G", to)
	var de, le bytes.Buffer
	dump.Stderr, load.Stderr = &de, &le
	out, _ := dump.StdoutPipe()
	in, _ := load.StdinPipe()
	if err := load.Start(); err != nil {
		return err
	}
	if err := dump.Start(); err != nil {
		in.Close()
		load.Wait()
		return err
	}
	cerr := stripDefiners(in, out)
	in.Close()
	if err := dump.Wait(); err != nil {
		return fmt.Errorf("dump: %v %s", err, clip(de.String(), 400))
	}
	if err := load.Wait(); err != nil {
		return fmt.Errorf("load: %v %s", err, clip(le.String(), 400))
	}
	return cerr
}

// --- FXServer ---

func (w *winSetup) fxserver() {
	step("FXServer")
	url, build, err := fxArtifact("windows", w.c.get("FX_BUILD"))
	if err != nil {
		die("%v", err)
	}
	arts := filepath.Join(w.root, "artifacts")
	dest := filepath.Join(arts, build)
	if !fileExists(filepath.Join(dest, "FXServer.exe")) {
		info("downloading build %s...", build)
		zf := filepath.Join(os.TempDir(), "fxserver-"+build+".zip")
		if err := download(url, zf, ""); err != nil {
			die("downloading FXServer: %v", err)
		}
		defer os.Remove(zf)
		os.RemoveAll(dest + ".tmp")
		if err := unzip(zf, dest+".tmp"); err != nil {
			die("unpacking FXServer: %v", err)
		}
		os.RemoveAll(dest)
		if err := os.Rename(dest+".tmp", dest); err != nil {
			die("%v", err)
		}
	}
	cur := filepath.Join(arts, "current")
	was, _ := os.Readlink(cur)
	if !strings.EqualFold(filepath.Clean(was), filepath.Clean(dest)) {
		os.Remove(cur) // removes the junction only, never the build it points at
		must("linking the current build", "cmd", "/c", "mklink", "/J", cur, dest)
	}
	if was != "" && !strings.EqualFold(filepath.Clean(was), filepath.Clean(dest)) {
		okf("build %s (servers switch to it at their next restart)", build)
	} else {
		okf("build %s", build)
	}
}

// --- game servers ---

func (w *winSetup) serverData(n string) string { return filepath.Join(w.root, n, "server-data") }

func writeIfChanged(p string, b []byte) error {
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return nil
	}
	return os.WriteFile(p, b, 0o644)
}

func (w *winSetup) importCfgs(data string) {
	c := w.c
	if c.get("REPO_LAYOUT") == "server-data" || fileExists(filepath.Join(data, "server.cfg")) {
		return
	}
	src := c.get("CFG_SOURCE")
	switch src {
	case "starter":
		src = filepath.Join(w.prog, "examples", "resources-layout")
	case "repo":
		tmp, _ := os.MkdirTemp("", "fxship-cfg-")
		defer os.RemoveAll(tmp)
		if out, err := w.git("clone", "-q", "--depth", "1", "--branch", c.get("REPO_BRANCH"), c.get("REPO_URL"), tmp); err != nil {
			die("fetching the repo's cfg files: %v %s", err, out)
		}
		src = tmp
	}
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".cfg") || n == "myLogo.png") || n == "secrets.cfg" || n == "fxship.cfg" || n == "instance.cfg" {
			continue
		}
		if dst := filepath.Join(data, n); !fileExists(dst) {
			if err := copyFile(filepath.Join(src, n), dst); err != nil {
				die("copying %s: %v", n, err)
			}
		}
	}
	if !fileExists(filepath.Join(data, "server.cfg")) {
		die("no server.cfg came from %s", c.get("CFG_SOURCE"))
	}
}

// tebexLine is prod's Tebex secret from the shared server.cfg; it moves into prod's secrets.cfg.
func tebexLine(cfg string) string {
	for _, l := range strings.Split(cfg, "\n") {
		if regexp.MustCompile(`(?i)^\s*(set\s+)?sv_tebexSecret\b`).MatchString(l) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func (w *winSetup) servers() {
	c := w.c
	step("Game servers")
	for _, n := range w.servers_() {
		base, data := filepath.Join(w.root, n), w.serverData(n)
		for _, d := range []string{base, filepath.Join(base, "txData", "default")} {
			os.MkdirAll(d, 0o755)
		}
		if c.get("REPO_LAYOUT") != "server-data" {
			os.MkdirAll(data, 0o755)
		}
		w.importCfgs(data)
		for _, f := range []struct{ name, block string }{{"instance.cfg", instanceCfg(c, n)}, {"secrets.cfg", secretsCfg(c, n)}} {
			p := filepath.Join(data, f.name)
			old, _ := os.ReadFile(p)
			out := managedBlock(string(old), f.block)
			if n == "prod" && f.name == "secrets.cfg" && !strings.Contains(strings.ToLower(out), "tebexsecret") {
				if sc, err := os.ReadFile(filepath.Join(data, "server.cfg")); err == nil {
					if t := tebexLine(string(sc)); t != "" {
						out += t + "\n"
					}
				}
			}
			if err := writeIfChanged(p, []byte(out)); err != nil {
				die("writing %s: %v", p, err)
			}
		}
		writeIfChanged(filepath.Join(data, "fxship.cfg"), []byte(fxshipCfg))
		onesync := ""
		if fileExists(filepath.Join(data, "server.cfg")) {
			if _, err := SyncCfg(data); err != nil {
				die("%v", err)
			}
			src, _ := os.ReadFile(filepath.Join(data, "server.cfg"))
			onesync = CfgOneSync(src)
		}

		port := map[string]string{"prod": "40120", "dev": "40130"}[n]
		hash, err := txPasswordHash(c.get(strings.ToUpper(n) + "_TX_PASSWORD"))
		if err != nil {
			die("%v", err)
		}
		envf := fmt.Sprintf("TXHOST_DATA_PATH=%s\nTXHOST_TXA_PORT=%s\nTXHOST_PROVIDER_NAME=%s\nTXHOST_IGNORE_DEPRECATED_CONFIGS=true\nTXHOST_DEFAULT_ACCOUNT=%s::%s\n",
			filepath.Join(base, "txData"), port, c.get("BRAND_NAME"), c.get("TX_USER"), hash)
		if w.tsName != "" || c.get("TS_NAME") != "" {
			envf += fmt.Sprintf("TXHOST_TXA_URL=http://%s:%s\n", firstNonEmpty(w.tsName, c.get("TS_NAME")), port)
		}
		if n == "dev" {
			envf += fmt.Sprintf("FX_CPU_CORES=%s\nFX_MEMORY_MAX_MB=%s\n", firstNonEmpty(c.get("DEV_CPU_CORES"), "2"), firstNonEmpty(c.get("DEV_MEMORY_MAX_MB"), "4096"))
		}
		// The bcrypt hash changes every run; keep the old file if only that differs, so a
		// re-run doesn't look like a change.
		ep := filepath.Join(w.data, n+".env")
		if old, err := os.ReadFile(ep); err != nil || !sameExceptHash(string(old), envf) {
			if err := os.WriteFile(ep, []byte(envf), 0o600); err != nil {
				die("%v", err)
			}
		}
		sched := []string{}
		if n == "prod" {
			sched = restartSchedule(c.get("RESTART_TIMES"))
		}
		name := c.get("BRAND_NAME")
		if n == "dev" {
			name += " [DEV]"
		}
		tp := filepath.Join(base, "txData", "default", "config.json")
		old, _ := os.ReadFile(tp)
		js, err := txAdminConfig(old, name, filepath.ToSlash(data)+"/", sched, onesync)
		if err != nil {
			die("%s: %v", tp, err)
		}
		writeIfChanged(tp, js)

		ensureService("fxship-"+n, "FiveM "+n+" (fxship)", "FXServer and txAdmin for the "+n+" server, managed by fxship.",
			[]string{"fxserver", n}, `NT SERVICE\fxship-`+n)
		okf("%s: %s", n, data)
	}
	if !c.is("DEV_ENABLED") && serviceExists("fxship-dev") {
		stopService("fxship-dev")
		setStartType("fxship-dev", mgr.StartDisabled)
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func sameExceptHash(a, b string) bool {
	re := regexp.MustCompile(`(TXHOST_DEFAULT_ACCOUNT=[^:]*::).*`)
	return re.ReplaceAllString(strings.ReplaceAll(a, "\r\n", "\n"), "$1") == re.ReplaceAllString(b, "$1")
}

// --- services ---

func serviceExists(name string) bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

// ensureService creates or updates a service running fxship.exe with args as account ("" for
// LocalSystem). Virtual accounts (NT SERVICE\name) need no password.
func ensureService(name, display, desc string, args []string, account string) {
	m, err := mgr.Connect()
	if err != nil {
		die("service manager: %v", err)
	}
	defer m.Disconnect()
	exe := filepath.Join(env("ProgramFiles", `C:\Program Files`), "fxship", "fxship.exe")
	cfg := mgr.Config{DisplayName: display, Description: desc, StartType: mgr.StartAutomatic, DelayedAutoStart: true, ServiceStartName: account}
	s, err := m.OpenService(name)
	if err != nil {
		s, err = m.CreateService(name, exe, cfg, args...)
		if err != nil {
			die("creating service %s: %v", name, err)
		}
	} else {
		cur, err := s.Config()
		if err != nil {
			die("%v", err)
		}
		cur.DisplayName, cur.Description, cur.DelayedAutoStart = display, desc, true
		if cur.StartType == mgr.StartDisabled {
			cur.StartType = mgr.StartAutomatic
		}
		cur.BinaryPathName = windows.EscapeArg(exe)
		for _, a := range args {
			cur.BinaryPathName += " " + windows.EscapeArg(a)
		}
		if account != "" {
			cur.ServiceStartName = account
		} else {
			cur.ServiceStartName = "LocalSystem"
		}
		if err := s.UpdateConfig(cur); err != nil {
			die("updating service %s: %v", name, err)
		}
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second}, {Type: mgr.ServiceRestart, Delay: 30 * time.Second}}, 24*60*60)
}

func setServiceRecovery(name string) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	if s, err := m.OpenService(name); err == nil {
		s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 30 * time.Second}}, 24*60*60)
		s.Close()
	}
}

func setStartType(name string, t uint32) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	if s, err := m.OpenService(name); err == nil {
		if cfg, err := s.Config(); err == nil {
			cfg.StartType = t
			s.UpdateConfig(cfg)
		}
		s.Close()
	}
}

func serviceState(name string) svc.State {
	m, err := mgr.Connect()
	if err != nil {
		return 0
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return 0
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0
	}
	return st.State
}

func startService(name string) {
	if serviceState(name) == svc.Running {
		return
	}
	m, err := mgr.Connect()
	if err != nil {
		die("service manager: %v", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		die("service %s: %v", name, err)
	}
	defer s.Close()
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		die("starting %s: %v", name, err)
	}
	for i := 0; i < 60 && serviceState(name) == svc.StartPending; i++ {
		time.Sleep(time.Second)
	}
}

func stopService(name string) {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return
	}
	defer s.Close()
	s.Control(svc.Stop)
	for i := 0; i < 90 && serviceState(name) != svc.Stopped; i++ {
		time.Sleep(time.Second)
	}
}

// --- agent ---

func (w *winSetup) agent() {
	c := w.c
	step("fxship agent")
	os.MkdirAll(filepath.Join(w.root, "deploy"), 0o755)
	if c.get("REPO_LAYOUT") == "server-data" {
		c.set("PROD_WORKTREE", w.serverData("prod"))
		c.set("DEV_WORKTREE", w.serverData("dev"))
	} else {
		c.set("PROD_WORKTREE", filepath.Join(w.serverData("prod"), "resources"))
		c.set("DEV_WORKTREE", filepath.Join(w.serverData("dev"), "resources"))
	}
	c.set("PROD_INFO_URL", infoURL(c.get("TOPOLOGY"), c.get("PROD_IP"), c.get("PROD_GAME_PORT")))
	c.set("DEV_INFO_URL", infoURL(c.get("TOPOLOGY"), c.get("DEV_IP"), c.get("DEV_GAME_PORT")))
	c.set("FIVEM_ROOT", w.root)
	c.set("FXSHIP_PATH", strings.Join(w.toolDirs(), ";"))
	for _, k := range c.keys {
		os.Setenv(k, c.get(k))
	}

	ensureService("fxship", "fxship", "Deploys the game servers from GitHub after Discord approval.", []string{"daemon"}, "")
	stopService("fxship")
	startService("fxship")
	info("fetching the repo onto the servers (a large repo takes a while the first time)...")
	for i := 0; ; i++ {
		var st struct {
			ProdSha string `json:"prodSha"`
		}
		if apiGetJSON("/status", &st) == nil && st.ProdSha != "" {
			okf("prod is on %s", short(st.ProdSha))
			break
		}
		if serviceState("fxship") != svc.Running && i > 5 {
			die("the agent stopped: see %s", filepath.Join(w.data, "logs", "fxship.log"))
		}
		if i > 0 && i%20 == 0 {
			info("still fetching...")
		}
		time.Sleep(3 * time.Second)
	}
}

// infoURL: where fxship asks a game server if it's up. Two-ip servers listen on their own IP only;
// the others listen everywhere, and loopback works even if the public IP is NAT'd. A server still
// running on two-ip endpoints until its next restart answers on its IP only.
func infoURL(topo, ip, port string) string {
	lo, own := "http://127.0.0.1:"+port+"/info.json", "http://"+ip+":"+port+"/info.json"
	if topo == "two-ip" || (!gameUp(lo) && gameUp(own)) {
		return own
	}
	return lo
}

func gameUp(url string) bool {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func apiGetJSON(path string, v any) error {
	req, _ := http.NewRequest("GET", "http://"+env("API_ADDR", "127.0.0.1:7070")+path, nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("API_TOKEN"))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// permissions: each game server gets its own folder and nothing else. Run after the services exist,
// since a virtual account's name only resolves once its service does.
func (w *winSetup) permissions() {
	for _, n := range w.servers_() {
		acct := `NT SERVICE\fxship-` + n
		icacls(filepath.Join(w.root, n), "/grant:r", acct+":(OI)(CI)M")
		icacls(w.root, "/grant:r", acct+":(RX)") // this folder only, to reach its own
		icacls(filepath.Join(w.root, "artifacts"), "/grant:r", acct+":(OI)(CI)RX")
		icacls(filepath.Join(w.data, n+".env"), "/inheritance:r", "/grant:r", sidSystem+":F", "/grant:r", sidAdmins+":F", "/grant:r", acct+":R")
		icacls(w.data, "/grant:r", acct+":(RX)")
		icacls(filepath.Join(w.data, "logs"), "/grant:r", acct+":(OI)(CI)M")
		// secrets.cfg: its own server reads it; staff editing dev never can.
		sec := filepath.Join(w.serverData(n), "secrets.cfg")
		icacls(sec, "/inheritance:r", "/grant:r", sidSystem+":F", "/grant:r", sidAdmins+":F", "/grant:r", acct+":R")
	}
	if w.c.is("DEV_ENABLED") {
		w.staffShare()
	}
	if w.c.is("DB_INSTALL") {
		// MariaDB's own account keeps its data folder; nobody else needs it.
		if acct := serviceAccount("fxship-mariadb"); acct != "" && !strings.EqualFold(acct, "LocalSystem") {
			icacls(filepath.Join(w.root, "mariadb"), "/grant:r", acct+":(OI)(CI)F")
			icacls(w.root, "/grant:r", acct+":(RX)")
		}
	}
	okf("each game server can only reach its own folder")
}

// staffShare: the fxship-staff group may change dev's files through the fxship-dev share, except
// the files that decide where dev listens and what it loads first (on Linux these belong to fivem).
func (w *winSetup) staffShare() {
	if !staffEnabled() {
		must("creating the "+staffGrp+" group", "net", "localgroup", staffGrp, "/add", "/comment:fxship staff: edit the dev server")
	}
	data := w.serverData("dev")
	icacls(data, "/grant:r", staffGrp+":(OI)(CI)M")
	for _, f := range []string{"instance.cfg", "fxship.cfg"} {
		icacls(filepath.Join(data, f), "/inheritance:r", "/grant:r", sidSystem+":F", "/grant:r", sidAdmins+":F",
			"/grant:r", `NT SERVICE\fxship-dev:R`, "/grant:r", staffGrp+":R")
	}
	script := "if (-not (Get-SmbShare -Name 'fxship-dev' -ErrorAction SilentlyContinue)) { New-SmbShare -Name 'fxship-dev' -Path " + psq(data) +
		" -ChangeAccess " + psq(staffGrp) + " -FullAccess '*S-1-5-32-544' -Description 'fxship dev server files' | Out-Null }"
	if out, err := powershell(script); err != nil {
		// Some languages name Administrators differently; the SID form isn't accepted everywhere.
		script = strings.Replace(script, "'*S-1-5-32-544'", "(New-Object System.Security.Principal.SecurityIdentifier('S-1-5-32-544')).Translate([System.Security.Principal.NTAccount]).Value", 1)
		if out2, err2 := powershell(script); err2 != nil {
			die("sharing dev's files: %v %s %s", err, out, out2)
		}
	}
}

func serviceAccount(name string) string {
	m, err := mgr.Connect()
	if err != nil {
		return ""
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return ""
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return ""
	}
	return cfg.ServiceStartName
}

func (w *winSetup) startServers() {
	c := w.c
	step("Starting servers")
	for _, n := range w.servers_() {
		svcName := "fxship-" + n
		url := c.get(strings.ToUpper(n) + "_INFO_URL")
		if serviceState(svcName) == svc.Running {
			okf("%s is already running (changes apply at its next restart)", n)
			continue
		}
		startService(svcName)
		info("waiting for %s to come up on port %s...", n, c.get(strings.ToUpper(n)+"_GAME_PORT"))
		up := false
		for i := 0; i < 100 && !up; i++ {
			if up = gameUp(url); !up {
				time.Sleep(3 * time.Second)
			}
		}
		if up {
			okf("%s is up", n)
		} else {
			warnf("%s didn't answer within 5 minutes: check txAdmin (port %s) or %s", n,
				map[string]string{"prod": "40120", "dev": "40130"}[n], filepath.Join(w.data, "logs", svcName+"-console.log"))
		}
	}
}

// --- backups, firewall, finish ---

func (w *winSetup) backups() {
	c := w.c
	if !c.is("BACKUP_ENABLED") {
		run("schtasks", "/Delete", "/TN", "fxship-backup", "/F")
		return
	}
	step("Backups")
	tr := `"` + filepath.Join(w.prog, "fxship.exe") + `" backup run`
	must("scheduling backups", "schtasks", "/Create", "/TN", "fxship-backup", "/TR", tr, "/SC", "DAILY",
		"/ST", c.get("BACKUP_TIME"), "/RU", "SYSTEM", "/RL", "HIGHEST", "/F")
	info("taking a backup now...")
	cmd := exec.Command(filepath.Join(w.prog, "fxship.exe"), "backup", "run")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		warnf("the first backup failed: run fxship backup run to see why")
		return
	}
	okf("backup done, then daily at %s to %s", c.get("BACKUP_TIME"), c.get("BACKUP_REPO"))
}

func (w *winSetup) access() {
	c := w.c
	step("Firewall and admin access")
	if c.is("TAILSCALE") {
		ts := filepath.Join(env("ProgramFiles", `C:\Program Files`), "Tailscale", "tailscale.exe")
		if !fileExists(ts) {
			info("installing Tailscale...")
			f := filepath.Join(os.TempDir(), "tailscale.msi")
			if err := download("https://pkgs.tailscale.com/stable/tailscale-setup-latest-amd64.msi", f, ""); err != nil {
				die("downloading Tailscale: %v", err)
			}
			defer os.Remove(f)
			if out, err := powershell("$s = Get-AuthenticodeSignature " + psq(f) + "; if ($s.Status -ne 'Valid' -or $s.SignerCertificate.Subject -notmatch 'Tailscale') { throw 'bad signature: ' + $s.Status }"); err != nil {
				die("the Tailscale download isn't signed by Tailscale: %s", out)
			}
			must("installing Tailscale", "msiexec", "/i", f, "/qn", "/norestart", "TS_UNATTENDEDMODE=always")
		}
		var st struct {
			BackendState string
			Self         struct{ DNSName string }
		}
		stat := func() { b, _ := exec.Command(ts, "status", "--json").Output(); json.Unmarshal(b, &st) }
		stat()
		if st.BackendState != "Running" {
			hostArg := "--hostname=" + strings.ReplaceAll(slug(c.get("BRAND_NAME")), "_", "-")
			switch {
			case c.get("TAILSCALE_AUTHKEY") != "":
				must("tailscale up", ts, "up", "--unattended", "--authkey="+c.get("TAILSCALE_AUTHKEY"), hostArg)
			case w.p.yes:
				warnf("Tailscale isn't logged in: run 'tailscale up --unattended' then re-run the installer")
			default:
				info("Log this machine into your tailnet (open the link it prints):")
				cmd := exec.Command(ts, "up", "--unattended", hostArg)
				cmd.Stdout, cmd.Stderr = stdout, stdout
				if err := cmd.Run(); err != nil {
					die("tailscale up failed: %v", err)
				}
			}
			stat()
		}
		if w.tsName = strings.TrimSuffix(st.Self.DNSName, "."); w.tsName != "" {
			c.set("TS_NAME", w.tsName)
			okf("Tailscale: %s", w.tsName)
		}
	}

	scope := "100.64.0.0/10"
	if !c.is("TAILSCALE") {
		scope = strings.Join(strings.Fields(strings.ReplaceAll(c.get("ADMIN_ALLOW_IPS"), ",", " ")), ",")
	}
	var ps strings.Builder
	ps.WriteString("Get-NetFirewallRule -Group 'fxship' -ErrorAction SilentlyContinue | Remove-NetFirewallRule; ")
	rule := func(name, proto, ports, remote string) {
		fmt.Fprintf(&ps, "New-NetFirewallRule -Group 'fxship' -DisplayName %s -Direction Inbound -Action Allow -Protocol %s -LocalPort %s", psq("fxship: "+name), proto, ports)
		if remote != "" {
			fmt.Fprintf(&ps, " -RemoteAddress %s", strings.Join(quoteAll(strings.Split(remote, ",")), ","))
		}
		ps.WriteString(" | Out-Null; ")
	}
	gamePorts := c.get("PROD_GAME_PORT")
	if c.is("DEV_ENABLED") && c.get("DEV_GAME_PORT") != c.get("PROD_GAME_PORT") {
		gamePorts += "," + c.get("DEV_GAME_PORT")
	}
	rule("game servers (TCP)", "TCP", gamePorts, "")
	rule("game servers (UDP)", "UDP", gamePorts, "")
	if scope != "" {
		rule("txAdmin", "TCP", "40120,40130", scope)
		if staffEnabled() {
			rule("dev file share", "TCP", "445", scope)
		}
	}
	ps.WriteString("if ((Get-NetFirewallProfile | Where-Object { -not $_.Enabled }).Count -gt 0) { 'OFF' }")
	out, err := powershell(ps.String())
	if err != nil {
		die("firewall rules: %v %s", err, out)
	}
	if strings.Contains(out, "OFF") {
		warnf("Windows Firewall is off for some networks: turn it on, or these rules do nothing")
	}
	where := "on Tailscale only"
	if !c.is("TAILSCALE") {
		where = "from " + firstNonEmpty(c.get("ADMIN_ALLOW_IPS"), "nowhere (this machine only)")
	}
	okf("firewall: game ports public; txAdmin %s", where)
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = psq(strings.TrimSpace(x))
	}
	return out
}

func staffEnabled() bool {
	_, err := run("net", "localgroup", staffGrp)
	return err == nil
}

func (w *winSetup) finish() {
	c := w.c
	step("Done")
	host := firstNonEmpty(w.tsName, c.get("TS_NAME"), c.get("PUBLIC_IP"))
	line := func(k, v string) { fmt.Fprintf(stdout, "  %s%-18s%s  %s\n", colB, k, colN, v) }
	pp := c.get("PROD_IP") + ":" + c.get("PROD_GAME_PORT")
	if c.get("PROD_GAME_PORT") == "30120" {
		pp += " (or just " + c.get("PROD_IP") + ")"
	}
	line("Players connect to", pp)
	if c.is("DEV_ENABLED") {
		line("Dev server", c.get("DEV_IP")+":"+c.get("DEV_GAME_PORT"))
	}
	line("txAdmin prod", "http://"+host+":40120  (user "+c.get("TX_USER")+")")
	if c.is("DEV_ENABLED") {
		line("txAdmin dev", "http://"+host+":40130")
	}
	line("Passwords", w.creds+" (Administrators only)")
	line("Settings", paths().EnvFile+", then re-run the installer")
	info("Push to %s and a release card appears in Discord within a minute.", c.get("REPO_BRANCH"))
	if c.is("DEV_ENABLED") {
		info("Give staff access to dev's files: fxship staff add <name>")
	}
	info("Commands: fxship status | fxship doctor | fxship backup list")
}
