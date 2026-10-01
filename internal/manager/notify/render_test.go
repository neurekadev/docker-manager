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
		Title: "[Home] Prune on homelab reclaimed 4.2 GiB", Body: "Removed 15 objects & reclaimed 4.2 GiB.",
		URL: "https://docker.example.com/jobs/j1", Tone: domain.ToneSuccess, Footer: "Docker Manager",
		Time: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC),
		Fields: []domain.NotificationField{
			{Name: "Reclaimed", Value: "4.2 GiB", Inline: true},
			{Name: "Images", Value: "10 images · 4 GiB", Inline: true},
			{Name: "Updated", Value: "web <nginx:1.27>\nworker"},
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

func TestDiscordGetsAnEmbedInTheTonesColor(t *testing.T) {
	address := "discord://token@123456789"
	svc := initialized(t, address)
	r := render("discord", svc, sample(), url.Values{}, "https://docker.example.com")
	if len(r.params) != 0 {
		t.Fatalf("params %v", r.params)
	}
	var p struct {
		Username        string              `json:"username"`
		AvatarURL       string              `json:"avatar_url"`
		AllowedMentions map[string][]string `json:"allowed_mentions"`
		Embeds          []struct {
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
	if err := json.Unmarshal([]byte(r.body), &p); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	if len(p.Embeds) != 1 || p.Username != "Docker Manager" || p.AllowedMentions == nil ||
		p.AvatarURL != "https://docker.example.com/icons/apple-touch-icon-180x180.png" {
		t.Fatalf("%+v", p)
	}
	e := p.Embeds[0]
	if e.Color != 0x4cf683 || e.Title != "[Home] Prune on homelab reclaimed 4.2 GiB" || e.URL != "https://docker.example.com/jobs/j1" ||
		!strings.HasSuffix(e.Description, "[Open in Docker Manager](https://docker.example.com/jobs/j1)") ||
		e.Timestamp != "2026-10-01T09:30:00Z" || e.Footer.Text != "Docker Manager" || len(e.Fields) != 3 || !e.Fields[0].Inline || e.Fields[2].Inline {
		t.Fatalf("%+v", e)
	}
	// Failures are red, warnings amber, news blue.
	for tone, want := range map[domain.NotificationTone]uint{domain.ToneCritical: 0xfd6b66, domain.ToneWarning: 0xf5b544, domain.ToneInfo: 0x2566fd} {
		if toneColor(tone) != want {
			t.Errorf("%s: %x", tone, toneColor(tone))
		}
	}
	// A plain http origin is no icon Discord could fetch.
	if iconURL("http://10.0.0.2:8080") != "" {
		t.Error("icon over http")
	}
}

func TestDiscordLimitsAreKept(t *testing.T) {
	msg := sample()
	msg.Title = strings.Repeat("t", 300)
	for range 30 {
		msg.Fields = append(msg.Fields, domain.NotificationField{Name: "n", Value: strings.Repeat("v", 2000)})
	}
	var p struct {
		Embeds []struct {
			Title  string
			Fields []struct{ Value string }
		}
	}
	if err := json.Unmarshal([]byte(discordPayload(msg, "", "", "")), &p); err != nil {
		t.Fatal(err)
	}
	e := p.Embeds[0]
	if len([]rune(e.Title)) != discordTitleMax || len(e.Fields) != discordFieldsMax || len([]rune(e.Fields[5].Value)) != discordFieldValueMax {
		t.Fatalf("title %d, fields %d", len([]rune(e.Title)), len(e.Fields))
	}
}

func TestServicesGetTheirRichestForm(t *testing.T) {
	msg := sample()
	slack := render("slack", nil, msg, url.Values{}, "")
	if slack.params["color"] != "#4cf683" || slack.params["title"] != msg.Title ||
		!strings.Contains(slack.body, "*Updated:* web &lt;nginx:1.27&gt;") || !strings.Contains(slack.body, "<https://docker.example.com/jobs/j1|Open in Docker Manager>") ||
		strings.Contains(slack.body, "\n\n") {
		t.Fatalf("%+v", slack)
	}
	tg := render("telegram", nil, msg, url.Values{}, "")
	if tg.params["parsemode"] != "HTML" || !strings.Contains(tg.body, "<b>Reclaimed:</b> 4.2 GiB") ||
		!strings.Contains(tg.body, "web &lt;nginx:1.27&gt;") || !strings.Contains(tg.body, "objects &amp; reclaimed") {
		t.Fatalf("%+v", tg)
	}
	ntfy := render("ntfy", nil, msg, url.Values{}, "")
	if ntfy.params["priority"] != "2" || ntfy.params["tags"] != "white_check_mark" || ntfy.params["click"] != msg.URL ||
		ntfy.params["markdown"] != "yes" || !strings.Contains(ntfy.body, "**Reclaimed:** 4.2 GiB") {
		t.Fatalf("%+v", ntfy)
	}
	gotify := render("gotify", nil, msg, url.Values{}, "")
	if gotify.params["priority"] != "4" || !strings.Contains(gotify.params["extras"], `"contentType":"text/markdown"`) ||
		!strings.Contains(gotify.params["extras"], msg.URL) {
		t.Fatalf("%+v", gotify)
	}
	teams := render("teams", nil, msg, url.Values{}, "")
	if teams.params["color"] != "good" || !strings.Contains(teams.body, "[Open in Docker Manager](https://docker.example.com/jobs/j1)") {
		t.Fatalf("%+v", teams)
	}
	critical := msg
	critical.Tone = domain.ToneCritical
	if po := render("pushover", nil, critical, url.Values{}, ""); po.params["priority"] != "1" {
		t.Fatalf("%+v", po)
	}
	generic := render("generic", nil, msg, url.Values{}, "")
	if generic.params["tone"] != "success" || generic.params["url"] != msg.URL || generic.params["title"] != msg.Title ||
		generic.body != plainText(msg) {
		t.Fatalf("%+v", generic)
	}
	// Anything else: the title and plain text with a line per field.
	other := render("bark", nil, msg, url.Values{}, "")
	if len(other.params) != 1 || other.body != "Removed 15 objects & reclaimed 4.2 GiB.\n\nReclaimed: 4.2 GiB\nImages: 10 images · 4 GiB\n"+
		"Updated: web <nginx:1.27>\nworker\n\nhttps://docker.example.com/jobs/j1" {
		t.Fatalf("%q", other.body)
	}
}

func TestTheOwnersOptionsWin(t *testing.T) {
	msg := sample()
	q, _ := url.ParseQuery("Color=%23000000&title=Mine")
	slack := render("slack", nil, msg, q, "")
	if _, ok := slack.params["color"]; ok {
		t.Fatalf("%+v", slack.params)
	}
	if _, ok := slack.params["title"]; ok {
		t.Fatalf("%+v", slack.params)
	}
	// A parse mode of the owner's: the body stays plain text.
	tg := render("telegram", nil, msg, url.Values{"parsemode": {"Markdown"}}, "")
	if _, ok := tg.params["parsemode"]; ok || tg.body != plainText(msg) {
		t.Fatalf("%+v", tg)
	}
}

func TestEmailIsAnHTMLCardWithAPlainPart(t *testing.T) {
	svc := initialized(t, "smtp://mail.example.com:587/?from=dm@example.com&to=ops@example.com")
	msg := sample()
	msg.Body = "Body with {{ braces }} & <tags>"
	r := render("smtp", svc, msg, url.Values{}, "")
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
		!strings.Contains(html, "border-top:4px solid #4cf683") || !strings.Contains(html, `href="https://docker.example.com/jobs/j1"`) {
		t.Fatalf("%s", html)
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

	if class := deliver(context.Background(), "ntfy://"+host+"/dm-topic?scheme=http", msg, 5*time.Second, ""); class != "" {
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

	if class := deliver(context.Background(), "gotify://"+host+"/Aaaaaaaaaaaaaaa?disabletls=yes", msg, 5*time.Second, ""); class != "" {
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
	if g.Title != msg.Title || g.Priority != 4 || display["contentType"] != "text/markdown" || !strings.Contains(g.Message, "**Images:**") {
		t.Fatalf("gotify: %+v", g)
	}
}
