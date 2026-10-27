package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type serverTarget struct {
	Name       string
	Worktree   string // git worktree checked out at the deployed sha
	ServerData string // FXServer's working directory (server.cfg lives here)
	TxURL      string // txAdmin base URL
	TxUser     string
	TxPassword string
	InfoURL    string // FXServer /info.json, only answers while the game server is up
	LogPath    string // txAdmin's fxserver.log
}

type Config struct {
	RepoURL      string
	Branch       string
	GitDir       string
	WebURL       string // https://github.com/owner/repo, for commit and compare links
	PollInterval time.Duration
	// Layout is "resources" (the repo is the resources folder) or "server-data" (the repo is the
	// whole server-data folder: server.cfg at the root, resources/ below it).
	Layout string

	DevEnabled bool // false on prod-only boxes: no dev test, no dev edits
	Dev        serverTarget
	Prod       serverTarget

	BrandName    string // the server's own name, on cards and alerts
	BrandIconURL string // card thumbnail; empty uses the Discord server's icon
	PoweredBy    bool   // "Powered by Rivo" line at the foot of each card

	DiscordToken   string
	DiscordChannel string
	ApproverRoles  []string
	ShipperRoles   []string // may ship or discard dev edits (approvers always can)
	SlashCommand   string   // name of the slash command, "" to register none

	StatePath string
	APIAddr   string
	APIToken  string

	Location     *time.Location
	RestartTimes []string      // prod schedule, must match txAdmin's restarter
	ApplyLead    time.Duration // queued releases are checked out this long before a scheduled restart
	DevSettle    time.Duration // how long to watch the dev console after ensuring resources

	// Resources that can't be restarted live: frameworks everything else hangs off.
	CoreResources map[string]bool
	// Never treated as core or streaming, whatever detection says. Removing one still needs a restart.
	HotResources map[string]bool
	// Optional path prefixes whose resources always need a restart, on top of detection.
	ColdPrefixes []string

	// Dev edits: how often dev is checked, how long edits must sit still before a reload, how
	// long to read the console after one, and files that resources rewrite on their own.
	DevWatch      time.Duration
	DevDebounce   time.Duration
	DevEditSettle time.Duration
	DevIgnore     []*regexp.Regexp

	// Commits made by Ship are authored as the Discord user. ShipAuthors maps a Discord username to
	// a real git identity ("Name <email>"); everyone else gets <username>@ShipEmailDomain.
	ShipEmailDomain string
	ShipAuthors     map[string]string

	DB DBConfig
}

// DBConfig is what dev database refreshes need. Empty when the box has no managed database.
type DBConfig struct {
	Host         string
	Port         string
	ProdName     string
	ProdUser     string
	ProdPassword string
	DevName      string
	DevUser      string
	DevPassword  string
	ScrubSQL     string // run against the dev database after each refresh, if the file exists
}

func (d DBConfig) Configured() bool {
	return d.ProdName != "" && d.ProdUser != "" && d.DevName != "" && d.DevUser != ""
}

// DefaultCoreResources covers the common frameworks. Only names that exist in the repo matter, so
// a long list costs nothing.
const DefaultCoreResources = "oxmysql,mysql-async,ghmattimysql,ox_lib,ox_core,ox_inventory,ox_target,ox_doorlock," +
	"qbx_core,qb-core,qb-inventory,qb-target,qb-multicharacter,es_extended,esx_multicharacter,esx_identity," +
	"ND_Core,vrp,pma-voice,saltychat,illenium-appearance,fivem-appearance,Renewed-Lib," +
	"chat,sessionmanager,spawnmanager,mapmanager,hardcap,baseevents,yarn,webpack,screenshot-basic"

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range list(s) {
		m[p] = true
	}
	return m
}

func boolEnv(k string, def bool) (bool, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: want true or false, got %q", k, v)
	}
	return b, nil
}

// loadEnvFile sets every KEY=value in p that the environment doesn't already set. Quotes around a
// value are removed, so the file can also be sourced by the installer's shell scripts.
func loadEnvFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
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
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return sc.Err()
}

// webURL turns a GitHub clone URL into the repository's web address.
func webURL(repo string) string {
	r := strings.TrimSuffix(strings.TrimSpace(repo), ".git")
	switch {
	case strings.HasPrefix(r, "git@github.com:"):
		return "https://github.com/" + strings.TrimPrefix(r, "git@github.com:")
	case strings.HasPrefix(r, "ssh://git@github.com/"):
		return "https://github.com/" + strings.TrimPrefix(r, "ssh://git@github.com/")
	case strings.HasPrefix(r, "https://github.com/"):
		return r
	}
	return ""
}

func target(name, root, layout string, txPort, gamePort string) serverTarget {
	up := strings.ToUpper(name)
	data := env(up+"_SERVER_DATA", filepath.Join(root, name, "server-data"))
	wt := filepath.Join(data, "resources")
	if layout == "server-data" {
		wt = data
	}
	return serverTarget{
		Name:       name,
		Worktree:   env(up+"_WORKTREE", wt),
		ServerData: data,
		TxURL:      env(up+"_TX_URL", "http://127.0.0.1:"+txPort),
		TxUser:     env(up+"_TX_USER", env("TX_USER", "admin")),
		TxPassword: os.Getenv(up + "_TX_PASSWORD"),
		InfoURL:    env(up+"_INFO_URL", "http://127.0.0.1:"+gamePort+"/info.json"),
		LogPath:    env(up+"_LOG", filepath.Join(root, name, "txData", "default", "logs", "fxserver.log")),
	}
}

func loadConfig() (*Config, error) {
	loc, err := time.LoadLocation(env("TZ", "Local"))
	if err != nil {
		return nil, fmt.Errorf("TZ: %w", err)
	}
	c := &Config{
		RepoURL:         env("REPO_URL", ""),
		Branch:          env("REPO_BRANCH", "main"),
		Layout:          env("REPO_LAYOUT", "resources"),
		BrandName:       env("BRAND_NAME", "FiveM"),
		BrandIconURL:    env("BRAND_ICON_URL", ""),
		DiscordToken:    os.Getenv("DISCORD_TOKEN"),
		DiscordChannel:  os.Getenv("DISCORD_CHANNEL_ID"),
		ApproverRoles:   list(os.Getenv("DISCORD_APPROVER_ROLE_IDS")),
		ShipperRoles:    list(os.Getenv("DISCORD_SHIPPER_ROLE_IDS")),
		SlashCommand:    env("DISCORD_SLASH_COMMAND", "deploy"),
		APIAddr:         env("API_ADDR", "127.0.0.1:7070"),
		APIToken:        os.Getenv("API_TOKEN"),
		Location:        loc,
		RestartTimes:    list(env("RESTART_TIMES", "06:00,18:00")),
		ColdPrefixes:    list(os.Getenv("COLD_PREFIXES")),
		CoreResources:   set(env("CORE_RESOURCES", DefaultCoreResources)),
		HotResources:    set(os.Getenv("HOT_RESOURCES")),
		ShipEmailDomain: env("SHIP_EMAIL_DOMAIN", "users.noreply.fxship.invalid"),
		ShipAuthors:     map[string]string{},
	}
	if strings.EqualFold(c.SlashCommand, "none") {
		c.SlashCommand = ""
	}
	root := env("FIVEM_ROOT", paths().FivemRoot)
	c.GitDir = env("GIT_DIR_PATH", filepath.Join(root, "repo.git"))
	c.StatePath = env("STATE_PATH", filepath.Join(root, "deploy", "state.json"))
	c.WebURL = strings.TrimSuffix(env("REPO_WEB_URL", webURL(c.RepoURL)), "/")
	if c.Layout != "resources" && c.Layout != "server-data" {
		return nil, fmt.Errorf("REPO_LAYOUT: want resources or server-data, got %q", c.Layout)
	}
	c.Prod = target("prod", root, c.Layout, "40120", env("PROD_GAME_PORT", "30120"))
	c.Dev = target("dev", root, c.Layout, "40130", env("DEV_GAME_PORT", "30130"))
	if c.DevEnabled, err = boolEnv("DEV_ENABLED", true); err != nil {
		return nil, err
	}
	if c.PoweredBy, err = boolEnv("POWERED_BY", true); err != nil {
		return nil, err
	}
	for k, dst := range map[string]*time.Duration{
		"POLL_INTERVAL": &c.PollInterval, "APPLY_LEAD": &c.ApplyLead, "DEV_SETTLE": &c.DevSettle,
		"DEV_WATCH": &c.DevWatch, "DEV_DEBOUNCE": &c.DevDebounce, "DEV_EDIT_SETTLE": &c.DevEditSettle,
	} {
		def := map[string]string{"POLL_INTERVAL": "60s", "APPLY_LEAD": "90s", "DEV_SETTLE": "20s",
			"DEV_WATCH": "3s", "DEV_DEBOUNCE": "3s", "DEV_EDIT_SETTLE": "5s"}[k]
		if *dst, err = time.ParseDuration(env(k, def)); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	for _, hm := range c.RestartTimes {
		var h, m int
		if _, err := fmt.Sscanf(hm, "%d:%d", &h, &m); err != nil || h > 23 || m > 59 || h < 0 || m < 0 {
			return nil, fmt.Errorf("RESTART_TIMES: %q is not HH:MM", hm)
		}
	}
	for _, p := range list(env("DEV_IGNORE", "**/output/**,**/*.log,**/cache/**")) {
		c.DevIgnore = append(c.DevIgnore, globRe(p))
	}
	for _, kv := range strings.Split(os.Getenv("SHIP_AUTHORS"), ";") {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
			c.ShipAuthors[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	c.DB = DBConfig{
		Host: env("DB_HOST", "127.0.0.1"), Port: env("DB_PORT", "3306"),
		ProdName: os.Getenv("DB_PROD_NAME"), ProdUser: os.Getenv("DB_PROD_USER"), ProdPassword: os.Getenv("DB_PROD_PASSWORD"),
		DevName: os.Getenv("DB_DEV_NAME"), DevUser: os.Getenv("DB_DEV_USER"), DevPassword: os.Getenv("DB_DEV_PASSWORD"),
		ScrubSQL: env("DB_SCRUB_SQL", paths().DevScrubSQL),
	}

	var missing []string
	for k, v := range map[string]string{"API_TOKEN": c.APIToken, "REPO_URL": c.RepoURL, "PROD_TX_PASSWORD": c.Prod.TxPassword} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if c.DevEnabled && c.Dev.TxPassword == "" {
		missing = append(missing, "DEV_TX_PASSWORD")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required settings: %s (in %s)", strings.Join(missing, ", "), envFile())
	}
	if (c.DiscordToken == "") != (c.DiscordChannel == "") {
		return nil, fmt.Errorf("set both DISCORD_TOKEN and DISCORD_CHANNEL_ID, or neither")
	}
	return c, nil
}
