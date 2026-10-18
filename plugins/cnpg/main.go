// Command rta-plugin-cnpg reads the state of CloudNativePG PostgreSQL
// clusters: which ones exist, and everything one of them will tell you about
// its own health, replication, backups and storage.
//
// Build it and put it on your $PATH as `rta-plugin-cnpg`:
//
//	cd plugins/cnpg && go build -o ~/.local/bin/rta-plugin-cnpg .
//
// It needs `kubectl` and nothing else — no address to configure, no
// credential to state. Whatever cluster kubectl can already reach, this can
// read, through the same kubeconfig, contexts and exec credential plugins the
// operator already keeps working.
//
// # Why this exists next to `kubectl cnpg`
//
// CloudNativePG ships its own kubectl plugin and its `status` is excellent.
// It also does considerably more than read one custom resource — it reaches
// into pods — and that is the part that stops working behind proxies people
// put in front of clusters, where a plain API read goes through untouched.
// This asks one GET and derives the same answers from it, so it works
// wherever `kubectl get` works. Where both work, use theirs: it knows things
// only an exec into the pod can know.
//
// # What it deliberately does not do
//
// No switchover, no promotion, no restart, no destroy. Every one of those is
// an operation on a production database that can take it down, and this
// plugin holds the line plugins/kube holds against `kubectl`'s mutating half:
// a read-first fast path is a different thing from a control plane, and the
// tool that already does it well is one command away.
//
// # The one thing it writes
//
// This list once ended "no backup trigger", and said every capability here
// was Safety: Read. `cnpg.backup.request` ends that, so the reasoning is
// written out rather than quietly dropped.
//
// A backup is the one member of that list whose failure mode is not an
// outage. Switchover, promotion, restart and destroy can all take a database
// away; a backup costs I/O and object-store space. More to the point, it is
// the one that **chooses nothing**: a Backup object carries a cluster
// reference and no destination, so where the bytes go is whatever that
// cluster's own configuration already said. Every gate elsewhere in rta
// exists because a caller could name a place — `net.probe` and `cert.expiry`
// were closed for exactly that — and here there is no place to name.
//
// It is still gated twice over. Safety: Write keeps it off the default MCP
// surface entirely, so an agent sees it only after an operator passes
// `--allow-write cnpg`; NeedsGrant then makes each call a decision that
// names the cluster and expires on its own. The blanket claim that used to
// stand here — "an agent granted this can change nothing" — was a simpler
// sentence than the boundary's actual answer, and the actual answer is the
// one that holds: nothing reaches an agent that an operator did not turn on
// and then grant.
package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk"
	"github.com/this-is-tobi/rta/pkg/view"
)

func main() { sdk.Serve(Plugin()) }

// connFields is the connection half of every capability's inputs.
//
// `context` is Local for the reason plugins/kube states at length and which
// is worth restating because it is the security property of this plugin:
// **choosing which cluster a call reaches is choosing a destination, and a
// remote caller may never choose a destination.** A kubeconfig is a list of
// every cluster this machine can reach with a working identity attached to
// each; an agent free to pass `--context` would not be reading "the cluster
// the operator is working in" but any of them, production included. So config
// fills it, a person at a terminal passes it, and MCP cannot.
//
// `namespace` is not Local, and the difference is the point: a namespace is a
// record inside a cluster somebody has already chosen.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "context", Type: plugin.String, Config: "context", Local: true,
			Help:    "kubeconfig context to use — the current one when omitted",
			Suggest: suggestContexts},
	}
}

// clusterField is the cluster a capability works on: a flag rather than a
// positional, so a profile can carry it (`set: {cluster: shop-prod}`) and the
// day-to-day call is `rta cnpg status` with nothing typed. Config is refused
// on a positional — args fill positionals left to right, so a config-filled
// first argument would change what a typed one means — and kubectl draws the
// same line: the scope is a flag, the record is the argument.
//
// Required for a capability that works on one cluster, so a call with neither a
// flag nor a configured value is refused before it reaches the cluster, naming
// both ways to supply one; optional for a read that is a filter, where leaving
// it out means every cluster in the connection.
//
// **An empty value is a value, and it lifts a pin.** plugin.Resolve lets any
// value the caller gave win over the profile and the config, an empty string
// included, so `cluster: ""` against a profile that pins `shop-prod` reads
// every cluster (or, where it is Required, is refused as missing) rather than
// shop-prod. That is how a pinned default is overridden for one call, and the
// help says so; whether a caller may do it is the host's to decide, and a test
// holds this declaration to the behaviour it describes.
func clusterField(help string, required bool) plugin.Field {
	return plugin.Field{Name: "cluster", Short: "c", Type: plugin.String, Required: required, Config: "cluster",
		Help: help + "; empty lifts one a profile or the config pins", Live: true, Suggest: suggestClusters}
}

// allNamespacesField is the wide form of a read that takes a namespace: the
// same read over every namespace in the connection, in place of the one it
// would have made.
//
// One declaration for every capability that has it, so the name, the letter
// and the refusal of a namespace given beside it (selectionOf) are one thing
// and not three that drifted. Declared by the reads and by no write: a call
// that changes something names the one record it changes, and a switch that
// widened it would be a grant covering every namespace the kubeconfig reaches.
//
// No Config key, deliberately. A namespace is a place somebody sets once and
// keeps; "every namespace" is the answer to a question asked now, and a
// default of it in a file nobody is watching would widen every read made
// through that file without anybody having typed the word.
//
// Widens says the host what this switch does to the input beside it: it reads
// past it. A profile that fences `namespace` to the one or two an operator
// named (`only:`) is held to that by the host for every value of the input,
// and the host cannot read a handler to learn that this switch undoes it, so a
// switch that is not declared is a hole in a boundary the operator believes is
// closed. Declared, a call that sets it while the input is fenced is refused
// before anything is dialled.
func allNamespacesField() plugin.Field {
	return plugin.Field{Name: "all-namespaces", Short: "A", Type: plugin.Bool,
		Widens: "namespace", Help: "every namespace instead of one"}
}

func nsFields() []plugin.Field {
	return []plugin.Field{
		// Live, because the Suggest contacts the cluster: that read must be
		// something a completion press asked for, never something typing
		// caused. plugins/kube pins the same rule on its own namespace field.
		{Name: "namespace", Short: "n", Type: plugin.String, Config: "namespace",
			Help: "namespace to look in — the context's own when omitted",
			Live: true, Suggest: suggestNamespaces},
	}
}

// leading puts the Namespace column ahead of cols when the read covered every
// namespace, and leaves them alone otherwise.
//
// A column that says what is already said is noise, and in one namespace the
// namespace is the question that was asked. Across all of them a row without
// its namespace is a name that may mean two things: two clusters called
// app-db in two namespaces are two databases, and a listing that does not say
// which is which is a listing nobody can act on. So the scope leads, only
// when it is a variable, in every wide read the same way.
func (s selection) leading(cols ...view.Column) []view.Column {
	if !s.allNamespace {
		return cols
	}
	return append([]view.Column{{Name: "Namespace"}}, cols...)
}

// lead is leading for a row: the namespace ahead of cells, in the wide read.
func (s selection) lead(namespace string, cells ...string) []string {
	if !s.allNamespace {
		return cells
	}
	return append([]string{namespace}, cells...)
}

// cap assembles a capability's inputs in the order a person reads them, and
// sets NoPreview on all of them: each reaches off the box, so none belongs on
// a dashboard that refreshes on a timer without somebody having said so.
//
// The scope leads, in the order plugins/kube lays it out: the cluster, the
// namespace it is in, and the switch that reads past that namespace. A form is
// filled from the top, and a switch that widens a box reads as an answer to it
// only when it is below it. Declared by the capability or by the helpers that
// give it these, they arrive in whatever order those were written in, with the
// cluster and the limit between the switch and the box it widens; so they are
// ranked here, once, and everything else keeps the order it was declared in.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	inputs := append(append(append(own, c.Inputs...), nsFields()...), connFields()...)
	slices.SortStableFunc(inputs, func(a, b plugin.Field) int { return inputRank(a.Name) - inputRank(b.Name) })
	c.Inputs = inputs
	c.NoPreview = true
	return c
}

// inputRank is where an input stands among a capability's: the scope first, the
// capability's own inputs after it, and the connection last.
func inputRank(name string) int {
	switch name {
	case "cluster":
		return 0
	case "namespace":
		return 1
	case "all-namespaces":
		return 2
	case "context":
		return 4
	}
	return 3
}

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut — an index entry carries this verbatim, and a
// version is a fact about a release, not about the source it came from.
var version = "dev"

func Plugin() plugin.Plugin {
	return plugin.Plugin{
		Name:    "cnpg",
		Summary: "Read the state of CloudNativePG PostgreSQL clusters",
		Version: version,
		// Everything this plugin does is a kubectl call, and kubectl cannot
		// make one without the kubeconfig. Declaring it is a request, not a
		// grant: rta denies credential locations to every plugin by default,
		// and `rta plugin allow cnpg` is where an operator decides — against
		// this artifact's digest, so a rebuild asks again.
		Needs: []plugin.Need{plugin.NeedKubeconfig},
		Capabilities: []plugin.Capability{
			cap(plugin.Capability{
				ID: "cnpg.list", Summary: "Every CloudNativePG cluster, and whether it is healthy",
				Keywords: []string{"postgres", "postgresql", "operator", "databases"},
				Examples: []plugin.Example{
					{Title: "every cluster in every namespace", Inputs: map[string]any{"all-namespaces": true}},
					{Title: "the clusters of one namespace", Inputs: map[string]any{"namespace": "production"}},
				},
				Safety: plugin.Read, Idempotent: true,
				Description: "One `kubectl get clusters.postgresql.cnpg.io -o json`, one row " +
					"per cluster, named namespace/name. Status is the worst finding cnpg.status " +
					"lists for the cluster under Needs attention — ok, warn or fail — so a cluster " +
					"short of its own spec, whose WAL archiving fails, with no backup, with a " +
					"certificate near expiry or without a primary is not ok whatever its phase " +
					"says. Backup is how long ago the last one succeeded, or `none` when nothing " +
					"is set up to take one, `never` when one is and none has, `failing` when WAL " +
					"archiving fails or the last attempt failed after the last success, and " +
					"`not recorded` for a cluster that archives through a plugin, whose backups " +
					"the resource does not record (cnpg.backup.list does). Ready is " +
					"ready/desired instances; Phase is the operator's own, with the sentence of a " +
					"healthy cluster said as `healthy`. Reads the context's own namespace unless " +
					"`namespace` names another or `all-namespaces` reads every one, and an empty " +
					"answer says which it read. `cluster` narrows the listing to one: a profile " +
					"that fences it (`only: cluster:`) lists the clusters it names and no other. " +
					"Reads one custom resource and nothing else: no pods, no exec, no logs.",
				Agent: "One row per cluster, named namespace/name: Status (ok, warn or fail, the worst finding " +
					"cnpg.status lists, so failing WAL archiving, no backup, a certificate near expiry or a cluster " +
					"short of its instances is not ok), Backup (age of the last success; none, never, failing, or " +
					"not recorded for a cluster that archives through a plugin), Ready, Primary and Phase. Reads " +
					"the context's own namespace, `namespace` names another, `all-namespaces` reads every one and " +
					"`cluster` narrows to one. One custom resource: no pods, exec or logs.",
				Inputs: []plugin.Field{
					clusterField("only this cluster — every cluster of the namespace when omitted", false),
					allNamespacesField(),
				},
				Run: runList,
			}),
			cap(plugin.Capability{
				ID: "cnpg.status", Summary: "One cluster in depth: instances, replication, backups, storage",
				Keywords: []string{"primary", "replicas", "failover", "switchover", "lag", "wal"},
				Examples: []plugin.Example{
					{Title: "one cluster in depth", Inputs: map[string]any{"cluster": "app-db", "namespace": "production"}},
				},
				Safety: plugin.Read, Idempotent: true,
				Description: "Everything the Cluster resource reports about itself, laid out as " +
					"the questions somebody opens it to ask. Instances are listed primary first, " +
					"with the role taken from the cluster's own currentPrimary rather than each " +
					"instance's self-report — during a failover those disagree, and the " +
					"cluster's view is the one the rest of the fields are consistent with. " +
					"A switchover in flight is derived rather than printed raw: CNPG moves " +
					"targetPrimary before currentPrimary, so the two differing means a " +
					"promotion is happening now. Every instance on one node is reported, " +
					"because the CRD's own documentation calls that the absence of high " +
					"availability. Conditions are shown only when they are not satisfied. " +
					"The last successful backup is reported as an age, since that is the form " +
					"the question is asked in — and a backup that is not configured at all is " +
					"distinguished from one that is configured and failing, which the resource " +
					"spells identically. A cluster that archives through a WAL-archiver plugin " +
					"is told apart from both: the resource records no backup taken through a " +
					"plugin (CloudNativePG documents its backup fields as not set for them), so " +
					"the row says the resource cannot tell and points at `cnpg.backup.list`, " +
					"whose Backup objects are the record, and at `cnpg.backup.show`, which " +
					"reads where they go, rather than claiming none ever succeeded. The primary's tenure is derived (a young primary on an " +
					"old cluster is the trace of a failover), certificate expiries are graded " +
					"against the same 30-day window rta's other certificate checks use, and the " +
					"replication posture, resource bounds and superuser-access switch are read " +
					"from the spec. The resource holds no replication positions or lag, and this " +
					"reads no pod. For a cluster with replicas one row says where they are read: " +
					"the pg plugin's replication page, which lists every standby's sent, write, " +
					"flush and replay LSN and lag from the primary, through a profile's `kube:` " +
					"forward to the read-write service `<cluster>-rw`, as a role in pg_monitor.",
				Agent: "One cluster as its Cluster resource reports it: phase and reason, instances (primary " +
					"first, by the cluster's own currentPrimary), a switchover in flight, instances sharing a " +
					"node, unmet conditions only, the last successful backup as an age (not configured, failing " +
					"and unrecorded by a backup plugin are told apart; cnpg.backup.list and cnpg.backup.show read them), " +
					"WAL archiving, certificate expiries graded against 30 days, " +
					"replication posture, resource bounds and superuser access. Replication positions (LSN) and lag " +
					"are not in the resource: the PostgreSQL plugin's replication capability lists " +
					"every standby's from the primary, over a profile's kube: forward to " +
					"<cluster>-rw. One GET of one resource: no pods, exec or logs.",
				Inputs: []plugin.Field{clusterField("the cluster to read", true)},
				Run:    runStatus,
			}),
			backupListCapability(),
			backupShowCapability(),
			backupRequestCapability(),
			storageCapability(),
		},
	}
}

func runList(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := strings.TrimSpace(req.String("cluster"))
	if verr := checkName("cluster", name); verr != nil {
		return nil, verr
	}
	var list clusterList
	// Asked of the API server and not filtered here, as storage does for a
	// volume's label: the clusters a call is not about are not read to be thrown
	// away, and a profile that fences `cluster` lists the ones it names and no
	// other. Without it a connection fenced to one cluster answered with every
	// cluster of the namespace, names the fence was written to keep out.
	var extra []string
	if name != "" {
		extra = []string{"--field-selector", "metadata.name=" + name}
	}
	if verr := getResource(ctx, s, clusterCRD, "", &list, extra...); verr != nil {
		return nil, verr
	}
	// What a narrow tile keeps is decided by the order of these columns and by
	// Priority (the host's glance rules, glance.go in rta): the first column is
	// the row's name and always stays, the plain ones go from the last to the
	// first, and the one that grades itself goes last. So the name leads, the
	// verdict is next, and what explains the verdict follows it. Primary and
	// Phase say what the verdict already does and are given up first; the phase
	// is the prose the host would otherwise let take the slack, at the cost of
	// the Status when the names are long.
	t := view.Table{Columns: []view.Column{
		{Name: "Cluster"},
		{Name: "Status", Kind: view.KindStatus},
		{Name: "Backup"},
		{Name: "Ready"},
		{Name: "Primary", Priority: 1},
		{Name: "Phase", Priority: 2},
	}}
	for _, c := range list.Items {
		t.Rows = append(t.Rows, []string{
			c.Metadata.Namespace + "/" + c.Metadata.Name, c.verdict(), c.backupCell(), c.ready(),
			orDash(c.Status.CurrentPrimary), shortPhase(c.Status.Phase),
		})
	}
	t.Total = len(t.Rows)
	if len(t.Rows) == 0 {
		t.Empty = emptyClusters(name, s)
	}
	return t, nil
}

// emptyClusters is the sentence an empty listing carries (view.Table.Empty). It
// says where it looked, and what that was: a call that names no namespace reads
// the context's own and not the whole cluster, and an empty answer that did not
// say so read as "there are none" to somebody whose clusters live in another
// namespace. Unless the read already covered them all, it names the call that
// does.
func emptyClusters(name string, s selection) string {
	body := "no CloudNativePG clusters in " + s.where()
	if name != "" {
		body = "no CloudNativePG cluster called " + strconv.Quote(name) + " in " + s.where()
	}
	if !s.allNamespace {
		body += " — " + s.caller().listEverywhere() + " reads every namespace"
	}
	return body
}

func runStatus(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := req.String("cluster")
	if verr := checkName("cluster", name); verr != nil {
		return nil, verr
	}
	var c cluster
	if verr := getJSON(ctx, s, name, &c); verr != nil {
		return nil, verr
	}
	return statusView(c, pointers{lag: lagPointer(req), backups: s.backupListOf(c), arrangement: s.backupShowOf(c)}), nil
}

// lagPointer says where the positions and the lag this resource does not hold
// are read: the pg plugin's replication page, which connects to the server and
// reads them from the primary. Spelled for the surface that asked, a command at
// a terminal and the tool to an agent, and beside the profile the call came
// through when there was one, since a connection to the server is the
// profile's to make and the cnpg read is of the cluster around it.
//
// A pointer and not a read: this capability is one GET of one resource, and
// stays that. It is also a plugin that may not be installed, which is why this
// only names it.
func lagPointer(req plugin.Request) string {
	sf, call := req.Surface(), ""
	if profile := req.Profile(); profile != "" {
		call = "`" + sf.Call("pg.replication.status", plugin.Arg{Name: "profile", Value: profile}) + "`"
	} else {
		call = sf.CapabilityName("pg.replication.status")
	}
	return "not in the Cluster resource — " + call + " lists each standby's sent, write, flush and replay " +
		"position (LSN) and how far behind it is, read from the primary over a connection to " + readWriteService(req) +
		", which a profile's kube: forward reaches, as a role in pg_monitor"
}

// readWriteService names the Service that follows a cluster's primary, which is
// where every standby's position is read from. CloudNativePG names it
// <cluster>-rw, so with the cluster named the page can say the one thing to
// point a forward at; a call that named none says what it is.
func readWriteService(req plugin.Request) string {
	if name := strings.TrimSpace(req.String("cluster")); name != "" {
		return "the cluster's read-write service (" + name + "-rw)"
	}
	return "the cluster's read-write service"
}

// pointers are where a status page sends its reader for what the Cluster
// resource cannot say. A pointer and not a read, for the reason lagPointer
// gives: this capability is one GET of one resource, and stays that.
type pointers struct {
	// lag is where the replicas' positions and lag are read, shown only for a
	// cluster that has replicas.
	lag string
	// backups is the call that lists the Backup objects of this cluster, which
	// are the record of a backup the resource itself keeps none of.
	backups string
	// arrangement is the call that shows where this cluster's backups go and,
	// for a plugin, what the plugin's own resources say of them.
	arrangement string
}

// statusView lays one cluster out as three sections: what it is, what its
// instances are doing, and what is wrong with it.
//
// The third section is absent when there is nothing in it, which is the same
// conditional-column doctrine the rest of rta follows one level up: a
// "Problems" heading with nothing under it trains people to skip the heading.
func statusView(c cluster, p pointers) view.View {
	overview := []view.Pair{
		{Key: "Cluster", Value: c.Metadata.Namespace + "/" + c.Metadata.Name},
		{Key: "Phase", Value: orDash(c.Status.Phase)},
	}
	if c.Status.PhaseReason != "" {
		overview = append(overview, view.Pair{Key: "Reason", Value: c.Status.PhaseReason})
	}
	overview = append(overview,
		view.Pair{Key: "Instances", Value: c.ready() + " ready"},
		view.Pair{Key: "Primary", Value: primaryLine(c)},
	)
	// The major version answers "what am I actually running" one field
	// earlier than the image tag does; when the operator is too old to report
	// it, the image row carries what there is.
	if info := c.Status.PGDataImageInfo; info.MajorVersion > 0 {
		overview = append(overview, view.Pair{Key: "PostgreSQL",
			Value: strconv.Itoa(info.MajorVersion) + " — " +
				firstNonEmpty(info.Image, c.Status.Image, c.Spec.ImageName)})
	} else {
		overview = append(overview, view.Pair{
			Key: "Image", Value: orDash(firstNonEmpty(c.Status.Image, c.Spec.ImageName))})
	}
	overview = append(overview,
		view.Pair{Key: "Storage", Value: c.storageLine()},
	)
	if r := c.resourceLine(); r != "" {
		overview = append(overview, view.Pair{Key: "Resources", Value: r})
	}
	overview = append(overview,
		view.Pair{Key: "Replication", Value: c.replicationLine()},
	)
	if p.lag != "" && c.Spec.Instances > 1 {
		overview = append(overview, view.Pair{Key: "Lag and positions", Value: p.lag})
	}
	if e := c.Spec.EnableSuperuserAccess; e != nil {
		v := "disabled"
		if *e {
			v = "enabled"
		}
		overview = append(overview, view.Pair{Key: "Superuser access", Value: v})
	}
	overview = append(overview,
		view.Pair{Key: "Timeline", Value: strconv.Itoa(c.Status.TimelineID)},
		view.Pair{Key: "Backup", Value: backupLine(c, p)},
	)
	// Beside the backup row, because it is the half that decides what a backup
	// is worth: a base backup recovers to the moment it was taken, and
	// everything after that comes from archived WAL.
	if line := c.archivingLine(); line != "" {
		overview = append(overview, view.Pair{Key: "WAL archiving", Value: line})
	}
	if name, at, ok, unparsed := c.soonestCert(); ok || len(unparsed) > 0 {
		var v string
		switch {
		case ok && time.Until(at) <= 0:
			v = name + " expired " + age(at) + " ago"
		case ok:
			v = "soonest expires in " + until(at) + " (" + name + ")"
		}
		if len(unparsed) > 0 {
			note := "expiry unreadable for " + strings.Join(unparsed, ", ")
			if v == "" {
				v = note + " — this cannot say whether they are near expiry"
			} else {
				v += " · " + note
			}
		}
		overview = append(overview, view.Pair{Key: "Certificates", Value: v})
	}
	if from := c.recoverableFrom(); from != "" {
		overview = append(overview, view.Pair{Key: "Recoverable from", Value: from})
	}

	instances := view.Table{Columns: []view.Column{
		{Name: "Instance"}, {Name: "Role"}, {Name: "State"}, {Name: "IP"}, {Name: "Timeline"},
	}}
	for _, r := range c.instances() {
		instances.Rows = append(instances.Rows,
			[]string{r.name, r.role, r.state, r.ip, strconv.Itoa(r.timeline)})
	}
	instances.Total = len(instances.Rows)

	sections := []view.Section{
		{Title: "Cluster", View: view.KeyValue{Pairs: overview}},
		{Title: "Instances", View: instances},
	}
	// Both settings sections come out of the read that already happened.
	// **This capability still makes exactly one GET**, which is the property
	// its own doc comment claims and the reason it works where `kubectl cnpg
	// status` does not — so what a cluster states about itself belongs here,
	// and what needs a second resource (its volumes, its backups) is its own
	// capability rather than a second round trip hidden inside this one.
	if s := settingsTable(c, walSettings); len(s.Rows) > 0 {
		sections = append(sections, view.Section{
			Title: "Write-ahead log and recovery", View: s})
	}
	if s := settingsTable(c, serverSettings); len(s.Rows) > 0 {
		sections = append(sections, view.Section{Title: "Server settings", View: s})
	}
	if problems := problemTable(c, p); len(problems.Rows) > 0 {
		sections = append(sections, view.Section{Title: "Needs attention", View: problems})
	}
	return view.Sections{Items: sections}
}

// settingsTable renders the parameters a cluster states from one of the
// curated lists, and says what it left to CloudNativePG.
//
// The unstated half is a row rather than an omission, because "max_connections
// is CloudNativePG's default" and "max_connections is 100" are different
// answers and only the first is one rta can stand behind — the CRD publishes
// no defaults, and somebody sizing a connection pool against a number this
// page made up would be sizing against nothing.
func settingsTable(c cluster, want []struct{ key, means string }) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Setting"}, {Name: "Value"}, {Name: "What it decides"},
	}}
	for _, row := range c.settings(want) {
		t.Rows = append(t.Rows, []string{row.key, row.value, row.means})
	}
	if len(t.Rows) == 0 {
		return t
	}
	if unstated := c.unstatedSettings(want); len(unstated) > 0 {
		t.Rows = append(t.Rows, []string{
			"not stated", strings.Join(unstated, ", "),
			"left at CloudNativePG's own defaults, which the resource does not publish",
		})
	}
	t.Total = len(t.Rows)
	return t
}

// verdict is the worst finding the status page lists under "Needs attention":
// fail, else warn, else ok. The same rows from the same function, so that the
// list cannot call a cluster ok whose status page has a warning on it, which it
// did when it graded only the instance count and a failing primary and said ok
// to a cluster whose WAL archiving was failing. A note (the grade of a finding
// that nothing is known to be wrong with) is not a finding.
func (c cluster) verdict() string {
	worst := "ok"
	for _, row := range problemTable(c, pointers{}).Rows {
		switch row[1] {
		case "fail":
			return "fail"
		case "warn":
			worst = "warn"
		}
	}
	return worst
}

// healthyPhase is the sentence CloudNativePG gives a cluster with nothing to
// report, the one every row of a healthy fleet would otherwise spend its width
// on.
const healthyPhase = "Cluster in healthy state"

// shortPhase is the operator's phase, with the one sentence it says of a
// healthy cluster as the word it comes to. Every other phase is the operator's
// own words and the news, and is left as it wrote them.
func shortPhase(phase string) string {
	if phase == healthyPhase {
		return "healthy"
	}
	return orDash(phase)
}

// certWarnDays mirrors builtin/internal/x509check.DefaultWarnDays — restated
// rather than imported, because this plugin is its own module and that
// package is internal to the core binary. If the two ever disagree, the core
// value is the one to follow: every certificate rta grades should go amber on
// the same day.
const certWarnDays = 30

// problemTable is everything worth acting on, and is empty on a healthy
// cluster.
func problemTable(c cluster, p pointers) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "What"}, {Name: "Status", Kind: view.KindStatus}, {Name: "Detail"},
	}}
	add := func(what, status, detail string) {
		t.Rows = append(t.Rows, []string{what, status, detail})
	}

	// **No primary is the worst state a database cluster has**, and the first
	// version of this table had no case for it: the list view graded it fail
	// from the same field, and the page somebody opens to find out *why* said
	// nothing at all. Found by writing the test for every field and asking
	// what each one's absence means.
	switch {
	case c.Status.CurrentPrimary == "":
		add("primary", "fail", "there is no primary — nothing can be written to this cluster")
	case c.Status.PrimaryFailingSince != nil:
		add("primary", "fail", "unhealthy since "+age(*c.Status.PrimaryFailingSince)+" ago")
	}
	if c.switchingOver() {
		add("switchover", "warn", "promoting "+c.Status.TargetPrimary+
			" — the primary is still "+c.Status.CurrentPrimary)
	}
	if !c.healthy() {
		add("instances", "warn", c.ready()+" ready — the cluster is short of its own spec")
	}
	if c.singleNode() {
		add("topology", "warn", "every instance is on one node, so nothing survives losing it")
	}
	// **Divergence rather than lag, and the difference is the finding.** An
	// instance on another timeline is not behind and will not catch up: it
	// followed a history the cluster has abandoned, which is what a promotion
	// an instance missed leaves behind. Live replication lag is not in this
	// resource at all — see settings.go for why this plugin does not go and
	// get it — so this is the desync question the single read can answer, and
	// it happens to be the one that does not resolve itself.
	if diverged := c.divergedInstances(); len(diverged) > 0 {
		add("timeline", "fail", strings.Join(diverged, ", ")+" reports a timeline other "+
			"than the cluster's "+strconv.Itoa(c.Status.TimelineID)+
			" — a diverged instance does not catch up")
	}
	// Archiving off is not a backup finding: a cluster can have a perfectly
	// good base backup and still only recover to the instant it was taken.
	if c.Spec.PostgreSQL.Parameters["archive_mode"] == "off" && c.backupConfigured() {
		add("WAL archiving", "warn", "off, while a backup is configured — a restore "+
			"recovers to the backup's own instant and no further")
	}
	// Four backup findings, one row: "nothing is configured", "configured
	// and never once worked", "worked before, failing now" and "the resource
	// cannot say" are different conversations with different people, and the
	// old single message ("no successful backup has ever been recorded")
	// collapsed the first two — sending somebody to debug a backup job that
	// does not exist.
	//
	// The last one is the fourth because "never" is only a finding where the
	// record could have held an answer (statusIsTheRecord). It is graded info
	// and not warn: nothing is known to be wrong, and a warning here would be
	// the false alarm this row used to raise on every plugin-archived cluster
	// that was backing up fine.
	success, ever := c.lastSuccess()
	lastFail, failedOnce := parseWhen(c.Status.LastFailedBackup)
	switch {
	case !ever && !c.backupConfigured():
		add("backup", "warn", "not configured — nothing backs this cluster up")
	case !ever && !c.statusIsTheRecord():
		msg := "the Cluster resource cannot say — it records no backup taken through the " +
			c.walArchiverPlugin() + " plugin"
		if failedOnce {
			msg += ", and the one failure it does record is " + age(lastFail) + " old"
		}
		add("backup", "info", msg+groundTruthPointer(p))
	case !ever:
		msg := "configured, but no backup has ever succeeded"
		if failedOnce {
			msg += " — the last attempt failed " + age(lastFail) + " ago"
		}
		add("backup", "warn", msg)
	case failedOnce && lastFail.After(success.at) && !c.statusIsTheRecord():
		// What an older arrangement left behind: a cluster that moved to a
		// plugin keeps the last failure its in-tree mechanism recorded, and
		// since the plugin's successes are recorded elsewhere nothing a later
		// backup does can ever clear it. Graded warn it would stay on the page
		// for as long as the cluster lives, over daily backups that all worked.
		add("backup", "info", "the Cluster resource's last recorded attempt failed "+age(lastFail)+
			" ago and its last recorded success is "+age(success.at)+" old, but it records no backup taken "+
			"through the "+c.walArchiverPlugin()+" plugin, so this says nothing of the backups taken now"+
			groundTruthPointer(p))
	case failedOnce && lastFail.After(success.at):
		add("backup", "warn", "the most recent attempt failed "+age(lastFail)+
			" ago — the last success is "+age(success.at)+" old")
	}
	name, at, ok, unparsedCerts := c.soonestCert()
	if len(unparsedCerts) > 0 {
		// A certificate whose expiry could not be read is not a certificate
		// that is fine: with no row at all, an expired one is indistinguishable
		// from a cluster whose certificates are all healthy.
		add("certificates", "warn", "expiry unreadable for "+strings.Join(unparsedCerts, ", ")+
			" — whether they are expired or near it could not be determined")
	}
	if ok {
		switch {
		case time.Until(at) <= 0:
			add("certificates", "fail", name+" expired "+age(at)+
				" ago — TLS connections to this cluster are failing or about to")
		case time.Until(at) < certWarnDays*24*time.Hour:
			// The operator rotates these certificates itself, so one this
			// close to expiry is not a renewal somebody forgot — it means the
			// rotation is not happening, which is an operator problem wearing
			// a certificate's clothes.
			add("certificates", "warn", name+" expires in "+until(at)+
				" — the operator should have rotated it already")
		}
	}
	for _, pvc := range []struct {
		what  string
		names []string
	}{
		{"dangling PVCs", c.Status.DanglingPVC},
		{"unusable PVCs", c.Status.UnusablePVC},
		{"resizing PVCs", c.Status.ResizingPVC},
	} {
		if len(pvc.names) > 0 {
			add(pvc.what, "warn", joinClipped(pvc.names))
		}
	}
	for _, cond := range c.notTrue() {
		detail := cond.Reason
		if cond.Message != "" {
			detail += ": " + cond.Message
		}
		add(cond.Type, "warn", orDash(detail))
	}
	t.Total = len(t.Rows)
	return t
}

func primaryLine(c cluster) string {
	if c.Status.CurrentPrimary == "" {
		return "none — the cluster has no primary right now"
	}
	if c.switchingOver() {
		return c.Status.CurrentPrimary + " → " + c.Status.TargetPrimary + " (switching over)"
	}
	line := c.Status.CurrentPrimary
	// The tenure is the failover trace: a cluster whose primary is hours old
	// on a resource that is months old changed primaries recently, and this
	// is the only line that says so.
	if tenure, ok := c.primaryFor(); ok {
		line += " — primary for " + tenure
	}
	return line
}

// backupLine is the Backup row of the overview, and it says what the record is
// a record of: "none recorded" is a statement about the resource, and for a
// cluster whose backups are taken by a plugin the resource was never going to
// hold them.
func backupLine(c cluster, p pointers) string {
	last, ever := c.lastSuccess()
	archiver := c.walArchiverPlugin()
	switch {
	case ever && c.statusIsTheRecord():
		return "last succeeded " + age(last.at) + " ago"
	case ever:
		// Recorded, and so a real success, but of the mechanisms the operator
		// runs itself: the plugin's own are not counted.
		by := ""
		if last.method != "" {
			by = " by " + last.method
		}
		return "last succeeded " + age(last.at) + " ago" + by + " — backups taken through the " +
			archiver + " plugin are not recorded in the Cluster resource"
	case !c.statusIsTheRecord():
		line := "not recorded — the Cluster resource keeps no record of backups taken through the " +
			archiver + " plugin"
		if p.backups != "" {
			line += "; " + p.backups + " lists them"
			if p.arrangement != "" {
				line += ", and " + p.arrangement + " shows where they go"
			}
		}
		return line
	}
	return "none recorded"
}

// backupCell is the Backup column of the list: how long ago the last backup
// succeeded, or in a word why there is nothing to say of one. Not the reason in
// full, which the status page has, but enough to read beside the Status.
//
// Archiving that fails is first, over an age that would read as a clean record:
// a backup four days old is the recovery point of a cluster that archives, and
// of one that has stopped it is the last point there is. A cluster that archives
// through a plugin is "not recorded" whatever the resource holds, because the
// record it keeps is of the mechanisms the operator runs itself and says nothing
// of the plugin's backups (statusIsTheRecord).
func (c cluster) backupCell() string {
	success, ever := c.lastSuccess()
	lastFail, failedOnce := parseWhen(c.Status.LastFailedBackup)
	switch {
	case c.archivingFailing():
		return "failing"
	case !ever && !c.backupConfigured():
		return "none"
	case !c.statusIsTheRecord():
		return "not recorded"
	case !ever:
		return "never"
	case failedOnce && lastFail.After(success.at):
		return "failing"
	}
	return age(success.at) + " ago"
}

// groundTruthPointer is the clause that sends somebody to the Backup objects,
// which are where a cluster archived through a plugin keeps its record, and to
// the call that shows where those backups go. It is empty when the surface has
// no call to name.
func groundTruthPointer(p pointers) string {
	if p.backups == "" {
		return ""
	}
	out := "; " + p.backups + " lists the Backup objects, which are the ground truth"
	if p.arrangement != "" {
		out += ", and " + p.arrangement + " shows where they go"
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// joinClipped names a few and counts the rest, so a cluster with forty stuck
// PVCs produces a finding rather than a wall.
func joinClipped(names []string) string {
	const max = 3
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + " and " + strconv.Itoa(len(names)-max) + " more"
}
