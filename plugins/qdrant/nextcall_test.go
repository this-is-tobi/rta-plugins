package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The listing a refusal offers is a call, and it reaches the instance the
// refused call reached: through a profile's forward the endpoint was 127.0.0.1
// and a port that closed with the call, so the profile is what names the
// instance, and reached directly the endpoint stays beside it with how it was
// reached. Spelled bare, it listed whatever the configuration where it was
// pasted names, and an agent was given a call that named no profile at all.
func TestTheListingARefusalOffersReachesTheInstanceItCameFrom(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]any
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		want    string
		unwant  string
	}{
		{"a terminal, no profile", map[string]any{"endpoint": "qdrant.internal:6333", "tls": true, "ca-file": "/etc/qdrant/ca.pem"},
			plugin.TunnelNone, "", plugin.SurfaceCLI,
			"`rta qdrant collection list --endpoint qdrant.internal:6333 --tls --ca-file /etc/qdrant/ca.pem` shows what is there",
			"--profile"},
		{"a terminal, through a forward", map[string]any{"endpoint": "127.0.0.1:54321", "tls-server-name": "qdrant.svc"},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			"`rta qdrant collection list --profile prod --tls-server-name qdrant.svc` shows what is there", "--endpoint"},
		{"an agent, through a profile", map[string]any{"endpoint": "127.0.0.1:54321"},
			plugin.TunnelKube, "prod", plugin.SurfaceMCP,
			"`qdrant_collection_list {\"profile\":\"prod\"}` shows what is there", "endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "qdrant.overview", tc.values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classifyStatus(http.StatusNotFound, []byte(`{"status":{"error":"Not found"}}`), r)
			if !strings.Contains(verr.Hint, tc.want) || (tc.unwant != "" && strings.Contains(verr.Hint, tc.unwant)) {
				t.Errorf("hint = %q, want %q and not %q", verr.Hint, tc.want, tc.unwant)
			}
		})
	}
}
