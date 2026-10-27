package main

// Dev edits: staff change files straight on the dev server (the mapped drive or SSH). The agent
// notices within seconds, reloads the touched resources on dev, and keeps one Discord card listing
// what is live on dev but not shipped. Ship commits those edits to GitHub under the presser's name
// and opens the normal prod release; Discard puts the files back. Prod is never touched from here.

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	EditOpen      = "open"
	EditShipped   = "shipped"
	EditDiscarded = "discarded"
)

type EditFile struct {
	Path  string `json:"path"`
	Code  string `json:"code"`            // M changed, D deleted, ? new
	Owner string `json:"owner,omitempty"` // who created a new file, when that is known
}

type DevEdit struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Files      []EditFile  `json:"files"`
	Resources  []string    `json:"resources"`
	Lint       []LintIssue `json:"lint"`
	DevOK      *bool       `json:"devOk"`
	Reload     string      `json:"reload"` // what the last reload on dev did and saw
	Note       string      `json:"note,omitempty"`
	Actor      string      `json:"actor,omitempty"`
	ShippedSha string      `json:"shippedSha,omitempty"`
	MessageID  string      `json:"messageId,omitempty"`
	Since      time.Time   `json:"since"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

// devWatch is the watcher's memory between ticks; it never needs to survive a restart.
type devWatch struct {
	lastSig     string
	lastChange  time.Time
	reloadedSig string
	fileSigs    map[string]string // per file, as of the last reload

	// Files a resource rewrites on its own (stock lists, saved positions) would reload dev and
	// update the card every few seconds forever. A file that keeps changing is treated as runtime
	// data: it stops triggering reloads and card updates, but is still listed and still ships.
	seen       map[string]string      // per file, as of the last tick
	hits       map[string][]time.Time // when each file was seen changing, last minute only
	noisyUntil map[string]time.Time
}

func newDevWatch() *devWatch {
	return &devWatch{fileSigs: map[string]string{}, seen: map[string]string{}, hits: map[string][]time.Time{}, noisyUntil: map[string]time.Time{}}
}

const (
	noisyWindow  = time.Minute
	noisyHits    = 8               // changes inside noisyWindow that make a file runtime data
	noisyRelease = 5 * time.Minute // quiet time before it counts as a person's edit again
)

// track notes which files changed since the last tick and reports the ones changing on their own.
func (w *devWatch) track(now time.Time, sigs map[string]string) map[string]bool {
	noisy := map[string]bool{}
	for p, s := range sigs {
		if old, ok := w.seen[p]; ok && old != s {
			keep := w.hits[p][:0]
			for _, t := range w.hits[p] {
				if now.Sub(t) < noisyWindow {
					keep = append(keep, t)
				}
			}
			w.hits[p] = append(keep, now)
			if len(w.hits[p]) >= noisyHits {
				w.noisyUntil[p] = now.Add(noisyRelease)
			}
		}
		if now.Before(w.noisyUntil[p]) {
			noisy[p] = true
		}
	}
	for p := range w.seen {
		if _, ok := sigs[p]; !ok {
			delete(w.hits, p)
			delete(w.noisyUntil, p)
		}
	}
	w.seen = sigs
	return noisy
}

// globRe turns a DEV_IGNORE pattern into a regexp: ** crosses folders, * and ? stay inside one.
// Everything else is literal, so resource folders like [criminal] need no escaping.
func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func (a *Agent) ignored(p string) bool {
	for _, re := range a.cfg.DevIgnore {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// devEdits lists what staff changed on dev. Tracked files count unless DEV_IGNORE names them
// (resources that write runtime data into their own files). New files only count when someone
// other than the server's own user made them: FXServer writes caches and logs as fivem.
func (a *Agent) devEdits() ([]EditFile, error) {
	st, err := a.git.Status(a.cfg.Dev.Worktree)
	if err != nil {
		return nil, err
	}
	var out []EditFile
	for _, e := range st {
		if a.ignored(e.Path) {
			continue
		}
		f := EditFile{Path: e.Path, Code: e.Code}
		if e.Code == "?" {
			owner, mine := fileOwner(filepath.Join(a.cfg.Dev.Worktree, e.Path))
			if mine {
				continue
			}
			f.Owner = owner
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (a *Agent) fileSig(p string) string {
	fi, err := os.Stat(filepath.Join(a.cfg.Dev.Worktree, p))
	if err != nil {
		return "gone"
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

// resourceOf finds the resource folder holding p: the nearest parent with a manifest on disk or
// at dev's commit. gone is true when the manifest is no longer on disk (resource deleted).
func (a *Agent) resourceOf(p string, atHead *Tree) (dir string, gone bool) {
	for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
		onDisk := false
		for _, m := range []string{"fxmanifest.lua", "__resource.lua"} {
			if _, err := os.Stat(filepath.Join(a.cfg.Dev.Worktree, d, m)); err == nil {
				onDisk = true
			}
		}
		if onDisk || (atHead != nil && atHead.Dirs[d]) {
			return d, !onDisk
		}
	}
	return "", false
}

// devChange describes a resource (or server .cfg) touched by dev edits, the way the release path
// would, reading streaming hints from the files on disk.
func (a *Agent) devChange(p, d string, gone bool) *ResourceChange {
	if d == "" {
		return &ResourceChange{Name: p, Dir: p, Kind: "config"}
	}
	rc := &ResourceChange{Name: path.Base(d), Dir: d, Kind: "changed"}
	if gone {
		rc.Kind = "removed"
		return rc
	}
	root := filepath.Join(a.cfg.Dev.Worktree, d)
	filepath.WalkDir(root, func(fp string, de os.DirEntry, err error) error {
		if err == nil && de.IsDir() && de.Name() == "stream" {
			rc.Streams = true
			return filepath.SkipAll
		}
		return nil
	})
	for _, m := range []string{"fxmanifest.lua", "__resource.lua"} {
		if src, err := os.ReadFile(filepath.Join(root, m)); err == nil && manifestLoadsAssets(src) {
			rc.Streams = true
		}
	}
	return rc
}

func (a *Agent) DevWatchLoop() {
	w := newDevWatch()
	for {
		time.Sleep(a.cfg.DevWatch)
		a.devTick(w)
	}
}

// devTick runs once per interval. It skips while a deploy holds the lock, waits until the edits
// have stopped changing for DevDebounce (an editor saving several files), then reloads.
func (a *Agent) devTick(w *devWatch) {
	if !a.mu.TryLock() {
		return
	}
	defer a.mu.Unlock()
	edits, err := a.devEdits()
	if err != nil {
		log.Printf("dev watch: %v", err)
		return
	}
	now := time.Now()
	sigs := make(map[string]string, len(edits))
	for _, f := range edits {
		sigs[f.Path] = a.fileSig(f.Path)
	}
	noisy := w.track(now, sigs)
	var sig strings.Builder
	for _, f := range edits {
		if noisy[f.Path] {
			fmt.Fprintf(&sig, "noisy|%s\n", f.Path) // becoming noisy updates the card once
			continue
		}
		fmt.Fprintf(&sig, "%s|%s|%s\n", f.Code, f.Path, sigs[f.Path])
	}
	if s := sig.String(); s != w.lastSig {
		w.lastSig, w.lastChange = s, now
		return
	}
	// A big copy over the drive can stall for a few seconds between files: wait longer for it, so
	// half a batch isn't loaded (or a restart triggered twice).
	debounce := a.cfg.DevDebounce
	if len(edits) > 20 {
		debounce = max(debounce, 15*time.Second)
	}
	if w.lastSig == w.reloadedSig || now.Sub(w.lastChange) < debounce {
		return
	}
	w.reloadedSig = w.lastSig

	e := a.state.DevEdit
	if len(edits) == 0 {
		if e != nil && e.Status == EditOpen {
			e.Status, e.Note, e.Files, e.UpdatedAt = EditDiscarded, "The edits were undone on dev before anyone shipped them.", nil, now
			a.note.PublishEdit(e)
			a.save()
		}
		w.fileSigs = map[string]string{}
		return
	}
	if e == nil || e.Status != EditOpen {
		e = &DevEdit{ID: "edit-" + now.In(a.cfg.Location).Format("0102-150405"), Status: EditOpen, Since: now}
		a.state.DevEdit = e
	}

	// Only resources whose files changed since the last reload get reloaded again.
	atHead, _ := a.git.ResourceDirs(a.git.Head(a.cfg.Dev.Worktree))
	fresh := map[string]string{}
	changed := map[string]bool{}
	gone := map[string]bool{}
	all := map[string]bool{}
	cold := ""
	touched := false
	touch := func(p string, d string, g bool) {
		touched = true
		if d != "" {
			changed[d], gone[d] = true, g
		}
		rc := a.devChange(p, d, g)
		if why := coldReason(a.cfg, rc, p); why != "" && cold == "" {
			cold = why + ": " + path.Base(rc.Dir)
		}
	}
	var runtime []string
	for _, f := range edits {
		if noisy[f.Path] {
			runtime = append(runtime, f.Path)
			if old, ok := w.fileSigs[f.Path]; ok {
				fresh[f.Path] = old // not "changed" while it is runtime data
			}
			continue
		}
		s := sigs[f.Path]
		fresh[f.Path] = s
		d, g := a.resourceOf(f.Path, atHead)
		if d == "" && !isServerCfg(a.cfg, f.Path) {
			continue
		}
		if d != "" {
			all[d] = true
		} else {
			all[f.Path] = true
		}
		if w.fileSigs[f.Path] != s {
			touch(f.Path, d, g)
		}
	}
	// A file that went back to its committed version also changed its resource.
	for p := range w.fileSigs {
		if _, still := fresh[p]; !still {
			if d, g := a.resourceOf(p, atHead); d != "" || isServerCfg(a.cfg, p) {
				touch(p, d, g)
			}
		}
	}
	w.fileSigs = fresh

	e.Files, e.Resources, e.UpdatedAt, e.Note = edits, sortedKeys(all), now, ""
	if len(runtime) > 0 {
		e.Note = fmt.Sprintf("Changing on its own, so treated as runtime data and not reloaded: %s. If a resource writes it, add it to DEV_IGNORE.",
			strings.Join(clipList(runtime, 3), ", "))
	}
	e.Lint = a.lintDisk(edits)
	if touched {
		e.DevOK, e.Reload = a.reloadDev(changed, gone, cold)
	}
	a.note.PublishEdit(e)
	a.save()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *Agent) lintDisk(edits []EditFile) []LintIssue {
	var issues []LintIssue
	for _, f := range edits {
		if f.Code == "D" {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Path))
		if ext != ".lua" && ext != ".json" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(a.cfg.Dev.Worktree, f.Path))
		if err != nil || (ext == ".lua" && isEscrowed(src)) {
			continue
		}
		if is := lintFile(f.Path, src); is != nil {
			issues = append(issues, *is)
		}
	}
	return issues
}

// devRestartOver: more changed resources than this and dev restarts instead of reloading each one.
const devRestartOver = 25

// reloadDev refreshes the server's resource list and restarts each changed resource on dev, then
// reads the console for errors that weren't there before. Changes that can't load live (core
// scripts, streamed files) or a big batch restart the whole dev server instead.
func (a *Agent) reloadDev(dirs, gone map[string]bool, cold string) (*bool, string) {
	x := a.dev
	if !x.Online() {
		return nil, "dev server is offline, the edits load when it starts"
	}
	if cold == "" && len(dirs) > devRestartOver {
		cold = fmt.Sprintf("%d resources changed", len(dirs))
	}
	var names, steps []string
	for _, d := range sortedKeys(dirs) {
		names = append(names, path.Base(d))
	}
	off := x.LogSize()
	if cold != "" {
		a.syncCfg(x.t)
		if err := x.Restart(); err != nil {
			f := false
			return &f, "restart failed: " + err.Error()
		}
		time.Sleep(5 * time.Second)
		if !x.WaitOnline(5 * time.Minute) {
			f := false
			return &f, "dev did not come back within 5 minutes of the restart"
		}
		steps = append(steps, "restarted dev ("+cold+")")
	} else {
		if err := x.Command("refresh_res", ""); err != nil {
			f := false
			return &f, "refresh failed: " + err.Error()
		}
		var done []string
		for _, d := range sortedKeys(dirs) {
			name := path.Base(d)
			action, verb := "ensure_res", "reloaded"
			if gone[d] {
				action, verb = "stop_res", "stopped"
			}
			if err := x.Command(action, name); err != nil {
				steps = append(steps, fmt.Sprintf("%s failed: %v", name, err))
				continue
			}
			done = append(done, verb+" "+name)
		}
		if len(done) > 4 {
			done = []string{fmt.Sprintf("reloaded %d resources", len(done))}
		}
		steps = append(done, steps...)
	}
	time.Sleep(a.cfg.DevEditSettle)
	if cold != "" {
		time.Sleep(a.cfg.DevSettle) // a full boot logs for longer
	}
	errs := ConsoleErrors(x.LogSince(off), names)
	freshErrs, _ := splitKnown(errs, a.state.KnownErrors[x.t.Name])
	ok := len(freshErrs) == 0
	sum := strings.Join(steps, ", ")
	if ok {
		return &ok, sum + ", no new errors"
	}
	if len(freshErrs) > 6 {
		freshErrs = append(freshErrs[:6], fmt.Sprintf("...and %d more", len(freshErrs)-6))
	}
	return &ok, sum + ", new errors:\n" + strings.Join(freshErrs, "\n")
}

func (a *Agent) openEdit(id string) (*DevEdit, error) {
	e := a.state.DevEdit
	if e == nil || (id != "" && e.ID != id) {
		return nil, fmt.Errorf("no dev edits with id %s", id)
	}
	if e.Status != EditOpen {
		return e, fmt.Errorf("those dev edits are already %s", e.Status)
	}
	return e, nil
}

// Ship commits the dev edits to GitHub as actor and opens a prod release for them. If main moved
// on GitHub meanwhile, the edits are replayed on top of it first.
func (a *Agent) Ship(id, actor, msg string) (*DevEdit, *Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, err := a.openEdit(id)
	if err != nil {
		return e, nil, err
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return e, nil, fmt.Errorf("say what changed")
	}
	edits, err := a.devEdits()
	if err != nil {
		return e, nil, err
	}
	if len(edits) == 0 {
		return e, nil, fmt.Errorf("nothing left to ship, dev matches GitHub")
	}
	dir := a.cfg.Dev.Worktree
	before, mine := a.git.Head(dir), ""
	undo := func(why error) (*DevEdit, *Release, error) {
		// Back to uncommitted edits, so nothing on dev is lost and Ship can be pressed again. After a
		// replay the files are first put back to the edits' own commit, or the newer GitHub work
		// would show up as edits.
		if mine != "" && a.git.Head(dir) != mine {
			a.git.run(dir, "reset", "--hard", "-q", mine)
		}
		a.git.run(dir, "reset", "--mixed", "-q", before)
		e.Note, e.UpdatedAt = why.Error(), time.Now()
		a.note.PublishEdit(e)
		a.save()
		return e, nil, why
	}
	paths := make([]string, 0, len(edits))
	var huge []string
	for _, f := range edits {
		paths = append(paths, f.Path)
		// GitHub rejects any file over 100 MB, after the whole push has uploaded.
		if fi, err := os.Stat(filepath.Join(dir, f.Path)); err == nil && !fi.IsDir() && fi.Size() > 100<<20 {
			huge = append(huge, fmt.Sprintf("%s (%d MB)", f.Path, fi.Size()>>20))
		}
	}
	if len(huge) > 0 {
		return undo(fmt.Errorf("GitHub won't take files over 100 MB: %s. Shrink or split them, then ship again", strings.Join(huge, ", ")))
	}
	for rest := paths; len(rest) > 0; {
		n := min(len(rest), 200)
		if _, err := a.git.run(dir, append([]string{"add", "-A", "--"}, rest[:n]...)...); err != nil {
			return undo(err)
		}
		rest = rest[n:]
	}
	name, email := a.shipIdentity(actor)
	via := "from Discord"
	if strings.HasPrefix(actor, "server (") {
		via = "from the server's command line"
	}
	if _, err := a.git.run(dir, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-q", "-m", msg,
		"-m", "Edited on the dev server and shipped "+via+" by "+name+"."); err != nil {
		return undo(err)
	}
	mine = a.git.Head(dir)

	rebased := false
	for attempt := 0; ; attempt++ {
		if _, err := a.git.run(dir, "push", a.git.URL, "HEAD:refs/heads/"+a.git.Branch); err == nil {
			break
		} else if attempt > 0 || !strings.Contains(err.Error(), "rejected") {
			if strings.Contains(err.Error(), "read only") {
				return undo(fmt.Errorf("GitHub refused the push: the server's deploy key is read only. A repo admin has to re-add it with write access"))
			}
			return undo(fmt.Errorf("push failed: %v", err))
		}
		// Someone pushed first: replay the edit on top of their work.
		latest, err := a.git.Fetch()
		if err != nil {
			return undo(err)
		}
		// Replaying makes new commits, so it needs an identity; the agent's container has none.
		if _, err := a.git.run(dir, "-c", "user.name="+name, "-c", "user.email="+email, "rebase", "-q", "--autostash", latest); err != nil {
			a.git.run(dir, "rebase", "--abort")
			if strings.Contains(err.Error(), "CONFLICT") || strings.Contains(err.Error(), "could not apply") {
				return undo(fmt.Errorf("a newer push to GitHub changes the same lines; pull it into your edits or discard them"))
			}
			return undo(fmt.Errorf("replaying the edits on the newer GitHub version failed: %v", err))
		}
		rebased = true
	}

	sha := a.git.Head(dir)
	prevDev := a.state.DevSha
	a.state.DevSha, a.state.LastSeen = sha, sha
	e.Status, e.Actor, e.ShippedSha, e.UpdatedAt = EditShipped, actor, sha, time.Now()
	e.Note = "Shipped to GitHub as " + short(sha) + ". The prod release card is below."
	a.note.PublishEdit(e)
	a.save()

	var pre *devOutcome
	if !rebased {
		// Dev already runs exactly this commit and was tested as the edits landed.
		pre = &devOutcome{ok: e.DevOK, report: e.Reload}
		prevDev = sha
	}
	if err := a.stage(prevDev, sha, pre); err != nil {
		return e, nil, err
	}
	return e, a.state.Latest(StatusPending), nil
}

var reIdentity = regexp.MustCompile(`^\s*(.+?)\s*<([^<>\s]+@[^<>\s]+)>\s*$`)

// shipIdentity is the git author for a Ship pressed by actor: their mapped identity from
// SHIP_AUTHORS, or their Discord name at SHIP_EMAIL_DOMAIN.
func (a *Agent) shipIdentity(actor string) (name, email string) {
	// The CLI's actor is "server (<login>)": the person is the login.
	if strings.HasPrefix(actor, "server (") && strings.HasSuffix(actor, ")") {
		actor = strings.TrimSuffix(strings.TrimPrefix(actor, "server ("), ")")
	}
	if m := reIdentity.FindStringSubmatch(a.cfg.ShipAuthors[actor]); m != nil {
		return m[1], m[2]
	}
	local := strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(actor, ""))
	if local == "" {
		local = "staff"
	}
	return actor, local + "@" + a.cfg.ShipEmailDomain
}

// Discard puts every edited file on dev back to the committed version and reloads what it touched.
func (a *Agent) Discard(id, actor string) (*DevEdit, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, err := a.openEdit(id)
	if err != nil {
		return e, err
	}
	edits, err := a.devEdits()
	if err != nil {
		return e, err
	}
	atHead, _ := a.git.ResourceDirs(a.git.Head(a.cfg.Dev.Worktree))
	dirs, gone, was := map[string]bool{}, map[string]bool{}, map[string]string{}
	paths := make([]string, 0, len(edits))
	for _, f := range edits {
		paths = append(paths, f.Path)
		if d, _ := a.resourceOf(f.Path, atHead); d != "" {
			dirs[d], was[f.Path] = true, d
		}
	}
	if err := a.git.dropLocal(a.cfg.Dev.Worktree, paths); err != nil {
		return e, err
	}
	cold := ""
	for _, f := range edits {
		d, g := a.resourceOf(f.Path, atHead)
		if d == "" && was[f.Path] != "" {
			// A resource made on dev and never shipped: discarding deleted it, so it is stopped.
			d, g = was[f.Path], true
		}
		if d == "" && !isServerCfg(a.cfg, f.Path) {
			continue
		}
		if d != "" {
			gone[d] = g
		}
		rc := a.devChange(f.Path, d, g)
		if why := coldReason(a.cfg, rc, f.Path); why != "" && cold == "" {
			cold = why + ": " + path.Base(rc.Dir)
		}
	}
	ok, report := a.reloadDev(dirs, gone, cold)
	e.Status, e.Actor, e.DevOK, e.Reload, e.UpdatedAt = EditDiscarded, actor, ok, report, time.Now()
	e.Note = "Dev is back to what GitHub has."
	a.note.PublishEdit(e)
	a.save()
	return e, a.catchUpDev()
}

// catchUpDev brings dev to the latest commit when a push was held back by clashing dev edits.
func (a *Agent) catchUpDev() error {
	if a.state.LastSeen == "" || a.git.Head(a.cfg.Dev.Worktree) == a.state.LastSeen {
		return nil
	}
	prev := a.state.DevSha
	files, err := a.git.Diff(prev, a.state.LastSeen)
	if err != nil {
		return err
	}
	res, err := a.git.Resources(a.cfg, prev, a.state.LastSeen, files)
	if err != nil {
		return err
	}
	if err := a.git.Checkout(a.cfg.Dev.Worktree, a.state.LastSeen); err != nil {
		return err
	}
	a.state.DevSha = a.state.LastSeen
	a.save()
	_, report := a.activate(a.dev, res, true)
	a.note.Info("Dev caught up to " + short(a.state.LastSeen) + ": " + report)
	return nil
}

// editClash reports open dev edits that a move of dev to sha would overwrite.
func (a *Agent) editClash(sha string) []string {
	e := a.state.DevEdit
	if e == nil || e.Status != EditOpen {
		return nil
	}
	clash, err := a.git.Clashes(a.cfg.Dev.Worktree, sha)
	if err != nil {
		return nil
	}
	mine := map[string]bool{}
	for _, f := range e.Files {
		mine[f.Path] = true
	}
	var out []string
	for _, p := range clash {
		if mine[p] {
			out = append(out, p)
		}
	}
	return out
}
