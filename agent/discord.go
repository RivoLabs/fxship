package main

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

type Discord struct {
	cfg   *Config
	s     *discordgo.Session
	agent *Agent
	icon  string // the server's icon, shown in each release card's header
}

func NewDiscord(cfg *Config) (*Discord, error) {
	s, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuilds
	d := &Discord{cfg: cfg, s: s, icon: cfg.BrandIconURL}
	s.AddHandler(d.onInteraction)
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("discord: logged in as %s", r.User.Username)
		ch, err := s.Channel(cfg.DiscordChannel)
		if err != nil {
			log.Printf("discord: can't see channel %s: %v (invite the bot and give it View Channel)", cfg.DiscordChannel, err)
			return
		}
		if g, err := s.Guild(ch.GuildID); err == nil && g.Icon != "" && d.icon == "" {
			d.icon = g.IconURL("256")
		}
		d.registerCommands(r.User.ID, ch.GuildID)
	})
	if err := s.Open(); err != nil {
		return nil, err
	}
	return d, nil
}

// poweredBy is the optional last line of every card (POWERED_BY=false removes it).
const poweredBy = "Powered by [Rivo](https://rivo.tools)"

// footer adds the powered-by line to a card's closing small print.
func (d *Discord) footer(small string) string {
	if !d.cfg.PoweredBy {
		return small
	}
	if small == "" {
		return "-# " + poweredBy
	}
	return small + "\n-# " + poweredBy
}

var statusStyle = map[string]struct {
	color int
	label string
}{
	StatusPending:    {0xE3A33B, "Waiting for approval"},
	StatusQueued:     {0x5865F2, "Approved, waiting for the restart"},
	StatusApplied:    {0x3BA55D, "Live on prod"},
	StatusRejected:   {0xDA373C, "Rejected"},
	StatusSuperseded: {0x80848E, "Superseded"},
	StatusFailed:     {0xDA373C, "Failed"},
	StatusWorking:    {0x5865F2, "Working..."},
}

// clip shortens s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n-3]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}

// card renders a release as a Components V2 message: one container tinted by status, with the
// server icon in the header, then changes, what goes live how, checks and the buttons.
func (d *Discord) card(r *Release) []discordgo.MessageComponent {
	st := statusStyle[r.Status]
	color := st.color

	status := st.label
	if r.Status == StatusApplied && r.Offline {
		status = "On prod, waiting for the server to start"
	}
	head := fmt.Sprintf("### Release `%s`\n**%s**", r.ID, status)
	if r.Actor != "" && r.Status != StatusPending && r.Status != StatusSuperseded {
		verb := "approved"
		if r.Status == StatusRejected {
			verb = "rejected"
		}
		head += "\n-# " + verb + " by " + r.Actor
	}
	if r.Note != "" {
		head += "\n" + upperFirst(r.Note)
	}
	if r.ApplyAt != nil {
		head += fmt.Sprintf("\nFiles swap <t:%d:R>", r.ApplyAt.Unix())
	}
	header := []discordgo.MessageComponent{discordgo.TextDisplay{Content: clip(head, 900)}}

	inner := []discordgo.MessageComponent{}
	if d.icon != "" {
		inner = append(inner, discordgo.Section{
			Components: header,
			Accessory:  discordgo.Thumbnail{Media: discordgo.UnfurledMediaItem{URL: d.icon}},
		})
	} else {
		inner = append(inner, header...)
	}

	if len(r.Commits) > 0 {
		var cl []string
		for i, c := range r.Commits {
			if i == 8 {
				cl = append(cl, fmt.Sprintf("-# and %d more", len(r.Commits)-8))
				break
			}
			cl = append(cl, fmt.Sprintf("[`%s`](%s/commit/%s) %s\n-# %s", short(c.Sha), d.cfg.WebURL, c.Sha, clip(c.Subject, 90), c.Author))
		}
		inner = append(inner, divider(), discordgo.TextDisplay{Content: clip("**Changes**\n"+strings.Join(cl, "\n"), 1100)})
	}

	var hot, cold []string
	for _, rc := range r.Resources {
		label := "`" + rc.Name + "`"
		if rc.Kind == "added" {
			label += " · new"
		}
		if rc.Cold {
			cold = append(cold, label+" · "+plainReason(rc))
		} else {
			hot = append(hot, label)
		}
	}
	var live []string
	if len(hot) > 0 {
		live = append(live, "**Goes live without a restart**\n"+listClip(hot, ", ", 450))
	}
	if len(cold) > 0 {
		live = append(live, "**Needs a server restart**\n"+listClip(cold, "\n", 600))
	}
	if len(live) > 0 {
		inner = append(inner, divider(), discordgo.TextDisplay{Content: strings.Join(live, "\n\n")})
	}

	checks := []string{"**Checks**"}
	if len(r.Lint) == 0 {
		checks = append(checks, "Syntax: clean")
	} else {
		var ls []string
		for i, l := range r.Lint {
			if i == 5 {
				ls = append(ls, fmt.Sprintf("and %d more", len(r.Lint)-5))
				break
			}
			ls = append(ls, fmt.Sprintf("%s: %s", clip(l.Path, 70), clip(l.Msg, 110)))
		}
		checks = append(checks, fmt.Sprintf("Syntax: %d issue(s)\n```\n%s\n```", len(r.Lint), strings.Join(ls, "\n")))
	}
	devLine, devLog, _ := strings.Cut(r.DevReport, "\n")
	switch {
	case r.DevOK == nil:
		checks = append(checks, "Dev test: "+upperFirst(devLine))
	case *r.DevOK:
		checks = append(checks, "Dev test: **passed**. "+upperFirst(devLine))
	default:
		checks = append(checks, "Dev test: **failed**. "+upperFirst(devLine))
	}
	if devLog != "" {
		checks = append(checks, "```\n"+clip(devLog, 700)+"\n```")
	}
	inner = append(inner, divider(), discordgo.TextDisplay{Content: strings.Join(checks, "\n")})

	foot := fmt.Sprintf("-# %d files changed · +%d −%d lines · prod `%s` → `%s` · <t:%d:R>", r.FileCount, r.Added, r.Deleted, short(r.From), short(r.To), r.CreatedAt.Unix())
	inner = append(inner, discordgo.TextDisplay{Content: d.footer(foot)})

	if row := d.buttons(r); row != nil {
		inner = append(inner, divider(), *row)
	}
	return []discordgo.MessageComponent{discordgo.Container{AccentColor: &color, Components: inner}}
}

func (d *Discord) buttons(r *Release) *discordgo.ActionsRow {
	var bs []discordgo.MessageComponent
	switch r.Status {
	case StatusPending:
		bs = append(bs,
			discordgo.Button{Label: "Approve for prod", Style: discordgo.SuccessButton, CustomID: "approve:" + r.ID},
			discordgo.Button{Label: "Reject", Style: discordgo.DangerButton, CustomID: "reject:" + r.ID})
	case StatusQueued:
		bs = append(bs, discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: "reject:" + r.ID})
	}
	if r.From != "" && r.From != r.To {
		bs = append(bs, discordgo.Button{Label: "View diff", Style: discordgo.LinkButton, URL: fmt.Sprintf("%s/compare/%s...%s", d.cfg.WebURL, r.From, r.To)})
	}
	if len(bs) == 0 {
		return nil
	}
	return &discordgo.ActionsRow{Components: bs}
}

func divider() discordgo.Separator {
	t := true
	s := discordgo.SeparatorSpacingSizeSmall
	return discordgo.Separator{Divider: &t, Spacing: &s}
}

// listClip joins items and stops before n bytes, saying how many were left out.
func listClip(items []string, sep string, n int) string {
	out := ""
	for i, it := range items {
		next := out
		if i > 0 {
			next += sep
		}
		next += it
		if len(next) > n {
			return out + fmt.Sprintf("\n-# and %d more", len(items)-i)
		}
		out = next
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (d *Discord) Publish(r *Release) {
	comps := d.card(r)
	if r.MessageID == "" {
		m, err := d.s.ChannelMessageSendComplex(d.cfg.DiscordChannel, &discordgo.MessageSend{Components: comps, Flags: discordgo.MessageFlagsIsComponentsV2})
		if err != nil {
			log.Printf("discord send: %v", err)
			return
		}
		r.MessageID = m.ID
		return
	}
	if _, err := d.s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: r.MessageID, Channel: d.cfg.DiscordChannel, Components: &comps, Flags: discordgo.MessageFlagsIsComponentsV2}); err != nil {
		log.Printf("discord edit: %v", err)
	}
}

func (d *Discord) Info(msg string) {
	grey := 0x80848E
	inner := []discordgo.MessageComponent{discordgo.TextDisplay{Content: clip(upperFirst(msg), 3800)}}
	if f := d.footer(""); f != "" {
		inner = append(inner, discordgo.TextDisplay{Content: f})
	}
	comps := []discordgo.MessageComponent{discordgo.Container{AccentColor: &grey, Components: inner}}
	if _, err := d.s.ChannelMessageSendComplex(d.cfg.DiscordChannel, &discordgo.MessageSend{Components: comps, Flags: discordgo.MessageFlagsIsComponentsV2}); err != nil {
		log.Printf("discord info: %v", err)
	}
}

func (d *Discord) allowed(m *discordgo.Member) bool {
	if m == nil {
		return false
	}
	// Server admins (the owner usually holds no named role) can always decide.
	if m.Permissions&discordgo.PermissionAdministrator != 0 {
		return true
	}
	for _, have := range m.Roles {
		for _, want := range d.cfg.ApproverRoles {
			if have == want {
				return true
			}
		}
	}
	return false
}

func (d *Discord) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	var custom string
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		d.onCommand(s, i)
		return
	case discordgo.InteractionMessageComponent:
		custom = i.MessageComponentData().CustomID
	case discordgo.InteractionModalSubmit:
		custom = i.ModalSubmitData().CustomID
	default:
		return
	}
	action, id, ok := strings.Cut(custom, ":")
	if !ok {
		return
	}
	switch action {
	case "shipask", "ship", "discardask", "discard":
		d.onEditInteraction(s, i, action, id)
		return
	case "applynow", "dbrefresh":
		d.onConfirm(s, i, action)
		return
	}
	if !d.allowed(i.Member) {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "You don't have a deploy approver role.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	actor := i.Member.User.Username

	// Answer inside Discord's 3 second window by flipping the card to a working state with the
	// buttons gone, so the click shows at once and can't be pressed twice. The release is read
	// from disk because the agent may hold its lock for minutes during a dev restart.
	resp := &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}
	if st, err := LoadState(d.cfg.StatePath); err == nil {
		if r := st.Find(id); r != nil {
			w := *r
			w.Status, w.Actor, w.ApplyAt = StatusWorking, "", nil
			w.Note = map[string]string{"approve": "Approving for prod", "reject": "Rejecting"}[action] + ", pressed by " + actor
			comps := d.card(&w)
			resp = &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{Components: comps, Flags: discordgo.MessageFlagsIsComponentsV2},
			}
		}
	}
	if err := s.InteractionRespond(i.Interaction, resp); err != nil {
		log.Printf("discord respond: %v", err)
	}

	go func() {
		var r *Release
		var err error
		switch action {
		case "approve":
			r, err = d.agent.Approve(id, actor)
		case "reject":
			r, err = d.agent.Reject(id, actor)
		}
		msg := ""
		switch {
		case err != nil:
			msg = "That didn't work: " + err.Error()
			if r != nil {
				d.Publish(r) // put the real state and buttons back
			}
		case r == nil:
		case r.Status == StatusApplied && r.Offline:
			msg = fmt.Sprintf("Done. Release `%s` is on prod, and loads when the server is next started.", r.ID)
		case r.Status == StatusApplied:
			msg = fmt.Sprintf("Done. Release `%s` is live on prod.", r.ID)
		case r.Status == StatusQueued && r.ApplyAt != nil:
			msg = fmt.Sprintf("Release `%s` is approved and goes live with the restart <t:%d:R>.", r.ID, r.ApplyAt.Unix())
		case r.Status == StatusRejected:
			msg = fmt.Sprintf("Release `%s` rejected, prod stays as it is.", r.ID)
		default:
			msg = fmt.Sprintf("Release `%s`: %s.", r.ID, statusStyle[r.Status].label)
		}
		if msg != "" {
			if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: msg, Flags: discordgo.MessageFlagsEphemeral}); err != nil {
				log.Printf("discord followup: %v", err)
			}
		}
	}()
}

// consoleNotifier stands in when no bot token is configured, so the pipeline still runs and logs.
type consoleNotifier struct{}

func (consoleNotifier) Publish(r *Release) {
	log.Printf("release %s [%s] %d commits, %d resources, lint issues %d, dev: %s %s",
		r.ID, r.Status, len(r.Commits), len(r.Resources), len(r.Lint), strings.ReplaceAll(r.DevReport, "\n", " | "), r.Note)
}
func (consoleNotifier) Info(msg string) { log.Print(msg) }

// plainReason says why a resource has to wait for a restart, in words a non-developer follows.
func plainReason(rc ResourceChange) string {
	switch {
	case rc.Kind == "removed":
		return "deleted"
	case rc.Kind == "config":
		return "server config, read at startup"
	case rc.Reason == "core resource":
		return "core script (inventory, framework, database)"
	case rc.Reason == "streamed assets":
		return "streamed files (maps, clothing, vehicles)"
	case rc.Reason == "cold prefix":
		return "in a folder set to always restart"
	}
	return rc.Reason
}
