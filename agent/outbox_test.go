package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// A burst of 200 updates to 3 cards must reach Discord as a handful of edits carrying the latest
// state, spaced per card and per channel, and a deleted card must never be edited again.
func TestOutboxCoalescesAndPaces(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	var at []time.Time
	o := newOutbox(200*time.Millisecond, 50*time.Millisecond,
		func(id string, c []discordgo.MessageComponent) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, id+"="+c[0].(discordgo.TextDisplay).Content)
			at = append(at, time.Now())
			return nil
		},
		func(id string) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, id+"=deleted")
			at = append(at, time.Now())
			return nil
		})
	go o.Run()
	for i := 0; i < 200; i++ {
		for _, id := range []string{"a", "b", "c"} {
			o.Edit(id, []discordgo.MessageComponent{discordgo.TextDisplay{Content: fmt.Sprint(i)}})
		}
	}
	o.Delete("c")
	o.Edit("c", []discordgo.MessageComponent{discordgo.TextDisplay{Content: "after delete"}})
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) > 6 {
		t.Fatalf("%d sends for a burst of 600 updates: %v", len(sent), sent)
	}
	last := map[string]string{}
	for _, s := range sent {
		last[s[:1]] = s[2:]
	}
	if last["a"] != "199" || last["b"] != "199" || last["c"] != "deleted" {
		t.Fatalf("final states %v (sent %v)", last, sent)
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < 45*time.Millisecond {
			t.Fatalf("sends %d and %d only %v apart", i-1, i, gap)
		}
	}
}

func TestDevWatchNoisyFiles(t *testing.T) {
	w := newDevWatch()
	now := time.Unix(1_000_000, 0)
	var noisy map[string]bool
	// stock.json is rewritten every 3 seconds; config.lua is saved by a person twice.
	for tick := 0; tick < 20; tick++ {
		now = now.Add(3 * time.Second)
		sigs := map[string]string{"stock.json": fmt.Sprint(tick), "config.lua": "v1"}
		if tick > 5 {
			sigs["config.lua"] = "v2"
		}
		if tick > 15 {
			sigs["config.lua"] = "v3"
		}
		noisy = w.track(now, sigs)
	}
	if !noisy["stock.json"] || noisy["config.lua"] {
		t.Fatalf("noisy = %v", noisy)
	}
	// Quiet for longer than noisyRelease: it counts as a person's edit again.
	now = now.Add(noisyRelease + time.Second)
	if noisy = w.track(now, map[string]string{"stock.json": "19", "config.lua": "v3"}); noisy["stock.json"] {
		t.Fatal("stock.json still noisy after going quiet")
	}
}
