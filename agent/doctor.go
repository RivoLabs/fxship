package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type checkResult struct {
	Level string `json:"level"` // ok, warn, fail, skip
	Name  string `json:"name"`
	Msg   string `json:"msg"`
	Fix   string `json:"fix,omitempty"`
}

type checker struct{ results []checkResult }

func (c *checker) add(level, name, msg, fix string) {
	c.results = append(c.results, checkResult{level, name, msg, fix})
}
func (c *checker) ok(name, msg string)        { c.add("ok", name, msg, "") }
func (c *checker) warn(name, msg, fix string) { c.add("warn", name, msg, fix) }
func (c *checker) fail(name, msg, fix string) { c.add("fail", name, msg, fix) }

// Service names differ: systemd units on Linux, Windows services on Windows.
func serverService(name string) string {
	if runtime.GOOS == "windows" {
		return "fxship-" + name
	}
	return "fivem@" + name
}

// doctor checks the whole setup from the config file outward and exits non-zero on any failure.
func doctor(args []string) int {
	asJSON := len(args) > 0 && args[0] == "--json"
	c := &checker{}
	cfg, err := loadConfig()
	if err != nil {
		c.fail("config", err.Error(), "fix "+envFile()+", or re-run the installer")
		return report(c, asJSON)
	}
	c.ok("config", fmt.Sprintf("%s, %s layout, %s", cfg.BrandName, cfg.Layout, map[bool]string{true: "prod + dev", false: "prod only"}[cfg.DevEnabled]))
	if !isAdmin() {
		c.warn("admin", "not running as root/Administrator, some checks fail for that reason alone", map[bool]string{true: "run it from an elevated PowerShell", false: "sudo fxship doctor"}[runtime.GOOS == "windows"])
	}

	// The agent itself.
	if serviceActive("fxship") {
		c.ok("agent service", "fxship is running")
	} else {
		c.fail("agent service", "fxship is not running", logsHint("fxship")+", then restart the fxship service")
	}
	if resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + cfg.APIAddr + "/healthz"); err == nil && resp.StatusCode == 200 {
		resp.Body.Close()
		c.ok("agent api", "answering on "+cfg.APIAddr)
	} else {
		c.fail("agent api", "no answer on "+cfg.APIAddr, logsHint("fxship"))
	}
	for _, bin := range []string{"git", luacPath(), "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			c.fail("tools", bin+" is not installed", "re-run the installer")
		}
	}
	if cfg.DB.Configured() {
		for _, bin := range []string{"mariadb", "mariadb-dump"} {
			if _, err := exec.LookPath(bin); err != nil {
				c.warn("tools", bin+" is not installed, dev database refresh won't work", "apt install mariadb-client")
			}
		}
	}

	// GitHub: read with the deploy key, and write, which Ship needs.
	gitEnv := []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=" + env("GIT_SSH_COMMAND", "ssh")}
	if out, err := runAs("fivem", gitEnv, "git", "ls-remote", cfg.RepoURL, "refs/heads/"+cfg.Branch); err != nil {
		c.fail("github read", "can't read "+cfg.RepoURL+": "+lastLine(out), "add the deploy key the installer printed (deploy_key.pub) on the repo: Settings > Deploy keys")
	} else if out == "" {
		c.fail("github read", "branch "+cfg.Branch+" not found in "+cfg.RepoURL, "set REPO_BRANCH to the branch you deploy from")
	} else {
		c.ok("github read", cfg.RepoURL+" "+cfg.Branch+" at "+short(strings.Fields(out)[0]))
		if cfg.DevEnabled {
			// A dry run push of the current head opens receive-pack, which GitHub refuses at once
			// for a read-only key, without changing anything.
			head := strings.Fields(out)[0]
			out, err := runAs("fivem", gitEnv, "git", "--git-dir", cfg.GitDir, "push", "--dry-run", cfg.RepoURL, head+":refs/heads/"+cfg.Branch)
			switch {
			case err == nil:
				c.ok("github write", "the deploy key can push, so Ship works")
			case strings.Contains(out, "read only") || strings.Contains(out, "read-only"):
				c.warn("github write", "the deploy key is read only, so Ship from Discord will fail",
					"on GitHub delete the deploy key and add the same key again with Allow write access ticked")
			default:
				c.warn("github write", "couldn't confirm write access: "+lastLine(out), "check the deploy key has Allow write access")
			}
		}
	}

	// Each server: service, txAdmin login, game port, cfg files, restart schedule.
	for _, t := range targetsOf(cfg) {
		n := t.Name
		svc := serverService(n)
		if serviceActive(svc) {
			c.ok(n+" service", svc+" is running")
		} else {
			c.fail(n+" service", svc+" is not running", "start the "+svc+" service; "+logsHint(svc))
		}
		if err := NewTxAdmin(t).Login(); err != nil {
			c.fail(n+" txadmin", "login as "+t.TxUser+" failed: "+err.Error(), "check "+strings.ToUpper(n)+"_TX_PASSWORD matches the txAdmin account, or reset it in txAdmin")
		} else {
			c.ok(n+" txadmin", "signed in as "+t.TxUser)
		}
		if NewTxAdmin(t).Online() {
			c.ok(n+" game server", "answering at "+t.InfoURL)
		} else {
			c.warn(n+" game server", "not answering at "+t.InfoURL, "start it in txAdmin; if it won't boot, check secrets.cfg has your license key")
		}
		checkServerFiles(c, cfg, t)
	}

	// Discord.
	if cfg.DiscordToken == "" {
		c.warn("discord", "no bot configured, approvals only work from the CLI", "set DISCORD_TOKEN and DISCORD_CHANNEL_ID")
	} else {
		checkDiscord(c, cfg)
	}

	// Disk, clock, timers, backups.
	if free, gb, ok := diskFree(filepath.Dir(cfg.GitDir)); ok {
		switch {
		case free < 5:
			c.fail("disk", fmt.Sprintf("%.0f%% free (%.1f GB)", free, gb), "free space now: old FXServer artifacts, logs, backups")
		case free < 15:
			c.warn("disk", fmt.Sprintf("%.0f%% free (%.1f GB)", free, gb), "free some space soon")
		default:
			c.ok("disk", fmt.Sprintf("%.0f%% free (%.1f GB)", free, gb))
		}
	}
	if synced, ok := clockSynced(); ok {
		if synced {
			c.ok("clock", "synchronised, time zone "+cfg.Location.String())
		} else {
			c.warn("clock", "not synchronised: scheduled restarts may drift", map[bool]string{true: "w32tm /resync", false: "timedatectl set-ntp true"}[runtime.GOOS == "windows"])
		}
	}
	if cfg.DevEnabled {
		// NTFS inherits the share's permissions on new files; only Linux needs the fix-up timer.
		if runtime.GOOS != "windows" {
			if serviceActive("fxship-dev-perms.timer") {
				c.ok("dev permissions", "staff can edit dev's files")
			} else {
				c.warn("dev permissions", "fxship-dev-perms.timer is not running, staff edits may block deploys", "systemctl enable --now fxship-dev-perms.timer")
			}
		}
	}
	checkBackups(c)

	return report(c, asJSON)
}

func logsHint(svc string) string {
	if runtime.GOOS == "windows" {
		return `read C:\ProgramData\fxship\logs\` + svc + `.log`
	}
	return "journalctl -u " + svc + " -n 50"
}

func scheduledTaskExists(name string) bool {
	return exec.Command("schtasks", "/Query", "/TN", name).Run() == nil
}

func targetsOf(cfg *Config) []serverTarget {
	if cfg.DevEnabled {
		return []serverTarget{cfg.Prod, cfg.Dev}
	}
	return []serverTarget{cfg.Prod}
}

func checkServerFiles(c *checker, cfg *Config, t serverTarget) {
	n := t.Name
	for _, f := range []string{"fxship.cfg", "instance.cfg", "secrets.cfg", "server.cfg"} {
		if _, err := os.Stat(filepath.Join(t.ServerData, f)); err != nil {
			c.fail(n+" files", f+" is missing from "+t.ServerData, "re-run the installer")
			return
		}
	}
	if b, err := os.ReadFile(filepath.Join(t.ServerData, "secrets.cfg")); err == nil {
		if !strings.Contains(string(b), "cfxk_") {
			c.fail(n+" license", "no license key in secrets.cfg", "get one at https://portal.cfx.re and set sv_licenseKey in "+filepath.Join(t.ServerData, "secrets.cfg"))
		} else {
			c.ok(n+" license", "license key set")
		}
	}
	// txAdmin's restarter must match RESTART_TIMES, or queued releases swap files for a restart that never comes.
	if n == "prod" {
		var tx struct {
			Restarter struct {
				Schedule []string `json:"schedule"`
			} `json:"restarter"`
		}
		p := filepath.Join(filepath.Dir(filepath.Dir(t.LogPath)), "config.json")
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &tx) == nil {
			norm := func(xs []string) string {
				var out []string
				for _, x := range xs {
					var h, m int
					fmt.Sscanf(x, "%d:%d", &h, &m)
					out = append(out, fmt.Sprintf("%02d:%02d", h, m))
				}
				sort.Strings(out)
				return strings.Join(out, ",")
			}
			if norm(tx.Restarter.Schedule) == norm(cfg.RestartTimes) {
				c.ok("restart schedule", "txAdmin and fxship agree: "+norm(cfg.RestartTimes))
			} else {
				c.fail("restart schedule", fmt.Sprintf("txAdmin restarts at %s but RESTART_TIMES is %s", joinOr(tx.Restarter.Schedule, "never"), strings.Join(cfg.RestartTimes, ",")),
					"make them the same: txAdmin > Settings > Restarter, or RESTART_TIMES in "+envFile())
			}
		}
	}
}

func checkDiscord(c *checker, cfg *Config) {
	s, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		c.fail("discord", err.Error(), "")
		return
	}
	s.Client.Timeout = 10 * time.Second
	me, err := s.User("@me")
	if err != nil {
		c.fail("discord bot", "token rejected: "+err.Error(), "reset the token in the Discord Developer Portal (Bot tab) and update DISCORD_TOKEN")
		return
	}
	c.ok("discord bot", "token works, bot is "+me.Username)
	ch, err := s.Channel(cfg.DiscordChannel)
	if err != nil {
		c.fail("discord channel", "the bot can't see channel "+cfg.DiscordChannel, "invite the bot to your server and let it view the deploy channel")
		return
	}
	perms, err := s.UserChannelPermissions(me.ID, ch.ID)
	need := map[string]int64{"View Channel": discordgo.PermissionViewChannel, "Send Messages": discordgo.PermissionSendMessages,
		"Read Message History": discordgo.PermissionReadMessageHistory}
	var missing []string
	if err == nil {
		for name, bit := range need {
			if perms&bit == 0 {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		c.fail("discord channel", "#"+ch.Name+" is missing permissions: "+strings.Join(missing, ", "), "give the bot's role those permissions on #"+ch.Name)
	} else {
		c.ok("discord channel", "posting in #"+ch.Name)
	}
	if roles, err := s.GuildRoles(ch.GuildID); err == nil {
		have := map[string]string{}
		for _, r := range roles {
			have[r.ID] = r.Name
		}
		var names, unknown []string
		for _, id := range cfg.ApproverRoles {
			if have[id] != "" {
				names = append(names, "@"+have[id])
			} else {
				unknown = append(unknown, id)
			}
		}
		switch {
		case len(unknown) > 0:
			c.fail("discord roles", "approver role ids not in the server: "+strings.Join(unknown, ", "), "copy role ids with Developer Mode on (right click the role > Copy Role ID)")
		case len(names) == 0:
			c.warn("discord roles", "no approver roles: only server admins can approve", "set DISCORD_APPROVER_ROLE_IDS")
		default:
			c.ok("discord roles", "approvers: "+strings.Join(names, ", ")+" and server admins")
		}
	}
	if cfg.SlashCommand != "" {
		if cmds, err := s.ApplicationCommands(me.ID, ch.GuildID); err != nil {
			c.warn("discord commands", "couldn't list slash commands: "+err.Error(), "re-invite the bot with the applications.commands scope")
		} else {
			found := false
			for _, cmd := range cmds {
				found = found || cmd.Name == cfg.SlashCommand
			}
			if found {
				c.ok("discord commands", "/"+cfg.SlashCommand+" is registered")
			} else {
				c.warn("discord commands", "/"+cfg.SlashCommand+" isn't registered yet", "restart fxship; if it stays missing, re-invite the bot with the applications.commands scope")
			}
		}
	}
}

// checkBackups reads the marker the backup script writes after every run.
func checkBackups(c *checker) {
	mark := paths().BackupMark
	b, err := os.ReadFile(mark)
	if err != nil {
		if serviceActive("fxship-backup.timer") || runtime.GOOS == "windows" && scheduledTaskExists("fxship-backup") {
			c.warn("backups", "scheduled but none has finished yet", "run a backup now: fxship backup run")
		} else {
			c.warn("backups", "not set up: a dead disk would lose the database", "re-run the installer and turn backups on")
		}
		return
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		c.warn("backups", "can't read "+mark, "run a backup now: fxship backup run")
		return
	}
	at, _ := strconv.ParseInt(f[0], 10, 64)
	age := time.Since(time.Unix(at, 0)).Round(time.Minute)
	switch {
	case f[1] != "ok":
		c.fail("backups", "the last backup failed "+age.String()+" ago", logsHint("fxship-backup"))
	case age > 36*time.Hour:
		c.fail("backups", "the last good backup is "+age.String()+" old", "check the fxship-backup schedule is enabled")
	default:
		c.ok("backups", "last good backup "+age.String()+" ago")
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return clip(lines[len(lines)-1], 200)
}

func report(c *checker, asJSON bool) int {
	fails := 0
	for _, r := range c.results {
		if r.Level == "fail" {
			fails++
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(c.results, "", "  ")
		fmt.Println(string(b))
	} else {
		mark := map[string]string{"ok": "\033[32m✓\033[0m", "warn": "\033[33m!\033[0m", "fail": "\033[31m✗\033[0m", "skip": "-"}
		if fi, _ := os.Stdout.Stat(); fi == nil || fi.Mode()&os.ModeCharDevice == 0 {
			mark = map[string]string{"ok": "ok  ", "warn": "WARN", "fail": "FAIL", "skip": "skip"}
		}
		for _, r := range c.results {
			fmt.Printf("%s %-18s %s\n", mark[r.Level], r.Name, r.Msg)
			if r.Fix != "" && r.Level != "ok" {
				fmt.Printf("  %-18s fix: %s\n", "", r.Fix)
			}
		}
		if fails == 0 {
			fmt.Println("\nAll required checks passed.")
		} else {
			fmt.Printf("\n%d %s failed.\n", fails, plural(fails, "check", "checks"))
		}
	}
	if fails > 0 {
		return 1
	}
	return 0
}
