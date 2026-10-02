package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"
	"unicode/utf8"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/neurekadev/docker-manager/internal/domain"
)

func sample() domain.NotificationMessage {
	return domain.NotificationMessage{
		Label: "Image Updates · Applied", Title: "Update of Paperless succeeded",
		Body: "Recreated silo_data & web with the new image.",
		URL:  "https://docker.example.com/jobs/j1", Tone: domain.ToneSuccess, Footer: "Home",
		Time: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
		Fields: []domain.NotificationField{
			{Name: "Environment", Value: "homelab", Inline: true, Link: "https://docker.example.com/environments/e1"},
			{Name: "Reclaimed", Value: "4.2 GiB", Inline: true},
			{Name: "Updated", Value: "web <x>: 1a2b → 3c4d\nworker", Items: []domain.NotificationItem{
				{Text: "web <x>", Link: "https://docker.example.com/stacks/s1/logs?service=web", From: "1a2b", To: "3c4d"},
				{Text: "worker"},
			}},
		},
	}
}

// initialized locates the Shoutrrr service of address without network
// access (like address validation).
func initialized(t *testing.T, address string) types.Service {
	t.Helper()
	svc, err := locate(address, newWire(time.Second, true))
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	return svc
}

// discordJSON is the part of Discord's webhook JSON the tests read.
type discordJSON struct {
	Username        string              `json:"username"`
	AvatarURL       string              `json:"avatar_url"`
	AllowedMentions map[string][]string `json:"allowed_mentions"`
	Embeds          []struct {
		Author *struct {
			Name string `json:"name"`
		} `json:"author"`
		Title, URL, Description, Timestamp string
		Color                              uint
		Fields                             []struct {
			Name, Value string
			Inline      bool
		}
		Footer struct {
			Text    string `json:"text"`
			IconURL string `json:"icon_url"`
		}
	}
}

func TestDiscordGetsAnEmbedInTheTonesColor(t *testing.T) {
	svc := initialized(t, "discord://token@123456789")
	r := render("discord", svc, sample(), url.Values{})
	if len(r.params) != 0 {
		t.Fatalf("params %v", r.params)
	}
	var p discordJSON
	if err := json.Unmarshal([]byte(r.body), &p); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	// The webhook's own name and avatar stay.
	if len(p.Embeds) != 1 || p.Username != "" || p.AvatarURL != "" || p.AllowedMentions == nil || strings.Contains(r.body, `"username"`) {
		t.Fatalf("%+v", p)
	}
	e := p.Embeds[0]
	// The title is no link: "Open in Docker Manager" is the last line,
	// after every field.
	if e.Color != 0x4cf683 || e.Author == nil || e.Author.Name != "Image Updates · Applied" || e.Title != "Update of Paperless succeeded" ||
		e.URL != "" || strings.Contains(r.body, `"url"`) ||
		e.Description != "Recreated silo\\_data & web with the new image." ||
		e.Timestamp != "2026-10-01T09:30:00Z" || e.Footer.Text != "Home" || e.Footer.IconURL != LogoURL || len(e.Fields) != 4 {
		t.Fatalf("%+v", e)
	}
	if f := e.Fields[3]; f.Inline || f.Name != "\u200b" || f.Value != "[Open in Docker Manager](https://docker.example.com/jobs/j1)" {
		t.Fatalf("%+v", f)
	}
	// The environment links to its page; the list is bulleted, each entry
	// linked, its digests as code.
	if f := e.Fields[0]; !f.Inline || f.Value != "[homelab](https://docker.example.com/environments/e1)" {
		t.Fatalf("%+v", f)
	}
	if f := e.Fields[2]; f.Inline ||
		f.Value != "- [web <x>](https://docker.example.com/stacks/s1/logs?service=web) `1a2b` → `3c4d`\n- worker" {
		t.Fatalf("%q", f.Value)
	}
	// Failures are red, warnings amber, news blue.
	for tone, want := range map[domain.NotificationTone]uint{domain.ToneCritical: 0xfd6b66, domain.ToneWarning: 0xf5b544, domain.ToneInfo: 0x2566fd} {
		if toneColor(tone) != want {
			t.Errorf("%s: %x", tone, toneColor(tone))
		}
	}
}

func TestDiscordKeepsTheAddressesNameAndAvatar(t *testing.T) {
	svc := initialized(t, "discord://token@123456789?username=Ops&avatar=https%3A%2F%2Fimg.example.com%2Fa.png")
	var p discordJSON
	if err := json.Unmarshal([]byte(render("discord", svc, sample(), url.Values{}).body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Username != "Ops" || p.AvatarURL != "https://img.example.com/a.png" {
		t.Fatalf("%+v", p)
	}
}

// embedSize counts what Discord's total limit counts.
func embedSize(p discordJSON) int {
	e := p.Embeds[0]
	n := len([]rune(e.Title)) + len([]rune(e.Description)) + len([]rune(e.Footer.Text))
	if e.Author != nil {
		n += len([]rune(e.Author.Name))
	}
	for _, f := range e.Fields {
		n += len([]rune(f.Name)) + len([]rune(f.Value))
	}
	return n
}

func TestDiscordLimitsAreKept(t *testing.T) {
	msg := sample()
	msg.Title = strings.Repeat("t", 300)
	for range 30 {
		msg.Fields = append(msg.Fields, domain.NotificationField{Name: "n", Value: "v"})
	}
	var p discordJSON
	if err := json.Unmarshal([]byte(discordPayload(msg, "", "")), &p); err != nil {
		t.Fatal(err)
	}
	// The link keeps its place: the last of the fields Discord allows.
	if e := p.Embeds[0]; len([]rune(e.Title)) != discordTitleMax || len(e.Fields) != discordFieldsMax ||
		!strings.HasPrefix(e.Fields[discordFieldsMax-1].Value, "[Open in Docker Manager](") {
		t.Fatalf("title %d, fields %d", len([]rune(e.Title)), len(e.Fields))
	}
	// Long fields: each within its limit, the embed within the total.
	msg.Fields = msg.Fields[:3]
	for range 10 {
		msg.Fields = append(msg.Fields, domain.NotificationField{Name: "n", Value: strings.Repeat("v", 2000)})
	}
	msg.Body = strings.Repeat("b", 5000)
	p = discordJSON{}
	if err := json.Unmarshal([]byte(discordPayload(msg, "", "")), &p); err != nil {
		t.Fatal(err)
	}
	e := p.Embeds[0]
	if embedSize(p) > discordEmbedMax || len([]rune(e.Description)) > discordDescriptionMax || len(e.Fields) == 0 {
		t.Fatalf("embed %d, description %d, fields %d", embedSize(p), len([]rune(e.Description)), len(e.Fields))
	}
	// The link is never crowded out.
	if last := e.Fields[len(e.Fields)-1]; last.Value != "[Open in Docker Manager](https://docker.example.com/jobs/j1)" {
		t.Fatalf("last field %+v", last)
	}
	for _, f := range e.Fields {
		if len([]rune(f.Value)) > discordFieldValueMax {
			t.Fatalf("field %d", len([]rune(f.Value)))
		}
	}
}

func TestDiscordShowsAListPlainWhenItsLinksWouldPassTheTotal(t *testing.T) {
	msg := sample()
	msg.Body = strings.Repeat("b", 4000)
	list := domain.NotificationField{Name: "Updated"}
	var plain []string
	for i := range 10 {
		name := fmt.Sprintf("service-%02d", i)
		list.Items = append(list.Items, domain.NotificationItem{Text: name,
			Link: "https://docker.example.com/stacks/s1/logs?service=" + name, From: "0123456789ab", To: "ba9876543210"})
		plain = append(plain, list.Items[i].Plain())
	}
	list.Value = strings.Join(plain, "\n")
	// Four lists, then a short field.
	msg.Fields = []domain.NotificationField{list, list, list, list, {Name: "Do", Value: "x"}}
	var p discordJSON
	if err := json.Unmarshal([]byte(discordPayload(msg, "", "")), &p); err != nil {
		t.Fatal(err)
	}
	e := p.Embeds[0]
	if embedSize(p) > discordEmbedMax || len(e.Fields) != 6 || !strings.Contains(e.Fields[0].Value, "](https://") ||
		strings.Contains(e.Fields[1].Value, "](https://") || !strings.Contains(e.Fields[1].Value, "- service-09 0123456789ab → ba9876543210") {
		t.Fatalf("embed %d: %+v", embedSize(p), e.Fields)
	}
	// A list with little room left is cut after a whole entry and says
	// how many are left; the short field after it still fits, and the
	// link comes last.
	cut := false
	for _, f := range e.Fields[:4] {
		for l := range strings.SplitSeq(f.Value, "\n") {
			switch {
			case strings.HasPrefix(l, "- …and "):
				cut = true
			case !strings.HasSuffix(l, "ba9876543210") && !strings.HasSuffix(l, "ba9876543210`"):
				t.Fatalf("cut entry %q", l)
			}
		}
	}
	if !cut || e.Fields[4].Name != "Do" || e.Fields[4].Value != "x" || !strings.HasPrefix(e.Fields[5].Value, "[Open in Docker Manager](") {
		t.Fatalf("%+v", e.Fields)
	}
}

func TestDiscordCutsALongListAfterAWholeEntry(t *testing.T) {
	f := domain.NotificationField{Name: "Updated"}
	for i := range 40 {
		f.Items = append(f.Items, domain.NotificationItem{Text: fmt.Sprintf("service-%02d", i),
			Link: fmt.Sprintf("https://docker.example.com/stacks/s1/logs?service=service-%02d", i), From: "0123456789ab", To: "ba9876543210"})
	}
	v := discordValue(f, mdMarkup, discordFieldValueMax)
	lines := strings.Split(v, "\n")
	last := lines[len(lines)-1]
	if len([]rune(v)) > discordFieldValueMax || last != fmt.Sprintf("- …and %d more", 40-(len(lines)-1)) {
		t.Fatalf("%d runes, last %q", len([]rune(v)), last)
	}
	for _, l := range lines[:len(lines)-1] {
		if !strings.HasSuffix(l, "`0123456789ab` → `ba9876543210`") {
			t.Fatalf("cut entry %q", l)
		}
	}
	// A linked value too long for its link is shown plain.
	long := domain.NotificationField{Name: "Target", Value: strings.Repeat("a", 1000), Link: "https://docker.example.com/" + strings.Repeat("b", 100)}
	if got := discordValue(long, mdMarkup, discordFieldValueMax); got != long.Value {
		t.Fatalf("%q", got)
	}
}

func TestServicesGetTheirRichestForm(t *testing.T) {
	msg := sample()
	slack := render("slack", nil, msg, url.Values{})
	if slack.params["color"] != "#4cf683" || slack.params["title"] != msg.Title ||
		!strings.HasPrefix(slack.body, "_Image Updates · Applied_\n") ||
		!strings.Contains(slack.body, "*Environment:* <https://docker.example.com/environments/e1|homelab>") ||
		!strings.Contains(slack.body, "*Updated:*\n• <https://docker.example.com/stacks/s1/logs?service=web|web &lt;x&gt;> `1a2b` → `3c4d`\n• worker") ||
		!strings.Contains(slack.body, "<https://docker.example.com/jobs/j1|Open in Docker Manager>") || strings.Contains(slack.body, "\n\n") {
		t.Fatalf("%+v", slack)
	}
	tg := render("telegram", nil, msg, url.Values{})
	if tg.params["parsemode"] != "HTML" || !strings.HasPrefix(tg.body, "<i>Image Updates · Applied</i>") ||
		!strings.Contains(tg.body, "<b>Reclaimed:</b> 4.2 GiB") ||
		!strings.Contains(tg.body, `<b>Environment:</b> <a href="https://docker.example.com/environments/e1">homelab</a>`) ||
		!strings.Contains(tg.body, `• <a href="https://docker.example.com/stacks/s1/logs?service=web">web &lt;x&gt;</a> <code>1a2b</code> → <code>3c4d</code>`) ||
		!strings.Contains(tg.body, "silo_data &amp; web") {
		t.Fatalf("%+v", tg)
	}
	ntfy := render("ntfy", nil, msg, url.Values{})
	if ntfy.params["priority"] != "2" || ntfy.params["tags"] != "white_check_mark" || ntfy.params["click"] != msg.URL ||
		ntfy.params["markdown"] != "yes" || !strings.Contains(ntfy.body, "**Reclaimed:** 4.2 GiB") ||
		!strings.Contains(ntfy.body, "**Environment:** [homelab](https://docker.example.com/environments/e1)") ||
		!strings.Contains(ntfy.body, "**Updated:**\n- [web <x>](https://docker.example.com/stacks/s1/logs?service=web) `1a2b` → `3c4d`\n- worker") ||
		!strings.Contains(ntfy.body, `silo\_data`) {
		t.Fatalf("%+v", ntfy)
	}
	gotify := render("gotify", nil, msg, url.Values{})
	if gotify.params["priority"] != "4" || !strings.Contains(gotify.params["extras"], `"contentType":"text/markdown"`) ||
		!strings.Contains(gotify.params["extras"], msg.URL) {
		t.Fatalf("%+v", gotify)
	}
	teams := render("teams", nil, msg, url.Values{})
	if teams.params["color"] != "good" || !strings.Contains(teams.body, "[Open in Docker Manager](https://docker.example.com/jobs/j1)") {
		t.Fatalf("%+v", teams)
	}
	critical := msg
	critical.Tone = domain.ToneCritical
	if po := render("pushover", nil, critical, url.Values{}); po.params["priority"] != "1" {
		t.Fatalf("%+v", po)
	}
	generic := render("generic", nil, msg, url.Values{})
	if generic.params["tone"] != "success" || generic.params["url"] != msg.URL || generic.params["title"] != msg.Title ||
		generic.body != plainText(msg) {
		t.Fatalf("%+v", generic)
	}
	// Anything else: the title and plain text, the status line first, a
	// line per field and a list's entries below its name.
	other := render("bark", nil, msg, url.Values{})
	if len(other.params) != 1 || other.body != "Image Updates · Applied\n\nRecreated silo_data & web with the new image.\n\n"+
		"Environment: homelab\nReclaimed: 4.2 GiB\nUpdated:\n- web <x>: 1a2b → 3c4d\n- worker\n\nhttps://docker.example.com/jobs/j1" {
		t.Fatalf("%q", other.body)
	}
}

func TestTheOwnersOptionsWin(t *testing.T) {
	msg := sample()
	q, _ := url.ParseQuery("Color=%23000000&title=Mine")
	slack := render("slack", nil, msg, q)
	if _, ok := slack.params["color"]; ok {
		t.Fatalf("%+v", slack.params)
	}
	if _, ok := slack.params["title"]; ok {
		t.Fatalf("%+v", slack.params)
	}
	// A parse mode of the owner's: the body stays plain text.
	tg := render("telegram", nil, msg, url.Values{"parsemode": {"Markdown"}})
	if _, ok := tg.params["parsemode"]; ok || tg.body != plainText(msg) {
		t.Fatalf("%+v", tg)
	}
}

func TestEmailIsAnHTMLCardWithAPlainPart(t *testing.T) {
	svc := initialized(t, "smtp://mail.example.com:587/?from=dm@example.com&to=ops@example.com")
	msg := sample()
	msg.Body = "Body with {{ braces }} & <tags>"
	r := render("smtp", svc, msg, url.Values{})
	if r.params["usehtml"] != "yes" || r.params["title"] != msg.Title || r.body != plainText(msg) {
		t.Fatalf("%+v", r)
	}
	tpl, ok := svc.(interface {
		GetTemplate(id string) (*template.Template, bool)
	}).GetTemplate("HTML") // the ID Shoutrrr's SMTP service writes the HTML part with
	if !ok {
		t.Fatal("no HTML template")
	}
	var b strings.Builder
	if err := tpl.Execute(&b, map[string]string{"message": "ignored"}); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	if html != emailHTML(msg) {
		t.Fatalf("%s", html)
	}
	// Checked without the line breaks emailLines adds.
	flat := func(m domain.NotificationMessage) string { return strings.ReplaceAll(emailHTML(m), "\n", "") }
	html = strings.ReplaceAll(html, "\n", "")
	if !strings.Contains(html, "{{ braces }} &amp; &lt;tags&gt;") ||
		!strings.Contains(html, "border-top:4px solid #4cf683") || !strings.Contains(html, `href="https://docker.example.com/jobs/j1"`) ||
		!strings.Contains(html, `href="https://docker.example.com/environments/e1"`) ||
		!strings.Contains(html, ">1a2b</code> → <code") {
		t.Fatalf("%s", html)
	}
	// Branded and dark like the app: the logo and name, the app's canvas
	// and panel, a dark color scheme, the accent button; an Outlook-only
	// fixed width; no title of ours (the address may set the subject).
	for _, want := range []string{
		`<img src="` + EmailLogoURL + `"`, ";letter-spacing:-0.01em;\">Docker Manager</td>",
		`<meta name="color-scheme" content="dark">`, "background:#0a0f15", "background:#121a24",
		"&nbsp; Image Updates · Applied</td>", "background:#2566fd", `<!--[if mso]><table role="presentation" cellspacing="0" cellpadding="0" border="0" width="600"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q in %s", want, html)
		}
	}
	if strings.Contains(html, "<title>") {
		t.Fatalf("%s", html)
	}
	// Every cell names the font (Outlook does not pass it on).
	if n, m := strings.Count(html, "<td style=\"font-family:"), strings.Count(html, "<td style="); n < m-4 {
		t.Fatalf("%d of %d cells name the font: %s", n, m, html)
	}
	// The inbox preview is the description, before the header.
	if i, j := strings.Index(html, "Body with {{ braces }}"), strings.Index(html, ">Docker Manager</td>"); i < 0 || i > j {
		t.Fatalf("%s", html)
	}
	// The footer names the instance and the time, either alone without
	// the other.
	if !strings.Contains(html, ">Home · Oct 1, 2026, 09:30 UTC</td>") {
		t.Fatalf("%s", html)
	}
	noFooter := msg
	noFooter.Footer = ""
	if h := flat(noFooter); !strings.Contains(h, `line-height:16px;">Oct 1, 2026, 09:30 UTC</td>`) {
		t.Fatalf("%s", h)
	}
	noFooter.Time = time.Time{}
	if h := flat(noFooter); strings.Contains(h, "UTC</td>") || strings.Contains(h, "padding:16px 4px 0;") {
		t.Fatalf("%s", h)
	}
	// Without a status line the badge names the tone.
	msg.Label = ""
	if !strings.Contains(flat(msg), "&nbsp; OK</td>") {
		t.Fatal("no tone word")
	}
	// Each tone's badge is the app's: the tone's text, soft background and
	// border; information (and an unknown tone) the accent badge.
	badge := func(bg, border, fg string) string {
		return "background:" + bg + ";border:1px solid " + border + ";border-radius:6px;padding:3px 8px;color:" + fg + ";"
	}
	for tone, want := range map[domain.NotificationTone]string{
		domain.ToneCritical: badge("#3f2029", "#5a2a33", "#fd6b66"),
		domain.ToneWarning:  badge("#33280f", "#4d3c14", "#f5b544"),
		domain.ToneSuccess:  badge("#0f2a1f", "#1d4a33", "#4cf683"),
		domain.ToneInfo:     badge("#112745", "#19408f", "#52a3f7"),
		"other":             badge("#112745", "#19408f", "#52a3f7"),
	} {
		msg.Tone = tone
		if h := flat(msg); !strings.Contains(h, want) {
			t.Fatalf("%s: missing %q in %s", tone, want, h)
		}
	}
}

// smtpServer is an SMTP server on loopback that keeps the data of each
// message it receives (no TLS, no sign-in).
func smtpServer(t *testing.T) (addr string, messages <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got := make(chan string, 4)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		reply := func(s string) { _, _ = fmt.Fprintf(c, "%s\r\n", s) }
		reply("220 localhost ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 localhost")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"), cmd == "RSET", cmd == "NOOP":
				reply("250 OK")
			case cmd == "DATA":
				reply("354 Go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(strings.TrimPrefix(l, ".")) // dot stuffing
				}
				got <- b.String()
				reply("250 Queued")
			case cmd == "QUIT":
				reply("221 Bye")
				return
			default:
				reply("502 Not implemented")
			}
		}
	}()
	return l.Addr().String(), got
}

// A real send through Shoutrrr's SMTP service carries the branded card as
// the HTML part (the template ID is Shoutrrr's) beside the plain part, in
// lines a mail server accepts.
func TestEmailArrivesWithTheBrandedCard(t *testing.T) {
	addr, messages := smtpServer(t)
	msg := (&Service{opts: Options{PublicURL: "https://docker.example.com"}}).testMessage(
		domain.NotificationChannel{Name: "Ops"}, time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	msg.Body = strings.Repeat("A long description of what happened. ", 60)
	address := "smtp://" + addr + "/?from=dm@example.com&to=ops@example.com&encryption=None&usestarttls=No"
	if class := deliver(context.Background(), address, msg, 5*time.Second); class != "" {
		t.Fatalf("send: %s", class)
	}
	var data string
	select {
	case data = <-messages:
	default:
		t.Fatal("no message")
	}
	data = strings.ReplaceAll(data, "\r\n", "\n")
	_, html, ok := strings.Cut(data, "Content-Type: text/html; charset=\"UTF-8\"\nContent-Transfer-Encoding: 8bit\n\n")
	if !ok || !strings.Contains(data, "Content-Type: text/plain") || !strings.Contains(data, "Subject: [Test] Docker Manager test message") {
		t.Fatalf("%s", data)
	}
	want := emailHTML(msg)
	if !strings.HasPrefix(html, want) {
		t.Fatalf("the HTML part is not the card:\n%s", html)
	}
	for _, s := range []string{`<img src="` + EmailLogoURL + `"`, "&nbsp; Test Message</td>", `href="https://docker.example.com"`} {
		if !strings.Contains(html, s) {
			t.Fatalf("missing %q in %s", s, html)
		}
	}
	for l := range strings.SplitSeq(html, "\n") {
		if len(l) > 998 {
			t.Fatalf("a line of %d characters", len(l))
		}
	}
}

// emailLines breaks before a tag past the soft length, at a space in a
// long text past the hard one, and changes nothing else.
func TestEmailLinesStayShort(t *testing.T) {
	s := emailHTML(sample())
	for l := range strings.SplitSeq(s, "\n") {
		if len(l) > emailLineHard+200 {
			t.Fatalf("a line of %d characters", len(l))
		}
	}
	if !strings.Contains(s, "\n") {
		t.Fatal("not broken")
	}
	if got := emailLines("<p>short</p>"); got != "<p>short</p>" {
		t.Fatalf("%q", got)
	}
	text := strings.Repeat("word ", 400)
	for l := range strings.SplitSeq(emailLines(text), "\n") {
		if len(l) > emailLineHard+5 {
			t.Fatalf("a line of %d characters", len(l))
		}
	}
	if strings.ReplaceAll(emailLines(text), "\n", " ") != text {
		t.Fatal("text changed")
	}
	// A tag starting just under the soft length: it starts a line when it
	// would not fit, and is broken only between attributes, never inside
	// a quoted value.
	tag := strings.Repeat("x", emailLineSoft-1) + `<td style="font-family:Inter,'Segoe UI';` + strings.Repeat("padding:0 4px; ", 60) + `" ` +
		strings.Repeat(`data-a="b" `, 100) + `>t`
	got := emailLines(tag)
	_, rest, _ := strings.Cut(got, `style="`)
	if value, _, _ := strings.Cut(rest, `"`); strings.Contains(value, "\n") {
		t.Fatalf("broken inside a value: %q", value)
	}
	if !strings.Contains(got, "\n<td") || strings.ReplaceAll(strings.ReplaceAll(got, "\n<", "<"), "\n", " ") != tag {
		t.Fatalf("%q", got)
	}
	for l := range strings.SplitSeq(got, "\n") {
		if len(l) > 998 {
			t.Fatalf("a line of %d octets", len(l))
		}
	}
	// A long text without spaces is cut anyway, never inside a character
	// or an entity.
	for _, text := range []string{strings.Repeat("字", 700), strings.Repeat("&nbsp;", 400), strings.Repeat("a", 2500)} {
		got := emailLines(text)
		if strings.ReplaceAll(got, "\n", "") != text {
			t.Fatalf("changed: %q", got)
		}
		for l := range strings.SplitSeq(got, "\n") {
			if len(l) > 998 || !utf8.ValidString(l) || strings.HasPrefix(text, "&") && (!strings.HasPrefix(l, "&") || !strings.HasSuffix(l, ";")) {
				t.Fatalf("line %q", l)
			}
		}
	}
}

// TestEmailColorsAreTheAppsTokens keeps the email's copied colors equal to
// the web's design tokens.
func TestEmailColorsAreTheAppsTokens(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "lib", "design", "tokens.css"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*--([a-z0-9-]+):\s*(#[0-9a-f]{6});`).FindAllStringSubmatch(string(css), -1) {
		tokens[m[1]] = m[2]
	}
	for token, value := range map[string]string{
		"surface-canvas": emailCanvas, "surface-panel": emailPanel, "border-subtle": emailBorder,
		"code-bg": emailCodeBg, "border-strong": emailCodeBorder, "text-strong": emailStrong,
		"text-default": emailText, "text-muted": emailMuted, "accent-text": emailLink, "text-on-accent": emailOnAccent,
		"danger": toneHex(domain.ToneCritical), "warn": toneHex(domain.ToneWarning), "ok": toneHex(domain.ToneSuccess),
		"accent":      toneHex(domain.ToneInfo),
		"danger-soft": emailBadges[domain.ToneCritical].bg, "danger-border": emailBadges[domain.ToneCritical].border,
		"warn-soft": emailBadges[domain.ToneWarning].bg, "warn-border": emailBadges[domain.ToneWarning].border,
		"ok-soft": emailBadges[domain.ToneSuccess].bg, "ok-border": emailBadges[domain.ToneSuccess].border,
		"accent-soft": emailBadges[domain.ToneInfo].bg,
	} {
		if tokens[token] != value {
			t.Errorf("--%s is %q in tokens.css, %q in the email", token, tokens[token], value)
		}
	}
}

func TestEmailSubjectHasTheTagAndTheSender(t *testing.T) {
	svc := initialized(t, "smtp://mail.example.com:587/?from=dm@example.com&to=ops@example.com")
	msg := sample()
	msg.Tag = "Hyperion"
	r := render("smtp", svc, msg, url.Values{})
	if r.params["title"] != "[Hyperion] Update of Paperless succeeded" || r.params["fromname"] != EmailFromName {
		t.Fatalf("%+v", r.params)
	}
	// Without a tag the subject is the title.
	msg.Tag = ""
	if r := render("smtp", svc, msg, url.Values{}); r.params["title"] != msg.Title || r.params["fromname"] != "Docker Manager" {
		t.Fatalf("%+v", r.params)
	}
	// A sender name or subject in the address wins.
	msg.Tag = "Hyperion"
	r = render("smtp", svc, msg, url.Values{"FromName": {"Ops"}, "subject": {"Alert"}})
	if _, ok := r.params["fromname"]; ok {
		t.Fatalf("%+v", r.params)
	}
	if _, ok := r.params["title"]; ok {
		t.Fatalf("%+v", r.params)
	}
	// A test message's subject reads "[Test] Docker Manager test message".
	test := (&Service{}).testMessage(domain.NotificationChannel{Name: "Ops"}, time.Now())
	if r := render("smtp", svc, test, url.Values{}); r.params["title"] != "[Test] Docker Manager test message" {
		t.Fatalf("%+v", r.params)
	}
	// Other services keep the plain title.
	if r := render("ntfy", initialized(t, "ntfy://ntfy.sh/topic"), msg, url.Values{}); r.params["title"] != msg.Title {
		t.Fatalf("%+v", r.params)
	}
}

// received is one request a fake push server got.
type received struct {
	header http.Header
	body   string
}

// pushServer answers every request with ok, a JSON object.
func pushServer(t *testing.T) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{header: r.Header.Clone(), body: string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), got...)
	}
}

// The parameters render sets are ones the services accept: real sends
// through Shoutrrr succeed and carry them.
func TestNtfyAndGotifyAcceptTheRenderedParameters(t *testing.T) {
	msg := sample()
	srv, got := pushServer(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	if class := deliver(context.Background(), "ntfy://"+host+"/dm-topic?scheme=http", msg, 5*time.Second); class != "" {
		t.Fatalf("ntfy: %s", class)
	}
	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("ntfy requests %d", len(reqs))
	}
	n := reqs[0]
	headers := fmt.Sprint(n.header)
	if !strings.HasPrefix(n.header.Get("Content-Type"), "text/markdown") || !strings.Contains(n.body, "**Reclaimed:** 4.2 GiB") ||
		!strings.Contains(headers, "white_check_mark") || !strings.Contains(headers, msg.URL) {
		t.Fatalf("ntfy: %v %q", n.header, n.body)
	}

	if class := deliver(context.Background(), "gotify://"+host+"/Aaaaaaaaaaaaaaa?disabletls=yes", msg, 5*time.Second); class != "" {
		t.Fatalf("gotify: %s", class)
	}
	reqs = got()
	if len(reqs) != 2 {
		t.Fatalf("gotify requests %d", len(reqs))
	}
	var g struct {
		Title    string         `json:"title"`
		Message  string         `json:"message"`
		Priority int            `json:"priority"`
		Extras   map[string]any `json:"extras"`
	}
	if err := json.Unmarshal([]byte(reqs[1].body), &g); err != nil {
		t.Fatalf("gotify body %q: %v", reqs[1].body, err)
	}
	display, _ := g.Extras["client::display"].(map[string]any)
	if g.Title != msg.Title || g.Priority != 4 || display["contentType"] != "text/markdown" || !strings.Contains(g.Message, "**Reclaimed:**") {
		t.Fatalf("gotify: %+v", g)
	}
}
