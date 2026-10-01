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
// message. Discord an embed (the status line as its author, the tone's
// color as its strip, the title linking to the page, fields side by side,
// linked values and bulleted lists, the logo beside the footer and the
// time), Slack colored attachments, Microsoft Teams an accented card,
// email HTML (with a plain part), Telegram HTML, ntfy and Gotify Markdown
// with a priority and a click link, Pushover a priority; a generic webhook
// gets the tone and link as extra JSON keys; everything else plain text.
// Options the owner put in the address (a color, a priority, a parse
// mode, a Discord username or avatar) win over ours.

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

// toneWords name the tone in plain text (the email's top line of a
// message without a status line).
var toneWords = map[domain.NotificationTone]string{
	domain.ToneCritical: "Critical", domain.ToneWarning: "Warning", domain.ToneSuccess: "OK", domain.ToneInfo: "Info",
}

const openLabel = "Open in Docker Manager"

// LogoURL is Docker Manager's logo where services can always fetch it
// (the documentation site): the manager's own address may be private or
// behind a sign-in.
const LogoURL = "https://docs.neureka.dev/docker-manager/logo-512.png"

// Discord's limits (characters).
const (
	discordTitleMax       = 256
	discordDescriptionMax = 4096
	discordFieldsMax      = 25
	discordFieldNameMax   = 256
	discordFieldValueMax  = 1024
	discordFooterMax      = 2048
	discordAuthorMax      = 256
	// discordEmbedMax bounds title, description, author, footer and every
	// field's name and value together.
	discordEmbedMax = 6000
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
// initialized Shoutrrr service, q the address's query options).
func render(service string, svc types.Service, msg domain.NotificationMessage, q url.Values) rendered {
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
			r.body = discordPayload(msg, d.Config.Username, d.Config.Avatar)
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

// markup is how a service writes text, links, code and list entries.
type markup struct {
	esc    func(string) string
	link   func(text, url string) string // text already escaped
	code   func(string) string
	bullet string
}

// value is a field's value: its entries one per line, or the value,
// linked when it names a page.
func (m markup) value(f domain.NotificationField) string {
	if len(f.Items) > 0 {
		lines := make([]string, len(f.Items))
		for i, it := range f.Items {
			lines[i] = m.bullet + m.item(it)
		}
		return strings.Join(lines, "\n")
	}
	v := m.esc(f.Value)
	if f.Link != "" {
		return m.link(v, f.Link)
	}
	return v
}

// item is a list entry: its name (linked when it names a page) and its
// change as code ("web `1a2b` → `3c4d`").
func (m markup) item(it domain.NotificationItem) string {
	s := m.esc(it.Text)
	if it.Link != "" {
		s = m.link(s, it.Link)
	}
	switch {
	case it.From != "" && it.To != "":
		s += " " + m.code(it.From) + " → " + m.code(it.To)
	case it.To != "":
		s += " " + m.code(it.To)
	}
	return s
}

// markdownEscaper escapes what Markdown (and Discord) would format in a
// value.
var markdownEscaper = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "~", `\~`,
	"|", `\|`)

var mdMarkup = markup{
	esc:    markdownEscaper.Replace,
	link:   func(text, u string) string { return "[" + text + "](" + u + ")" },
	code:   func(s string) string { return "`" + s + "`" },
	bullet: "- ",
}

// plainText is the body as text: the status line, the description, a
// line per field (a list's entries below its name) and the link.
func plainText(msg domain.NotificationMessage) string {
	var parts []string
	if msg.Label != "" {
		parts = append(parts, msg.Label)
	}
	if body := strings.TrimSpace(msg.Body); body != "" {
		parts = append(parts, body)
	}
	if len(msg.Fields) > 0 {
		lines := make([]string, 0, len(msg.Fields))
		for _, f := range msg.Fields {
			if len(f.Items) == 0 {
				lines = append(lines, f.Name+": "+f.Value)
				continue
			}
			lines = append(lines, f.Name+":")
			for _, it := range f.Items {
				lines = append(lines, "- "+it.Plain())
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if msg.URL != "" {
		parts = append(parts, msg.URL)
	}
	return strings.Join(parts, "\n\n")
}

// markdownText is the body as Markdown: the status line in italics,
// fields as bold labels (a list below its label), the link as "Open in
// Docker Manager". sep separates the fields.
func markdownText(msg domain.NotificationMessage, sep string) string {
	var parts []string
	if msg.Label != "" {
		parts = append(parts, "_"+markdownEscaper.Replace(msg.Label)+"_")
	}
	if body := strings.TrimSpace(msg.Body); body != "" {
		parts = append(parts, markdownEscaper.Replace(body))
	}
	if len(msg.Fields) > 0 {
		lines := make([]string, 0, len(msg.Fields))
		for _, f := range msg.Fields {
			name := "**" + markdownEscaper.Replace(f.Name) + ":**"
			if len(f.Items) > 0 {
				lines = append(lines, name+"\n"+mdMarkup.value(f))
			} else {
				lines = append(lines, name+" "+mdMarkup.value(f))
			}
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
	Author      *discordEmbedAuthor `json:"author,omitempty"`
	Title       string              `json:"title,omitempty"`
	URL         string              `json:"url,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       uint                `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
	Footer      *discordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordEmbedAuthor struct {
	Name string `json:"name"`
}

type discordEmbedFooter struct {
	Text    string `json:"text"`
	IconURL string `json:"icon_url,omitempty"`
}

type discordPayloadJSON struct {
	// Username and AvatarURL come from the address only: otherwise the
	// webhook's own name and avatar stay.
	Username  string         `json:"username,omitempty"`
	AvatarURL string         `json:"avatar_url,omitempty"`
	Embeds    []discordEmbed `json:"embeds"`
	// AllowedMentions stops names in a message from pinging anyone.
	AllowedMentions map[string][]string `json:"allowed_mentions"`
}

// discordValue is a field's value within Discord's limit: a list is cut
// after a whole entry ("…and 3 more"), and a linked value too long to
// keep its link is shown plain.
func discordValue(f domain.NotificationField) string {
	if len(f.Items) == 0 {
		v := mdMarkup.value(f)
		if utf8.RuneCountInString(v) > discordFieldValueMax {
			v = markdownEscaper.Replace(f.Value)
		}
		return clip(v, discordFieldValueMax)
	}
	more := func(n int) string { return fmt.Sprintf("%s…and %d more", mdMarkup.bullet, n) }
	var lines []string
	used := 0
	for i, it := range f.Items {
		l := mdMarkup.bullet + mdMarkup.item(it)
		size := used + utf8.RuneCountInString(l)
		room := discordFieldValueMax
		if rest := len(f.Items) - i - 1; rest > 0 {
			room -= utf8.RuneCountInString(more(rest)) + 1
		}
		if size > room {
			lines = append(lines, more(len(f.Items)-i))
			break
		}
		lines = append(lines, l)
		used = size + 1
	}
	return clip(strings.Join(lines, "\n"), discordFieldValueMax)
}

// discordPayload is one embed: the status line as its author, the tone's
// color, the title linking to the page, the description, the fields, the
// footer beside the logo and the time. The whole embed stays within
// Discord's total (discordEmbedMax): a field that would pass it is shown
// plain (without links and code), and fields that still don't fit are
// left out (Discord refuses the whole message otherwise, at every retry).
func discordPayload(msg domain.NotificationMessage, username, avatar string) string {
	runes := utf8.RuneCountInString
	e := discordEmbed{
		Title: clip(msg.Title, discordTitleMax), URL: msg.URL, Color: toneColor(msg.Tone),
	}
	total := runes(e.Title)
	if msg.Label != "" {
		e.Author = &discordEmbedAuthor{Name: clip(msg.Label, discordAuthorMax)}
		total += runes(e.Author.Name)
	}
	if msg.Footer != "" {
		e.Footer = &discordEmbedFooter{Text: clip(msg.Footer, discordFooterMax), IconURL: LogoURL}
		total += runes(e.Footer.Text)
	}
	desc := markdownEscaper.Replace(strings.TrimSpace(msg.Body))
	if msg.URL != "" {
		if desc != "" {
			desc += "\n\n"
		}
		desc += "[" + openLabel + "](" + msg.URL + ")"
	}
	if desc != "" {
		e.Description = clip(desc, max(min(discordDescriptionMax, discordEmbedMax-total), 1))
		total += runes(e.Description)
	}
	for i, f := range msg.Fields {
		if i == discordFieldsMax {
			break
		}
		name, value := clip(f.Name, discordFieldNameMax), discordValue(f)
		if total+runes(name)+runes(value) > discordEmbedMax {
			value = clip(markdownEscaper.Replace(f.Value), discordFieldValueMax)
		}
		if total+runes(name)+runes(value) > discordEmbedMax {
			break
		}
		total += runes(name) + runes(value)
		e.Fields = append(e.Fields, discordEmbedField{Name: name, Value: value, Inline: f.Inline})
	}
	if !msg.Time.IsZero() {
		e.Timestamp = msg.Time.UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(discordPayloadJSON{Username: username, AvatarURL: avatar, Embeds: []discordEmbed{e},
		AllowedMentions: map[string][]string{"parse": {}}})
	return string(b)
}

// slackEscaper escapes Slack's control characters.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var slackMarkup = markup{
	esc:    slackEscaper.Replace,
	link:   func(text, u string) string { return "<" + u + "|" + text + ">" },
	code:   func(s string) string { return "`" + s + "`" },
	bullet: "• ",
}

// slackText is one line per attachment (Slack's service sends each line
// as an attachment in the tone's color): the status line, the
// description, bold labeled fields (a list's entries on lines of their
// own) and the link. Empty lines are left out.
func slackText(msg domain.NotificationMessage) string {
	var lines []string
	if msg.Label != "" {
		lines = append(lines, "_"+slackEscaper.Replace(msg.Label)+"_")
	}
	for l := range strings.SplitSeq(strings.TrimSpace(msg.Body), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, slackEscaper.Replace(l))
		}
	}
	for _, f := range msg.Fields {
		name := "*" + slackEscaper.Replace(f.Name) + ":*"
		if len(f.Items) > 0 {
			lines = append(lines, name, slackMarkup.value(f))
		} else {
			lines = append(lines, name+" "+slackMarkup.value(f))
		}
	}
	if msg.URL != "" {
		lines = append(lines, "<"+msg.URL+"|"+openLabel+">")
	}
	return strings.Join(lines, "\n")
}

var telegramMarkup = markup{
	esc:    html.EscapeString,
	link:   func(text, u string) string { return `<a href="` + html.EscapeString(u) + `">` + text + `</a>` },
	code:   func(s string) string { return "<code>" + html.EscapeString(s) + "</code>" },
	bullet: "• ",
}

// telegramHTML is the body in Telegram's HTML (the service adds the
// title in bold).
func telegramHTML(msg domain.NotificationMessage) string {
	var parts []string
	if msg.Label != "" {
		parts = append(parts, "<i>"+html.EscapeString(msg.Label)+"</i>")
	}
	if body := strings.TrimSpace(msg.Body); body != "" {
		parts = append(parts, html.EscapeString(body))
	}
	if len(msg.Fields) > 0 {
		lines := make([]string, 0, len(msg.Fields))
		for _, f := range msg.Fields {
			name := "<b>" + html.EscapeString(f.Name) + ":</b>"
			if len(f.Items) > 0 {
				lines = append(lines, name+"\n"+telegramMarkup.value(f))
			} else {
				lines = append(lines, name+" "+telegramMarkup.value(f))
			}
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if msg.URL != "" {
		parts = append(parts, `<a href="`+html.EscapeString(msg.URL)+`">`+openLabel+`</a>`)
	}
	return strings.Join(parts, "\n\n")
}

var emailMarkup = markup{
	esc: html.EscapeString,
	link: func(text, u string) string {
		return `<a href="` + html.EscapeString(u) + `" style="color:#58a6ff;text-decoration:none;">` + text + `</a>`
	},
	code: func(s string) string {
		return `<code style="font-family:SFMono-Regular,Consolas,Menlo,monospace;font-size:12px;background:#0d1117;` +
			`border:1px solid #262f3d;border-radius:4px;padding:1px 4px;">` + html.EscapeString(s) + `</code>`
	},
	bullet: "• ",
}

// emailHTML is a small card: a colored top bar with the status line (the
// tone's word without one), the title, the description, a table of
// fields, a button to the page and the footer. Inline styles only (mail
// programs drop style sheets).
func emailHTML(msg domain.NotificationMessage) string {
	color := toneHex(msg.Tone)
	esc := html.EscapeString
	label := msg.Label
	if label == "" {
		label = toneWords[msg.Tone]
	}
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="margin:0;padding:24px;background:#0d1117;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="max-width:600px;margin:0 auto;background:#161b22;border:1px solid #262f3d;border-radius:12px;border-top:4px solid ` + color + `;">`)
	b.WriteString(`<tr><td style="padding:20px 24px 4px;color:` + color + `;font-size:12px;font-weight:600;">` + esc(label) + `</td></tr>`)
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
				strings.ReplaceAll(emailMarkup.value(f), "\n", "<br>") + `</td></tr>`)
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
