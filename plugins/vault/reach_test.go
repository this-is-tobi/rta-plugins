package main

import (
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The snapshot and restore refusals the Vault gave, as the status ones are,
// name the Vault as the reader reaches it again and not the end of a forward.
func TestASnapshotRefusalNamesTheProfileAndNotTheEndOfItsForward(t *testing.T) {
	r := req(t, "vault.snapshot", map[string]any{"address": "http://127.0.0.1:41233"}).
		WithProfile("prod", plugin.TunnelKube)
	notFound := &vaultapi.ResponseError{StatusCode: 404}
	mismatch := &vaultapi.ResponseError{StatusCode: 400, Errors: []string{"could not verify hash file"}}
	for name, got := range map[string]string{
		"incomplete":           classifySnapshot(vaultapi.ErrIncompleteSnapshot, r).Message,
		"snapshot unsupported": classifySnapshot(notFound, r).Message,
		"restore unsupported":  classifyRestore(notFound, r).Message,
		"restore mismatch":     classifyRestore(mismatch, r).Message,
	} {
		if !strings.Contains(got, "profile prod (through its kube: forward)") || strings.Contains(got, "41233") {
			t.Errorf("%s: message = %q, want the profile and its forward, not the forward's end", name, got)
		}
	}
}

// A refusal the Vault itself gave names the Vault as the reader reaches it
// again, and the status check it offers reaches that Vault too. Through a
// profile's forward the address is 127.0.0.1 and a port that closed with the
// call, so "http://127.0.0.1:41233 refused: permission denied" named nothing
// the reader could change, when the token was the profile's, and the status
// check named no profile at all: pasted, it asked about whatever token and
// Vault the configuration there named.
func TestAnAnsweredRefusalNamesTheProfileAndItsStatusCheckReachesTheVault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		message []string
		hint    []string
		unhint  []string
	}{
		{"a token refused through a forward", 403, plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			[]string{"profile prod (through its kube: forward) refused"},
			[]string{"`rta vault token status --profile prod`"}, []string{"--address"}},
		{"a token refused reached directly", 403, plugin.TunnelNone, "prod", plugin.SurfaceCLI,
			[]string{"https://vault.internal:8200 (profile prod) refused"},
			[]string{"`rta vault token status --profile prod --address https://vault.internal:8200`"}, nil},
		{"a token refused, to an agent", 403, plugin.TunnelKube, "prod", plugin.SurfaceMCP, nil,
			[]string{"`vault_token_status {\"profile\":\"prod\"}`"}, []string{"address"}},
		{"a sealed Vault through a forward", 412, plugin.TunnelSSH, "prod", plugin.SurfaceCLI,
			[]string{"profile prod (through its ssh: forward) is sealed"},
			[]string{"`rta vault seal status --profile prod`"}, nil},
		{"nothing at a path", 404, plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			[]string{"nothing at that path on profile prod (through its kube: forward)"}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "vault.kv.get", map[string]any{"path": "app/db", "address": "https://vault.internal:8200"}).
				WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classify(&vaultapi.ResponseError{StatusCode: tc.status, Errors: []string{"permission denied"}}, r)
			for _, w := range tc.message {
				if !strings.Contains(verr.Message, w) {
					t.Errorf("message %q does not hold %q", verr.Message, w)
				}
			}
			for _, w := range tc.hint {
				if !strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q does not hold %q", verr.Hint, w)
				}
			}
			for _, w := range tc.unhint {
				if strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q holds %q", verr.Hint, w)
				}
			}
		})
	}
}
