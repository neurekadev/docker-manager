package notify

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/discord"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/neurekadev/docker-manager/internal/domain"
)

// Rendering: each service gets the richest form it can show of a
// message. Discord an embed (the tone's color as its strip, fields side by
// side, the link on the title, footer and time), Slack colored
// attachments, Microsoft Teams an accented card, email HTML (with a plain
// part), Telegram HTML, ntfy and Gotify Markdown with a priority and a
// click link, Pushover a priority; a generic webhook gets the tone and
// link as extra JSON keys; everything else plain text. Options the owner
// put in the address (a color, a priority, a parse mode) win over ours.

// Tone colors: the app's danger, warn and ok tokens, and its accent blue
// for information.
var toneColors = map[domain.NotificationTone]uint{
	domain.ToneCritical: 0xfd6b66,
	domain.ToneWarning:  0xf5b544,
	domain.ToneSuccess:  0x4cf683,
	domain.ToneInfo:     0x2566fd,
}

func toneColor(t domain.NotificationTone) uint {
	if c, ok := toneColors[t]; ok {
		return c
	}
	return toneColors[domain.ToneInfo]
}

func toneHex(t domain.NotificationTone) string { return fmt.Sprintf("#%06x", toneColor(t)) }

// toneWords name the tone in plain text (email header, digest lines).
var toneWords = map[domain.NotificationTone]string{
	domain.ToneCritical: "Critical", domain.ToneWarning: "Warning", domain.ToneSuccess: "OK", domain.ToneInfo: "Info",
}

const openLabel = "Open in Docker Manager"

// Discord's limits (characters).
const (
	discordTitleMax       = 256
	discordDescriptionMax = 4096
	discordFieldsMax      = 25
	discordFieldNameMax   = 256
	discordFieldValueMax  = 1024
	discordFooterMax      = 2048
)

// clip shortens s to at most n runes, ending with "…" when cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// rendered is what deliver sends: the body and Shoutrrr's parameters.
type rendered struct {
	body   string
	params types.Params
}

// userSet reports whether the address sets query option key itself (the
// owner's choice wins).
func userSet(q url.Values, key string) bool {
	for k := range q {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// render builds the body and parameters of msg for service (svc is the
// initialized Shoutrrr service, q the address's query options, publicURL
// the manager's origin for the icon).
func render(service string, svc types.Service, msg domain.NotificationMessage, q url.Values, publicURL string) rendered {
	r := rendered{body: plainText(msg), params: types.Params{}}
	set := func(key, value string) {
		if !userSet(q, key) && value != "" {
			r.params[key] = value
		}
	}
	if msg.Title != "" {
		set("title", msg.Title)
	}
	switch service {
	case "discord":
		if d, ok := svc.(*discord.Service); ok && d.Config != nil {
			d.Config.JSON = true
			r.body = discordPayload(msg, d.Config.Username, d.Config.Avatar, publicURL)
			r.params = types.Params{}
		}
	case "slack":
		set("color", toneHex(msg.Tone))
		r.body = slackText(msg)
	case "teams":
		set("color", teamsColors[msg.Tone])
		r.body = markdownText(msg, "\n\n")
	case "telegram":
		if !userSet(q, "parsemode") {
			r.params["parsemode"] = "HTML"
			r.body = telegramHTML(msg)
		}
	case "ntfy":
		set("priority", ntfyPriorities[msg.Tone])
		set("tags", ntfyTags[msg.Tone])
		set("click", msg.URL)
		if !userSet(q, "markdown") {
			r.params["markdown"] = "yes"
			r.body = markdownText(msg, "\n")
		}
	case "gotify":
		set("priority", gotifyPriorities[msg.Tone])
		if !userSet(q, "extras") {
			extras := map[string]any{"client::display": map[string]string{"contentType": "text/markdown"}}
			if msg.URL != "" {
				extras["client::notification"] = map[string]any{"click": map[string]string{"url": msg.URL}}
			}
			b, _ := json.Marshal(extras)
			r.params["extras"] = string(b)
			r.body = markdownText(msg, "\n")
		}
	case "pushover":
		if msg.Tone == domain.ToneCritical {
			set("priority", "1")
		}
	case "smtp":
		// The subject is the title; the HTML part replaces the plain one
		// where the mail program can show it.
		if !userSet(q, "usehtml") {
			if t, ok := svc.(interface{ SetTemplateString(id, body string) error }); ok &&
				t.SetTemplateString("html", templateLiteral(emailHTML(msg))) == nil {
				r.params["usehtml"] = "yes"
			}
		}
	case "generic":
		// Extra JSON keys beside title and message for a webhook of one's
		// own.
		set("tone", string(msg.Tone))
		set("url", msg.URL)
	}
	return r
}

// templateLiteral makes s a Go template that prints s as it is.
func templateLiteral(s string) string { return strings.ReplaceAll(s, "{{", `{{"{{"}}`) }

// plainText is the body as text: the description, a line per field and
// the link.
func plainText(msg domain.NotificationMessage) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(msg.Body, "\n"))
	if len(msg.Fields) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		for i, f := range msg.Fields {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(f.Name + ": " + f.Value)
		}
	}
	if msg.URL != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(msg.URL)
	}
	return b.String()
}

// markdownEscaper escapes what Markdown would format in a value.
var markdownEscaper = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`)

// markdownText is the body as Markdown: fields as bold labels, the link
// as "Open in Docker Manager". sep separates the lines of fields.
func markdownText(msg domain.NotificationMessage, sep string) string {
	var parts []string
	if body := strings.TrimSpace(msg.Body); body != "" {
		parts = append(parts, markdownEscaper.Replace(body))
	}
	if len(msg.Fields) > 0 {
		lines := make([]string, 0, len(msg.Fields))
		for _, f := range msg.Fields {
			lines = append(lines, "**"+markdownEscaper.Replace(f.Name)+":** "+markdownEscaper.Replace(f.Value))
		}
		parts = append(parts, strings.Join(lines, sep))
	}
	if msg.URL != "" {
		parts = append(parts, "["+openLabel+"]("+msg.URL+")")
	}
	return strings.Join(parts, "\n\n")
}

var teamsColors = map[domain.NotificationTone]string{
	domain.ToneCritical: "attention", domain.ToneWarning: "warning", domain.ToneSuccess: "good", domain.ToneInfo: "accent",
}

var ntfyPriorities = map[domain.NotificationTone]string{
	domain.ToneCritical: "4", domain.ToneWarning: "3", domain.ToneSuccess: "2", domain.ToneInfo: "3",
}

var ntfyTags = map[domain.NotificationTone]string{
	domain.ToneCritical: "rotating_light", domain.ToneWarning: "warning", domain.ToneSuccess: "white_check_mark",
	domain.ToneInfo: "information_source",
}

var gotifyPriorities = map[domain.NotificationTone]string{
	domain.ToneCritical: "8", domain.ToneWarning: "5", domain.ToneSuccess: "4", domain.ToneInfo: "4",
}

// discordEmbed and its parts are Discord's webhook JSON.
type discordEmbed struct {
	Title       string              `json:"title,omitempty"`
	URL         string              `json:"url,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       uint                `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Author      *discordEmbedAuthor `json:"author,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordEmbedAuthor struct {
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

type discordEmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

type discordPayloadJSON struct {
	Username  string         `json:"username,omitempty"`
	AvatarURL string         `json:"avatar_url,omitempty"`
	Embeds    []discordEmbed `json:"embeds"`
	// AllowedMentions stops names in a message from pinging anyone.
	AllowedMentions map[string][]string `json:"allowed_mentions"`
}

// iconURL is Docker Manager's icon at the public URL (Discord fetches it
// itself, so only an https origin is used).
func iconURL(publicURL string) string {
	if !strings.HasPrefix(publicURL, "https://") {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/icons/apple-touch-icon-180x180.png"
}

// discordPayload is one embed: the tone's color, the title linking to the
// page, the description, inline fields, the footer and the time.
func discordPayload(msg domain.NotificationMessage, username, avatar, publicURL string) string {
	e := discordEmbed{
		Title: clip(msg.Title, discordTitleMax), URL: msg.URL, Color: toneColor(msg.Tone),
	}
	desc := strings.TrimSpace(msg.Body)
	if msg.URL != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "[" + openLabel + "](" + msg.URL + ")"
	}
	e.Description = clip(desc, discordDescriptionMax)
	for i, f := range msg.Fields {
		if i == discordFieldsMax {
			break
		}
		e.Fields = append(e.Fields, discordEmbedField{Name: clip(f.Name, discordFieldNameMax),
			Value: clip(f.Value, discordFieldValueMax), Inline: f.Inline})
	}
	if msg.Footer != "" {
		e.Footer = &discordEmbedFooter{Text: clip(msg.Footer, discordFooterMax), IconURL: iconURL(publicURL)}
	}
	if !msg.Time.IsZero() {
		e.Timestamp = msg.Time.UTC().Format(time.RFC3339)
	}
	if avatar == "" {
		avatar = iconURL(publicURL)
	}
	if username == "" {
		username = "Docker Manager"
	}
	b, _ := json.Marshal(discordPayloadJSON{Username: username, AvatarURL: avatar, Embeds: []discordEmbed{e},
		AllowedMentions: map[string][]string{"parse": {}}})
	return string(b)
}

// slackEscaper escapes Slack's control characters.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// slackText is one line per attachment (Slack's service sends each line
// as an attachment in the tone's color): the description, bold labeled
// fields and the link. Empty lines are left out.
func slackText(msg domain.NotificationMessage) string {
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(msg.Body), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, slackEscaper.Replace(l))
		}
	}
	for _, f := range msg.Fields {
		lines = append(lines, "*"+slackEscaper.Replace(f.Name)+":* "+slackEscaper.Replace(f.Value))
	}
	if msg.URL != "" {
		lines = append(lines, "<"+msg.URL+"|"+openLabel+">")
	}
	return strings.Join(lines, "\n")
}

// telegramHTML is the body in Telegram's HTML (the service adds the
// title in bold).
func telegramHTML(msg domain.NotificationMessage) string {
	var parts []string
	if body := strings.TrimSpace(msg.Body); body != "" {
		parts = append(parts, html.EscapeString(body))
	}
	if len(msg.Fields) > 0 {
		lines := make([]string, 0, len(msg.Fields))
		for _, f := range msg.Fields {
			lines = append(lines, "<b>"+html.EscapeString(f.Name)+":</b> "+html.EscapeString(f.Value))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if msg.URL != "" {
		parts = append(parts, `<a href="`+html.EscapeString(msg.URL)+`">`+openLabel+`</a>`)
	}
	return strings.Join(parts, "\n\n")
}

// emailHTML is a small card: a colored top bar with the tone's word, the
// title, the description, a table of fields, a button to the page and
// the footer. Inline styles only (mail programs drop style sheets).
func emailHTML(msg domain.NotificationMessage) string {
	color := toneHex(msg.Tone)
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="margin:0;padding:24px;background:#0d1117;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:600px;margin:0 auto;background:#161b22;border:1px solid #262f3d;border-radius:12px;border-top:4px solid ` + color + `;">`)
	b.WriteString(`<tr><td style="padding:20px 24px 4px;color:` + color + `;font-size:12px;font-weight:600;">` + esc(toneWords[msg.Tone]) + `</td></tr>`)
	b.WriteString(`<tr><td style="padding:0 24px 8px;color:#e6edf3;font-size:18px;font-weight:600;">` + esc(msg.Title) + `</td></tr>`)
	if body := strings.TrimSpace(msg.Body); body != "" {
		b.WriteString(`<tr><td style="padding:0 24px 16px;color:#c3cdd9;font-size:14px;line-height:20px;">` +
			strings.ReplaceAll(esc(body), "\n", "<br>") + `</td></tr>`)
	}
	if len(msg.Fields) > 0 {
		b.WriteString(`<tr><td style="padding:0 24px 16px;"><table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="font-size:14px;line-height:20px;">`)
		for _, f := range msg.Fields {
			b.WriteString(`<tr><td style="padding:6px 16px 6px 0;color:#8b98a9;white-space:nowrap;vertical-align:top;border-top:1px solid #262f3d;">` +
				esc(f.Name) + `</td><td style="padding:6px 0;color:#e6edf3;border-top:1px solid #262f3d;">` +
				strings.ReplaceAll(esc(f.Value), "\n", "<br>") + `</td></tr>`)
		}
		b.WriteString(`</table></td></tr>`)
	}
	if msg.URL != "" {
		b.WriteString(`<tr><td style="padding:4px 24px 20px;"><a href="` + esc(msg.URL) +
			`" style="display:inline-block;padding:8px 14px;background:#2566fd;color:#ffffff;text-decoration:none;border-radius:8px;font-size:14px;font-weight:600;">` +
			openLabel + `</a></td></tr>`)
	}
	if msg.Footer != "" {
		b.WriteString(`<tr><td style="padding:12px 24px;color:#8b98a9;font-size:12px;border-top:1px solid #262f3d;">` + esc(msg.Footer))
		if !msg.Time.IsZero() {
			b.WriteString(` · ` + esc(msg.Time.UTC().Format("Jan 2, 2006, 15:04")) + ` UTC`)
		}
		b.WriteString(`</td></tr>`)
	}
	b.WriteString(`</table></body></html>`)
	return b.String()
}
