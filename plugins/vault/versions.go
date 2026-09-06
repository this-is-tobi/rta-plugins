package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A KV v2 secret is a chain of versions, and the chain is most of what makes
// the engine safe to write to: vault.kv.set never replaces anything, it adds
// a version and keeps the one before. Until this file, rta could add to the
// chain and read its head and nothing else — no way to see how long it was,
// which links were deleted, or to reach back one link after a bad rotation
// without the vault CLI. These are the four operations the metadata
// endpoint offers on the chain, with the safety classes their reversibility
// earns: reading the chain is a Read, hiding and unhiding a version is a
// Write under a grant naming the path, and destroying one — the only
// operation on a secret that nothing undoes — is for a person at a terminal.

// versionsField names versions the way vault.kv.history numbers them. A
// string list rather than an int one so that `1,2` and `--versions 1
// --versions 2` both work; parseVersions is where a word that is not a
// number is refused.
func versionsField(required bool, help string) plugin.Field {
	return plugin.Field{Name: "versions", Type: plugin.StringSlice, Required: required, Help: help}
}

func parseVersions(raw []string) ([]int, *view.Error) {
	out := make([]int, 0, len(raw))
	for _, r := range raw {
		for _, part := range strings.Split(r, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 {
				return nil, view.Errorf("vault.kv.version.invalid", "%q is not a version number", part).
					WithHint("vault.kv.history numbers them, from 1")
			}
			out = append(out, n)
		}
	}
	return out, nil
}

func versionWords(versions []int) string {
	if len(versions) == 0 {
		return "the current version"
	}
	words := make([]string, len(versions))
	for i, v := range versions {
		words[i] = strconv.Itoa(v)
	}
	if len(words) == 1 {
		return "version " + words[0]
	}
	return "versions " + strings.Join(words, ", ")
}

func versionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05Z07:00")
}

// versionState is one word per version, in the order the words outrank each
// other: destroyed is final, deleted is reversible, and current names the
// head of the chain — which can itself be deleted, so it is a suffix rather
// than an alternative.
func versionState(v vaultapi.KVVersionMetadata, current int) string {
	state := "kept"
	switch {
	case v.Destroyed:
		state = "destroyed"
	case !v.DeletionTime.IsZero():
		state = "deleted " + versionTime(v.DeletionTime)
	}
	if v.Version != current {
		return state
	}
	if state == "kept" {
		return "current"
	}
	return state + ", current"
}

func kvHistoryCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.history",
		Summary:    "A secret's versions — which is current, which are deleted or destroyed — never values",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The structured equivalent of `vault kv metadata get`: every version the engine " +
			"still knows about, when each was written, and whether it is the current one, deleted " +
			"(hidden, and vault.kv.undelete brings it back) or destroyed (gone). Read, like " +
			"vault.kv.list, for the same reason: this is the shape of the chain and never a link " +
			"of it — no version's data is fetched. The count is how many earlier values a " +
			"rotation could still reach, which is what `vault kv get --version` reads.",
		Run: runKVHistory,
	}, mountField(), pathField("the secret's path within the mount"))
}

func runKVHistory(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		meta, err := client.KVv2(req.String("mount")).GetMetadata(ctx, req.String("path"))
		if err != nil {
			return nil, classify(err, req)
		}
		// The map's keys are the numbers; the values do not carry their own.
		versions := make([]vaultapi.KVVersionMetadata, 0, len(meta.Versions))
		for key, v := range meta.Versions {
			if n, err := strconv.Atoi(key); err == nil {
				v.Version = n
			}
			versions = append(versions, v)
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
		t := view.Table{Columns: []view.Column{{Name: "Version"}, {Name: "Created"}, {Name: "State"}}}
		for _, v := range versions {
			t.Rows = append(t.Rows, []string{strconv.Itoa(v.Version), versionTime(v.CreatedTime), versionState(v, meta.CurrentVersion)})
		}
		t.Total = len(t.Rows)
		limit := "10 (Vault's default)"
		if meta.MaxVersions > 0 {
			limit = strconv.Itoa(meta.MaxVersions)
		}
		about := view.KeyValue{Pairs: []view.Pair{
			{Key: "current", Value: strconv.Itoa(meta.CurrentVersion)},
			{Key: "versions", Value: strconv.Itoa(len(versions))},
			{Key: "max versions", Value: limit},
			{Key: "updated", Value: versionTime(meta.UpdatedTime)},
		}}
		return view.Sections{Items: []view.Section{
			{ID: "versions", Title: "Versions", View: t},
			{ID: "metadata", Title: "Metadata", View: about},
		}}, nil
	})
}

func kvDeleteCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.delete",
		Summary:    "Delete a secret's current version, or the versions named — undoable",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: true,
		Description: "A soft delete, which is what `vault kv delete` does: the data of the versions " +
			"named is hidden and vault.kv.undelete brings it back — nothing here is destroyed. " +
			"With no --versions, the current version. The metadata and every other version stay, " +
			"so vault.kv.get still answers for those and vault.kv.history lists the deleted ones " +
			"as deleted. Needs the grant vault.kv.set needs, naming the path, because hiding the " +
			"current version is what a reader of that path sees as the secret going away.",
		Run: runKVDelete,
	}, mountField(), pathField("the secret's path within the mount"),
		versionsField(false, "version numbers, as vault.kv.history lists them; none means the current version"))
}

func runKVDelete(ctx context.Context, req plugin.Request) (view.View, error) {
	versions, verr := parseVersions(req.StringSlice("versions"))
	if verr != nil {
		return nil, verr
	}
	where := req.String("mount") + "/" + req.String("path")
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf("would delete %s of %s — hidden, and vault.kv.undelete brings it back",
			versionWords(versions), where)}, nil
	}
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		kv := client.KVv2(req.String("mount"))
		var err error
		if len(versions) == 0 {
			err = kv.Delete(ctx, req.String("path"))
		} else {
			err = kv.DeleteVersions(ctx, req.String("path"), versions)
		}
		if err != nil {
			return nil, classify(err, req)
		}
		return view.Text{Body: fmt.Sprintf("deleted %s of %s — `rta vault kv undelete` brings it back",
			versionWords(versions), where)}, nil
	})
}

func kvUndeleteCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.kv.undelete",
		Summary:    "Bring back deleted versions of a secret",
		Safety:     plugin.Write,
		NeedsGrant: true,
		Scope:      "path",
		Idempotent: true,
		Description: "The other half of vault.kv.delete: the versions named become readable again, " +
			"exactly as they were. A destroyed version cannot come back this way or any other; " +
			"vault.kv.history says which is which before you ask.",
		Run: runKVUndelete,
	}, mountField(), pathField("the secret's path within the mount"),
		versionsField(true, "version numbers, as vault.kv.history lists them"))
}

func runKVUndelete(ctx context.Context, req plugin.Request) (view.View, error) {
	versions, verr := parseVersions(req.StringSlice("versions"))
	if verr != nil {
		return nil, verr
	}
	where := req.String("mount") + "/" + req.String("path")
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf("would bring back %s of %s", versionWords(versions), where)}, nil
	}
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		if err := client.KVv2(req.String("mount")).Undelete(ctx, req.String("path"), versions); err != nil {
			return nil, classify(err, req)
		}
		return view.Text{Body: fmt.Sprintf("brought back %s of %s", versionWords(versions), where)}, nil
	})
}

func kvDestroyCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:        "vault.kv.destroy",
		HumanOnly: true,
		Summary:   "Destroy versions of a secret for good, for a person at a terminal",
		// Destructive for the --yes gate: this is the one operation on a
		// secret that nothing undoes, and vault.kv.set's whole safety story
		// is that the version before is still there.
		Safety:     plugin.Destructive,
		Idempotent: true,
		Description: "The versions named are gone: the data is erased and the chain keeps only the " +
			"fact that a version of that number existed. **Refuses MCP outright**, on the reasoning " +
			"vault.snapshot states from the other side — vault.kv.delete hides a version and takes " +
			"a grant because undelete undoes it; nothing undoes this, so no grant can name what it " +
			"costs. vault.kv.history shows what you are about to lose.",
		Run: runKVDestroy,
	}, mountField(), pathField("the secret's path within the mount"),
		versionsField(true, "version numbers, as vault.kv.history lists them"))
}

func runKVDestroy(ctx context.Context, req plugin.Request) (view.View, error) {
	if verr := humanOnly(req, "vault.kv.destroy",
		"destroying a version is the one operation on a secret nothing undoes — vault.kv.delete "+
			"hides one and takes a grant naming the path; this takes the person"); verr != nil {
		return nil, verr
	}
	versions, verr := parseVersions(req.StringSlice("versions"))
	if verr != nil {
		return nil, verr
	}
	where := req.String("mount") + "/" + req.String("path")
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf("would destroy %s of %s — the data erased, nothing brings it back",
			versionWords(versions), where)}, nil
	}
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		if err := client.KVv2(req.String("mount")).Destroy(ctx, req.String("path"), versions); err != nil {
			return nil, classify(err, req)
		}
		return view.Text{Body: fmt.Sprintf("destroyed %s of %s", versionWords(versions), where)}, nil
	})
}
