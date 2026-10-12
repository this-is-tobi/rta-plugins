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

func (p pvc) requested() string { return orDash(p.Spec.Resources.Requests["storage"]) }
func (p pvc) actual() string    { return orDash(p.Status.Capacity["storage"]) }
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
// Compared as strings, deliberately. Kubernetes quantities are a grammar of
// their own ("7Gi", "7000M", "7e9") and parsing them to compare would mean
// carrying that grammar, for a check whose whole job is to notice that two
// values a controller normally keeps identical have stopped being identical.
// Two spellings of one size read as short here; that is a visible, harmless
// false positive on a page that also prints both numbers, and the alternative
// is a quantity parser nobody asked for.
func (p pvc) short() bool {
	req, got := p.requested(), p.actual()
	return p.bound() && req != "—" && got != "—" && req != got
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
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "`cnpg.status` reports the storage the spec asks for. This reports what " +
			"the cluster got, which is a different question the moment anything has gone " +
			"wrong: a claim still Pending, one whose capacity came back smaller than " +
			"requested, and an expansion that never finished all look identical in the " +
			"spec, and each is a database about to stop writing.\n\n" +
			"With no `cluster` it reads the volumes of every CloudNativePG cluster in the " +
			"namespace and adds a Cluster column; `all-namespaces` reads every namespace " +
			"and adds a leading Namespace column. Only volumes CloudNativePG labelled " +
			"are read, never the rest of a namespace's storage.\n\n" +
			"**It does not report how full a volume is.** That comes from the kubelet's " +
			"own stats endpoint through the node proxy — a different mechanism and a " +
			"different permission, and one that does not survive every proxy people put " +
			"in front of a cluster, which is the property this plugin is built around. A " +
			"column that looked like usage and was capacity would be worse than no " +
			"column. `kube.pvc.usage` reports it, graded and worst first, for whoever " +
			"holds nodes/proxy.",
		Agent: "Reports what the cluster got, not what its spec asks for: a claim still Pending, one " +
			"smaller than requested and an expansion that never finished show here and nowhere in the " +
			"spec. One row per volume, data and WAL; without `cluster`, every cluster's, and " +
			"`all-namespaces` reads every namespace. It does not report how full a volume is.",
		Run: runStorage,
	}, clusterField("the cluster whose volumes to read — every cluster's when omitted", false), allNamespacesField())
}

func runStorage(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := strings.TrimSpace(req.String("cluster"))
	if verr := checkName("cluster", name); verr != nil {
		return nil, verr
	}
	var list pvcList
	// The label selector goes to the API server rather than being filtered
	// here, because a namespace's other volumes are not this call's business
	// and reading them would be reading somebody else's storage layout to
	// throw it away. Named, it is the one cluster's label value; unnamed it is
	// the label's existence, which is every volume CloudNativePG made and none
	// it did not — the same line, drawn at the label rather than at a value.
	selector := pvcSelector
	if name != "" {
		selector += "=" + name
	}
	if verr := getResource(ctx, s, "persistentvolumeclaims", "", &list, "-l", selector); verr != nil {
		return nil, verr
	}
	if len(list.Items) == 0 {
		return view.Text{Body: emptyStorageBody(name, s)}, nil
	}

	// The Cluster column is for the read that does not name one: with a name
	// it would say the same thing on every row.
	cols := []view.Column{{Name: "Volume"}, {Name: "Instance"}, {Name: "Holds"},
		{Name: "Status", Kind: view.KindStatus},
		{Name: "Requested"}, {Name: "Capacity"}, {Name: "Class"}, {Name: "Phase"}}
	if name == "" {
		cols = append([]view.Column{{Name: "Cluster"}}, cols...)
	}
	t := view.Table{Columns: s.leading(cols...)}
	for _, p := range pvcsFor(list.Items) {
		cells := []string{
			p.Metadata.Name, p.instance(), p.holds(), p.status(),
			p.requested(), p.actual(), orDash(p.Spec.StorageClassName), orDash(p.Status.Phase),
		}
		if name == "" {
			cells = append([]string{p.owner()}, cells...)
		}
		t.Rows = append(t.Rows, s.lead(p.Metadata.Namespace, cells...))
	}
	t.Total = len(t.Rows)

	if problems := storageProblems(s, list.Items); len(problems.Rows) > 0 {
		return view.Sections{Items: []view.Section{
			{Title: "Volumes", View: t},
			{Title: "Needs attention", View: problems},
		}}, nil
	}
	return t, nil
}

// emptyStorageBody says what was looked for and where, and which call shows
// where the clusters are.
func emptyStorageBody(name string, s selection) string {
	what := "No volumes labelled " + pvcSelector
	if name != "" {
		what += "=" + name
	}
	return what + " in " + s.where() + ".\n\n" +
		s.caller().listEverywhere() + " shows which clusters exist, and in which namespace."
}

func storageProblems(s selection, items []pvc) view.Table {
	t := view.Table{Columns: s.leading(
		view.Column{Name: "Volume"}, view.Column{Name: "Status", Kind: view.KindStatus}, view.Column{Name: "Detail"},
	)}
	for _, p := range pvcsFor(items) {
		switch {
		case !p.bound():
			t.Rows = append(t.Rows, s.lead(p.Metadata.Namespace, p.Metadata.Name, "fail",
				"phase "+orDash(p.Status.Phase)+" — nothing is behind this claim, so the "+
					"instance it belongs to cannot start"))
		case p.short():
			t.Rows = append(t.Rows, s.lead(p.Metadata.Namespace, p.Metadata.Name, "warn",
				"asked for "+p.requested()+" and has "+p.actual()+
					" — an expansion that has not finished, or a class that will not resize"))
		}
	}
	t.Total = len(t.Rows)
	return t
}
