package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var editStyle = map[string]struct {
	color int
	label string
}{
	EditOpen:      {0xE67E22, "Live on dev, not shipped yet"},
	EditShipped:   {0x3BA55D, "Shipped"},
	EditDiscarded: {0x80848E, "Discarded"},
}

// editCard renders the dev edits as one Components V2 card, edited in place as files change.
func (d *Discord) editCard(e *DevEdit) []discordgo.MessageComponent {
	st := editStyle[e.Status]
	color := st.color
	head := fmt.Sprintf("### Edits on dev\n**%s**", st.label)
	if e.Actor != "" && e.Status != EditOpen {
		head += "\n-# by " + e.Actor
	}
	head += fmt.Sprintf("\n-# started <t:%d:R>, last change <t:%d:R>", e.Since.Unix(), e.UpdatedAt.Unix())
	if e.Note != "" {
		head += "\n" + e.Note
	}
	header := []discordgo.MessageComponent{discordgo.TextDisplay{Content: clip(head, 900)}}
	inner := []discordgo.MessageComponent{}
	if d.icon != "" {
		inner = append(inner, discordgo.Section{Components: header, Accessory: discordgo.Thumbnail{Media: discordgo.UnfurledMediaItem{URL: d.icon}}})
	} else {
		inner = append(inner, header...)
	}

	if len(e.Files) > 0 {
		var fl []string
		for _, f := range e.Files {
			what := map[string]string{"M": "edited", "D": "deleted", "?": "new"}[f.Code]
			if f.Owner != "" {
				what += " by " + f.Owner
			}
			fl = append(fl, fmt.Sprintf("`%s` · %s", f.Path, what))
		}
		inner = append(inner, divider(), discordgo.TextDisplay{Content: fmt.Sprintf("**Files (%d)**\n", len(e.Files)) + listClip(fl, "\n", 1100)})
	}

	if e.Status == EditOpen || e.Reload != "" {
		checks := []string{"**Checks**"}
		if len(e.Lint) == 0 {
			checks = append(checks, "Syntax: clean")
		} else {
			var ls []string
			for i, l := range e.Lint {
				if i == 5 {
					ls = append(ls, fmt.Sprintf("and %d more", len(e.Lint)-5))
					break
				}
				ls = append(ls, fmt.Sprintf("%s: %s", clip(l.Path, 70), clip(l.Msg, 110)))
			}
			checks = append(checks, fmt.Sprintf("Syntax: **%d issue(s)**\n```\n%s\n```", len(e.Lint), strings.Join(ls, "\n")))
		}
		line, logs, _ := strings.Cut(e.Reload, "\n")
		switch {
		case e.Reload == "":
		case e.DevOK == nil:
			checks = append(checks, "Dev: "+upperFirst(line))
		case *e.DevOK:
			checks = append(checks, "Dev: **ok**. "+upperFirst(line))
		default:
			checks = append(checks, "Dev: **errors**. "+upperFirst(line))
		}
		if logs != "" {
			checks = append(checks, "```\n"+clip(logs, 500)+"\n```")
		}
		inner = append(inner, divider(), discordgo.TextDisplay{Content: strings.Join(checks, "\n")})
	}

	foot := "-# Edit files on the dev drive and they reload on dev within seconds. Ship sends them to GitHub and opens a prod release."
	inner = append(inner, discordgo.TextDisplay{Content: d.footer(foot)})
	if e.Status == EditOpen {
		inner = append(inner, divider(), discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Ship to prod...", Style: discordgo.PrimaryButton, CustomID: "shipask:" + e.ID},
			discordgo.Button{Label: "Discard", Style: discordgo.DangerButton, CustomID: "discardask:" + e.ID},
		}})
	} else if e.ShippedSha != "" {
		inner = append(inner, divider(), discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "View commit", Style: discordgo.LinkButton, URL: d.cfg.WebURL + "/commit/" + e.ShippedSha},
		}})
	}
	return []discordgo.MessageComponent{discordgo.Container{AccentColor: &color, Components: inner}}
}

func (d *Discord) PublishEdit(e *DevEdit) {
	comps := d.editCard(e)
	if e.MessageID == "" {
		m, err := d.s.ChannelMessageSendComplex(d.cfg.DiscordChannel, &discordgo.MessageSend{Components: comps, Flags: discordgo.MessageFlagsIsComponentsV2})
		if err != nil {
			log.Printf("discord send dev edits: %v", err)
			return
		}
		e.MessageID = m.ID
		return
	}
	if _, err := d.s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: e.MessageID, Channel: d.cfg.DiscordChannel, Components: &comps, Flags: discordgo.MessageFlagsIsComponentsV2}); err != nil {
		log.Printf("discord edit dev edits: %v", err)
	}
}

// canShip: approvers, server admins and DISCORD_SHIPPER_ROLE_IDS may ship or discard dev edits.
// Shipping only opens a release; prod still needs an approver.
func (d *Discord) canShip(m *discordgo.Member) bool {
	if d.allowed(m) {
		return true
	}
	if m == nil {
		return false
	}
	for _, have := range m.Roles {
		for _, want := range d.cfg.ShipperRoles {
			if have == want {
				return true
			}
		}
	}
	return false
}

func ephemeral(s *discordgo.Session, i *discordgo.Interaction, msg string, comps ...discordgo.MessageComponent) {
	data := &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral}
	if len(comps) > 0 {
		data.Components = []discordgo.MessageComponent{discordgo.ActionsRow{Components: comps}}
	}
	if err := s.InteractionRespond(i, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: data}); err != nil {
		log.Printf("discord respond: %v", err)
	}
}

// onEditInteraction handles the dev edits card. Ship opens a form asking what changed; Discard
// asks for a second click first, because it throws work away.
func (d *Discord) onEditInteraction(s *discordgo.Session, i *discordgo.InteractionCreate, action, id string) {
	if !d.canShip(i.Member) {
		ephemeral(s, i.Interaction, "You don't have a role that can ship or discard dev edits.")
		return
	}
	actor := i.Member.User.Username
	switch action {
	case "shipask":
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "ship:" + id,
				Title:    "Ship dev edits",
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{CustomID: "msg", Label: "What did you change?", Style: discordgo.TextInputParagraph,
						Placeholder: "Fixed shop prices, moved the bank blip", Required: true, MaxLength: 300},
				}}},
			},
		})
		if err != nil {
			log.Printf("discord modal: %v", err)
		}
	case "ship":
		msg := ""
		for _, row := range i.ModalSubmitData().Components {
			if r, ok := row.(*discordgo.ActionsRow); ok {
				for _, c := range r.Components {
					if t, ok := c.(*discordgo.TextInput); ok && t.CustomID == "msg" {
						msg = t.Value
					}
				}
			}
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}})
		go func() {
			_, r, err := d.agent.Ship(id, actor, msg)
			out := "Shipped to GitHub."
			switch {
			case err != nil:
				out = "Didn't ship: " + err.Error()
			case r != nil:
				out = fmt.Sprintf("Shipped. Release `%s` is waiting for approval in this channel.", r.ID)
			}
			if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: out, Flags: discordgo.MessageFlagsEphemeral}); err != nil {
				log.Printf("discord followup: %v", err)
			}
		}()
	case "discardask":
		ephemeral(s, i.Interaction, "This puts every edited file on dev back to what GitHub has. The edits are gone for good. Sure?",
			discordgo.Button{Label: "Yes, discard them", Style: discordgo.DangerButton, CustomID: "discard:" + id})
	case "discard":
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Content: "Discarding...", Components: []discordgo.MessageComponent{}}})
		go func() {
			out := "Discarded. Dev is back to what GitHub has."
			if _, err := d.agent.Discard(id, actor); err != nil {
				out = "Didn't discard: " + err.Error()
			}
			if _, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{Content: out, Flags: discordgo.MessageFlagsEphemeral}); err != nil {
				log.Printf("discord followup: %v", err)
			}
		}()
	}
}

func (consoleNotifier) PublishEdit(e *DevEdit) {
	log.Printf("dev edits %s [%s] %d files, resources %v, lint issues %d, dev: %s %s",
		e.ID, e.Status, len(e.Files), e.Resources, len(e.Lint), strings.ReplaceAll(e.Reload, "\n", " | "), e.Note)
}
