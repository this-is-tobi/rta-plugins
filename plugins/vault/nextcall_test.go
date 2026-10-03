package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The listing a policy not found offers is a call, and it reaches the Vault
// the refused call reached: through a profile's forward the address was
// 127.0.0.1 and a port that closed with the call, so the profile names the
// Vault, and reached directly the address stays beside it.
func TestTheListingAPolicyNotFoundOffersReachesTheVaultItCameFrom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		name    string
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		want    string
		unwant  string
	}{
		{"a terminal, no profile", plugin.TunnelNone, "", plugin.SurfaceCLI,
			"`rta vault policy list --address " + srv.URL + "` shows what exists", "--profile"},
		{"a terminal, through a forward", plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			"`rta vault policy list --profile prod` shows what exists", "--address"},
		{"an agent, through a profile", plugin.TunnelKube, "prod", plugin.SurfaceMCP,
			"`vault_policy_list {\"profile\":\"prod\"}` shows what exists", "address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "vault.policy.get", map[string]any{"address": srv.URL, "token": "t", "name": "ghost"}).
				WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			_, err := runPolicyGet(context.Background(), r)
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "vault.policy.notfound" {
				t.Fatalf("err = %v, want vault.policy.notfound", err)
			}
			if !strings.Contains(verr.Hint, tc.want) || strings.Contains(verr.Hint, tc.unwant) {
				t.Errorf("hint = %q, want %q and not %q", verr.Hint, tc.want, tc.unwant)
			}
		})
	}
}
