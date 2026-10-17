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
		Keywords:   []string{"summary", "status", "health", "version"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Description: "Whether this Vault is worth talking to at all, and what the configured " +
			"token can do — never a secret value. `detail` adds the full policy list to the same " +
			"page. A sealed or uninitialized Vault is its state alone: nothing else it could be " +
			"asked would answer.",
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
	kv, _, err := overviewStatus(ctx, client, req)
	if err != nil {
		return nil, err
	}
	return kv, nil
}

// overviewStatus is the rows the compact page is, and whether the Vault is in
// a state to be asked anything else.
//
// **A sealed Vault is one row.** Sealed, or not yet initialized, it answers
// the seal status and refuses everything after it, so the token lookup and the
// policy list were asked only to be told what the first call had said: a token
// row reading "unreadable — Vault is sealed" beside a state row that is
// "sealed", and in the detail a failing call more. The state is the page, and
// asking nothing more of it is also what keeps a sealed Vault's audit log, and
// its operator's, from a burst of refused requests for every refresh of a tile.
func overviewStatus(ctx context.Context, client *vaultapi.Client, req plugin.Request) (view.KeyValue, bool, error) {
	var kv view.KeyValue
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
	//
	// The row holds the classified message and never the client's own text,
	// which is a paragraph over several lines naming the request's URL — through
	// a forward, the end of one that closed with the call — in a cell that is
	// a line.
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
		if status.Sealed || !status.Initialized {
			return kv, false, nil
		}
	} else {
		cause = classify(err, req)
		add("state", "unreadable — "+cause.Message)
	}
	if secret, err := client.Auth().Token().LookupSelfWithContext(ctx); err == nil {
		read++
		if policies, ok := secret.Data["policies"]; ok {
			// A token made without the default policy carries none, and the
			// row's absence would read as a page that did not look.
			list := nameCell(policies)
			if list == "" {
				list = "none"
			}
			add("token policies", list)
		}
		if ttl, ok := secret.Data["ttl"]; ok {
			add("token ttl (seconds)", tokenTTL(ttl))
		}
	} else {
		refusal := classify(err, req)
		if cause == nil {
			cause = refusal
		}
		add("token", "unreadable — "+refusal.Message)
	}

	if read == 0 {
		return view.KeyValue{}, true, nothingRead(cause)
	}
	return kv, true, nil
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
	kv, serving, err := overviewStatus(ctx, client, req)
	if err != nil {
		cause = view.AsError(err, "vault.overview.unavailable")
	}
	put("status", kv, err)
	if !serving && err == nil {
		return p.View(), nil
	}

	names, err := client.Sys().ListPoliciesWithContext(ctx)
	if err == nil {
		put("policies", policyTable(names), nil)
	} else {
		// Classified before it is a warning, for the reason the compact page
		// gives its rows the classified message: the client's own text is a
		// paragraph that names the request's URL, in a place that is a line.
		refusal := classify(err, req)
		if cause == nil {
			cause = refusal
		}
		put("policies", nil, refusal)
	}

	if p.Empty() {
		return nil, nothingRead(cause)
	}
	return p.View(), nil
}
