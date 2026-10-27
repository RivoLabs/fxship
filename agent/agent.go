package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

type Notifier interface {
	Publish(r *Release)     // create or update the release's message
	PublishEdit(e *DevEdit) // create or update the dev edits card
	Info(msg string)
}

type Agent struct {
	cfg   *Config
	git   *Git
	dev   *TxAdmin
	prod  *TxAdmin
	state *State
	note  Notifier
	mu    sync.Mutex // every git checkout and state change goes through here, one at a time
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// newID keeps release ids unique when the same sha comes round twice (force push, repeat rollback).
func (a *Agent) newID(base string) string {
	id := base
	for n := 2; a.state.Find(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (a *Agent) save() {
	if err := a.state.Save(); err != nil {
		log.Printf("state save: %v", err)
	}
}

// Bootstrap clones the repo and, on a fresh box, puts both servers on the current main without asking:
// there is nothing live yet to protect.
func (a *Agent) Bootstrap() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.git.Ensure(); err != nil {
		return err
	}
	sha, err := a.git.Fetch()
	if err != nil {
		return err
	}
	type wt struct {
		dir string
		dst *string
	}
	trees := []wt{{a.cfg.Prod.Worktree, &a.state.ProdSha}}
	if a.cfg.DevEnabled {
		trees = append(trees, wt{a.cfg.Dev.Worktree, &a.state.DevSha})
	}
	for _, t := range trees {
		if head := a.git.Head(t.dir); head != "" {
			*t.dst = head
			continue
		}
		if err := a.git.Checkout(t.dir, sha); err != nil {
			return err
		}
		*t.dst = sha
		log.Printf("bootstrap: %s checked out at %s", t.dir, short(sha))
	}
	if a.state.LastSeen == "" {
		a.state.LastSeen = a.state.ProdSha
		if a.cfg.DevEnabled {
			a.state.LastSeen = a.state.DevSha
		}
	}
	for _, t := range a.targets() {
		a.syncCfg(t)
	}
	a.save()
	return nil
}

// CfgLoop keeps each server's server.fxship.cfg in step with its server.cfg, wherever the edit came
// from: a deploy, a staff member on dev, or an admin on the box.
func (a *Agent) CfgLoop() {
	for {
		time.Sleep(3 * time.Second)
		for _, t := range a.targets() {
			a.syncCfg(t)
		}
	}
}

func (a *Agent) PollLoop() {
	for {
		if err := a.Poll(); err != nil {
			log.Printf("poll: %v", err)
		}
		time.Sleep(a.cfg.PollInterval)
	}
}

// Poll holds the lock from the fetch through staging: a Ship slipping in between would be undone
// by staging the older commit after it.
func (a *Agent) Poll() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	sha, err := a.git.Fetch()
	if err != nil || sha == a.state.LastSeen {
		return err
	}
	prevDev := a.state.DevSha
	a.state.LastSeen = sha
	a.save()
	log.Printf("new commit on %s: %s", a.cfg.Branch, short(sha))
	return a.stage(prevDev, sha, nil)
}

// base is what prod will be running once everything already approved has landed.
func (a *Agent) base() string {
	if q := a.state.Latest(StatusQueued); q != nil {
		return q.To
	}
	return a.state.ProdSha
}

// devOutcome is a dev test that already happened, for a commit dev was running before it existed
// on GitHub (shipped dev edits).
type devOutcome struct {
	ok     *bool
	report string
}

// Stage deploys sha to dev, tests it there, and opens a release for prod.
func (a *Agent) Stage(prevDev, sha string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stage(prevDev, sha, nil)
}

func (a *Agent) stage(prevDev, sha string, pre *devOutcome) error {
	from := a.base()
	r := &Release{ID: a.newID(short(sha)), From: from, To: sha, Status: StatusPending, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if from == sha {
		return nil
	}
	var err error
	if r.Commits, err = a.git.Commits(from, sha); err != nil {
		return err
	}
	files, err := a.git.Diff(from, sha)
	if err != nil {
		return err
	}
	if r.Resources, err = a.git.Resources(a.cfg, from, sha, files); err != nil {
		return err
	}
	for _, f := range files {
		r.Added += f.Added
		r.Deleted += f.Deleted
	}
	r.FileCount = len(files)
	r.Lint = Lint(a.git, sha, files)

	// Older undecided releases are folded into this one.
	for _, old := range a.state.Releases {
		if old.Status == StatusPending {
			old.Status, old.Note, old.UpdatedAt = StatusSuperseded, "superseded by "+r.ID, time.Now()
			a.note.Publish(old)
		}
	}
	a.state.Releases = append(a.state.Releases, r)
	if !a.cfg.DevEnabled {
		r.DevReport = "this box has no dev server, so nothing was tested live"
		a.note.Publish(r)
		a.save()
		return nil
	}
	if pre != nil {
		r.DevOK, r.DevReport = pre.ok, "tested live on dev while it was being edited: "+pre.report
		a.note.Publish(r)
		a.save()
		return nil
	}
	r.DevReport = "deploying to dev..."
	a.note.Publish(r)
	a.save()

	// Unshipped edits on dev win over a push that changes the same files: dev stays put until
	// someone ships or discards them, and the release says so.
	if clash := a.editClash(sha); len(clash) > 0 {
		r.DevReport = fmt.Sprintf("dev not updated: unshipped edits on dev change the same files (%s). Ship or discard them and dev catches up", strings.Join(clash, ", "))
		r.UpdatedAt = time.Now()
		a.note.Publish(r)
		if e := a.state.DevEdit; e != nil {
			e.Note = "A newer push to GitHub (" + short(sha) + ") changes the same files. Ship or discard these edits so dev can take it."
			a.note.PublishEdit(e)
		}
		a.save()
		return nil
	}

	// Dev only gets what changed since dev's last deploy.
	devFiles, err := a.git.Diff(prevDev, sha)
	if err != nil {
		return err
	}
	devRes, err := a.git.Resources(a.cfg, prevDev, sha, devFiles)
	if err != nil {
		return err
	}
	if err := a.git.Checkout(a.cfg.Dev.Worktree, sha); err != nil {
		r.DevReport = "checkout failed: " + err.Error()
		ok := false
		r.DevOK = &ok
		a.note.Publish(r)
		a.save()
		return err
	}
	a.state.DevSha = sha
	a.save()
	ok, report := a.activate(a.dev, devRes, true)
	r.DevOK, r.DevReport, r.UpdatedAt = ok, report, time.Now()
	a.note.Publish(r)
	a.save()
	return nil
}

// activate loads changed resources into a running server. Dev restarts outright when it must;
// prod never restarts from here.
func (a *Agent) activate(x *TxAdmin, res []ResourceChange, allowRestart bool) (*bool, string) {
	if len(res) == 0 {
		t := true
		return &t, "no resource changes"
	}
	if !x.Online() {
		return nil, x.t.Name + " server offline, files updated, live test skipped"
	}
	names := make([]string, 0, len(res))
	cold := false
	for _, rc := range res {
		names = append(names, rc.Name)
		cold = cold || rc.Cold
	}
	off := x.LogSize()
	var steps []string
	if cold && allowRestart {
		a.syncCfg(x.t)
		if err := x.Restart(); err != nil {
			f := false
			return &f, "restart failed: " + err.Error()
		}
		time.Sleep(5 * time.Second)
		if !x.WaitOnline(5 * time.Minute) {
			f := false
			return &f, "server did not come back within 5 minutes of restart"
		}
		steps = append(steps, "restarted")
	} else {
		if err := x.Command("refresh_res", ""); err != nil {
			f := false
			return &f, "refresh failed: " + err.Error()
		}
		for _, rc := range res {
			action := "ensure_res"
			switch rc.Kind {
			case "config":
				continue // read at boot only
			case "removed":
				action = "stop_res"
			}
			if err := x.Command(action, rc.Name); err != nil {
				steps = append(steps, fmt.Sprintf("%s %s failed: %v", action, rc.Name, err))
			}
		}
		steps = append(steps, fmt.Sprintf("reloaded %d %s", len(res), plural(len(res), "resource", "resources")))
	}
	time.Sleep(a.cfg.DevSettle)
	errs := ConsoleErrors(x.LogSince(off), names)
	fresh, known := splitKnown(errs, a.state.KnownErrors[x.t.Name])
	if a.state.KnownErrors == nil {
		a.state.KnownErrors = map[string][]string{}
	}
	if cold && allowRestart {
		// Only a full boot shows every error, so only a full boot resets the baseline.
		a.state.KnownErrors[x.t.Name] = errs
	} else {
		a.state.KnownErrors[x.t.Name] = append(a.state.KnownErrors[x.t.Name], fresh...)
	}
	okv := len(fresh) == 0
	sum := strings.Join(steps, ", ")
	older := ""
	if known > 0 {
		older = fmt.Sprintf(" (%d older %s still in the repo)", known, plural(known, "error", "errors"))
	}
	if okv {
		return &okv, sum + ", no new errors" + older
	}
	if len(fresh) > 6 {
		fresh = append(fresh[:6], fmt.Sprintf("...and %d more", len(fresh)-6))
	}
	return &okv, sum + ", new errors" + older + ":\n" + strings.Join(fresh, "\n")
}

// Approve is called from Discord or the CLI.
func (a *Agent) Approve(id, actor string) (*Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.state.Find(id)
	if r == nil {
		return nil, fmt.Errorf("no release %s", id)
	}
	if r.Status != StatusPending {
		return r, fmt.Errorf("release %s is %s", id, r.Status)
	}
	r.Actor = actor
	a.applyLocked(r, false)
	return r, nil
}

func (a *Agent) Reject(id, actor string) (*Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.state.Find(id)
	if r == nil {
		return nil, fmt.Errorf("no release %s", id)
	}
	if r.Status != StatusPending && r.Status != StatusQueued {
		return r, fmt.Errorf("release %s is %s", id, r.Status)
	}
	r.Status, r.Actor, r.Note, r.UpdatedAt, r.ApplyAt = StatusRejected, actor, "", time.Now(), nil
	a.note.Publish(r)
	a.save()
	return r, nil
}

// Rollback opens an already approved release that moves prod to an older sha.
func (a *Agent) Rollback(sha, actor string) (*Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	full, err := a.git.bare("rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return nil, err
	}
	sha = strings.TrimSpace(full)
	from := a.state.ProdSha
	files, err := a.git.Diff(from, sha)
	if err != nil {
		return nil, err
	}
	res, err := a.git.Resources(a.cfg, from, sha, files)
	if err != nil {
		return nil, err
	}
	for _, q := range a.state.Releases {
		if q.Status == StatusQueued || q.Status == StatusPending {
			q.Status, q.Note, q.UpdatedAt, q.ApplyAt = StatusSuperseded, "superseded by rollback to "+short(sha), time.Now(), nil
			a.note.Publish(q)
		}
	}
	r := &Release{ID: a.newID("rb-" + short(sha)), From: from, To: sha, Resources: res, FileCount: len(files), Status: StatusPending,
		Actor: actor, CreatedAt: time.Now(), UpdatedAt: time.Now(), Note: "rollback"}
	r.Commits = []Commit{{Sha: sha, Author: actor, Subject: "rollback prod to " + short(sha)}}
	a.state.Releases = append(a.state.Releases, r)
	a.applyLocked(r, false)
	return r, nil
}

// ApplyNow lands the queued release immediately with a full prod restart.
func (a *Agent) ApplyNow(actor string) (*Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.state.Latest(StatusQueued)
	if r == nil {
		return nil, fmt.Errorf("nothing queued")
	}
	r.Actor = actor
	a.applyLocked(r, true)
	return r, nil
}

// applyLocked lands r on prod: live when every change is hot, otherwise at the next scheduled restart.
func (a *Agent) applyLocked(r *Release, forceRestart bool) {
	now := time.Now()
	r.UpdatedAt = now
	// Releases queued before this one are folded in.
	for _, q := range a.state.Releases {
		if q != r && q.Status == StatusQueued {
			q.Status, q.Note, q.UpdatedAt, q.ApplyAt = StatusSuperseded, "folded into "+r.ID, now, nil
			a.note.Publish(q)
		}
	}
	files, err := a.git.Diff(a.state.ProdSha, r.To)
	if err != nil {
		a.fail(r, err)
		return
	}
	res, err := a.git.Resources(a.cfg, a.state.ProdSha, r.To, files)
	if err != nil {
		a.fail(r, err)
		return
	}
	cold := false
	for _, rc := range res {
		cold = cold || rc.Cold
	}
	online := a.prod.Online()

	switch {
	case !online || (!cold && !forceRestart):
		if err := a.git.Checkout(a.cfg.Prod.Worktree, r.To); err != nil {
			a.fail(r, err)
			return
		}
		a.state.ProdSha = r.To
		r.Status, r.ApplyAt = StatusApplied, nil
		if !online {
			r.Offline, r.Note = true, "Prod is switched off, so these files load the next time it starts."
		} else {
			_, report := a.activate(a.prod, res, false)
			r.Note = "applied live: " + report
		}
	case forceRestart:
		if err := a.git.Checkout(a.cfg.Prod.Worktree, r.To); err != nil {
			a.fail(r, err)
			return
		}
		a.state.ProdSha = r.To
		a.prod.Broadcast("Server restarting in 60 seconds for an update.")
		go func() {
			time.Sleep(60 * time.Second)
			a.syncCfg(a.cfg.Prod)
			if err := a.prod.Restart(); err != nil {
				log.Printf("prod restart: %v", err)
			}
		}()
		r.Status, r.ApplyAt, r.Note = StatusApplied, nil, "applied with an immediate restart"
	default:
		at := a.nextRestart(now)
		apply := at.Add(-a.cfg.ApplyLead)
		r.Status, r.ApplyAt = StatusQueued, &apply
		r.Note = "needs a full restart, goes live at the " + at.In(a.cfg.Location).Format("15:04 MST Mon") + " restart"
	}
	a.note.Publish(r)
	a.save()
}

func (a *Agent) fail(r *Release, err error) {
	r.Status, r.Note, r.UpdatedAt = StatusFailed, err.Error(), time.Now()
	log.Printf("release %s failed: %v", r.ID, err)
	a.note.Publish(r)
	a.save()
}

func (a *Agent) nextRestart(after time.Time) time.Time {
	loc := a.cfg.Location
	t := after.In(loc)
	var cands []time.Time
	for d := 0; d < 2; d++ {
		day := t.AddDate(0, 0, d)
		for _, hm := range a.cfg.RestartTimes {
			var h, m int
			if _, err := fmt.Sscanf(hm, "%d:%d", &h, &m); err != nil {
				continue
			}
			c := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc)
			// Too close to call: there's no time left to check files out before it.
			if c.Sub(t) > a.cfg.ApplyLead+30*time.Second {
				cands = append(cands, c)
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	if len(cands) == 0 {
		return t.Add(24 * time.Hour)
	}
	return cands[0]
}

// SchedulerLoop checks queued releases out shortly before txAdmin's scheduled restart picks them up.
func (a *Agent) SchedulerLoop() {
	for {
		time.Sleep(15 * time.Second)
		a.mu.Lock()
		r := a.state.Latest(StatusQueued)
		if r != nil && r.ApplyAt != nil && time.Now().After(*r.ApplyAt) {
			if err := a.git.Checkout(a.cfg.Prod.Worktree, r.To); err != nil {
				a.fail(r, err)
			} else {
				a.state.ProdSha = r.To
				r.Status, r.UpdatedAt = StatusApplied, time.Now()
				r.Note = "files swapped " + a.cfg.ApplyLead.String() + " before the scheduled restart"
				r.ApplyAt = nil
				a.syncCfg(a.cfg.Prod)
				a.note.Publish(r)
				a.save()
			}
		}
		a.mu.Unlock()
	}
}

// Snapshot reads the last saved state from disk instead of taking a.mu, so status still answers while a
// long clone, checkout or dev restart holds the lock. Save writes atomically, so the file is never torn.
func (a *Agent) Snapshot() map[string]any {
	st, err := LoadState(a.cfg.StatePath)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	recent := st.Releases
	if len(recent) > 10 {
		recent = recent[len(recent)-10:]
	}
	out := map[string]any{
		"version": version, "lastSeen": st.LastSeen, "prodSha": st.ProdSha, "prodOnline": a.prod.Online(),
		"devEnabled": a.cfg.DevEnabled, "releases": recent,
	}
	if a.cfg.DevEnabled {
		out["devSha"], out["devOnline"], out["devEdit"] = st.DevSha, a.dev.Online(), st.DevEdit
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
