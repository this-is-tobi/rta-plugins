// Command rta-plugin-keycloak reads a Keycloak realm the way its owner
// needs to: who exists and whether they have a second factor, which
// clients are registered and how each one authenticates, what the
// authentication flows require, who is signed in, what the event log
// says — and, behind keycloak.audit, a grade of the realm against named
// controls: MFA, brute-force detection, password policy, client settings,
// token lifetimes, event logging, realm defaults and who holds admin.
//
// # What it acts as
//
// A confidential client's service account, never an administrator. The
// operator creates a client with service accounts enabled, grants that
// account the realm-management roles view-users, view-clients, view-realm,
// view-events and view-authorization, and gives this plugin the client id
// and secret. Every call mints a five-minute token from the secret and
// throws it away — no admin session, no stored bearer token, and a blast
// radius that is exactly the view-* roles the operator chose. The direct
// access grant (an admin's own password through a public client) would
// answer the same questions, and keycloak.audit flags it wherever a client
// has it on: the plugin must not be what it grades.
//
// # Reads only, and one line drawn twice
//
// Nothing here writes. Identity administration is authority-expanding by
// nature — a user created, a role granted, a client registered is how an
// agent would mint itself an identity — so a write set is its own design
// with its own gates, and none of it is here.
//
// Two things a read could return are never returned. A client's secret:
// a client representation carries it to anyone with view-clients, and this
// plugin decodes into a shape that has no field for it (types.go says how).
// And a user's credentials: what exists is listed by type — password, otp,
// webauthn — and never by value, which the API does not hand out either.
//
// Build it and put it on your $PATH as `rta-plugin-keycloak`:
//
//	cd plugins/keycloak && go build -o ~/.local/bin/rta-plugin-keycloak .
//
// State the instance once, in rta's config, under the artifact's own
// section — `rta explain keycloak.overview` prints the exact heading
// including the digest:
//
//	plugins:
//	  keycloak@<digest>:
//	    url: https://sso.internal
//	    realm: main
//	    client-id: rta
//
// and export RTA_KEYCLOAK_CLIENT_SECRET, or map it from the store in a
// profile. Every capability here reaches off the box, so none of them —
// keycloak.overview included — appear on the automatic dashboard on their
// own (see cap's comment); add one explicitly once you have decided
// polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: keycloak.overview
package main

import (
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk"
	"github.com/this-is-tobi/rta/pkg/view"
)

func main() { sdk.Serve(Plugin()) }

// cap builds a capability with the shared connection inputs appended, so no
// declaration here can forget one and no two can disagree about a default.
//
// Every capability here is NoPreview because every one reaches off the
// box: the automatic dashboard runs Read capabilities unasked every few
// seconds, and an identity provider that every login in the company goes
// through is not something this plugin gets to decide, on its own, is
// fine to poll — each poll is a token minted and a login event written
// to that realm's own log. An operator who has looked at their deployment
// and decided otherwise still can: dashboard.tiles accepts any capability
// regardless of NoPreview, because naming one in a config file is the
// asking.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(own, connFields()...)
	c.NoPreview = true
	return c
}

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut.
var version = "dev"

func Plugin() plugin.Plugin {
	return plugin.Plugin{
		Name:    "keycloak",
		Summary: "Keycloak: users, clients, roles, flows, sessions, events — and a realm graded against named controls",
		Version: version,
		Capabilities: []plugin.Capability{
			overviewCapability(),
			userListCapability(),
			userShowCapability(),
			clientListCapability(),
			clientShowCapability(),
			roleListCapability(),
			flowListCapability(),
			flowShowCapability(),
			sessionListCapability(),
			eventListCapability(),
			eventAdminCapability(),
			auditCapability(),
		},
	}
}

// maxField is the bound every listing here carries. The Admin API pages
// with first/max and defaults to a hundred; a realm with fifty thousand
// users is ordinary, and "list them all" is a report nobody reads on a
// terminal. Config-backed so an operator who knows their realm sets it
// once.
func maxField(def, ceiling int, help string) plugin.Field {
	return plugin.Field{Name: "max", Type: plugin.Int, Config: "max", Default: def, Min: 1, Max: ceiling, Help: help}
}

// finish stamps a table's Total once its rows are in.
func finish(t view.Table) view.Table {
	t.Total = len(t.Rows)
	return t
}

// columns is the shorthand for a table with plain text columns and a few
// kinded ones — the kinds are what let a renderer align and grade.
func columns(cols ...view.Column) view.Table { return view.Table{Columns: cols} }

func col(name string) view.Column { return view.Column{Name: name} }
