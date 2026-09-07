package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// mountField is shared by every capability that talks to a KV v2 engine.
// Vault's own `vault kv` CLI defaults to "secret" because `vault server -dev`
// mounts one there, not because every real deployment does — a production
// Vault routinely has several KV mounts under different names, so this is a
// Field with that default rather than a literal baked into the path.
func mountField() plugin.Field {
	return plugin.Field{Name: "mount", Type: plugin.String, Default: "secret", Config: "kv-mount",
		Help: "the KV v2 secrets engine's mount path", Live: true, Suggest: suggestMounts("kv")}
}

func pathField(help string) plugin.Field {
	return plugin.Field{Name: "path", Type: plugin.String, Positional: true, Required: true, Help: help,
		Live: true, Suggest: suggestPaths}
}

func kvListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.list",
		Summary:    "List secret names at a path — never values",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The structured equivalent of `vault kv list`: names only, the same " +
			"Read/Write split builtin/kv's kv.list and kv.get already draw. A name ending in " +
			"\"/\" is itself a path, one level further to list.",
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
		return nil
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
		hint = "its KV mounts are: " + strings.Join(kv, ", ")
	}
	return view.Errorf("vault.kv.mount.unknown",
		"%s has no KV mount named %q", req.String("address"), mount).WithHint(hint)
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
					t.Rows = append(t.Rows, []string{n})
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

func kvGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.get",
		Summary:    "Reveal a secret's current version, or an earlier one",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: true,
		Description: "Write, the same as builtin/kv's kv.get, for the same reason: revealing a " +
			"secret's plaintext has blast radius even though nothing here is modified. A deleted " +
			"(but not destroyed) version reports which, rather than an empty secret that looks the " +
			"same as one that was never there.",
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
			kv.Pairs = append(kv.Pairs, view.Pair{Key: k, Value: cell(secret.Data[k])})
		}
		return kv, nil
	})
}

func kvSetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.set",
		Summary:    "Set (or overwrite) a secret",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: false,
		Description: "The same overwrite risk builtin/kv's kv.set carries, needing the same " +
			"grant, and the same name: this always creates a brand new version rather than " +
			"merging into the current one — vault.kv.list and vault.kv.get already show what is " +
			"there before this replaces it.",
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
	data, verr := dataFields(req.StringSlice("data"))
	if verr != nil {
		return nil, verr
	}
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		// The field names, never the values: this reports what a write would
		// do, and the values are the caller's own input rather than anything
		// only Vault could tell them.
		if req.DryRun {
			return view.Text{Body: fmt.Sprintf("would set %s/%s with %d field(s) — a new version, "+
				"the current one kept", req.String("mount"), req.String("path"), len(data))}, nil
		}
		secret, err := client.KVv2(req.String("mount")).Put(ctx, req.String("path"), data)
		if err != nil {
			return nil, classify(err, req)
		}
		return view.KeyValue{Pairs: []view.Pair{
			{Key: "path", Value: req.String("path")},
			{Key: "version", Value: cell(secret.VersionMetadata.Version)},
			{Key: "created", Value: secret.VersionMetadata.CreatedTime.Format("2006-01-02T15:04:05Z07:00")},
		}}, nil
	})
}
