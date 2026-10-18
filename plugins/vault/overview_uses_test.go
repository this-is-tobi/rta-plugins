package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The overview reads the token with auth/token/lookup-self, and a read with a
// token spends one of its uses. A token made with -use-limit=3 answered three
// runs and the fourth said "token unreadable — permission denied", on a tile
// that re-ran every thirty seconds. Vault reports num_uses after the lookup has
// been counted: 0 for a token with no limit, the uses left otherwise, and -1 on
// the lookup that spent the last.

func vaultWithUses(t *testing.T, uses string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "seal-status") {
			_, _ = w.Write([]byte(`{"sealed":false,"initialized":true,"version":"1.14.4"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"policies":["default"],"ttl":3600` + uses + `}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tokenRow(t *testing.T, srv *httptest.Server, detail bool) (string, bool) {
	t.Helper()
	v, err := overviewOf(t, srv, detail)
	if err != nil {
		t.Fatal(err)
	}
	if detail {
		for _, s := range v.(view.Sections).Items {
			if s.Title == "status" {
				v = s.View
			}
		}
	}
	for _, p := range v.(view.KeyValue).Pairs {
		if p.Key == "token" {
			return p.Value, true
		}
	}
	return "", false
}

func TestATokenWithAUseLimitSaysThisPageSpendsOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		uses string
		want string
	}{
		{"three left", `,"num_uses":3`, "has 3 uses left; this page spends one"},
		{"one left", `,"num_uses":1`, "has 1 use left; this page spends one"},
		{"the last was just spent", `,"num_uses":-1`, "has no uses left; this page spent the last one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, detail := range []bool{false, true} {
				got, there := tokenRow(t, vaultWithUses(t, tc.uses), detail)
				if !there || got != tc.want {
					t.Errorf("token = %q (present %v, detail %v), want %q", got, there, detail, tc.want)
				}
			}
		})
	}
}

func TestATokenWithNoUseLimitSaysNothingOfUses(t *testing.T) {
	for name, uses := range map[string]string{"unlimited": `,"num_uses":0`, "not reported": ``} {
		if got, there := tokenRow(t, vaultWithUses(t, uses), false); there {
			t.Errorf("%s: token = %q, want no row about uses", name, got)
		}
	}
}
