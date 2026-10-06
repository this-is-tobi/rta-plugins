package main

import (
	"context"
	"sort"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Policies stay Read the way builtin/kv's kv.recipients does (public keys, not
// secrets): a policy names paths and capabilities, never a secret value.
func policyListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.policy.list",
		Summary:    "Every ACL policy defined on this Vault",
		Keywords:   []string{"acl", "permissions", "rbac", "hcl"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names, not rules — vault.policy.get shows one policy's own document. A " +
			"policy names paths and capabilities, not secret values, so neither call needs a grant.",
		Run: runPolicyList,
	})
}

func runPolicyList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		names, err := client.Sys().ListPoliciesWithContext(ctx)
		if err != nil {
			return nil, classify(err, req)
		}
		return policyTable(names), nil
	})
}

// policyTable is the policy names Vault listed, sorted, one to a row. A policy
// is named by whoever may write one, so each name goes through
// plugin.ListedName: a name holding an escape sequence or a newline is shown
// quoted, and one that reads as itself is shown as it is.
func policyTable(names []string) view.Table {
	sort.Strings(names)
	t := view.Table{Columns: []view.Column{{Name: "Name"}}}
	for _, n := range names {
		t.Rows = append(t.Rows, []string{plugin.ListedName(n)})
	}
	t.Total = len(t.Rows)
	return t
}

func policyGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.policy.get",
		Summary:    "One policy's own rules, as HCL",
		Keywords:   []string{"acl", "permissions", "rbac", "hcl"},
		Safety:     plugin.Read,
		Idempotent: true,
		Run:        runPolicyGet,
	}, plugin.Field{Name: "name", Type: plugin.String, Positional: true, Required: true,
		Help: "the policy's name, as vault.policy.list shows it",
		Live: true, Suggest: suggestPolicies})
}

func runPolicyGet(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		rules, err := client.Sys().GetPolicyWithContext(ctx, req.String("name"))
		if err != nil {
			return nil, classify(err, req)
		}
		if rules == "" {
			return nil, view.Errorf("vault.policy.notfound", "no policy named %q", req.String("name")).
				WithHint(nextCall(req, "vault.policy.list") + " shows what exists")
		}
		return view.Text{Body: rules}, nil
	})
}
