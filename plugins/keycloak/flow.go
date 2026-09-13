package main

import (
	"context"
	"sort"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Authentication flows are where "is a second factor required" actually
// lives. A realm's OTP policy says what an OTP looks like; whether anyone
// has to present one is a step in the browser flow, and whether that step
// is REQUIRED, CONDITIONAL on the user having set one up, or DISABLED is
// the whole difference between enforced MFA and offered MFA. flow.show
// draws that tree; keycloak.audit reads it.

func flowListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.flow.list",
		Summary:    "The authentication flows, and which one each kind of login is bound to",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Every top-level flow, built-in or custom, with the binding that puts it in " +
			"force: browser, direct grant, registration, reset credentials, client authentication. " +
			"A custom flow that is bound nowhere is defined but does nothing. `keycloak flow show` " +
			"opens one.",
		Run: runFlowList,
	})
}

func runFlowList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		t, verr := s.flowTable(ctx)
		if verr != nil {
			return nil, verr
		}
		return t, nil
	})
}

func (s *session) flowTable(ctx context.Context) (view.View, *view.Error) {
	var realm realmRep
	if verr := s.get(ctx, "", nil, &realm); verr != nil {
		return nil, verr
	}
	var flows []flowRep
	if verr := s.get(ctx, "authentication/flows", nil, &flows); verr != nil {
		return nil, verr
	}
	bindings := map[string][]string{}
	bind := func(alias, use string) {
		if alias != "" {
			bindings[alias] = append(bindings[alias], use)
		}
	}
	bind(realm.BrowserFlow, "browser")
	bind(realm.DirectGrantFlow, "direct grant")
	bind(realm.RegistrationFlow, "registration")
	bind(realm.ResetCredentialsFlow, "reset credentials")
	bind(realm.ClientAuthenticationFlow, "client authentication")

	sort.Slice(flows, func(i, j int) bool { return flows[i].Alias < flows[j].Alias })
	t := columns(col("Flow"), col("Bound to"), col("Built-in"), col("Description"))
	for _, f := range flows {
		if !f.TopLevel {
			continue
		}
		t.Rows = append(t.Rows, []string{f.Alias, strings.Join(bindings[f.Alias], ", "), yesNo(f.BuiltIn), f.Description})
	}
	return finish(t), nil
}

func flowShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.flow.show",
		Summary:    "One flow's steps as a tree: each authenticator and whether it is required, alternative, conditional or disabled",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The executions of one flow, nested the way the console nests them. Read " +
			"it for the browser flow to answer whether a second factor is required of everyone " +
			"(an OTP or WebAuthn step marked required), offered to those who set one up (a " +
			"conditional sub-flow, the default), or absent.",
		Run: runFlowShow,
	},
		plugin.Field{Name: "flow", Type: plugin.String, Positional: true, Required: true,
			Help: "the flow's alias, as `keycloak flow list` shows it", Live: true, Suggest: suggestFlows},
	)
}

func runFlowShow(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		execs, verr := s.executions(ctx, req.String("flow"))
		if verr != nil {
			return nil, verr
		}
		return executionTree(execs), nil
	})
}

func (s *session) executions(ctx context.Context, alias string) ([]executionRep, *view.Error) {
	var execs []executionRep
	if verr := s.get(ctx, "authentication/flows/"+segment(strings.TrimSpace(alias))+"/executions", nil, &execs); verr != nil {
		if verr.Code == "keycloak.notfound" {
			return nil, view.Errorf("keycloak.flow.unknown", "no flow %q in realm %s", alias, s.realm).
				WithHint("`rta keycloak flow list` shows the aliases")
		}
		return nil, verr
	}
	return execs, nil
}

// executionTree nests executions by their level. The API returns them
// flat, in order, each carrying the depth it sits at — so a step's parent
// is the nearest preceding sub-flow one level up, which a stack of the
// current path recovers in one pass.
func executionTree(execs []executionRep) view.View {
	var roots []view.Node
	var path []*view.Node
	for _, e := range execs {
		node := view.Node{Label: e.DisplayName, Detail: strings.ToLower(e.Requirement)}
		if e.Level >= len(path) {
			e.Level = len(path)
		}
		path = path[:e.Level]
		if e.Level == 0 {
			roots = append(roots, node)
			path = append(path, &roots[len(roots)-1])
			continue
		}
		parent := path[e.Level-1]
		parent.Children = append(parent.Children, node)
		path = append(path, &parent.Children[len(parent.Children)-1])
	}
	return view.Tree{Roots: roots}
}

func suggestFlows(ctx context.Context, req plugin.Request) []string {
	s, verr := connect(ctx, req)
	if verr != nil {
		return nil
	}
	var flows []flowRep
	if verr := s.get(ctx, "authentication/flows", nil, &flows); verr != nil {
		return nil
	}
	var out []string
	for _, f := range flows {
		if f.TopLevel {
			out = append(out, f.Alias)
		}
	}
	sort.Strings(out)
	return capped(out)
}
