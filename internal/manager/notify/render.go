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

// toneWords name the tone in plain text (an email's badge and inbox
// preview when the message has no status line).
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
		// The subject is the title after the message's tag in brackets
		// (its environment, or Test), the sender's name ours unless the address names one
		// (a subject in the address wins as well); the HTML part replaces
		// the plain one where the mail program can show it.
		delete(r.params, "title")
		if !userSet(q, "subject") {
			set("title", emailSubject(msg))
		}
		set("fromname", EmailFromName)
		if !userSet(q, "usehtml") {
			if t, ok := svc.(interface{ SetTemplateString(id, body string) error }); ok &&
				t.SetTemplateString(emailHTMLTemplate, templateLiteral(emailHTML(msg))) == nil {
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

// EmailFromName is the sender's name of an email whose address names
// none (the web's EMAIL_FROM_NAME).
const EmailFromName = "Docker Manager"

// emailSubject is the title, after the message's tag in brackets when
// it has one ("[homelab] Disk /dev/sda is failing").
func emailSubject(msg domain.NotificationMessage) string {
	if msg.Tag == "" {
		return msg.Title
	}
	return "[" + msg.Tag + "] " + msg.Title
}

// emailHTMLTemplate is the template ID Shoutrrr's SMTP service writes
// the HTML part with (its templateHTML, matched case-sensitively; without
// it the plain message goes in the HTML part).
const emailHTMLTemplate = "HTML"

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

// discordPlainMarkup is Discord's text without links and code, for a
// field the embed's total has no room for with them.
var discordPlainMarkup = markup{
	esc:    markdownEscaper.Replace,
	link:   func(text, _ string) string { return text },
	code:   func(s string) string { return s },
	bullet: "- ",
}

// discordValue is a field's value in markup m within limit characters: a
// list is cut after a whole entry ("…and 3 more"), and a linked value
// too long to keep its link is shown plain.
func discordValue(f domain.NotificationField, m markup, limit int) string {
	if len(f.Items) == 0 {
		v := m.value(f)
		if utf8.RuneCountInString(v) > limit {
			v = m.esc(f.Value)
		}
		return clip(v, limit)
	}
	more := func(n int) string { return fmt.Sprintf("%s…and %d more", m.bullet, n) }
	var lines []string
	used := 0
	for i, it := range f.Items {
		l := m.bullet + m.item(it)
		size := used + utf8.RuneCountInString(l)
		room := limit
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
	return clip(strings.Join(lines, "\n"), limit)
}

// discordPayload is one embed: the status line as its author, the tone's
// color, the title linking to the page, the description, the fields, the
// footer beside the logo and the time. The whole embed stays within
// Discord's total (discordEmbedMax): a field that would pass it is shown
// plain (without links and code) within the room left, a list cut after
// a whole entry; a field without any room is left out, later ones may
// still fit (Discord refuses the whole message otherwise, at every
// retry).
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
		name := clip(f.Name, discordFieldNameMax)
		room := min(discordFieldValueMax, discordEmbedMax-total-runes(name))
		if room < 1 {
			continue // a shorter field after it may still fit
		}
		value := discordValue(f, mdMarkup, discordFieldValueMax)
		if runes(value) > room {
			value = discordValue(f, discordPlainMarkup, room)
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

// Email colors: the web's design tokens (web/src/lib/design/tokens.css),
// copied because mail programs know no custom properties
// (TestEmailColorsAreTheAppsTokens keeps them equal). Emails are dark like
// the app and declare it (color-scheme), which most mail programs follow;
// some (Gmail's apps, Outlook.com) recolor anyway.
const (
	emailCanvas     = "#0a0f15" // --surface-canvas
	emailPanel      = "#121a24" // --surface-panel
	emailBorder     = "#1f2a38" // --border-subtle
	emailCodeBg     = "#0f161f" // --code-bg
	emailCodeBorder = "#2b3747" // --border-strong
	emailStrong     = "#f2f4f7" // --text-strong
	emailText       = "#c8d3e2" // --text-default
	emailMuted      = "#8392a8" // --text-muted
	emailLink       = "#52a3f7" // --accent-text
	emailOnAccent   = "#ffffff" // --text-on-accent

	emailFont = `Inter,-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`
	emailMono = `'JetBrains Mono',SFMono-Regular,Consolas,Menlo,monospace`
)

// EmailLogoURL is the logo emails show at 32 px: the documentation site's
// 192 px one (LogoURL's reasons, a smaller file).
const EmailLogoURL = "https://docs.neureka.dev/docker-manager/logo.png"

// emailBadges are the app's status badge (Badge.svelte) background and
// border per tone: --danger-soft and --danger-border and so on, the
// accent badge for information (its border is the accent at 40 % over
// --accent-soft).
var emailBadges = map[domain.NotificationTone]struct{ bg, border string }{
	domain.ToneCritical: {"#3f2029", "#5a2a33"},
	domain.ToneWarning:  {"#33280f", "#4d3c14"},
	domain.ToneSuccess:  {"#0f2a1f", "#1d4a33"},
	domain.ToneInfo:     {"#112745", "#19408f"},
}

// emailBadge is the badge of tone t: its text and dot (the tone's color,
// the accent's text color for information), background and border.
func emailBadge(t domain.NotificationTone) (fg, bg, border string) {
	b, ok := emailBadges[t]
	if !ok || t == domain.ToneInfo {
		info := emailBadges[domain.ToneInfo]
		return emailLink, info.bg, info.border
	}
	return toneHex(t), b.bg, b.border
}

var emailMarkup = markup{
	esc: html.EscapeString,
	link: func(text, u string) string {
		return `<a href="` + html.EscapeString(u) + `" style="color:` + emailLink + `;text-decoration:none;">` + text + `</a>`
	},
	code: func(s string) string {
		return `<code style="font-family:` + emailMono + `;font-size:12px;color:` + emailStrong + `;background:` + emailCodeBg +
			`;border:1px solid ` + emailCodeBorder + `;border-radius:4px;padding:1px 4px;">` + html.EscapeString(s) + `</code>`
	},
	bullet: "• ",
}

// emailPreheader is the inbox preview line: the description (the status
// line without one), followed by blank space so mail programs do not
// fill the preview with the header.
func emailPreheader(msg domain.NotificationMessage, label string) string {
	text := strings.Join(strings.Fields(msg.Body), " ")
	if text == "" {
		text = label
	}
	return html.EscapeString(clip(text, 140)) + strings.Repeat("&#847;&zwnj;&nbsp; ", 40)
}

// emailTable opens a layout table (attrs extra attributes).
func emailTable(attrs string) string {
	return `<table role="presentation" cellspacing="0" cellpadding="0" border="0"` + attrs + `>`
}

// emailCell opens a cell with style; every cell names the font itself
// (Outlook does not pass it on into tables).
func emailCell(style string) string {
	return `<td style="font-family:` + emailFont + `;` + style + `">`
}

// emailHTML is every email's one layout, dark like the app: the logo and
// name above a card with the tone's strip, the status line as the app's
// badge (the tone's word without one), the title, the description, a
// table of fields, a button to the page, then the footer (the instance)
// and the time, either alone when the other is missing. Tables, cell
// padding and inline styles only (mail programs drop style sheets, and
// Outlook margins and max-width, which a fixed-width table stands in for).
func emailHTML(msg domain.NotificationMessage) string {
	esc := html.EscapeString
	label := msg.Label
	if label == "" {
		label = toneWords[msg.Tone]
	}
	fg, bg, border := emailBadge(msg.Tone)
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en" style="color-scheme:dark;"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta name="color-scheme" content="dark"><meta name="supported-color-schemes" content="dark"></head>`)
	b.WriteString(`<body bgcolor="` + emailCanvas + `" style="margin:0;padding:0;background:` + emailCanvas + `;color-scheme:dark;">`)
	b.WriteString(`<div style="display:none;max-height:0;overflow:hidden;opacity:0;mso-hide:all;">` + emailPreheader(msg, label) + `</div>`)
	b.WriteString(emailTable(` width="100%" bgcolor="`+emailCanvas+`" style="background:`+emailCanvas+`;"`) +
		`<tr><td align="center" style="padding:24px 12px;">` +
		`<!--[if mso]>` + emailTable(` width="600" align="center"`) + `<tr><td><![endif]-->` +
		emailTable(` width="100%" style="max-width:600px;"`))

	// The logo lockup, as in the sidebar.
	b.WriteString(`<tr>` + emailCell("padding:0 4px 16px;") + emailTable("") + `<tr>` +
		emailCell("padding-right:10px;vertical-align:middle;") + `<img src="` + EmailLogoURL +
		`" width="32" height="32" alt="" style="display:block;border:0;outline:none;"></td>` +
		emailCell("vertical-align:middle;color:"+emailStrong+";font-size:16px;line-height:24px;font-weight:600;letter-spacing:-0.01em;") +
		EmailFromName + `</td></tr></table></td></tr>`)

	// The card: one row per part, spaced by cell padding.
	b.WriteString(`<tr><td bgcolor="` + emailPanel + `" style="background:` + emailPanel + `;border:1px solid ` + emailBorder +
		`;border-top:4px solid ` + toneHex(msg.Tone) + `;border-radius:12px;padding:24px;">` + emailTable(` width="100%"`))
	b.WriteString(`<tr><td>` + emailTable("") + `<tr>` +
		emailCell("background:"+bg+";border:1px solid "+border+";border-radius:6px;padding:3px 8px;color:"+fg+
			";font-size:12px;line-height:16px;font-weight:500;white-space:nowrap;") +
		`<span style="font-size:10px;">&#9679;</span>&nbsp; ` + esc(label) + `</td></tr></table></td></tr>`)
	b.WriteString(`<tr>` + emailCell("padding-top:16px;color:"+emailStrong+";font-size:20px;line-height:28px;font-weight:600;") +
		esc(msg.Title) + `</td></tr>`)
	if body := strings.TrimSpace(msg.Body); body != "" {
		b.WriteString(`<tr>` + emailCell("padding-top:8px;color:"+emailText+";font-size:14px;line-height:20px;") +
			strings.ReplaceAll(esc(body), "\n", "<br>") + `</td></tr>`)
	}
	if len(msg.Fields) > 0 {
		b.WriteString(`<tr><td style="padding-top:20px;">` + emailTable(` width="100%"`))
		for _, f := range msg.Fields {
			b.WriteString(`<tr>` + emailCell("width:35%;padding:8px 16px 8px 0;color:"+emailMuted+
				";font-size:13px;line-height:20px;vertical-align:top;border-top:1px solid "+emailBorder+";") + esc(f.Name) + `</td>` +
				emailCell("padding:8px 0;color:"+emailStrong+";font-size:14px;line-height:20px;vertical-align:top;border-top:1px solid "+
					emailBorder+";") + strings.ReplaceAll(emailMarkup.value(f), "\n", "<br>") + `</td></tr>`)
		}
		b.WriteString(`</table></td></tr>`)
	}
	if msg.URL != "" {
		accent := toneHex(domain.ToneInfo)
		b.WriteString(`<tr><td style="padding-top:24px;">` + emailTable("") + `<tr><td bgcolor="` + accent +
			`" style="background:` + accent + `;border-radius:8px;"><a href="` + esc(msg.URL) +
			`" style="display:inline-block;padding:10px 16px;font-family:` + emailFont + `;color:` + emailOnAccent +
			`;font-size:14px;line-height:20px;font-weight:600;text-decoration:none;border-radius:8px;">` + openLabel +
			`</a></td></tr></table></td></tr>`)
	}
	b.WriteString(`</table></td></tr>`)

	// The footer: the instance and the time.
	var foot []string
	if msg.Footer != "" {
		foot = append(foot, esc(msg.Footer))
	}
	if !msg.Time.IsZero() {
		foot = append(foot, esc(msg.Time.UTC().Format("Jan 2, 2006, 15:04"))+" UTC")
	}
	if len(foot) > 0 {
		b.WriteString(`<tr>` + emailCell("padding:16px 4px 0;color:"+emailMuted+";font-size:12px;line-height:16px;") +
			strings.Join(foot, " · ") + `</td></tr>`)
	}
	b.WriteString(`</table><!--[if mso]></td></tr></table><![endif]--></td></tr></table></body></html>`)
	return emailLines(b.String())
}

// Email line lengths: SMTP allows 998 octets a line and Shoutrrr sends the
// HTML part as it is (8 bit), so emailLines breaks a line before a tag once
// it passes emailLineSoft or when the tag would not fit within
// emailLineMax (it starts a line then); past emailLineHard at a space, in a text or
// between a tag's attributes (never inside a quoted value); and past
// emailLineMax anywhere in a text but inside a character or an entity.
const (
	emailLineSoft = 500
	emailLineHard = 900
	emailLineMax  = 990
)

func emailLines(s string) string {
	var b strings.Builder
	n := 0
	inTag, inEntity := false, false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		brk := false
		switch {
		case inTag:
			switch {
			case quote != 0:
				if c == quote {
					quote = 0
				}
			case c == '"' || c == '\'':
				quote = c
			case c == '>':
				inTag = false
			case c == ' ' && n >= emailLineHard:
				brk = true
			}
		case c == '<':
			inTag, inEntity = true, false
			if n > 0 && (n >= emailLineSoft || n+tagLen(s, i) > emailLineMax) {
				b.WriteByte('\n')
				n = 0
			}
		case c == ' ' && n >= emailLineHard:
			brk, inEntity = true, false
		default:
			// A character starts at an ASCII or a lead byte; an entity runs
			// from & to ;.
			if n >= emailLineMax && !inEntity && (c < 0x80 || c >= 0xc0) {
				b.WriteByte('\n')
				n = 0
			}
			switch {
			case c == '&':
				inEntity = true
			case inEntity && (c == ';' || !entityByte(c)):
				inEntity = false
			}
		}
		if brk {
			// The space becomes the line break.
			b.WriteByte('\n')
			n = 0
			continue
		}
		b.WriteByte(c)
		n++
	}
	return b.String()
}

// entityByte reports whether c can be inside an entity's name or number
// ("nbsp", "#9679").
func entityByte(c byte) bool {
	return c == '#' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// tagLen is the length of the tag starting at s[i], to its '>' outside
// quoted values.
func tagLen(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j - i + 1
		}
	}
	return len(s) - i
}
