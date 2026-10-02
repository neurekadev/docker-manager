package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/neurekadev/docker-manager/internal/domain"
)

func sample() domain.NotificationMessage {
	return domain.NotificationMessage{
		Label: "Image updates · Applied", Title: "Update of Paperless succeeded",
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
	if e.Color != 0x4cf683 || e.Author == nil || e.Author.Name != "Image updates · Applied" || e.Title != "Update of Paperless succeeded" ||
		e.URL != "https://docker.example.com/jobs/j1" ||
		e.Description != "Recreated silo\\_data & web with the new image.\n\n[Open in Docker Manager](https://docker.example.com/jobs/j1)" ||
		e.Timestamp != "2026-10-01T09:30:00Z" || e.Footer.Text != "Home" || e.Footer.IconURL != LogoURL || len(e.Fields) != 3 {
		t.Fatalf("%+v", e)
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
	if e := p.Embeds[0]; len([]rune(e.Title)) != discordTitleMax || len(e.Fields) != discordFieldsMax {
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
	if embedSize(p) > discordEmbedMax || len(e.Fields) != 5 || !strings.Contains(e.Fields[0].Value, "](https://") ||
		strings.Contains(e.Fields[1].Value, "](https://") || !strings.Contains(e.Fields[1].Value, "- service-09 0123456789ab → ba9876543210") {
		t.Fatalf("embed %d: %+v", embedSize(p), e.Fields)
	}
	// A list with little room left is cut after a whole entry and says
	// how many are left; the short field after it still fits.
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
	if !cut || e.Fields[4].Name != "Do" || e.Fields[4].Value != "x" {
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
		!strings.HasPrefix(slack.body, "_Image updates · Applied_\n") ||
		!strings.Contains(slack.body, "*Environment:* <https://docker.example.com/environments/e1|homelab>") ||
		!strings.Contains(slack.body, "*Updated:*\n• <https://docker.example.com/stacks/s1/logs?service=web|web &lt;x&gt;> `1a2b` → `3c4d`\n• worker") ||
		!strings.Contains(slack.body, "<https://docker.example.com/jobs/j1|Open in Docker Manager>") || strings.Contains(slack.body, "\n\n") {
		t.Fatalf("%+v", slack)
	}
	tg := render("telegram", nil, msg, url.Values{})
	if tg.params["parsemode"] != "HTML" || !strings.HasPrefix(tg.body, "<i>Image updates · Applied</i>") ||
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
	if len(other.params) != 1 || other.body != "Image updates · Applied\n\nRecreated silo_data & web with the new image.\n\n"+
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
	}).GetTemplate("html")
	if !ok {
		t.Fatal("no HTML template")
	}
	var b strings.Builder
	if err := tpl.Execute(&b, map[string]string{"message": "ignored"}); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	if html != emailHTML(msg) || !strings.Contains(html, "{{ braces }} &amp; &lt;tags&gt;") ||
		!strings.Contains(html, "border-top:4px solid #4cf683") || !strings.Contains(html, `href="https://docker.example.com/jobs/j1"`) ||
		!strings.Contains(html, ">Image updates · Applied</td>") || !strings.Contains(html, `href="https://docker.example.com/environments/e1"`) ||
		!strings.Contains(html, ">1a2b</code> → <code") {
		t.Fatalf("%s", html)
	}
	// Without a status line the top bar names the tone.
	msg.Label = ""
	if !strings.Contains(emailHTML(msg), ">OK</td>") {
		t.Fatal("no tone word")
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
