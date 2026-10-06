package main

import (
	"context"
	"fmt"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func tokenStatusCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.token.status",
		Summary:    "What the current token can do, and when it expires",
		Keywords:   []string{"whoami", "lookup", "ttl", "expiry", "renew"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Always the caller's own token (Vault's own `auth/token/lookup-self`) — " +
			"looking up an arbitrary other token by value would need that value as an input, which " +
			"is a second credential to handle for a question this plugin does not otherwise need " +
			"to answer. Metadata only: policies, TTL, renewability — never a value this token " +
			"could itself go on to reveal.",
		Run: runTokenStatus,
	})
}

func runTokenStatus(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		secret, err := client.Auth().Token().LookupSelfWithContext(ctx)
		if err != nil {
			verr := classify(err, req)
			if verr.Code == "vault.denied" {
				// The hint every other refusal carries sends the reader here,
				// to this call, which is the one that was just refused.
				verr.Hint = "Vault refused the lookup of the token's own record, which a token it " +
					"accepts can do — so this one is invalid or has expired, or was made without the " +
					"default policy"
			}
			return nil, verr
		}
		kv := view.KeyValue{}
		add := func(key, dataKey string, render func(interface{}) string) {
			if v, ok := secret.Data[dataKey]; ok {
				kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: render(v)})
			}
		}
		add("accessor", "accessor", cell)
		// A token's display name is built from whoever logged in (userpass-alice),
		// and its policies are names whoever wrote a policy chose.
		add("display name", "display_name", nameCell)
		add("policies", "policies", nameCell)
		add("orphan", "orphan", cell)
		add("renewable", "renewable", cell)
		if v, ok := secret.Data["ttl"]; ok {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: "ttl (seconds)", Value: tokenTTL(v)})
		}
		add("expire time", "expire_time", cell)
		return kv, nil
	})
}

// tokenTTL is a token's ttl as a cell shows it. Vault reports 0 for a token
// that never expires — the root token, or one made with no ttl and no period
// — and a bare "0" under "ttl (seconds)" reads as a token already out of time,
// which is the opposite fact and the one an agent would act on.
func tokenTTL(v any) string {
	if c := cell(v); c != "0" {
		return c
	}
	return "0 — does not expire"
}

// vault.lease.show is the structured equivalent of `vault lease lookup`.
func leaseShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.lease.show",
		Summary:    "A lease's TTL and renewability",
		Keywords:   []string{"ttl", "renew", "expiry", "dynamic"},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "When a leased secret (a database credential, an issued certificate) expires " +
			"and whether it can be renewed — never the secret the lease was issued for.",
		Run: runLeaseShow,
	}, plugin.Field{Name: "id", Type: plugin.String, Positional: true, Required: true,
		Help: "the lease ID, as the response that issued the secret reports it (lease_id)"})
}

func runLeaseShow(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		secret, err := client.Sys().LookupWithContext(ctx, req.String("id"))
		if err != nil {
			return nil, classify(err, req)
		}
		kv := view.KeyValue{}
		keys := []string{"id", "issue_time", "expire_time", "last_renewal", "renewable", "ttl"}
		for _, k := range keys {
			if v, ok := secret.Data[k]; ok {
				value := cell(v)
				// A lease's ID opens on the path that issued it, which holds the
				// role it was issued for: a name its writer chose.
				if k == "id" {
					value = nameCell(v)
				}
				kv.Pairs = append(kv.Pairs, view.Pair{Key: k, Value: value})
			}
		}
		return kv, nil
	})
}

// nameCell is cell for a value that is a name, or a list of them, as a person
// reads it in a list: each name as plugin.ListedName shows it, and one that is
// empty as nothing, the way cell shows it.
func nameCell(v interface{}) string {
	if list, ok := v.([]interface{}); ok {
		parts := make([]string, len(list))
		for i, e := range list {
			parts[i] = nameCell(e)
		}
		return strings.Join(parts, ", ")
	}
	if name := cell(v); name != "" {
		return plugin.ListedName(name)
	}
	return ""
}

// cell renders an arbitrary Vault response value as text — the equivalent
// job plugins/pg's cell does for a driver-decoded SQL value, against JSON's
// smaller type set instead: every Vault response is decoded from JSON into
// exactly nil/bool/float64/string/[]interface{}/map[string]interface{}, and
// fmt's %v already renders every scalar among those the way a person would
// type it.
func cell(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []interface{}:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = cell(e)
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}
