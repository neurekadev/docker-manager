package authz

import (
	"context"
	"testing"
)

func TestDenyAllIsDefault(t *testing.T) {
	a := OrDenyAll(nil)
	for _, p := range []Principal{{Kind: KindUser, UserID: "u"}, Service()} {
		if d := a.Can(context.Background(), p, "job.read", Resource{Type: "job", ID: "j"}); d.Allowed || d.Reason == "" {
			t.Fatalf("DenyAll allowed %+v: %+v", p, d)
		}
	}
	allow := Func(func(context.Context, Principal, string, Resource) Decision { return Allow("test") })
	if !OrDenyAll(allow).Can(context.Background(), Principal{}, "x.y", Resource{}).Allowed {
		t.Fatal("Func adapter")
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := PrincipalFrom(ctx); ok {
		t.Fatal("principal in empty context")
	}
	u := Principal{Kind: KindUser, UserID: "u1"}
	ctx2, err := WithPrincipal(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := PrincipalFrom(ctx2); !ok || got != u {
		t.Fatalf("got %+v %v", got, ok)
	}
	for _, bad := range []Principal{Service(), {}, {Kind: KindUser}, {Kind: KindAPIToken, UserID: "u"}, {Kind: "robot", UserID: "x"}} {
		if _, err := WithPrincipal(ctx, bad); err == nil {
			t.Errorf("WithPrincipal accepted %+v", bad)
		}
	}
	keys := map[string]Principal{"user:u1": u, "token:t": {Kind: KindAPIToken, UserID: "u", TokenID: "t"}, "service": Service()}
	for want, p := range keys {
		if p.Key() != want || !p.Valid() {
			t.Errorf("%+v key %q valid %v", p, p.Key(), p.Valid())
		}
	}
	if (Principal{}).Key() != "" {
		t.Fatal("empty principal has a key")
	}
}
