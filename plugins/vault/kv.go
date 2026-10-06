package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// mountField is shared by every capability that talks to a KV v2 engine.
// Vault's own `vault kv` CLI defaults to "secret" because `vault server -dev`
// mounts one there, not because every real deployment does — a production
// Vault routinely has several KV mounts under different names, so this is a
// Field with that default rather than a literal baked into the path.
func mountField() plugin.Field {
	// Local: a grant's Scope covers "path" only, not the mount — so an
	// MCP-settable mount would let a grant on vault.kv.get app/db-password
	// authorize the identical path in a mount the grant never named.
	return plugin.Field{Name: "mount", Type: plugin.String, Default: "secret", Config: "kv-mount",
		Local: true, Help: "the KV v2 secrets engine's mount path", Live: true, Suggest: suggestMounts("kv")}
}

// listedEntry is a name Vault's LIST answered, as a person reads it in a row or
// a tree label: a name that reads as itself as it is, and one that does not
// quoted with its characters written out.
//
// **The "/" that marks a folder stays outside the quotes.** It is Vault's
// marker and not part of the name, and vault.kv.list's description tells a
// reader to look for a name that ends in it: quoted whole, a folder holding an
// escape sequence ended in a quotation mark and read as a secret.
func listedEntry(name string) string {
	if folder, ok := strings.CutSuffix(name, "/"); ok {
		return plugin.ListedName(folder) + "/"
	}
	return plugin.ListedName(name)
}

// listedPath is where a call reached in the mount, as a sentence names it: the
// path is whatever its caller typed, and over MCP that is an agent.
func listedPath(mount, path string) string { return plugin.ListedName(mount + "/" + path) }

func pathField(help string) plugin.Field {
	return plugin.Field{Name: "path", Type: plugin.String, Positional: true, Required: true, Help: help,
		Live: true, Suggest: suggestPaths}
}

// vault.kv.list is Read for the line builtin/kv's kv.list and kv.get already
// draw: names describe where secrets live, and only a read of a secret's data
// is a disclosure.
func kvListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.list",
		Summary:    "List secret names at a path — never values",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Names only, never values — vault.kv.get is where a secret's data is. A name " +
			"ending in \"/\" is itself a path, one level further to list.",
		Run: runKVList,
	}, mountField(),
		plugin.Field{Name: "path", Type: plugin.String, Positional: true, Default: "",
			Help: "list under this path; empty lists the mount's root",
			Live: true, Suggest: suggestPaths})
}

// unknownMount tells the two answers apart that Vault gives as one.
//
// **A LIST against a mount that does not exist is a 404, and the client turns
// a 404 into an empty result with no error** — the identical value an
// existing mount holding nothing produces. So a mount named `homleab` where
// the Vault has `homelab` printed an empty table and a tree saying "0
// secrets", and the operator reads their own typo as the secrets having gone
// missing. There is nothing in the answer to notice, which is what makes it
// worth a second request: the failure is silent, and silence about a
// secret store is the wrong kind.
//
// Asked only once a listing has come back empty, so the ordinary path costs
// no extra round trip.
//
// **Silent when sys/mounts cannot be read, and that is the important half.**
// A token allowed to list one mount and not to enumerate the engines is an
// ordinary least-privilege setup — the policy a CI role gets — and turning
// its honest empty listing into "no such mount" would be inventing a failure
// out of a permission. The same rule the completion helpers hold: unable to
// tell means say nothing.
func unknownMount(ctx context.Context, client *vaultapi.Client, req plugin.Request) *view.Error {
	mount := req.String("mount")
	mounts, err := client.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return nil //nolint:nilerr // the silence is the point, and the doc comment says why: a token that may list a mount but not enumerate the engines must not have its empty listing turned into "no such mount"
	}
	var kv []string
	for path, m := range mounts {
		if m.Type == "kv" {
			kv = append(kv, strings.TrimSuffix(path, "/"))
		}
	}
	if slices.Contains(kv, mount) {
		return nil
	}
	sort.Strings(kv)
	hint := "this Vault has no KV mount at all"
	if len(kv) > 0 {
		listed := make([]string, len(kv))
		for i, m := range kv {
			listed[i] = plugin.ListedName(m)
		}
		hint = "its KV mounts are: " + strings.Join(listed, ", ")
	}
	return view.Errorf("vault.kv.mount.unknown",
		"%s has no KV mount named %q", reached(req), mount).WithHint(hint)
}

func runKVList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		secret, err := client.Logical().ListWithContext(ctx, req.String("mount")+"/metadata/"+req.String("path"))
		if err != nil {
			return nil, classify(err, req)
		}
		t := view.Table{Columns: []view.Column{{Name: "Name"}}}
		if secret != nil {
			if keys, ok := secret.Data["keys"].([]interface{}); ok {
				names := make([]string, 0, len(keys))
				for _, k := range keys {
					if s, ok := k.(string); ok {
						names = append(names, s)
					}
				}
				sort.Strings(names)
				for _, n := range names {
					t.Rows = append(t.Rows, []string{listedEntry(n)})
				}
			}
		}
		t.Total = len(t.Rows)
		if t.Total == 0 {
			if verr := unknownMount(ctx, client, req); verr != nil {
				return nil, verr
			}
		}
		return t, nil
	})
}

// vault.kv.get is Write+NeedsGrant, the same as builtin/kv's kv.get and for the
// same reason: revealing a secret's plaintext has blast radius even though
// nothing here is modified, and the grant names the path it may read.
func kvGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.get",
		Summary:    "Reveal a secret's current version, or an earlier one",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: true,
		// Declared, so the host says in the agent's tool text that the stored
		// value comes back and becomes part of its context, records the call as
		// a reveal, and names it on the consent card. The grant naming the path
		// is the control, as it was.
		Reveals: true,
		Description: "Returns the secret's plaintext, which is why it needs a grant although nothing " +
			"here is modified. A deleted (but not destroyed) version says so, rather than answering " +
			"with an empty secret that looks the same as one that was never there.",
		Run: runKVGet,
	}, mountField(), pathField("the secret's path within the mount"),
		plugin.Field{Name: "version", Type: plugin.Int, Default: 0, Min: 0,
			Help: "a specific version, as vault.kv.history numbers them; 0 is the current one"})
}

func runKVGet(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		engine := client.KVv2(req.String("mount"))
		var (
			secret *vaultapi.KVSecret
			err    error
		)
		if v := req.Int("version"); v > 0 {
			secret, err = engine.GetVersion(ctx, req.String("path"), v)
		} else {
			secret, err = engine.Get(ctx, req.String("path"))
		}
		if err != nil {
			return nil, classify(err, req)
		}
		if secret.Data == nil {
			return view.KeyValue{Pairs: []view.Pair{
				{Key: "status", Value: "deleted — the metadata survives but this version's data does not"},
			}}, nil
		}
		kv := view.KeyValue{}
		keys := make([]string, 0, len(secret.Data))
		for k := range secret.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kv.Pairs = append(kv.Pairs, view.Pair{Key: plugin.ListedName(k), Value: cell(secret.Data[k])})
		}
		return kv, nil
	})
}

// vault.kv.set carries the overwrite risk builtin/kv's kv.set does, and needs
// the same grant. It always writes a new version rather than merging into the
// current one, which is what makes the earlier one recoverable.
func kvSetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.set",
		Summary:    "Set (or overwrite) a secret",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: false,
		Description: "Always creates a new version holding exactly `data`: the fields of the current " +
			"version are replaced, not merged into. vault.kv.get shows what is there before this " +
			"replaces it, and the version it replaces is kept — read with vault.kv.get's `version` " +
			"until the mount's version limit drops it.",
		Run: runKVSet,
	}, mountField(), pathField("the secret's path within the mount"),
		// SecretSlice, not StringSlice: this is the operation of writing a
		// secret into a secret manager, so every element of it is the
		// credential. Declared as a plain list it was written verbatim to
		// the completion shortlist and re-offered on tab, and — over MCP —
		// into the sealed agent log, which docs/22-audit-trail promises
		// holds the arguments with secrets masked. builtin/kv's own `value`
		// input has been Secret all along for the identical act.
		plugin.Field{Name: "data", Type: plugin.SecretSlice, Required: true,
			Help: "key=value, repeated for more than one field"})
}

func runKVSet(ctx context.Context, req plugin.Request) (view.View, error) {
	data, verr := dataFields(req.Surface(), req.StringSlice("data"))
	if verr != nil {
		return nil, verr
	}
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		// The field names, never the values: this reports what a write would
		// do, and the values are the caller's own input rather than anything
		// only Vault could tell them.
		if req.DryRun {
			return view.Text{Body: fmt.Sprintf("would set %s with %s — a new version, "+
				"the current one kept", listedPath(req.String("mount"), req.String("path")), format.CountOf(len(data), "field"))}, nil
		}
		secret, err := client.KVv2(req.String("mount")).Put(ctx, req.String("path"), data)
		if err != nil {
			return nil, classify(err, req)
		}
		return view.KeyValue{Pairs: []view.Pair{
			{Key: "path", Value: plugin.ListedName(req.String("path"))},
			{Key: "version", Value: cell(secret.VersionMetadata.Version)},
			{Key: "created", Value: secret.VersionMetadata.CreatedTime.Format("2006-01-02T15:04:05Z07:00")},
		}}, nil
	})
}
