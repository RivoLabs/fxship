package main

// Slash commands let approvers do from Discord what used to need SSH: status, rollback, landing a
// queued release now, polling GitHub and refreshing the dev database. Everything answers privately
// (ephemeral). Anything that restarts prod or overwrites data asks for a second click.

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func (d *Discord) commands() []*discordgo.ApplicationCommand {
	sub := func(name, desc string, opts ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: desc, Options: opts}
	}
	subs := []*discordgo.ApplicationCommandOption{
		sub("status", "What prod and dev are running, and any release waiting"),
		sub("rollback", "Move prod back to an older commit",
			&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "commit", Description: "Commit sha (7 characters is enough)", Required: true, MinLength: ptr(4), MaxLength: 40}),
		sub("apply-now", "Land the queued release now: 60 second warning, then prod restarts"),
		sub("poll", "Check GitHub for new commits now"),
	}
	if d.cfg.DevEnabled && d.cfg.DB.Configured() {
		subs = append(subs, sub("refresh-dev-db", "Copy the prod database over the dev database"))
	}
	return []*discordgo.ApplicationCommand{{
		Name:        d.cfg.SlashCommand,
		Description: "Deploys for " + clip(d.cfg.BrandName, 80),
		Options:     subs,
		Contexts:    &[]discordgo.InteractionContextType{discordgo.InteractionContextGuild},
	}}
}

func ptr[T any](v T) *T { return &v }

// registerCommands replaces the bot's commands in the deploy channel's server. Guild commands show
// up at once, where global ones can take an hour.
func (d *Discord) registerCommands(appID, guildID string) {
	if d.cfg.SlashCommand == "" {
		return
	}
	if _, err := d.s.ApplicationCommandBulkOverwrite(appID, guildID, d.commands()); err != nil {
		log.Printf("discord: registering /%s failed: %v (re-invite the bot with the applications.commands scope)", d.cfg.SlashCommand, err)
		return
	}
	log.Printf("discord: /%s registered", d.cfg.SlashCommand)
}

func (d *Discord) onCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	if data.Name != d.cfg.SlashCommand || len(data.Options) == 0 {
		return
	}
	sub := data.Options[0]
	if i.ChannelID != d.cfg.DiscordChannel {
		ephemeral(s, i.Interaction, fmt.Sprintf("Use this in <#%s>.", d.cfg.DiscordChannel))
		return
	}
	if sub.Name == "status" {
		if !d.canShip(i.Member) {
			ephemeral(s, i.Interaction, "You don't have a deploy role.")
			return
		}
		ephemeral(s, i.Interaction, d.statusText())
		return
	}
	if !d.allowed(i.Member) {
		ephemeral(s, i.Interaction, "You don't have a deploy approver role.")
		return
	}
	actor := i.Member.User.Username
	switch sub.Name {
	case "apply-now":
		ephemeral(s, i.Interaction, "This restarts prod after a 60 second warning to players. Go ahead?",
			discordgo.Button{Label: "Yes, restart prod", Style: discordgo.DangerButton, CustomID: "applynow:go"})
	case "refresh-dev-db":
		ephemeral(s, i.Interaction, fmt.Sprintf("This stops dev, replaces the dev database `%s` with a copy of prod `%s`, then starts dev again. Anything only in the dev database is lost. Go ahead?", d.cfg.DB.DevName, d.cfg.DB.ProdName),
			discordgo.Button{Label: "Yes, copy prod to dev", Style: discordgo.DangerButton, CustomID: "dbrefresh:go"})
	case "poll":
		d.later(s, i, func() string {
			if err := d.agent.Poll(); err != nil {
				return "Couldn't check GitHub: " + err.Error()
			}
			return "Checked GitHub. Any new commits are on their way to dev."
		})
	case "rollback":
		sha := ""
		if len(sub.Options) > 0 {
			sha = strings.TrimSpace(sub.Options[0].StringValue())
		}
		d.later(s, i, func() string {
			r, err := d.agent.Rollback(sha, actor)
			if err != nil {
				return "Rollback didn't start: " + err.Error()
			}
			return fmt.Sprintf("Rollback `%s`: %s. The card is in this channel.", r.ID, statusStyle[r.Status].label)
		})
	}
}

// onConfirm handles the second click on a dangerous command.
func (d *Discord) onConfirm(s *discordgo.Session, i *discordgo.InteractionCreate, action string) {
	if !d.allowed(i.Member) {
		ephemeral(s, i.Interaction, "You don't have a deploy approver role.")
		return
	}
	actor := i.Member.User.Username
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: "Working on it...", Components: []discordgo.MessageComponent{}}})
	go func() {
		out := ""
		switch action {
		case "applynow":
			r, err := d.agent.ApplyNow(actor)
			if err != nil {
				out = "Nothing applied: " + err.Error()
			} else {
				out = fmt.Sprintf("Release `%s` is on prod. Players were warned and prod restarts in 60 seconds.", r.ID)
			}
		case "dbrefresh":
			d.agent.note.Info(fmt.Sprintf("%s started a dev database refresh from prod. Dev is stopped until it finishes.", actor))
			msg, err := d.agent.RefreshDevDB(actor)
			if err != nil {
				out = "Dev database refresh failed: " + err.Error()
			} else {
				out = msg
			}
			d.agent.note.Info(out)
		}
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: clip(out, 1900), Flags: discordgo.MessageFlagsEphemeral}); err != nil {
			log.Printf("discord followup: %v", err)
		}
	}()
}

// later answers at once with "thinking", runs fn off the interaction goroutine and posts its
// result privately. Discord gives 3 seconds for the first answer and 15 minutes for the rest.
func (d *Discord) later(s *discordgo.Session, i *discordgo.InteractionCreate, fn func() string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}}); err != nil {
		log.Printf("discord respond: %v", err)
		return
	}
	go func() {
		out := fn()
		if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: clip(out, 1900), Flags: discordgo.MessageFlagsEphemeral}); err != nil {
			log.Printf("discord followup: %v", err)
		}
	}()
}

// statusText summarises the saved state. It reads from disk, so it answers even while a deploy
// holds the agent's lock.
func (d *Discord) statusText() string {
	st, err := LoadState(d.cfg.StatePath)
	if err != nil {
		return "Couldn't read the deploy state: " + err.Error()
	}
	on := func(b bool) string {
		if b {
			return "online"
		}
		return "offline"
	}
	link := func(sha string) string {
		if sha == "" {
			return "nothing yet"
		}
		if d.cfg.WebURL == "" {
			return "`" + short(sha) + "`"
		}
		return fmt.Sprintf("[`%s`](%s/commit/%s)", short(sha), d.cfg.WebURL, sha)
	}
	lines := []string{fmt.Sprintf("**%s**", d.cfg.BrandName),
		fmt.Sprintf("Prod: %s, running %s", on(d.agent.prod.Online()), link(st.ProdSha))}
	if d.cfg.DevEnabled {
		lines = append(lines, fmt.Sprintf("Dev: %s, running %s", on(d.agent.dev.Online()), link(st.DevSha)))
		if e := st.DevEdit; e != nil && e.Status == EditOpen {
			lines = append(lines, fmt.Sprintf("Unshipped edits on dev: %d %s", len(e.Files), plural(len(e.Files), "file", "files")))
		}
	}
	if r := st.Latest(StatusPending); r != nil {
		lines = append(lines, fmt.Sprintf("Waiting for approval: release `%s` (%d %s)", r.ID, len(r.Commits), plural(len(r.Commits), "commit", "commits")))
	}
	if r := st.Latest(StatusQueued); r != nil && r.ApplyAt != nil {
		lines = append(lines, fmt.Sprintf("Queued: release `%s`, files swap <t:%d:R>", r.ID, r.ApplyAt.Unix()))
	}
	if len(d.cfg.RestartTimes) > 0 {
		next := d.agent.nextRestart(time.Now())
		lines = append(lines, fmt.Sprintf("Next scheduled restart <t:%d:R>", next.Unix()))
	}
	return strings.Join(lines, "\n")
}
