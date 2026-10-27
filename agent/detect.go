package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Detection is what fxship can tell about a server folder before it manages it. The installer
// shows it, and it explains to owners why a change will or won't need a restart.
type Detection struct {
	Layout     string   `json:"layout"`               // resources or server-data
	Framework  string   `json:"framework"`            // Qbox, QBCore, ESX, ox_core, ND, vRP, or none found
	Resources  int      `json:"resources"`            // folders with a manifest
	Core       []string `json:"core"`                 // core resources present
	Streaming  []string `json:"streaming"`            // resources that load game assets
	ServerCfg  string   `json:"serverCfg,omitempty"`  // path of server.cfg, if found
	CfgRemoved []string `json:"cfgRemoved,omitempty"` // server.cfg lines fxship moves into instance/secrets
	Warnings   []string `json:"warnings,omitempty"`
}

var frameworks = []struct{ resource, name string }{
	{"qbx_core", "Qbox"}, {"qb-core", "QBCore"}, {"es_extended", "ESX"}, {"ox_core", "ox_core"}, {"ND_Core", "ND"}, {"vrp", "vRP"},
}

// Detect scans dir: either a resources folder or a whole server-data folder.
func Detect(dir string, core map[string]bool) (*Detection, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	d := &Detection{Layout: "resources", Framework: "none found"}
	resRoot := dir
	if fi, err := os.Stat(filepath.Join(dir, "resources")); err == nil && fi.IsDir() {
		if _, err := os.Stat(filepath.Join(dir, "server.cfg")); err == nil {
			d.Layout, resRoot = "server-data", filepath.Join(dir, "resources")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.cfg")); err == nil {
		d.ServerCfg = filepath.Join(dir, "server.cfg")
	}

	names := map[string]bool{}
	streams := map[string]bool{}
	var manifests []string
	err := filepath.WalkDir(resRoot, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			switch e.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if e.Name() == "fxmanifest.lua" || e.Name() == "__resource.lua" {
			manifests = append(manifests, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	for _, m := range manifests {
		dirs[filepath.Dir(m)] = true
	}
	for res := range dirs {
		name := filepath.Base(res)
		names[name] = true
		for _, m := range []string{"fxmanifest.lua", "__resource.lua"} {
			if src, err := os.ReadFile(filepath.Join(res, m)); err == nil && manifestLoadsAssets(src) {
				streams[name] = true
			}
		}
		if !streams[name] {
			filepath.WalkDir(res, func(p string, e fs.DirEntry, err error) error {
				if err == nil && e.IsDir() && e.Name() == "stream" {
					streams[name] = true
					return filepath.SkipAll
				}
				return nil
			})
		}
	}
	d.Resources = len(dirs)
	for _, f := range frameworks {
		if names[f.resource] {
			d.Framework = f.name
			break
		}
	}
	for n := range names {
		if core[n] {
			d.Core = append(d.Core, n)
		}
	}
	for n := range streams {
		d.Streaming = append(d.Streaming, n)
	}
	sort.Strings(d.Core)
	sort.Strings(d.Streaming)
	if d.Resources == 0 {
		d.Warnings = append(d.Warnings, "no fxmanifest.lua or __resource.lua found: is this the right folder?")
	}
	if d.ServerCfg != "" {
		if src, err := os.ReadFile(d.ServerCfg); err == nil {
			_, d.CfgRemoved = SanitizeCfg(src)
		}
	}
	// Secrets committed anywhere in cfg files are worth a warning even when fxship neutralises them.
	secrets := []struct{ needle, what string }{
		{"cfxk_", "a license key"}, {"mysql_connection_string", "a database password"},
		{"sv_tebexsecret", "a Tebex secret"}, {"discord.com/api/webhooks/", "a Discord webhook"},
	}
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if e.Name() == ".git" || e.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".cfg") || strings.HasPrefix(e.Name(), "secrets") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		low := strings.ToLower(string(src))
		for _, s := range secrets {
			if strings.Contains(low, s.needle) {
				d.Warnings = append(d.Warnings, fmt.Sprintf("%s contains %s: if it is in git, move it to secrets.cfg and rotate it", filepath.ToSlash(rel), s.what))
			}
		}
		return nil
	})
	return d, nil
}

func detectCmd(args []string) int {
	asJSON := false
	var dir string
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else {
			dir = a
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "usage: fxship detect [--json] <resources or server-data folder>")
		return 2
	}
	d, err := Detect(dir, set(env("CORE_RESOURCES", DefaultCoreResources)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if asJSON {
		b, _ := json.MarshalIndent(d, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	printDetection(d, "")
	return 0
}

func joinOr(xs []string, none string) string {
	if len(xs) == 0 {
		return none
	}
	return strings.Join(xs, ", ")
}

func clipList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), fmt.Sprintf("and %d more", len(xs)-n))
}

// printDetection shows what Detect found, each line starting with prefix.
func printDetection(d *Detection, prefix string) {
	line := func(format string, a ...any) { fmt.Fprintf(stdout, prefix+format+"\n", a...) }
	line("Layout:      %s", map[string]string{"resources": "resources folder (repo root holds the resources)", "server-data": "server-data folder (server.cfg and resources/ in the repo)"}[d.Layout])
	line("Framework:   %s", d.Framework)
	line("Resources:   %d", d.Resources)
	line("Core (%d):    %s", len(d.Core), joinOr(d.Core, "none"))
	line("Streaming (%d): %s", len(d.Streaming), joinOr(clipList(d.Streaming, 12), "none"))
	if len(d.CfgRemoved) > 0 {
		line("server.cfg:  fxship takes out %s (set per server, kept secret, or set in txAdmin)", strings.Join(d.CfgRemoved, ", "))
	}
	for _, w := range d.Warnings {
		line("Warning:     %s", w)
	}
	fmt.Fprintln(stdout)
	line("Changes to core or streaming resources wait for a restart; everything else goes live.")
}
