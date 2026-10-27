package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	StatusPending    = "pending"    // waiting for a human
	StatusQueued     = "queued"     // approved, goes live at the next scheduled restart
	StatusApplied    = "applied"    // live on prod
	StatusRejected   = "rejected"   // a human said no
	StatusSuperseded = "superseded" // a newer push replaced it before anyone decided
	StatusFailed     = "failed"
	// StatusWorking is only ever shown on a card while a button press is being handled, never saved.
	StatusWorking = "working"
)

type Release struct {
	ID        string           `json:"id"`
	From      string           `json:"from"` // what prod will be running when this lands
	To        string           `json:"to"`
	Commits   []Commit         `json:"commits"`
	Resources []ResourceChange `json:"resources"`
	Added     int              `json:"added"`
	Deleted   int              `json:"deleted"`
	FileCount int              `json:"fileCount"`
	Lint      []LintIssue      `json:"lint"`
	DevReport string           `json:"devReport"`
	DevOK     *bool            `json:"devOk"`
	Status    string           `json:"status"`
	Actor     string           `json:"actor,omitempty"`
	Note      string           `json:"note,omitempty"`
	MessageID string           `json:"messageId,omitempty"`
	ApplyAt   *time.Time       `json:"applyAt,omitempty"`
	Offline   bool             `json:"prodOffline,omitempty"` // landed while prod was stopped
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

func (r *Release) Cold() bool {
	for _, rc := range r.Resources {
		if rc.Cold {
			return true
		}
	}
	return false
}

type State struct {
	LastSeen string     `json:"lastSeen"`
	DevSha   string     `json:"devSha"`
	ProdSha  string     `json:"prodSha"`
	Releases []*Release `json:"releases"`
	// KnownErrors holds each server's console errors from its last test, so a release is only blamed
	// for errors it introduced, not ones already sitting in the repo.
	KnownErrors map[string][]string `json:"knownErrors,omitempty"`
	// DevEdit is the current or last batch of edits made straight on the dev server.
	DevEdit *DevEdit `json:"devEdit,omitempty"`
	path    string
}

func LoadState(p string) (*State, error) {
	s := &State{path: p}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	s.path = p
	return s, nil
}

// Save writes atomically so a crash mid write never leaves half a state file.
func (s *State) Save() error {
	if len(s.Releases) > 100 {
		s.Releases = s.Releases[len(s.Releases)-100:]
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Find(id string) *Release {
	for _, r := range s.Releases {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *State) Latest(status string) *Release {
	for i := len(s.Releases) - 1; i >= 0; i-- {
		if s.Releases[i].Status == status {
			return s.Releases[i]
		}
	}
	return nil
}
