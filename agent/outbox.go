package main

// The outbox keeps fxship well inside Discord's limits however busy the server gets. Card edits
// and deletes are queued per message: only the newest state of a card is sent, each card is edited
// at most once per gap, and the whole channel at most once per pace. Sending happens on its own
// goroutine, so a rate limit wait never holds up a deploy. discordgo also waits out any 429 itself.

import (
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type outItem struct {
	comps  []discordgo.MessageComponent
	delete bool
}

type outbox struct {
	mu      sync.Mutex
	pending map[string]*outItem  // message id -> newest change
	order   []string             // message ids in the order they were first queued
	last    map[string]time.Time // message id -> when it was last sent
	lastAny time.Time
	gap     time.Duration // per card
	pace    time.Duration // per channel
	wake    chan struct{}
	edit    func(id string, comps []discordgo.MessageComponent) error
	remove  func(id string) error
	now     func() time.Time
}

func newOutbox(gap, pace time.Duration, edit func(string, []discordgo.MessageComponent) error, remove func(string) error) *outbox {
	return &outbox{pending: map[string]*outItem{}, last: map[string]time.Time{}, gap: gap, pace: pace,
		wake: make(chan struct{}, 1), edit: edit, remove: remove, now: time.Now}
}

func (o *outbox) put(id string, it *outItem) {
	if id == "" {
		return
	}
	o.mu.Lock()
	if prev, ok := o.pending[id]; !ok {
		o.order = append(o.order, id)
	} else if prev.delete {
		it = prev // a deleted card stays deleted
	}
	o.pending[id] = it
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Edit queues the newest look of a card.
func (o *outbox) Edit(id string, comps []discordgo.MessageComponent) {
	o.put(id, &outItem{comps: comps})
}

// Delete queues a card's removal; any edit still waiting for it is dropped.
func (o *outbox) Delete(id string) { o.put(id, &outItem{delete: true}) }

// next takes the first queued change that is allowed to go now, or says how long to wait.
func (o *outbox) next() (string, *outItem, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	if wait := o.pace - now.Sub(o.lastAny); wait > 0 && len(o.order) > 0 {
		return "", nil, wait
	}
	wait := time.Duration(-1)
	for i, id := range o.order {
		it := o.pending[id]
		// Deletes don't wait for the per-card gap: a removed card can't be edited again.
		w := o.gap - now.Sub(o.last[id])
		if it.delete || w <= 0 {
			o.order = append(o.order[:i:i], o.order[i+1:]...)
			delete(o.pending, id)
			o.last[id], o.lastAny = now, now
			return id, it, 0
		}
		if wait < 0 || w < wait {
			wait = w
		}
	}
	return "", nil, wait
}

// Run sends queued changes until the process ends.
func (o *outbox) Run() {
	for {
		id, it, wait := o.next()
		switch {
		case it != nil:
			var err error
			if it.delete {
				err = o.remove(id)
			} else {
				err = o.edit(id, it.comps)
			}
			if err != nil {
				log.Printf("discord: updating message %s: %v", id, err)
			}
		case wait < 0:
			<-o.wake
		default:
			select {
			case <-o.wake:
			case <-time.After(wait):
			}
		}
		o.pruneLast()
	}
}

// pruneLast forgets send times older than the gap, so the map doesn't grow forever.
func (o *outbox) pruneLast() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.last) < 256 {
		return
	}
	cut := o.now().Add(-o.gap)
	for id, t := range o.last {
		if t.Before(cut) && o.pending[id] == nil {
			delete(o.last, id)
		}
	}
}
