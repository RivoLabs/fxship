package main

// Pieces of the installer that don't touch the system: the settings file, the questions, and the
// files written into each server folder. The Windows installer ("fxship install") is built on them;
// the Linux installer does the same in bash and writes the same files, so both read alike.

import (
	"archive/zip"
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// setupError stops the installer with a message for the owner (the bash installer's die).
type setupError struct{ msg string }

func (e setupError) Error() string { return e.msg }

func die(format string, a ...any) { panic(setupError{fmt.Sprintf(format, a...)}) }

// conf is the settings file: KEY="value" lines, the format install.sh writes and the agent reads.
type conf struct {
	path string
	vals map[string]string
	keys []string
}

func loadConf(p string) (*conf, error) {
	c := &conf{path: p, vals: map[string]string{}}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if _, seen := c.vals[k]; !seen {
			c.keys = append(c.keys, k)
		}
		c.vals[k] = v
	}
	return c, sc.Err()
}

func (c *conf) get(k string) string { return c.vals[k] }

func (c *conf) is(k string) bool { return isTrue(c.vals[k]) }

// set stores a value and rewrites the file at once, so an installer stopped halfway keeps every
// answer given so far.
func (c *conf) set(k, v string) {
	if strings.ContainsAny(v, "\"\r\n") {
		die("%s: values can't contain double quotes or line breaks", k)
	}
	if _, seen := c.vals[k]; !seen {
		c.keys = append(c.keys, k)
	}
	c.vals[k] = v
	if err := c.write(); err != nil {
		die("saving %s: %v", c.path, err)
	}
}

func (c *conf) write() error {
	var b strings.Builder
	for _, k := range c.keys {
		fmt.Fprintf(&b, "%s=\"%s\"\n", k, c.vals[k])
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// merge copies every setting from o over c (the installer's --config FILE).
func (c *conf) merge(o *conf) {
	for _, k := range o.keys {
		c.set(k, o.vals[k])
	}
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "y", "1":
		return true
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// genPass returns n random letters and digits.
func genPass(n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic(err)
		}
		b[i] = chars[x.Int64()]
	}
	return string(b)
}

var reSlugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns a server name into a database-safe name, like the bash installer's slug.
func slug(s string) string {
	s = strings.Trim(reSlugJunk.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "_")
	}
	return s
}

// Terminal output, matching the bash installer's look.
var (
	colB, colD, colG, colY, colR, colC, colN           = "\x1b[1m", "\x1b[2m", "\x1b[32m", "\x1b[33m", "\x1b[31m", "\x1b[36m", "\x1b[0m"
	stdout                                   io.Writer = os.Stdout
)

func noColor() { colB, colD, colG, colY, colR, colC, colN = "", "", "", "", "", "", "" }

func step(s string)                { fmt.Fprintf(stdout, "\n%s%s==> %s%s\n", colB, colC, s, colN) }
func info(format string, a ...any) { fmt.Fprintf(stdout, "    "+format+"\n", a...) }
func okf(format string, a ...any) {
	fmt.Fprintf(stdout, "    %s✓%s "+format+"\n", append([]any{colG, colN}, a...)...)
}
func warnf(format string, a ...any) {
	fmt.Fprintf(stdout, "    %s!%s "+format+"\n", append([]any{colY, colN}, a...)...)
}
func failf(msg string)     { fmt.Fprintf(stdout, "\n%s✗ %s%s\n", colR, msg, colN) }
func bold(s string) string { return colB + s + colN }

// prompter asks the questions. With yes set (--yes, --upgrade, --config) nothing is asked: saved
// values or the defaults are used, as in the bash installer.
type prompter struct {
	in     *bufio.Reader
	yes    bool
	secret func() (string, error) // reads a line without echoing it
}

func (p *prompter) line(q string) string {
	fmt.Fprint(stdout, q)
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		die("no answer to %q (stdin closed): run with --yes or --config to install without questions", strings.TrimSpace(q))
	}
	return strings.TrimSpace(s)
}

// ask returns the answer, the current value, or def.
func (p *prompter) ask(c *conf, key, q, def string) string {
	cur := c.get(key)
	if cur == "" {
		cur = def
	}
	if p.yes {
		return cur
	}
	hint := ""
	if cur != "" {
		hint = " " + colD + "[" + cur + "]" + colN
	}
	if a := p.line("  " + q + hint + ": "); a != "" {
		return a
	}
	return cur
}

// askSecret reads hidden input; Enter keeps the current value.
func (p *prompter) askSecret(c *conf, key, q string) string {
	cur := c.get(key)
	if p.yes {
		return cur
	}
	hint := ""
	if cur != "" {
		hint = " " + colD + "[keep current]" + colN
	}
	fmt.Fprint(stdout, "  "+q+hint+": ")
	var a string
	var err error
	if p.secret != nil {
		a, err = p.secret()
		fmt.Fprintln(stdout)
	} else {
		a, err = p.in.ReadString('\n')
	}
	if err != nil && a == "" && cur == "" {
		die("no answer to %q", q)
	}
	if a = strings.TrimSpace(a); a != "" {
		return a
	}
	return cur
}

func (p *prompter) askYes(c *conf, key, q string, def bool) bool {
	switch v := strings.ToLower(c.get(key)); v {
	case "true", "yes", "y", "1":
		def = true
	case "false", "no", "n", "0":
		def = false
	}
	if p.yes {
		return def
	}
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	switch strings.ToLower(p.line("  " + q + " " + colD + "[" + hint + "]" + colN + ": ")) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// choose shows a numbered menu of "name  description" options and returns the chosen name.
func (p *prompter) choose(c *conf, key, q, def string, opts []string) string {
	cur, tag := c.get(key), "current"
	if cur == "" {
		cur, tag = def, "default"
	}
	if p.yes {
		return cur
	}
	fmt.Fprintln(stdout, "  "+q)
	for i, o := range opts {
		mark := ""
		if strings.Fields(o)[0] == cur {
			mark = " " + colD + "(" + tag + ", Enter)" + colN
		}
		fmt.Fprintf(stdout, "    %s%d)%s %s%s\n", colB, i+1, colN, o, mark)
	}
	for {
		a := p.line(fmt.Sprintf("  Choose 1-%d: ", len(opts)))
		if a == "" {
			return cur
		}
		var n int
		if _, err := fmt.Sscanf(a, "%d", &n); err == nil && n >= 1 && n <= len(opts) {
			return strings.Fields(opts[n-1])[0]
		}
	}
}

// pick asks for numbers from a printed list ("2" or "1,3"); blank means none.
func (p *prompter) pick(q string, n int, many bool) []int {
	for {
		a := p.line("  " + q + " ")
		if a == "" {
			return nil
		}
		var out []int
		okAll := true
		for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' }) {
			var i int
			if _, err := fmt.Sscanf(f, "%d", &i); err != nil || i < 1 || i > n {
				okAll = false
				break
			}
			out = append(out, i-1)
		}
		if okAll && len(out) > 0 && (many || len(out) == 1) {
			return out
		}
	}
}

// managedBlock puts block between fxship's markers in a cfg file, keeping every other line, like the
// bash installer's managed().
func managedBlock(old, block string) string {
	var keep []string
	skip := false
	for _, l := range strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "# BEGIN fxship"):
			skip = true
		case strings.HasPrefix(l, "# END fxship"):
			skip = false
		case !skip:
			keep = append(keep, l)
		}
	}
	rest := strings.TrimLeft(strings.Join(keep, "\n"), "\n")
	out := "# BEGIN fxship (managed by the installer: re-run it to change these)\n" + strings.TrimRight(block, "\n") + "\n# END fxship\n"
	if strings.TrimSpace(rest) != "" {
		out += rest
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
	}
	return out
}

// instanceCfg is a server's instance.cfg block: endpoints, name and listing.
func instanceCfg(c *conf, name string) string {
	ip, port, host := c.get("PROD_IP"), c.get("PROD_GAME_PORT"), c.get("BRAND_NAME")
	if name == "dev" {
		ip, port, host = c.get("DEV_IP"), c.get("DEV_GAME_PORT"), c.get("BRAND_NAME")+" [DEV]"
	}
	var b strings.Builder
	if c.get("TOPOLOGY") == "two-ip" {
		fmt.Fprintf(&b, "endpoint_add_tcp \"%s:%s\"\nendpoint_add_udp \"%s:%s\"\nset sv_listingIpOverride \"%s\"\n", ip, port, ip, port, ip)
	} else {
		fmt.Fprintf(&b, "endpoint_add_tcp \"0.0.0.0:%s\"\nendpoint_add_udp \"0.0.0.0:%s\"\n", port, port)
	}
	fmt.Fprintf(&b, "sv_hostname \"%s\"\nsets fxship_instance \"%s\"\n", host, name)
	if name == "dev" {
		b.WriteString("set sv_master1 \"\"\n") // dev stays off the public server list
	}
	return b.String()
}

// secretsCfg is a server's secrets.cfg block: license key and database.
func secretsCfg(c *conf, name string) string {
	key := c.get("PROD_LICENSE_KEY")
	if name == "dev" && c.get("DEV_LICENSE_KEY") != "" {
		key = c.get("DEV_LICENSE_KEY")
	}
	s := fmt.Sprintf("sv_licenseKey \"%s\"\n", key)
	if c.is("DB_INSTALL") {
		up := strings.ToUpper(name)
		s += fmt.Sprintf("set mysql_connection_string \"mysql://%s:%s@127.0.0.1:3306/%s?charset=utf8mb4\"\n",
			c.get("DB_"+up+"_USER"), c.get("DB_"+up+"_PASSWORD"), c.get("DB_"+up+"_NAME"))
	}
	return s
}

// fxshipCfg is the file txAdmin starts each server with. FiveM treats a ; as the start of a new
// command even inside a # comment, so generated comments never contain one.
const fxshipCfg = `# Generated by fxship: txAdmin starts the server with this file. Do not edit it. Edit server.cfg,
# or instance.cfg and secrets.cfg for settings that differ between prod and dev.
exec instance.cfg
exec secrets.cfg
exec server.fxship.cfg
`

// txAdminConfig returns txAdmin's config.json for a server: on first install a new one, later the
// owner's with only fxship's settings enforced (data folder, cfg file, restart schedule), and
// onesync filled in only while txAdmin has none.
func txAdminConfig(old []byte, serverName, dataPath string, schedule []string, onesync string) ([]byte, error) {
	cfg := map[string]any{}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &cfg); err != nil {
			return nil, fmt.Errorf("txAdmin's config.json isn't valid JSON: %w", err)
		}
	} else {
		cfg["version"] = 2
		cfg["general"] = map[string]any{"serverName": serverName}
	}
	section := func(k string) map[string]any {
		m, ok := cfg[k].(map[string]any)
		if !ok {
			m = map[string]any{}
			cfg[k] = m
		}
		return m
	}
	srv := section("server")
	srv["dataPath"] = dataPath
	srv["cfgPath"] = "fxship.cfg"
	if _, ok := srv["autoStart"]; !ok {
		srv["autoStart"] = true
	}
	if _, ok := srv["onesync"]; !ok && onesync != "" {
		srv["onesync"] = onesync
	}
	if schedule == nil {
		schedule = []string{}
	}
	section("restarter")["schedule"] = schedule
	return json.MarshalIndent(cfg, "", "  ")
}

// txPasswordHash is the bcrypt hash txAdmin's TXHOST_DEFAULT_ACCOUNT expects.
func txPasswordHash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 11)
	if err != nil {
		return "", err
	}
	return "$2b$" + strings.TrimPrefix(string(h), "$2a$"), nil
}

var httpClient = &http.Client{Timeout: 30 * time.Minute}

func httpGet(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// fxArtifact finds an FXServer download: goos is "windows" or "linux", build is recommended,
// latest, optional or a build number. It returns the URL and the build number.
func fxArtifact(goos, build string) (string, string, error) {
	plat := map[string]string{"windows": "win32", "linux": "linux"}[goos]
	switch build {
	case "", "recommended", "latest", "optional":
		if build == "" {
			build = "recommended"
		}
		b, err := httpGet("https://changelogs-live.fivem.net/api/changelog/versions/" + plat + "/server")
		if err != nil {
			return "", "", err
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			return "", "", err
		}
		url, _ := v[build+"_download"].(string)
		num, _ := v[build].(string)
		if url == "" || num == "" {
			return "", "", fmt.Errorf("Cfx's changelog API has no %s build", build)
		}
		return url, num, nil
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(build) {
		return "", "", fmt.Errorf("FX_BUILD must be recommended, latest or a build number, not %q", build)
	}
	// Older builds are listed on Cfx's download page (the artifact index now redirects there).
	page, err := httpGet("https://docs.fivem.net/docs/server-download/?platform=legacy&os=" + map[string]string{"windows": "windows", "linux": "linux"}[goos])
	if err != nil {
		return "", "", err
	}
	dir := map[string]string{"windows": "build_server_windows", "linux": "build_proot_linux"}[goos]
	m := regexp.MustCompile(`https://runtime\.fivem\.net/artifacts/fivem/` + dir + `/master/` + build + `-[0-9a-f]+/`).Find(page)
	if m == nil {
		return "", "", fmt.Errorf("build %s isn't on Cfx's download page; use recommended, latest or a listed build", build)
	}
	file := map[string]string{"windows": "server.zip", "linux": "fx.tar.xz"}[goos]
	return string(m) + file, build, nil
}

// download saves url to dest. A non-empty sha256 must match, or the file is removed.
func download(url, dest, sum string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && sum != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum) {
		err = fmt.Errorf("%s: checksum mismatch (got %s, want %s)", url, hex.EncodeToString(h.Sum(nil)), sum)
	}
	if err != nil {
		os.Remove(dest)
	}
	return err
}

// unzip extracts src into dir, refusing entries that would land outside it.
func unzip(src, dir string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		p := filepath.Join(root, filepath.FromSlash(f.Name))
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return fmt.Errorf("%s: %s points outside the folder", src, f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		in, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode()|0o200)
		if err != nil {
			in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sumFromList finds name's sha256 in a checksum list ("<sum>  <name>" lines, or a markdown table
// row holding both, as Git for Windows publishes them).
func sumFromList(list, name string) string {
	re := regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	for _, l := range strings.Split(list, "\n") {
		if strings.Contains(l, name) {
			if m := re.FindString(l); m != "" {
				return strings.ToLower(m)
			}
		}
	}
	return ""
}

// githubLatest returns a GitHub repo's latest release tag, notes and asset URLs by name.
func githubLatest(repo string) (string, string, map[string]string, error) {
	b, err := httpGet("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", "", nil, err
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return "", "", nil, err
	}
	assets := map[string]string{}
	for _, a := range rel.Assets {
		assets[a.Name] = a.URL
	}
	return rel.Tag, rel.Body, assets, nil
}

// restartSchedule splits RESTART_TIMES ("06:00,18:00") for txAdmin.
func restartSchedule(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

var reRestartTimes = regexp.MustCompile(`^([0-2][0-9]:[0-5][0-9],?)+$`)

// winPSEnv is this process's environment for a Windows PowerShell child. Started from PowerShell 7,
// fxship inherits 7's PSModulePath, and Windows PowerShell 5.1 then fails to load its own modules.
func winPSEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "PSMODULEPATH=") {
			out = append(out, kv)
		}
	}
	return out
}
