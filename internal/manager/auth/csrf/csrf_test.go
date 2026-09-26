package csrf

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCrossOriginRequests(t *testing.T) {
	pub, _ := url.Parse("https://docker.example.com")
	g, err := New(pub)
	if err != nil {
		t.Fatal(err)
	}
	type req struct {
		method, host string
		headers      map[string]string
		ok           bool
	}
	cases := map[string]req{
		"same-origin fetch":                 {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://docker.example.com"}, true},
		"user typed / bookmark (none)":      {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "none"}, true},
		"cross-site form post":              {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		"same-site subdomain":               {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://blog.example.com"}, false},
		"cross-site delete":                 {http.MethodDelete, "docker.example.com", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		"old browser, foreign Origin":       {http.MethodPatch, "docker.example.com", map[string]string{"Origin": "https://evil.example"}, false},
		"old browser, public origin":        {http.MethodPost, "docker.example.com", map[string]string{"Origin": "https://docker.example.com"}, true},
		"proxy rewrote Host, public Origin": {http.MethodPost, "docker-manager:8080", map[string]string{"Origin": "https://docker.example.com"}, true},
		"proxy rewrote Host, foreign":       {http.MethodPost, "docker-manager:8080", map[string]string{"Origin": "http://docker-manager:8080.evil.example"}, false},
		"non-browser client":                {http.MethodPost, "docker.example.com", nil, true},
		"cross-site GET is safe":            {http.MethodGet, "docker.example.com", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		"bearer token request":              {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "cross-site", "Authorization": "Bearer dyt_x"}, true},
		"basic auth is not a token":         {http.MethodPost, "docker.example.com", map[string]string{"Sec-Fetch-Site": "cross-site", "Authorization": "Basic Zm9vOmJhcg=="}, false},
	}
	for name, c := range cases {
		r := httptest.NewRequest(c.method, "https://"+c.host+"/api/v1/auth/session", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		err := g.Check(r)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrCrossOrigin) {
			t.Errorf("%s: error %v is not ErrCrossOrigin", name, err)
		}
	}
}
