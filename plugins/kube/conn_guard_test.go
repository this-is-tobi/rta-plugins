package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// The namespace is the container a record like a ServiceAccount sits in,
// just as a mount is for a vault path or a bucket is for an s3 key: when a
// gated capability scopes its grant on something narrower than the
// namespace — kube.serviceaccount.revoke scopes on "name" alone — a
// caller-settable namespace lets a grant issued for one identity reach the
// identically-named one in any namespace the credentials can reach. A
// capability that scopes on "namespace" itself is exempt: there the grant
// is checked against the namespace directly, which is the point of it being
// caller-settable.
//
// Written against the declaration, mirroring plugins/s3's
// TestScopedByKeyBindsItsBucket and plugins/vault's
// TestEveryGatedCapabilityBindsItsMount, so a gated capability that adds a
// namespace input without binding it is caught the day it ships rather than
// by a hand-picked list of capability IDs that stops growing the day
// somebody forgets to add to it.
func TestEveryGatedCapabilityBindsItsNamespace(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if !c.NeedsGrant && c.Safety != plugin.Destructive {
			continue
		}
		if c.Scope == "namespace" {
			continue
		}
		for _, f := range c.Inputs {
			if f.Name == "namespace" && !f.Local {
				t.Errorf("%s: gated capability scopes on %q but declares namespace caller-settable — "+
					"a grant on one %s authorizes it in any namespace", c.ID, c.Scope, c.Scope)
			}
		}
	}
}
