package main

import (
	"context"
	"sort"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The volumes a cluster's data and WAL actually sit on.
//
// `cnpg.status` reports what the *spec* asks for — "50Gi + 10Gi WAL" — which
// is the right answer to "how was this cluster asked for" and not to "what did
// it get". A PersistentVolumeClaim that is still Pending, one whose capacity
// came back smaller than requested, and one whose resize never finished all
// look identical in the spec, and each of them is a database about to stop
// writing.
//
// **No percentage, and that is a limit rather than an omission.** How full a
// volume is comes from the kubelet's own `stats/summary` endpoint, reached
// through the node proxy — a different mechanism, a different permission, and
// one that does not work through every proxy people put in front of a cluster,
// which is the property this whole plugin is built around. plugins/kube's
// `pvc.list` refuses the same column for the same reason, and `kube.pvc.usage`
// is where it lives instead: graded worst-first, for whoever holds
// `nodes/proxy` — a permission that is the whole kubelet API, exec included,
// which is why it is a capability of its own over there and not a flag here.
// What is here is capacity, phase and class: the facts a claim actually
// carries.

// pvcSelector is the label CloudNativePG puts on every volume it creates for a
// cluster, which is how these are found. A label rather than a name prefix,
// because a prefix would also match a cluster called `shop-db-2` when asked
// about `shop-db`.
const pvcSelector = "cnpg.io/cluster"

// The labels that say what a volume is for and whose it is. Read from a live
// cluster rather than assumed: CloudNativePG sets pvcRole to PG_DATA or PG_WAL
// and names the instance and its role beside it.
const (
	pvcRoleLabel      = "cnpg.io/pvcRole"
	pvcInstanceLabel  = "cnpg.io/instanceName"
	pvcInstanceRole   = "cnpg.io/instanceRole"
	pvcRoleData       = "PG_DATA"
	pvcRoleWriteAhead = "PG_WAL"
)

type pvcList struct {
	Items []pvc `json:"items"`
}

type pvc struct {
	Metadata struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Labels    map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		StorageClassName string `json:"storageClassName"`
		Resources        struct {
			Requests map[string]string `json:"requests"`
		} `json:"resources"`
	} `json:"spec"`
	Status struct {
		Phase    string            `json:"phase"`
		Capacity map[string]string `json:"capacity"`
	} `json:"status"`
}

func (p pvc) requested() string { return p.Spec.Resources.Requests["storage"] }
func (p pvc) actual() string    { return p.Status.Capacity["storage"] }
func (p pvc) instance() string  { return orDash(p.Metadata.Labels[pvcInstanceLabel]) }
func (p pvc) owner() string     { return orDash(p.Metadata.Labels[pvcSelector]) }

// holds says what this volume is for, in words rather than in the label's
// own shouting.
func (p pvc) holds() string {
	switch p.Metadata.Labels[pvcRoleLabel] {
	case pvcRoleData:
		return "data"
	case pvcRoleWriteAhead:
		return "WAL"
	}
	return "—"
}

// bound reports whether this claim has a volume behind it. Everything else is
// a database that either cannot start or cannot grow.
func (p pvc) bound() bool { return strings.EqualFold(p.Status.Phase, "bound") }

// short reports a volume that came back smaller than it was asked for, which
// is what an unfinished expansion looks like from here — the spec says the new
// size and the status still says the old one.
//
// Compared in bytes (parseBytes). It was compared as strings, to carry no
// quantity grammar for one check, and two spellings of one size read as short:
// a 10G claim on a provisioner that rounds up to 10Gi was a warning that an
// expansion had not finished, on a volume larger than asked. The table now
// shows and values both sizes in bytes, so the parse is there anyway.
func (p pvc) short() bool {
	req, okReq := parseBytes(p.requested())
	got, okGot := parseBytes(p.actual())
	return p.bound() && okReq && okGot && got < req
}

func (p pvc) status() string {
	switch {
	case !p.bound():
		return "fail"
	case p.short():
		return "warn"
	}
	return "ok"
}

// pvcsFor sorts volumes the way somebody reads them: by namespace and cluster
// when there is more than one of either, then by instance, then data before
// WAL, so each instance's pair is together and the pair is always in the same
// order.
func pvcsFor(items []pvc) []pvc {
	out := append([]pvc{}, items...)
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].Metadata.Namespace, out[j].Metadata.Namespace; a != b {
			return a < b
		}
		if a, b := out[i].owner(), out[j].owner(); a != b {
			return a < b
		}
		if a, b := out[i].instance(), out[j].instance(); a != b {
			return a < b
		}
		if a, b := out[i].holds(), out[j].holds(); a != b {
			return a == "data"
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out
}

func storageCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "cnpg.storage",
		Summary:  "The volumes a cluster's data and WAL sit on: size, class, and whether each one is actually bound",
		Keywords: []string{"pvc", "disk", "capacity", "bound", "space"},
		Examples: []plugin.Example{
			{Title: "the volumes of one cluster", Inputs: map[string]any{"cluster": "app-db", "namespace": "production"}},
			{Title: "every cluster's volumes, in every namespace", Inputs: map[string]any{"all-namespaces": true}},
			{Title: "the volumes of two clusters in two namespaces", Inputs: map[string]any{
				"cluster": []string{"app-db", "billing-db"}, "namespace": []string{"production", "staging"}}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "`cnpg.status` reports the storage the spec asks for. This reports what " +
			"the cluster got, which is a different question the moment anything has gone " +
			"wrong: a claim still Pending, one whose capacity came back smaller than " +
			"requested, and an expansion that never finished all look identical in the " +
			"spec, and each is a database about to stop writing.\n\n" +
			"With no `cluster` it reads the volumes of every CloudNativePG cluster in the " +
			"namespace and adds a Cluster column. `namespace` is a list of at most 30, each " +
			"read with one call of its own (never the whole cluster with the rest filtered " +
			"out, so a Role in each namespace is enough), and `all-namespaces` reads every " +
			"one in one call; over several a leading Namespace column says which, and one " +
			"that cannot be read is a row under Needs attention while the others answer. " +
			"`cluster` is a list of at most 30 that keeps those clusters' volumes, selected " +
			"by label in the request. " + listsBelongInTheCall + " Only " +
			"volumes CloudNativePG labelled are read, never the rest of a namespace's " +
			"storage.\n\n" +
			"**It does not report how full a volume is.** That comes from the kubelet's " +
			"own stats endpoint through the node proxy — a different mechanism and a " +
			"different permission, and one that does not survive every proxy people put " +
			"in front of a cluster, which is the property this plugin is built around. A " +
			"column that looked like usage and was capacity would be worse than no " +
			"column. `kube.pvc.usage` reports it, graded and worst first, for whoever " +
			"holds nodes/proxy.",
		Agent: "Reports what the cluster got, not what its spec asks for: a claim still Pending, one " +
			"smaller than requested and an expansion that never finished show here and nowhere in the " +
			"spec. One row per volume, data and WAL; without `cluster`, every cluster's. `namespace` is " +
			"an array of at most 30, each read with one call of its own, and `all-namespaces` reads every " +
			"namespace; one that could not be read is a row under Needs attention. `cluster` is an array of " +
			"at most 30 that keeps those clusters' volumes. It does not report how full a volume is.",
		Run: runStorage,
	}, clustersField("only these clusters' volumes — every cluster's when omitted"), namespacesField(), allNamespacesField())
}

func runStorage(ctx context.Context, req plugin.Request) (view.View, error) {
	s, names, verr := listingOf(req)
	if verr != nil {
		return nil, verr
	}
	// The label selector goes to the API server rather than being filtered
	// here, because a namespace's other volumes are not this call's business
	// and reading them would be reading somebody else's storage layout to
	// throw it away. Named, it is the one cluster's label value, or the set of
	// them for several; unnamed it is the label's existence, which is every
	// volume CloudNativePG made and none it did not — the same line, drawn at
	// the label rather than at a value.
	selector := volumeSelector(names)
	r, verr := settle(s, readEach(ctx, s.scopes(), func(ctx context.Context, scope selection) (pvcList, *view.Error) {
		return fetch[pvcList](ctx, scope, "persistentvolumeclaims", "", "-l", selector)
	}))
	if verr != nil {
		return nil, verr
	}
	var items []pvc
	for _, o := range r.outcomes {
		items = append(items, o.got.Items...)
	}
	// The Cluster column is for the read that does not name exactly one: with a
	// name it would say the same thing on every row.
	cols := []view.Column{{Name: "Volume"}, {Name: "Instance"}, {Name: "Holds"},
		{Name: "Status", Kind: view.KindStatus},
		{Name: "Requested", Kind: view.KindBytes}, {Name: "Capacity", Kind: view.KindBytes},
		{Name: "Class"}, {Name: "Phase"}}
	showCluster := len(names) != 1
	if showCluster {
		cols = append([]view.Column{{Name: "Cluster"}}, cols...)
	}
	t := view.Table{Columns: s.leading(cols...)}
	for _, p := range pvcsFor(items) {
		cells := []view.Cell{
			view.Plain(p.Metadata.Name), view.Plain(p.instance()), view.Plain(p.holds()), view.Plain(p.status()),
			sizeCell(p.requested()), sizeCell(p.actual()),
			view.Plain(orDash(p.Spec.StorageClassName)), view.Plain(orDash(p.Status.Phase)),
		}
		if showCluster {
			cells = append([]view.Cell{view.Plain(p.owner())}, cells...)
		}
		t.Add(lead(s, view.Plain(p.Metadata.Namespace), cells...)...)
	}
	t.Total = len(t.Rows)
	if len(t.Rows) == 0 && r.answered() {
		t.Empty = emptyVolumes(names, r.read)
	}
	t.Warnings = r.warnings()

	if problems := storageProblems(s, items, r.unread); len(problems.Rows) > 0 {
		return view.Sections{Items: []view.Section{
			{Title: "Volumes", View: t},
			{Title: "Needs attention", View: problems},
		}}, nil
	}
	return t, nil
}

// emptyVolumes is the sentence an empty read carries (view.Table.Empty). It says
// what was looked for and where, and which call shows where the clusters are.
func emptyVolumes(names []string, s selection) string {
	return "no volumes labelled " + volumeSelector(names) + " in " + s.where() + " — " +
		s.caller().listEverywhere() + " shows which clusters exist, and in which namespace"
}

// volumeSelector is the label selector for the clusters named: the label's
// existence for none, its value for one, and a set for several.
func volumeSelector(names []string) string {
	switch len(names) {
	case 0:
		return pvcSelector
	case 1:
		return pvcSelector + "=" + names[0]
	}
	return pvcSelector + " in (" + strings.Join(names, ",") + ")"
}

// storageProblems is what needs attention: the namespaces that could not be read
// first, because a namespace missing from the read is a larger problem than any
// volume in it, and then each volume that is not as it was asked for.
func storageProblems(s selection, items []pvc, unread []outcome[pvcList]) view.Table {
	t := view.Table{Columns: s.leading(
		view.Column{Name: "Volume"}, view.Column{Name: "Status", Kind: view.KindStatus}, view.Column{Name: "Detail"},
	)}
	for _, o := range unread {
		t.Rows = append(t.Rows, lead(s, o.scope.namespace, "—", "fail", o.reason()))
	}
	for _, p := range pvcsFor(items) {
		switch {
		case !p.bound():
			t.Rows = append(t.Rows, lead(s, p.Metadata.Namespace, p.Metadata.Name, "fail",
				"phase "+orDash(p.Status.Phase)+" — nothing is behind this claim, so the "+
					"instance it belongs to cannot start"))
		case p.short():
			t.Rows = append(t.Rows, lead(s, p.Metadata.Namespace, p.Metadata.Name, "warn",
				"asked for "+sizeText(p.requested())+" and has "+sizeText(p.actual())+
					" — an expansion that has not finished, or a class that will not resize"))
		}
	}
	t.Total = len(t.Rows)
	return t
}
