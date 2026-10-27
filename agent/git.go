package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

type Git struct {
	Dir    string // bare repository
	URL    string
	Branch string
}

func (g *Git) run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (g *Git) bare(args ...string) (string, error) {
	return g.run("", append([]string{"--git-dir", g.Dir}, args...)...)
}

// localFiles are written into server-data by the installer and the agent, never by the repo. In
// the server-data layout they sit inside the worktree, so git must not see them as changes.
// .replxx_history is FXServer's console history, written to the folder it runs in.
var localFiles = []string{"/fxship.cfg", "/instance.cfg", "/secrets.cfg", "/server.fxship.cfg", "/cache/", "/txData/", "/.replxx_history"}

// Ensure clones the bare repository on first run (a large repo takes a while, once) and keeps the
// shared exclude list that every worktree reads.
func (g *Git) Ensure() error {
	if _, err := os.Stat(filepath.Join(g.Dir, "HEAD")); err != nil {
		if _, err := g.run("", "clone", "--bare", "--single-branch", "--branch", g.Branch, g.URL, g.Dir); err != nil {
			return err
		}
	}
	ex := filepath.Join(g.Dir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ex, []byte("# Managed by fxship: files that live next to the repo on the server, not in it.\n"+strings.Join(localFiles, "\n")+"\n"), 0o644)
}

// Fetch updates origin's branch and returns its sha.
func (g *Git) Fetch() (string, error) {
	ref := "refs/remotes/origin/" + g.Branch
	if _, err := g.bare("fetch", "--prune", "--no-tags", g.URL, "+refs/heads/"+g.Branch+":"+ref); err != nil {
		return "", err
	}
	out, err := g.bare("rev-parse", ref)
	return strings.TrimSpace(out), err
}

// Checkout puts a detached worktree at dir on sha. Local changes to files the move doesn't touch
// survive it: resources write runtime data into tracked files (a shop's stock JSON),
// and dev holds staff edits that haven't shipped. Where a file was changed locally and by the
// move, the repo wins and the local change is dropped; the dropped paths are logged.
func (g *Git) Checkout(dir, sha string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return g.addWorktree(dir, sha)
	}
	clash, err := g.Clashes(dir, sha)
	if err != nil {
		return err
	}
	if len(clash) > 0 {
		log.Printf("checkout %s: repo version wins for %d locally changed file(s): %s", dir, len(clash), strings.Join(clash, ", "))
		if err := g.dropLocal(dir, clash); err != nil {
			return err
		}
	}
	_, err = g.run(dir, "checkout", "--detach", sha)
	return err
}

// addWorktree creates the worktree at dir. dir may already hold fxship's own local files (the
// server-data layout keeps secrets.cfg and friends in it); anything else there is refused rather
// than overwritten. The local files are moved aside while git creates the worktree.
func (g *Git) addWorktree(dir, sha string) error {
	entries, _ := os.ReadDir(dir)
	keep := map[string]bool{}
	for _, f := range localFiles {
		keep[strings.Trim(f, "/")] = true
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			return fmt.Errorf("%s exists, is not empty and is not a worktree (found %s); refusing to overwrite", dir, e.Name())
		}
	}
	if len(entries) == 0 {
		os.Remove(dir)
		_, err := g.bare("worktree", "add", "--detach", "--force", dir, sha)
		return err
	}
	aside, err := os.MkdirTemp(filepath.Dir(dir), ".fxship-aside-")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(aside, e.Name())); err != nil {
			return err
		}
	}
	os.Remove(dir)
	_, werr := g.bare("worktree", "add", "--detach", "--force", dir, sha)
	if werr != nil {
		os.MkdirAll(dir, 0o755)
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(aside, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("putting %s back after creating the worktree: %w (it is in %s)", e.Name(), err, aside)
		}
	}
	os.Remove(aside)
	return werr
}

// StatusEntry is one path from git status: Code is "M" (changed), "D" (deleted) or "?" (untracked).
type StatusEntry struct {
	Path string
	Code string
}

// Status lists local changes in a worktree. Untracked files are included; callers filter them.
func (g *Git) Status(dir string) ([]StatusEntry, error) {
	out, err := g.run(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	var st []StatusEntry
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 4 {
			continue
		}
		xy, p := rec[:2], rec[3:]
		code := "M"
		switch {
		case xy == "??":
			code = "?"
		case strings.Contains(xy, "D"):
			code = "D"
		}
		st = append(st, StatusEntry{Path: p, Code: code})
	}
	return st, nil
}

// Clashes lists local changes in dir that a move to sha would overwrite.
func (g *Git) Clashes(dir, sha string) ([]string, error) {
	st, err := g.Status(dir)
	if err != nil || len(st) == 0 {
		return nil, err
	}
	head := g.Head(dir)
	if head == "" || head == sha {
		return nil, nil
	}
	out, err := g.run(dir, "diff", "--name-only", "--no-renames", "-z", head, sha)
	if err != nil {
		return nil, err
	}
	moved := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			moved[p] = true
		}
	}
	var clash []string
	for _, e := range st {
		if moved[e.Path] {
			clash = append(clash, e.Path)
		}
	}
	return clash, nil
}

// dropLocal throws away local changes to paths: tracked files go back to HEAD, untracked ones go.
func (g *Git) dropLocal(dir string, paths []string) error {
	var tracked []string
	for _, p := range paths {
		if _, err := g.run(dir, "ls-files", "--error-unmatch", "--", p); err != nil {
			if err := os.Remove(filepath.Join(dir, p)); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		tracked = append(tracked, p)
	}
	for len(tracked) > 0 {
		n := min(len(tracked), 200)
		if _, err := g.run(dir, append([]string{"checkout", "HEAD", "--"}, tracked[:n]...)...); err != nil {
			return err
		}
		tracked = tracked[n:]
	}
	return nil
}

func (g *Git) Head(dir string) string {
	out, err := g.run(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type Commit struct {
	Sha     string `json:"sha"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
}

func (g *Git) Commits(from, to string) ([]Commit, error) {
	rng := to
	args := []string{"log", "--no-merges", "--format=%H%x1f%an%x1f%s%x1e"}
	if from != "" {
		rng = from + ".." + to
	} else {
		args = append(args, "-n", "15")
	}
	out, err := g.bare(append(args, rng)...)
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) == 3 {
			cs = append(cs, Commit{Sha: f[0], Author: f[1], Subject: f[2]})
		}
	}
	return cs, nil
}

type FileChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A, M, D
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

func (g *Git) Diff(from, to string) ([]FileChange, error) {
	if from == "" {
		from = emptyTree
	}
	ns, err := g.bare("diff", "--name-status", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	byPath := map[string]*FileChange{}
	var order []string
	parts := strings.Split(ns, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		st, p := parts[i], parts[i+1]
		if st == "" {
			continue
		}
		byPath[p] = &FileChange{Path: p, Status: st[:1]}
		order = append(order, p)
	}
	num, err := g.bare("diff", "--numstat", "--no-renames", "-z", from, to)
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(num, "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			continue
		}
		if fc := byPath[f[2]]; fc != nil {
			fc.Added, _ = strconv.Atoi(f[0]) // "-" for binary stays 0
			fc.Deleted, _ = strconv.Atoi(f[1])
		}
	}
	out := make([]FileChange, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out, nil
}

// Tree is what the agent needs to know about the resources in one commit.
type Tree struct {
	Dirs     map[string]bool   // every directory holding a manifest
	Manifest map[string]string // dir -> manifest path
	Streams  map[string]bool   // dirs with a stream/ folder somewhere inside
}

// ResourceDirs indexes the resources at sha.
func (g *Git) ResourceDirs(sha string) (*Tree, error) {
	t := &Tree{Dirs: map[string]bool{}, Manifest: map[string]string{}, Streams: map[string]bool{}}
	if sha == "" {
		return t, nil
	}
	out, err := g.bare("ls-tree", "-r", "--name-only", "-z", sha)
	if err != nil {
		return nil, err
	}
	var streamed []string
	for _, p := range strings.Split(out, "\x00") {
		b := path.Base(p)
		if b == "fxmanifest.lua" || b == "__resource.lua" {
			d := path.Dir(p)
			t.Dirs[d] = true
			if t.Manifest[d] == "" || b == "fxmanifest.lua" {
				t.Manifest[d] = p
			}
		}
		if i := strings.Index("/"+p, "/stream/"); i >= 0 {
			streamed = append(streamed, p)
		}
	}
	for _, p := range streamed {
		if d := t.Owner(p); d != "" {
			t.Streams[d] = true
		}
	}
	return t, nil
}

// Owner is the resource folder holding p, or "" when p isn't inside one.
func (t *Tree) Owner(p string) string {
	for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
		if t.Dirs[d] {
			return d
		}
	}
	return ""
}

// reAssets matches manifest entries that load game data. Restarting such a resource under players
// re-streams it, which crashes or desyncs clients, so a change to it waits for a restart.
var reAssets = regexp.MustCompile(`(?m)^\s*(data_file\b|this_is_a_map\b|replace_level_meta\b)`)

func manifestLoadsAssets(src []byte) bool { return reAssets.Match(src) }

func (g *Git) Show(sha, p string) ([]byte, error) {
	cmd := exec.Command("git", "--git-dir", g.Dir, "show", sha+":"+p)
	return cmd.Output()
}

type ResourceChange struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir"`
	Kind    string   `json:"kind"` // added, changed, removed, or config (a server .cfg, not a resource)
	Cold    bool     `json:"cold"` // needs a full server restart
	Reason  string   `json:"reason,omitempty"`
	Files   int      `json:"files"`
	Paths   []string `json:"-"`
	Streams bool     `json:"-"` // loads game assets (stream/ folder or data_file entries)
}

// isServerCfg reports whether p, a path with no resource around it, is server config that FXServer
// reads at boot: a .cfg in the server-data layout's root folders (not inside resources/).
func isServerCfg(cfg *Config, p string) bool {
	return cfg.Layout == "server-data" && strings.HasSuffix(strings.ToLower(p), ".cfg") && !strings.HasPrefix(p, "resources/")
}

// Resources maps file changes onto the resources that own them and decides how each can go live.
func (g *Git) Resources(cfg *Config, from, to string, files []FileChange) ([]ResourceChange, error) {
	newDirs, err := g.ResourceDirs(to)
	if err != nil {
		return nil, err
	}
	oldDirs, err := g.ResourceDirs(from)
	if err != nil {
		return nil, err
	}
	owner := func(p string) string {
		if d := newDirs.Owner(p); d != "" {
			return d
		}
		return oldDirs.Owner(p)
	}
	byDir := map[string]*ResourceChange{}
	for _, f := range files {
		d := owner(f.Path)
		if d == "" {
			if !isServerCfg(cfg, f.Path) {
				continue // repo housekeeping (.gitignore, README), not a resource
			}
			d = f.Path
			if byDir[d] == nil {
				byDir[d] = &ResourceChange{Name: f.Path, Dir: f.Path, Kind: "config"}
			}
		}
		rc := byDir[d]
		if rc == nil {
			rc = &ResourceChange{Name: path.Base(d), Dir: d, Kind: "changed"}
			switch {
			case newDirs.Dirs[d] && !oldDirs.Dirs[d]:
				rc.Kind = "added"
			case !newDirs.Dirs[d] && oldDirs.Dirs[d]:
				rc.Kind = "removed"
			}
			tree, sha := newDirs, to
			if rc.Kind == "removed" {
				tree, sha = oldDirs, from
			}
			rc.Streams = tree.Streams[d]
			if !rc.Streams && tree.Manifest[d] != "" {
				if src, err := g.Show(sha, tree.Manifest[d]); err == nil {
					rc.Streams = manifestLoadsAssets(src)
				}
			}
			byDir[d] = rc
		}
		rc.Files++
		rc.Paths = append(rc.Paths, f.Path)
		if !rc.Cold {
			if why := coldReason(cfg, rc, f.Path); why != "" {
				rc.Cold, rc.Reason = true, why
			}
		}
	}
	out := make([]ResourceChange, 0, len(byDir))
	for _, rc := range byDir {
		out = append(out, *rc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// coldReason says why a change to file p in rc can't be hot-reloaded, or "" if it can.
func coldReason(cfg *Config, rc *ResourceChange, p string) string {
	switch {
	case rc.Kind == "config":
		return "server config"
	case rc.Kind == "removed":
		return "resource removed"
	case cfg.HotResources[rc.Name]:
		return ""
	case cfg.CoreResources[rc.Name]:
		return "core resource"
	case rc.Streams, strings.Contains("/"+p, "/stream/"):
		return "streamed assets"
	case hasAnyPrefix(rc.Dir+"/", cfg.ColdPrefixes):
		return "cold prefix"
	}
	return ""
}

func hasAnyPrefix(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, p) || strings.Contains(s, "/"+p) {
			return true
		}
	}
	return false
}
