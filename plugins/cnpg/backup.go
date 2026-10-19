package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The slice of the CloudNativePG Backup resource this reads, and the one it
// writes.
//
// Every field below was read off the CRD schema on a running operator
// (`kubectl get crd backups.postgresql.cnpg.io -o json`) rather than from
// memory — the same standard cluster.go holds itself to, and the reason it
// matters here is sharper: this is the one place in the plugin that *creates*
// an object, and a field invented from recall is a document the API server
// either rejects or, worse, accepts and ignores.
//
// **The credential fields are deliberately not decoded, and that is a rule
// rather than an omission.** A Backup's status carries `s3Credentials`,
// `azureCredentials`, `googleCredentials` and `endpointCA` — each an object of
// secret *references* rather than secret values, which is why reading them
// would leak nothing today. It is skipped anyway, on cluster.go's own
// principle that what is not decoded cannot be misinterpreted: the names of
// the secrets holding a cluster's object-store keys are not part of any
// question somebody opens a backup listing to ask, and a struct that does not
// have them cannot grow a view that prints them.

// backupCRD is the fully-qualified resource name, for the reason clusterCRD
// spells out: `backup` as a short name means whatever the cluster's discovery
// document says it means today.
const backupCRD = "backups.postgresql.cnpg.io"

type backupList struct {
	Items []backupObject `json:"items"`
}

type backupObject struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
		Method string `json:"method,omitempty"`
		Target string `json:"target,omitempty"`
		// The plugin a Backup asks, by name: all of its pluginConfiguration that
		// is decoded, since the rest is a free-form map a plugin defines.
		PluginConfiguration struct {
			Name string `json:"name"`
		} `json:"pluginConfiguration"`
		// A pointer for the reason EnableSuperuserAccess is one: absent means
		// "whatever .spec.backup.volumeSnapshot.online says", which is not
		// the same statement as false, and encoding a guess would silently
		// turn somebody's hot backup cold.
		Online *bool `json:"online,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
		Error string `json:"error"`
		// Method is echoed back resolved: the spec's may be empty, meaning
		// "the cluster decides", and this is what the cluster decided.
		Method          string `json:"method"`
		BackupID        string `json:"backupId"`
		BackupName      string `json:"backupName"`
		DestinationPath string `json:"destinationPath"`
		ServerName      string `json:"serverName"`
		StartedAt       string `json:"startedAt"`
		StoppedAt       string `json:"stoppedAt"`
		// The WAL the backup spans, which the operator records on the Backup
		// and nowhere on the Cluster: the log position and the file it is in
		// at the start of the backup and at the end. A restore replays from the
		// begin position and is consistent once it has replayed the end one. Each
		// is empty until the instance has reported it, so a backup that failed
		// before it began has none.
		BeginLSN   string `json:"beginLSN"`
		EndLSN     string `json:"endLSN"`
		BeginWal   string `json:"beginWal"`
		EndWal     string `json:"endWal"`
		InstanceID struct {
			PodName string `json:"podName"`
		} `json:"instanceID"`
	} `json:"status"`
}

// walRange is the WAL a Backup records itself as spanning, each value as the
// operator wrote it and trimmed of nothing else: a position is a position and
// a file name is a file name, and neither is shortened to fit a column.
type walRange struct {
	beginLSN, endLSN, beginWal, endWal string
}

func (b backupObject) walRange() walRange {
	s := b.Status
	return walRange{
		beginLSN: strings.TrimSpace(s.BeginLSN), endLSN: strings.TrimSpace(s.EndLSN),
		beginWal: strings.TrimSpace(s.BeginWal), endWal: strings.TrimSpace(s.EndWal),
	}
}

// recorded reports whether the Backup says anything of the WAL it spans.
func (r walRange) recorded() bool {
	return r.beginLSN != "" || r.endLSN != "" || r.beginWal != "" || r.endWal != ""
}

// The phases a Backup passes through.
//
// The CRD declares `status.phase` as a bare string with no enum, so these are
// matched rather than exhausted: an unrecognised phase renders as itself and
// grades neutral, which is the behaviour that survives the operator learning a
// new one.
const (
	phaseCompleted = "completed"
	phaseFailed    = "failed"
)

func (b backupObject) failed() bool { return strings.EqualFold(b.Status.Phase, phaseFailed) }
func (b backupObject) done() bool   { return strings.EqualFold(b.Status.Phase, phaseCompleted) }

// status grades one backup for the table's status column.
func (b backupObject) status() string {
	switch {
	case b.failed():
		return "fail"
	case b.done():
		return "ok"
	}
	// Running, pending, walArchivingLaunched — in flight is not a problem,
	// and colouring it warn would make every backup look wrong while it works.
	return "info"
}

// took is how long the backup ran, or how long it has been running.
func (b backupObject) took() string {
	start, ok := parseWhen(b.Status.StartedAt)
	if !ok {
		return "—"
	}
	if stop, ended := parseWhen(b.Status.StoppedAt); ended {
		return span(stop.Sub(start))
	}
	return span(time.Since(start)) + " so far"
}

// where names the destination, and says who chose it.
//
// The destination is never rta's to choose and never a caller's: a Backup
// carries a cluster reference and nothing else, so where the bytes land is
// whatever that cluster's own configuration already said. This row exists to
// make that visible rather than to be configurable.
func (b backupObject) where() string {
	path := strings.TrimSpace(b.Status.DestinationPath)
	if path == "" {
		if m := b.resolvedMethod(); m == "volumeSnapshot" {
			return "a volume snapshot in this cluster"
		}
		return "—"
	}
	if s := strings.TrimSpace(b.Status.ServerName); s != "" {
		return path + " (server " + s + ")"
	}
	return path
}

// resolvedMethod prefers what the operator decided over what was asked for,
// because an empty spec.method means "the cluster decides" and the answer to
// "how was this taken" is the decision, not the blank.
func (b backupObject) resolvedMethod() string {
	if m := strings.TrimSpace(b.Status.Method); m != "" {
		return m
	}
	return strings.TrimSpace(b.Spec.Method)
}

// backupsFor filters a listing to the clusters named, newest first; none named
// keeps every cluster's, and a blank is no name.
//
// Filtered on `spec.cluster.name` rather than a label selector. CNPG does
// label its Backup objects, but the label is the operator's own bookkeeping
// and the field is the one the CRD declares as a printer column — so this
// filters on the thing the resource is documented to mean rather than on an
// implementation detail that has no compatibility promise.
func backupsFor(items []backupObject, clusters ...string) []backupObject {
	clusters = slices.DeleteFunc(slices.Clone(clusters), func(name string) bool { return name == "" })
	out := make([]backupObject, 0, len(items))
	for _, b := range items {
		if len(clusters) == 0 || slices.Contains(clusters, b.Spec.Cluster.Name) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, z := out[i].Metadata.CreationTimestamp, out[j].Metadata.CreationTimestamp
		if !a.Equal(z) {
			return a.After(z)
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out
}

// backupListLimit is how many backups a listing returns unless told otherwise.
//
// A cluster with a daily schedule and a month of retention has thirty or so,
// and a namespace of clusters has hundreds, which is what a read of every
// namespace makes of it. The listing used to return every Backup it was
// handed, which was fine for the one namespace it was built for and is not a
// bound anything can rely on once it has a wide form: an agent's answer is
// paid for in context by the row.
const backupListLimit = 100

func backupListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "cnpg.backup.list",
		Summary:  "Backups taken of a cluster: when, how, where they went, and what failed",
		Keywords: []string{"barman", "snapshot", "restore", "pitr", "archive"},
		Examples: []plugin.Example{
			{Title: "the backups of one cluster", Inputs: map[string]any{"cluster": "app-db", "namespace": "production"}},
			{Title: "every backup in every namespace", Inputs: map[string]any{"all-namespaces": true}},
			{Title: "the backups of two clusters in two namespaces", Inputs: map[string]any{
				"cluster": []string{"app-db", "billing-db"}, "namespace": []string{"production", "staging"}}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "The Backup objects themselves, which the Cluster resource does not " +
			"carry. `cnpg.status` answers 'is this cluster backed up' from the summary " +
			"fields the operator keeps for the mechanisms it runs itself — last success, " +
			"last failure, first recoverable point — which is the right answer for those " +
			"and no answer for a cluster that backs up through a plugin, whose backups " +
			"are recorded only here. This is the other question: which individual " +
			"backups exist, how each was taken, how long it took, where the bytes went, " +
			"and what the operator said when one failed. The WAL a backup spans is not " +
			"in this table, which stays the same table whatever the backups record: " +
			"`cnpg.backup.show` gives the range of the newest completed one.\n\n" +
			"The context's own namespace by default; `namespace` is a list of at most 30, " +
			"each read with one call of its own (so a Role per namespace is enough), and " +
			"`all-namespaces` reads every one in one call. Over several namespaces a leading Namespace " +
			"column says which, and a namespace that cannot be read is a row of what " +
			"failed, and a warning, while the others answer. `cluster` is a list of at " +
			"most 30 and keeps the backups of the clusters it names, from the backups of " +
			"each namespace, which are listed whole. " + listsBelongInTheCall + " Bounded by " +
			"`limit`: the list is cut, across all of them, at that many " +
			"rows, `total` stays the exact count there is, and a cut list says so in a " +
			"warning and puts the failed backups first, so the rows that matter are not " +
			"the ones the cut drops.\n\n" +
			"Credentials are not read. A Backup's status carries the object-store " +
			"credential references its cluster was configured with, and this decodes none " +
			"of them — the names of the secrets holding your keys are not part of any " +
			"question a backup listing answers.",
		Agent: "One row per Backup object, newest first: the cluster, status, phase, method, how long it " +
			"took, its age and where it went, with a failed backup's error in its own section. This is the " +
			"record for a cluster that backs up through a plugin, which cnpg.status cannot see. `namespace` and " +
			"`cluster` are arrays of at most 30, none meaning the context's own namespace and every cluster; " +
			"each namespace is one call, `all-namespaces` reads every one, and over several a Namespace column " +
			"leads and one that could not be read is a row of What failed. Bounded by `limit`: `total` is the " +
			"exact count, and a cut list warns it is partial and puts the failed first. No credentials are read.",
		Run: runBackupList,
	},
		clustersField("only these clusters' backups — every cluster's when omitted"),
		namespacesField(),
		allNamespacesField(),
		plugin.Field{Name: "limit", Type: plugin.Int, Config: "limit", Default: backupListLimit, Min: 1, Max: 10000,
			Help: "how many backups to list; a cut list puts the failed ones first"})
}

func runBackupList(ctx context.Context, req plugin.Request) (view.View, error) {
	s, names, verr := listingOf(req)
	if verr != nil {
		return nil, verr
	}
	r, verr := settle(s, readEach(ctx, s.scopes(), func(ctx context.Context, scope selection) (backupList, *view.Error) {
		return fetch[backupList](ctx, scope, backupCRD, "")
	}))
	if verr != nil {
		return nil, verr
	}
	var items []backupObject
	for _, o := range r.outcomes {
		items = append(items, o.got.Items...)
	}
	all := backupsFor(items, names...)
	rows, cut := cutBackups(all, req.Int("limit"))

	t := view.Table{Columns: s.leading(
		view.Column{Name: "Backup"}, view.Column{Name: "Cluster"}, view.Column{Name: "Status", Kind: view.KindStatus},
		view.Column{Name: "Phase"}, view.Column{Name: "Method"}, view.Column{Name: "Took"},
		view.Column{Name: "Age"}, view.Column{Name: "Where"},
	)}
	for _, b := range rows {
		t.Rows = append(t.Rows, s.lead(b.Metadata.Namespace,
			b.Metadata.Name, b.Spec.Cluster.Name, b.status(), orDash(b.Status.Phase),
			orDash(b.resolvedMethod()), b.took(), age(b.Metadata.CreationTimestamp), b.where(),
		))
	}
	// The total is every backup there is, not the rows on hand, so a cut list
	// says how much of the whole it is.
	t.Total = len(all)
	if len(all) == 0 && r.answered() {
		t.Empty = emptyBackups(names, r.read)
	}
	if cut {
		t.Warnings = append(t.Warnings, partialBackups(s, len(rows), len(all)))
	}
	t.Warnings = append(t.Warnings, r.warnings()...)
	// The error is its own section rather than a ninth column: a barman
	// failure is a paragraph of somebody else's stderr, and a table that has
	// to hold one is a table with eight unreadable columns.
	//
	// **The WAL a backup spans is not here at all.** It is two positions and two
	// file names, up to 17, 17, 24 and 24 characters that the eight columns have
	// no room for, and every completed backup records one, so a section for it
	// would turn the common read from a table into a page: a listing to copy, to
	// select a row in and to read as one JSON shape would become something else
	// for every healthy cluster. It is the reference for the one backup somebody
	// restores from, and cnpg.backup.show gives it for the newest completed one.
	sections := []view.Section{{Title: "Backups", View: t}}
	if problems := backupErrors(s, rows, r.unread); len(problems.Rows) > 0 {
		sections = append(sections, view.Section{Title: "What failed", View: problems})
	}
	if len(sections) == 1 {
		return t, nil
	}
	return view.Sections{Items: sections}, nil
}

// cutBackups keeps limit of the backups, and when it has to drop any it drops
// the ones nobody opened a listing to find.
//
// **A cut that drops by age drops the failures.** The listing is newest first,
// and the backups somebody needs from it are the ones that failed — which, on a
// cluster that has been failing a while, are not the newest, and on a read of
// every namespace are one cluster's among a hundred healthy ones. So a list
// that has to be cut puts the failed ones first, each group still newest
// first, and the cut takes what room is left from the rest. A list that fits is
// left in time order, because there nothing is lost by it.
func cutBackups(rows []backupObject, limit int) (kept []backupObject, cut bool) {
	// The host holds limit to its range; a direct call of the handler is the
	// only way to arrive here without one.
	if limit <= 0 {
		limit = backupListLimit
	}
	if len(rows) <= limit {
		return rows, false
	}
	ordered := make([]backupObject, 0, len(rows))
	for _, wantFailed := range []bool{true, false} {
		for _, b := range rows {
			if b.failed() == wantFailed {
				ordered = append(ordered, b)
			}
		}
	}
	return ordered[:limit], true
}

// partialBackups is the warning a cut list carries, in the words of the
// surface that asked: the table alone is a shorter list, and a shorter list
// reads as the whole of a smaller thing.
func partialBackups(s selection, shown, total int) view.Error {
	return view.Error{
		Code: "cnpg.backup.list.partial",
		Message: fmt.Sprintf("showing %d of %s, the failed ones first and then the newest",
			shown, format.CountOf(total, "backup")),
		Hint: "raise " + s.sf.InputName("limit") + ", or narrow the list with " +
			s.sf.InputName("cluster") + " or " + s.sf.InputName("namespace"),
	}
}

// backupErrors is the section of what failed: the namespaces that could not be
// read first, because a namespace missing from the listing is a larger failure
// than any backup in it, and then each failed backup's error.
func backupErrors(s selection, rows []backupObject, unread []outcome[backupList]) view.Table {
	t := view.Table{Columns: s.leading(
		view.Column{Name: "Backup"}, view.Column{Name: "Status", Kind: view.KindStatus}, view.Column{Name: "Error"},
	)}
	for _, o := range unread {
		t.Rows = append(t.Rows, s.lead(o.scope.namespace, "—", "fail", o.reason()))
	}
	for _, b := range rows {
		if msg := strings.TrimSpace(b.Status.Error); msg != "" {
			t.Rows = append(t.Rows, s.lead(b.Metadata.Namespace, b.Metadata.Name, "fail", firstLine(msg)))
		}
	}
	t.Total = len(t.Rows)
	return t
}

// emptyBackups is the sentence an empty listing carries (view.Table.Empty). It
// tells the two empties apart, because they send somebody to different places:
// a cluster nothing has ever backed up is a configuration question, and a
// namespace with no Backup objects at all may simply be one where the schedule
// has not fired yet.
//
// Read across every namespace there is no one place to ask whether a cluster
// is configured, so the pointer is the listing of the clusters instead.
//
// **The pointer carries the namespaces that were read, and never the context's
// own.** cnpg.status takes one cluster in one namespace, so it is the pointer for
// exactly that read; for several clusters or several namespaces it is the
// listing of those clusters in those namespaces, which says where each is and
// what its Backup column makes of it, in one call. A pointer without the
// namespace reads the context's own, where the cluster may not be.
func emptyBackups(clusters []string, s selection) string {
	if len(clusters) == 0 {
		return "no CloudNativePG backups in " + s.where()
	}
	of := "no backups of " + strings.Join(clusters, ", ") + " in " + s.where() + " — "
	switch {
	case s.allNamespace:
		return of + s.caller().listEverywhere() + " shows where a cluster of " + thatName(clusters) + " is"
	case len(clusters) == 1 && len(s.namespaces) == 0:
		status := append([]plugin.Arg{{Name: "cluster", Value: clusters[0]}}, s.callArgs(s.namespace)...)
		return of + "`" + s.sf.Call("cnpg.status", status...) + "` says whether anything is configured to take one"
	}
	where := "it is"
	if len(clusters) > 1 {
		where = "they are"
	}
	return of + "`" + s.sf.Call("cnpg.list", s.listingArgs(clusters)...) + "` shows where " + where +
		" and whether anything is configured to take a backup"
}

// thatName is "that name" for one cluster and "those names" for several.
func thatName(clusters []string) string {
	if len(clusters) == 1 {
		return "that name"
	}
	return "those names"
}

func backupRequestCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:      "cnpg.backup.request",
		Summary: "Ask the operator to back a cluster up now, using that cluster's own configuration",
		Examples: []plugin.Example{
			{Title: "back one cluster up now", Inputs: map[string]any{"cluster": "app-db", "namespace": "production"}},
			{Title: "back one up through its WAL-archiver plugin", Inputs: map[string]any{"cluster": "app-db", "namespace": "production", "method": "plugin"}},
		},
		Keywords: []string{"barman", "snapshot", "trigger", "volumesnapshot"},
		Safety:   plugin.Write,
		// Not idempotent, and visibly so: two calls are two backups, each
		// costing a full copy of the database in whatever the cluster pays
		// for storage.
		Idempotent: false,
		// A grant, even though Write already keeps this off the default MCP
		// surface — `--allow-write cnpg` is what puts it there at all. The two
		// gates answer different questions and the operator wants both: the
		// flag is "this plugin may change things on this server", said once at
		// launch, and the grant is "this agent may back up this cluster until
		// this time", said per agent and expiring on its own.
		NeedsGrant: true,
		// The cluster, so a grant to back up one database is not a grant to
		// back up every database the kubeconfig can reach.
		//
		// **A cluster name and not a namespace, with a residual worth stating
		// plainly**: the scope is one input's value, so a grant naming
		// `shop-db` does not distinguish two namespaces that each hold a
		// cluster by that name. That is the same shape and the same tradeoff
		// plugins/kube accepted for `kube.serviceaccount.revoke`, and it is
		// tolerable here for a reason specific to this capability — the
		// backup goes wherever that cluster's own spec already said, which no
		// caller chooses and no caller sees, and the operation removes
		// nothing and reveals nothing. The cost of a confusion is an
		// unexpected backup, not an unexpected disclosure.
		Scope: "cluster",
		Description: "Creates a Backup object. **rta does not take the backup and does not " +
			"choose where it goes** — the operator does both, using the configuration " +
			"already on the cluster: destination, credentials, retention and encryption " +
			"all come from `.spec.backup` or from the WAL-archiver plugin the cluster " +
			"names. A Backup carries a cluster reference and nothing else, which is what " +
			"makes this safe to expose at all: there is no destination for a caller to " +
			"point somewhere useful to them.\n\n" +
			"Refused when the cluster configures no backup at all. CloudNativePG accepts " +
			"such a Backup and lets it fail asynchronously — verified against a running " +
			"operator, which admits the object with no complaint — so the failure would " +
			"surface minutes later in a place nobody is looking. rta reads the cluster " +
			"first and says so instead.\n\n" +
			"`method` says how, and when it is left out rta states the one mechanism the " +
			"cluster is set up for, and the receipt says rta chose it; a cluster set up for " +
			"several is refused until one is named. `plugin` asks the WAL-archiver plugin " +
			"the cluster names, whose name is copied from the cluster and is never a " +
			"caller's to give. It is refused up front, rather than accepted and failed " +
			"minutes later, when that plugin has declared it cannot take a backup or the " +
			"ObjectStore it names does not exist; the receipt says where the backup goes. " +
			"`target` and `online` override what the cluster settled on, and are optional: " +
			"left out they mean 'do what you would have done anyway'.",
		Agent: "Creates a Backup object for one cluster. The operator takes the backup and chooses where it " +
			"goes, from the cluster's own backup configuration: nothing here sets a destination, and " +
			"the receipt says where. `method` is the cluster's only configured mechanism when omitted " +
			"(the receipt says rta chose it) and must be named when it has several; `plugin` asks the " +
			"WAL-archiver plugin the cluster names. Refused when the cluster configures no backup, or its " +
			"plugin cannot back up or its ObjectStore is missing. Not idempotent: each call makes " +
			"another backup. Needs a grant naming the cluster.",
		Run: runBackupRequest,
	},
		clusterField("the cluster to back up", true),
		// The three overrides carry the CRD's own enums, so a wrong value is
		// refused by rta with the list in hand rather than by the API server
		// with a schema error.
		plugin.Field{Name: "method", Type: plugin.String, Config: "backup.method",
			Options: backupMethods,
			Help:    "how to take it — its only configured mechanism when omitted; plugin asks its WAL-archiver plugin"},
		plugin.Field{Name: "target", Type: plugin.String, Config: "backup.target",
			Options: []string{"primary", "prefer-standby"},
			Help:    "which instance performs it — the cluster's own choice when omitted"},
		plugin.Field{Name: "online", Type: plugin.String, Config: "backup.online",
			Options: []string{"true", "false"},
			Help: "hot or cold — only with a `method` of volumeSnapshot, and the cluster's " +
				"own choice when omitted"})
}

// backupRequest is the object sent to the API server.
//
// Written as a struct with omitempty rather than a map, so that a field
// nobody asked about is absent from the document rather than present and
// empty. That distinction is the whole contract of this capability: an absent
// `target` means "the cluster decides" and an empty one is a value CNPG has to
// interpret. (An absent `method` meant something else, a fixed default, and rta
// no longer leaves it absent: see resolveMethod.)
type backupRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace,omitempty"`
		Labels    map[string]string `json:"labels,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
		Method string `json:"method,omitempty"`
		// PluginConfiguration is what CloudNativePG's webhook requires of a Backup
		// of method plugin, and refuses one without. Its name is the cluster's own
		// enabled WAL-archiver entry, copied; no input of this capability can set
		// it, so a caller can ask for the plugin the cluster names and no other.
		PluginConfiguration *backupPluginConfiguration `json:"pluginConfiguration,omitempty"`
		Target              string                     `json:"target,omitempty"`
		Online              *bool                      `json:"online,omitempty"`
	} `json:"spec"`

	// methodByRta records that the method in the document is one rta resolved
	// from the cluster and not one the caller stated, so a receipt says who
	// chose it. It is no part of the document.
	methodByRta bool
}

// backupPluginConfiguration is `spec.pluginConfiguration` of a Backup, and the
// name is all of it that is sent.
//
// The CRD also has a `parameters` map, "the configuration parameters passed to
// the backup plugin for this backup", and it is left out on purpose. The
// barman-cloud plugin's own documentation shows a Backup with the name alone;
// the plugin reads which ObjectStore to use from the Cluster and not from the
// Backup; and the parameters of the cluster's entry configure its WAL archiving,
// which is a different thing from a parameter of one backup. Copying them across
// would put whatever a plugin was given for one purpose into an object made for
// another, and a plugin's parameters can be anything.
type backupPluginConfiguration struct {
	Name string `json:"name"`
}

// backupMethods are the methods rta can build a whole Backup for, and they are
// the CRD's three — the list the declaration offers and the check holds a value
// to, stated once so the two cannot drift.
//
// **`plugin` was left out of this list while rta built no pluginConfiguration,
// and is in it now that it does.** The operator's own webhook refuses a Backup
// of that method with no `spec.pluginConfiguration`, put to a running one with
// `kubectl create --dry-run=server`:
//
//	spec.pluginConfiguration: Invalid value: null: cannot be empty when the
//	backup method is plugin
//
// Its validation reads the configuration as empty when it is absent or has no
// name, so a name is what completes the document — and the name is the cluster's
// own WAL-archiver entry, copied, which is why the method can be offered at all:
// there is no input through which a caller could name another plugin. A cluster
// with no such entry has nothing to copy, and is refused (canTake).
var backupMethods = []string{"barmanObjectStore", "volumeSnapshot", "plugin"}

// requestedBy labels what rta asked for, so an operator finding an unexpected
// backup can tell where it came from without reading an audit log on another
// machine. It records the tool and nothing about who ran it: rta's ledger is
// where "which agent, under which grant" is answered, and copying an identity
// into a cluster object would put it somewhere with different readers and no
// expiry.
const requestedBy = "app.kubernetes.io/created-by"

func runBackupRequest(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := strings.TrimSpace(req.String("cluster"))
	if verr := checkName("cluster", name); verr != nil {
		return nil, verr
	}
	if name == "" {
		return nil, view.Errorf("cnpg.backup.nocluster", "no cluster named").
			WithHint(s.caller().listEverywhere() + " shows what is there")
	}

	// The cluster is read before anything is written, and it earns its round
	// trip three times over: it proves the cluster exists (a Backup naming a
	// missing cluster is accepted and fails later), it proves something is
	// configured to perform the backup, and it is what the receipt quotes
	// when it says where the bytes are going.
	var c cluster
	if verr := getJSON(ctx, s, name, &c); verr != nil {
		return nil, verr
	}
	if !c.backupConfigured() {
		return nil, view.Errorf("cnpg.backup.unconfigured",
			"%s configures no backup, so nothing would perform this one",
			c.Metadata.Namespace+"/"+c.Metadata.Name).
			WithHint("the cluster needs a `.spec.backup` stanza, or a WAL-archiver entry " +
				"in `.spec.plugins` — CloudNativePG accepts a Backup either way and fails " +
				"it minutes later, which is why rta stops here instead")
	}

	doc, b, verr := buildBackupRequest(req, c, s)
	if verr != nil {
		return nil, verr
	}
	// The second half of the same pre-flight: the method has to be one this
	// cluster is set up for, or it would be accepted, then failed minutes later,
	// exactly the shape the unconfigured check above stops.
	if !c.canTake(b.Spec.Method) {
		return nil, methodRefusal(req.Surface(), c, b.Spec.Method)
	}
	where := whereOf(c, b.Spec.Method)
	if b.Spec.Method == "plugin" {
		var verr *view.Error
		if where, verr = pluginDestination(ctx, s, c); verr != nil {
			return nil, verr
		}
	}
	if req.DryRun {
		return dryRunView(b, c, doc, where), nil
	}

	var created backupObject
	if verr := createJSON(ctx, s, doc, &created); verr != nil {
		return nil, verr
	}
	return requestReceipt(s, created, b, c, where), nil
}

// pluginDestination is the pre-flight of a backup that asks a plugin, and it
// says where the bytes will go.
//
// **Everything it refuses, the operator would have accepted.** A Backup whose
// plugin cannot take it, or whose ObjectStore is not there, is admitted without
// a word and fails minutes later in a place nobody is looking, with rta's
// receipt already saying it had asked. So the two things that can be known now
// are checked now.
//
// The plugin has to have declared it can back up — the operator itself refuses
// to hand it a Backup otherwise, so a status that names the plugin and lists no
// such capability is a certain failure. A status that does not name it at all is
// not (pluginDeclaresBackup), and is let through.
//
// And the ObjectStore the cluster's entry names has to exist in the cluster's
// namespace. One that is absent, or whose type is, is refused. One this identity
// may not read is let through, since that is a gap in what can be seen and not a
// fact about the store, with the destination worded as what it is: the store's
// name, not read. Any other failure of the read is the call's, since a write
// should not follow a question that could not be answered.
func pluginDestination(ctx context.Context, s selection, c cluster) (string, *view.Error) {
	archiver := c.walArchiverPlugin()
	if declared, known := c.pluginDeclaresBackup(); known && !declared {
		return "", view.Errorf("cnpg.backup.plugin.unsupported",
			"the %s plugin has not declared that it can take a backup", archiver).
			WithHint("the operator hands a plugin a Backup only when its status lists " + backupCapability +
				" among the plugin's backup capabilities, and this cluster's status lists none for it — " +
				"the Backup would be accepted and fail. Whether that plugin only archives WAL, or is " +
				"misconfigured, is its own documentation's to say")
	}
	ref := c.objectStoreName()
	if ref == "" {
		return "wherever the " + archiver + " plugin this cluster names sends it", nil
	}
	o, verr := readObjectStore(ctx, s, c, ref)
	switch {
	case verr == nil:
		return orDash(plainURL(o.Spec.Configuration.DestinationPath)) + " (ObjectStore " + ref +
			", server " + c.archiveServerName() + ")", nil
	case verr.Code == "cnpg.resource.missing":
		return "", view.Errorf("cnpg.backup.objectstore.missing",
			"the cluster names the ObjectStore %s, and there is none by that name in namespace %s",
			ref, c.Metadata.Namespace).
			WithHint("the plugin has nowhere to archive to, so the Backup would be accepted and fail — " +
				"create the ObjectStore, or correct the name in the cluster's `.spec.plugins` entry")
	case verr.Code == "cnpg.crd.missing", verr.Code == "cnpg.resource.unserved":
		return "", view.Errorf("cnpg.backup.objectstore.missing",
			"the cluster names the ObjectStore %s, and this cluster has no ObjectStore resource type", ref).
			WithHint("the barman-cloud plugin's CRD (" + objectStoreCRD + ") is not registered, so the plugin " +
				"is not installed and the Backup would be accepted and fail")
	case verr.Code == "cnpg.forbidden":
		return "ObjectStore " + ref + " (not read: " + verr.Message + ")", nil
	}
	return "", verr
}

// methodRefusal words the mismatch, and has a case for the cluster that
// configures a backup stanza naming no mechanism at all.
//
// That case looks like a hair-split and is not: `.spec.backup` carrying only
// a `retentionPolicy` passes backupConfigured — the stanza is there — and
// leaves configuredMethods empty, so the obvious message ("this cluster
// configures %s, pass --method %s" off the first entry) indexes an empty
// slice and takes the plugin down. Found by a fixture that turned out to be
// shaped exactly like it.
func methodRefusal(sf plugin.Surface, c cluster, asked string) *view.Error {
	named := asked + " backup"
	if asked == "" {
		named = "backup"
	}
	verr := view.Errorf("cnpg.backup.method.unconfigured",
		"%s is not set up to take a %s",
		c.Metadata.Namespace+"/"+c.Metadata.Name, named)
	have := c.configuredMethods()
	switch {
	case len(have) == 0:
		return verr.WithHint("its `.spec.backup` names no mechanism — it needs a " +
			"`barmanObjectStore` or a `volumeSnapshot` stanza under it before anything " +
			"can take a backup")
	case len(have) == 1 && have[0] == "plugin":
		// What a cluster on the newer arrangement gets when it is asked for the
		// in-tree method: no `.spec.backup` to perform it, and a plugin that
		// would, if asked by its method.
		return verr.WithHint("this cluster archives through the " + c.walArchiverPlugin() +
			" plugin, the newer arrangement, which replaces `.spec.backup` rather than " +
			"joining it — pass " + sf.InputTo("method", "plugin") + ", which asks that plugin to take it")
	}
	return verr.WithHint("this cluster configures " + strings.Join(have, " and ") +
		" — pass " + sf.InputTo("method", have[0]))
}

// resolveMethod is the method a request that names none is sent as: the one
// mechanism the cluster is set up for, stated in the Backup rather than left to
// CloudNativePG.
//
// **Leaving it out was never "the cluster decides".** The CRD says `spec.method`
// "Defaults to: `barmanObjectStore`" while `spec.target` right beside it says "If
// empty, it defaults to `cluster.spec.backup.target`". The two read alike and
// behave differently: a cluster set up for volume snapshots, or for a plugin,
// that was sent a Backup with no method got a barmanObjectStore one it could not
// perform — accepted, then failed minutes later, with rta's receipt having said
// the cluster chose. So an unstated method is resolved here, against what the
// cluster states, and the receipt says rta resolved it.
//
// One mechanism is a fact and resolves. None is the cluster's own problem and is
// refused with its hint. Several is a choice between destinations and costs that
// is not rta's to make for a backup that is a full copy — the caller names one,
// or pins it in the config (`backup.method`).
func resolveMethod(sf plugin.Surface, c cluster) (string, *view.Error) {
	have := c.configuredMethods()
	switch len(have) {
	case 1:
		return have[0], nil
	case 0:
		return "", methodRefusal(sf, c, "")
	}
	return "", view.Errorf("cnpg.backup.method.ambiguous",
		"%s is set up to take a backup %s, and none was named",
		c.Metadata.Namespace+"/"+c.Metadata.Name, strings.Join(have, " or ")).
		WithHint("rta will not choose between them for a backup that costs a full copy — name one with " +
			sf.InputName("method") + ", which takes " + strings.Join(have, ", "))
}

// buildBackupRequest assembles the document, and validates every value that
// goes into it before any of it is encoded.
func buildBackupRequest(req plugin.Request, c cluster, s selection) ([]byte, backupRequest, *view.Error) {
	var b backupRequest
	b.APIVersion, b.Kind = "postgresql.cnpg.io/v1", "Backup"
	// Named from the cluster and the minute, because a Backup's name is how
	// somebody finds it again and `backup-8f3a` is not that. Seconds are in
	// it so two requests a minute apart do not collide, which is the ordinary
	// spacing when the first one is retried.
	b.Metadata.Name = c.Metadata.Name + "-rta-" + time.Now().UTC().Format("20060102150405")
	// The namespace the cluster was actually read from, never the flag. They
	// agree whenever the flag was given, and when it was not this is the
	// context's own — which is the namespace the pre-flight read resolved, so
	// the object lands beside the cluster it names rather than wherever a
	// second resolution would have put it.
	b.Metadata.Namespace = c.Metadata.Namespace
	b.Metadata.Labels = map[string]string{requestedBy: "rta"}
	b.Spec.Cluster.Name = c.Metadata.Name

	method := strings.TrimSpace(req.String("method"))
	if verr := oneOf("method", method, backupMethods...); verr != nil {
		return nil, b, verr
	}
	if method == "" {
		var verr *view.Error
		if method, verr = resolveMethod(req.Surface(), c); verr != nil {
			return nil, b, verr
		}
		b.methodByRta = true
	}
	b.Spec.Method = method
	// The plugin's name is copied from the cluster's own enabled WAL-archiver
	// entry, and there is nowhere for a caller to give another: asking for the
	// method is asking for the plugin the cluster names. A cluster with no such
	// entry leaves it unset, and is refused by canTake before anything is sent.
	if name := c.walArchiverPlugin(); method == "plugin" && name != "" {
		b.Spec.PluginConfiguration = &backupPluginConfiguration{Name: name}
	}

	target := strings.TrimSpace(req.String("target"))
	if verr := oneOf("target", target, "primary", "prefer-standby"); verr != nil {
		return nil, b, verr
	}
	b.Spec.Target = target

	// `online` is a string of "true"/"false" rather than a Bool, because a
	// Bool has no way to say nothing: it arrives as false, and false is a
	// statement — "take this one cold" — that would silently override a
	// cluster configured for hot backups on every call that never mentioned
	// it. The three-state answer is the honest one, so the field carries it.
	switch online := strings.TrimSpace(req.String("online")); online {
	case "":
	case "true", "false":
		// **Only for a volumeSnapshot backup, which the CRD schema does not
		// say and a live operator does.** CNPG's webhook refuses `spec.online`
		// on any other method — "Online parameter can be specified only if the
		// backup method is volumeSnapshot". The method is the one that will be
		// sent, stated or resolved above, so a cluster set up for snapshots
		// alone is asked nothing more of a caller than the online flag. Found by
		// putting the document rta builds through a real API server; the schema
		// alone admits it happily.
		if method != "volumeSnapshot" {
			return nil, b, view.Errorf("cnpg.backup.online.unavailable",
				"online only means something for a volumeSnapshot backup").
				WithHint("pass " + req.Surface().InputTo("method", "volumeSnapshot") + " with it, or leave online out and " +
					"let the cluster's own `.spec.backup.volumeSnapshot.online` decide")
		}
		v := online == "true"
		b.Spec.Online = &v
	default:
		return nil, b, view.Errorf("cnpg.backup.online.invalid",
			"online is %q, which is neither true nor false", online)
	}

	doc, err := json.Marshal(b)
	if err != nil {
		return nil, b, view.Errorf("cnpg.backup.encode",
			"could not build the Backup object: %v", err)
	}
	return doc, b, nil
}

// oneOf refuses a value the CRD's own enum does not admit.
//
// Refused here as well as offered as Options, because Options is a
// suggestion the CLI and TUI render and not a constraint every surface
// enforces — and the alternative to refusing is the API server rejecting the
// document with a schema error that names a JSON path rather than a flag.
func oneOf(field, value string, allowed ...string) *view.Error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return view.Errorf("cnpg.backup."+field+".invalid",
		"%s is %q", field, value).
		WithHint("CloudNativePG admits " + strings.Join(allowed, ", ") +
			" — leaving it out means whatever the cluster already decided")
}

// dryRunView shows the exact document, because "what would this send" is the
// only question a dry run of a create is asked.
func dryRunView(b backupRequest, c cluster, doc []byte, where string) view.View {
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "would create", Value: b.Metadata.Namespace + "/" + b.Metadata.Name},
		{Key: "for cluster", Value: c.Metadata.Namespace + "/" + c.Metadata.Name},
		{Key: "how", Value: methodLine(b)},
		{Key: "where", Value: where},
		{Key: "which instance", Value: targetLine(b)},
		{Key: "the object", Value: string(doc)},
		{Key: "nothing was sent", Value: "this is a dry run — the cluster was read and not written"},
	}}
}

// requestReceipt says what was asked for, what will perform it, and where to
// look next. It never claims the backup happened: creating the object is the
// whole of what rta did, and the operator's work starts afterwards.
func requestReceipt(s selection, created backupObject, b backupRequest, c cluster, where string) view.View {
	name := created.Metadata.Name
	if name == "" {
		name = b.Metadata.Name
	}
	ns := created.Metadata.Namespace
	if ns == "" {
		ns = b.Metadata.Namespace
	}
	pairs := []view.Pair{
		{Key: "requested", Value: ns + "/" + name},
		{Key: "cluster", Value: c.Metadata.Namespace + "/" + c.Metadata.Name},
		{Key: "how", Value: methodLine(b)},
		{Key: "where", Value: where},
		{Key: "which instance", Value: targetLine(b)},
	}
	if p := strings.TrimSpace(created.Status.Phase); p != "" {
		pairs = append(pairs, view.Pair{Key: "phase", Value: p})
	}
	return view.KeyValue{Pairs: append(pairs,
		view.Pair{Key: "rta did not take it", Value: "the object is a request; CloudNativePG " +
			"performs the backup, and a Backup that was accepted can still fail"},
		view.Pair{Key: "watch it", Value: s.backupListOf(c)},
	)}
}

// methodLine words how the backup is taken, and who chose that.
//
// The two answers are different and a receipt that blurred them would be
// claiming a decision nobody made: a method the caller named is "stated for this
// backup", and one that was left out is rta's own, resolved against what the
// cluster is set up for — which is not "the cluster decided" either, since
// CloudNativePG's fallback for a Backup naming none is a fixed default that has
// nothing to do with the cluster's preference. For the plugin the row names the
// plugin whose name was copied.
func methodLine(b backupRequest) string {
	m := b.Spec.Method
	if pc := b.Spec.PluginConfiguration; pc != nil {
		m += " (" + pc.Name + ")"
	}
	if b.methodByRta {
		return m + " — chosen by rta: it is the only mechanism this cluster is set up for"
	}
	return m + " (stated for this backup)"
}

// targetLine words an empty target as the thing it means: `target` empty really
// is "the cluster decides", since the CRD says it defers to
// `cluster.spec.backup.target`.
func targetLine(b backupRequest) string {
	if b.Spec.Target != "" {
		return b.Spec.Target + " (stated for this backup)"
	}
	return "whatever the cluster's own target says — rta stated none"
}

// whereOf says where a backup of this cluster by this method goes, as far as
// the Cluster resource itself admits. The plugin's own answer is the ObjectStore
// it names, which is read for it (pluginDestination).
//
// The destination of the in-tree object store is the one thing of its stanza a
// receipt needs, shown without any user info (plainURL); the endpoint and the
// references to the secrets holding its keys beside it are not decoded.
func whereOf(c cluster, method string) string {
	switch method {
	case "plugin":
		return "wherever the " + c.walArchiverPlugin() + " plugin this cluster names sends it"
	case "volumeSnapshot":
		return "volume snapshots of this cluster's volumes, as its `.spec.backup.volumeSnapshot` says"
	case "barmanObjectStore":
		line := "wherever this cluster's `.spec.backup` sends it"
		if b := c.Spec.Backup; b != nil && b.BarmanObjectStore != nil {
			if path := plainURL(b.BarmanObjectStore.DestinationPath); path != "" {
				line += " (" + path + ")"
			}
		}
		return line
	}
	return "—"
}
