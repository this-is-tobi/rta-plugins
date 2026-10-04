package main

import (
	"context"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// vault.overview composes seal status, the current token and the policy
// list through the one client its own Run already built — the same reason
// pg.overview calls its sections directly rather than through
// plugin.Page.AddAs, which would open one connection per section for what
// is supposed to be a single glance.
//
// NoPreview like every capability here (cap's job): the automatic dashboard
// must not decide on its own that a Vault deployment is worth polling every
// few seconds. `dashboard.tiles` still accepts it explicitly — naming a
// capability in a config file is the asking.

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.overview",
		Summary:    "Seal state, the current token and the policy list at a glance",
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "Whether this Vault is worth talking to at all, and what the configured " +
			"token can do — never a secret value. `detail` adds the full policy list to the same " +
			"page.",
		Run: runOverview,
	})
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		if req.Bool("detail") {
			return detailedOverview(ctx, client, req)
		}
		return compactOverview(ctx, client, req)
	})
}

func compactOverview(ctx context.Context, client *vaultapi.Client, req plugin.Request) (view.View, error) {
	kv := view.KeyValue{}
	add := func(key, value string) {
		if value != "" {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: key, Value: value})
		}
	}

	// **A read that failed is said, not skipped.** These two used to be
	// wrapped in `if err == nil` alone, so a seal status this token may not
	// read — an ordinary least-privilege token, not a broken one — came back
	// as a page with one fewer row, which reads as a deliberately compact
	// report rather than as a question nobody answered. "Is this Vault
	// sealed" is the one thing this capability exists to say, and silence is
	// the wrong way to say it.
	//
	// read counts what actually answered, because the rows can no longer:
	// with a failure now occupying a row of its own, len(kv.Pairs) stopped
	// being the difference between a Vault half-read and one that said
	// nothing at all.
	read := 0
	var cause *view.Error
	if status, err := client.Sys().SealStatusWithContext(ctx); err == nil {
		read++
		state := "unsealed"
		if status.Sealed {
			state = "sealed"
		}
		if !status.Initialized {
			state = "not initialized"
		}
		add("state", state+" · "+status.Version)
	} else {
		cause = classify(err, req)
		add("state", "unreadable — "+err.Error())
	}
	if secret, err := client.Auth().Token().LookupSelfWithContext(ctx); err == nil {
		read++
		if policies, ok := secret.Data["policies"]; ok {
			add("token policies", nameCell(policies))
		}
		if ttl, ok := secret.Data["ttl"]; ok {
			add("token ttl (seconds)", tokenTTL(ttl))
		}
	} else {
		if cause == nil {
			cause = classify(err, req)
		}
		add("token", "unreadable — "+err.Error())
	}

	if read == 0 {
		return nil, nothingRead(cause)
	}
	return kv, nil
}

// nothingRead is the refusal for an overview that read nothing, and it says why
// whenever the reason is not the token's own. It was the bare "nothing could
// be read" for a Vault that refused the connection as much as for one that
// refused the token, so the one failure an agent can act on — the address is
// wrong, the server is down, the certificate does not verify — was reported
// as the one it cannot. A refusal by the token's policy stays the overview's
// own answer, since neither read was the token's to make.
func nothingRead(cause *view.Error) *view.Error {
	if cause != nil && cause.Code != "vault.denied" {
		return cause
	}
	return view.Errorf("vault.overview.unavailable", "nothing could be read").
		WithHint("this token may read neither the seal status nor itself, so there is nothing for an overview to say")
}

func detailedOverview(ctx context.Context, client *vaultapi.Client, req plugin.Request) (view.View, error) {
	p := plugin.NewPage(ctx, req)
	put := func(title string, v view.View, err error) {
		if err != nil {
			p.Warn(view.AsError(err, "page.section.failed"))
			return
		}
		p.Put(title, v)
	}

	var cause *view.Error
	kv, err := compactOverview(ctx, client, req)
	if err != nil {
		cause = view.AsError(err, "vault.overview.unavailable")
	}
	put("status", kv, err)

	names, err := client.Sys().ListPoliciesWithContext(ctx)
	if err == nil {
		put("policies", policyTable(names), nil)
	} else {
		if cause == nil {
			cause = classify(err, req)
		}
		put("policies", nil, err)
	}

	if p.Empty() {
		return nil, nothingRead(cause)
	}
	return p.View(), nil
}
