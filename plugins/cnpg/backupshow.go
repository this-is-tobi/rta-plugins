package main

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// How one cluster's backups are arranged, and where they land.
//
// `cnpg.status` answers whether a cluster is backed up from the one resource it
// reads, and for a cluster that backs up through a plugin that resource cannot
// say (cluster.go, statusIsTheRecord). The answer is in the plugin's own
// resources — an ObjectStore that holds the destination and the retention and
// reports a recovery window per server, and ScheduledBackups that say when
// anything runs — and reading them is a second and third request, which
// `cnpg.status` promises not to make. So this is the capability that does.
//
// **Every read beside the Cluster stands alone.** The Cluster is the one read
// whose failure is the page's: with it the arrangement is known, and without it
// nothing is. The ObjectStore and the ScheduledBackups are other resources with
// other RBAC and other CRDs, and an identity allowed to read clusters is not
// thereby allowed to read either, nor is either type installed everywhere. A
// refused or absent one is a note in its own section and a warning that the page
// is partial, and the sections that were read stand.
//
// **And nothing here reads a Secret.** The ObjectStore names the Secrets that
// hold its credentials, and the names are all that is wanted of them: where the
// keys are kept is the answer to "what would a rotation touch", and what is in
// them is nobody's business here. The ObjectStore is read through the same
// kubectl get every other read is, and `run` refuses a get of Secrets outright
// (refusesSecrets), so a slip in a later change fails closed instead of
// widening this plugin's reach.

const (
	// objectStoreCRD is the barman-cloud plugin's own resource: where its
	// backups and WAL go, and for how long they are kept.
	objectStoreCRD = "objectstores.barmancloud.cnpg.io"
	// scheduledBackupCRD is the resource that takes backups on a schedule.
	scheduledBackupCRD = "scheduledbackups.postgresql.cnpg.io"
)

// conditionsOfBackups are the two conditions of a Cluster that are about
// backups, shown beside the arrangement because they are its verdict: whether
// the WAL is reaching the store, and whether the last backup worked.
var conditionsOfBackups = []string{continuousArchiving, "LastBackupSucceeded"}

// secretRef is a reference to a Secret, decoded as its name and nothing else.
// The key inside the Secret is not part of the question and a struct without a
// field for it cannot print it.
type secretRef struct {
	Name string `json:"name"`
}

type objectStore struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		// Configuration is the stanza the in-tree object store has, and carries
		// the destination and the credentials. Its own serverName is not decoded:
		// the CRD refuses one ("use the 'serverName' plugin parameter in the
		// Cluster resource"), so the name a cluster files its backups under is
		// the Cluster's plugin parameter (archiveServerName) and a field here
		// could only be a stale second opinion.
		Configuration struct {
			DestinationPath string    `json:"destinationPath"`
			EndpointURL     string    `json:"endpointURL"`
			EndpointCA      secretRef `json:"endpointCA"`
			S3              struct {
				AccessKeyID        secretRef `json:"accessKeyId"`
				SecretAccessKey    secretRef `json:"secretAccessKey"`
				Region             secretRef `json:"region"`
				SessionToken       secretRef `json:"sessionToken"`
				InheritFromIAMRole bool      `json:"inheritFromIAMRole"`
			} `json:"s3Credentials"`
			Azure struct {
				ConnectionString          secretRef `json:"connectionString"`
				StorageAccount            secretRef `json:"storageAccount"`
				StorageKey                secretRef `json:"storageKey"`
				StorageSasToken           secretRef `json:"storageSasToken"`
				InheritFromAzureAD        bool      `json:"inheritFromAzureAD"`
				UseDefaultAzureCredential bool      `json:"useDefaultAzureCredentials"`
			} `json:"azureCredentials"`
			Google struct {
				ApplicationCredentials secretRef `json:"applicationCredentials"`
				GKEEnvironment         bool      `json:"gkeEnvironment"`
			} `json:"googleCredentials"`
		} `json:"configuration"`
		RetentionPolicy string `json:"retentionPolicy"`
	} `json:"spec"`
	Status struct {
		ServerRecoveryWindow map[string]recoveryWindow `json:"serverRecoveryWindow"`
	} `json:"status"`
}

// recoveryWindow is what an ObjectStore reports of one server's backups: the
// span a restore can reach, and the last time a backup worked and failed.
type recoveryWindow struct {
	FirstRecoverabilityPoint string `json:"firstRecoverabilityPoint"`
	LastSuccessfulBackupTime string `json:"lastSuccessfulBackupTime"`
	LastFailedBackupTime     string `json:"lastFailedBackupTime"`
}

// credentials says what kind of credential the object store uses and which
// Secrets it references, by name and deduplicated: the sentence "rotate these"
// needs, and the whole of what is wanted.
func (o objectStore) credentials() string {
	cfg := o.Spec.Configuration
	var kinds []string
	add := func(kind string, inherits string, refs ...secretRef) {
		var names []string
		for _, r := range refs {
			if n := strings.TrimSpace(r.Name); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		switch {
		case len(names) > 0:
			kinds = append(kinds, kind+", from "+format.PluralOf(len(names), "Secret")+" "+strings.Join(names, ", "))
		case inherits != "":
			kinds = append(kinds, kind+", "+inherits)
		}
	}
	s3 := cfg.S3
	inherit := ""
	if s3.InheritFromIAMRole {
		inherit = "inherited from the pod's IAM role"
	}
	add("S3", inherit, s3.AccessKeyID, s3.SecretAccessKey, s3.Region, s3.SessionToken)
	az := cfg.Azure
	inherit = ""
	switch {
	case az.InheritFromAzureAD:
		inherit = "inherited from Azure AD"
	case az.UseDefaultAzureCredential:
		inherit = "the default Azure credentials"
	}
	add("Azure", inherit, az.ConnectionString, az.StorageAccount, az.StorageKey, az.StorageSasToken)
	inherit = ""
	if cfg.Google.GKEEnvironment {
		inherit = "inherited from the GKE environment"
	}
	add("Google", inherit, cfg.Google.ApplicationCredentials)
	if len(kinds) == 0 {
		return "none stated"
	}
	return strings.Join(kinds, "; ")
}

type scheduledBackupList struct {
	Items []scheduledBackup `json:"items"`
}

type scheduledBackup struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
		Schedule            string `json:"schedule"`
		Method              string `json:"method"`
		Suspend             bool   `json:"suspend"`
		PluginConfiguration struct {
			Name string `json:"name"`
		} `json:"pluginConfiguration"`
	} `json:"spec"`
	Status struct {
		LastScheduleTime string `json:"lastScheduleTime"`
		NextScheduleTime string `json:"nextScheduleTime"`
		Error            string `json:"error"`
	} `json:"status"`
}

// scheduledFor filters a namespace's ScheduledBackups to the ones naming the
// cluster, on the field the CRD declares, as backupsFor does.
func scheduledFor(items []scheduledBackup, cluster string) []scheduledBackup {
	var out []scheduledBackup
	for _, sb := range items {
		if sb.Spec.Cluster.Name == cluster {
			out = append(out, sb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name < out[j].Metadata.Name })
	return out
}

func (sb scheduledBackup) status() string {
	switch {
	case strings.TrimSpace(sb.Status.Error) != "":
		return "fail"
	case sb.Spec.Suspend:
		return "warn"
	}
	return "ok"
}

// method names how the scheduled backup is taken, with the plugin it asks when
// that is how.
func (sb scheduledBackup) method() string {
	m := orDash(sb.Spec.Method)
	if n := strings.TrimSpace(sb.Spec.PluginConfiguration.Name); n != "" {
		m += " (" + n + ")"
	}
	return m
}

// userinfoRe is the `user:password@` of a URL, after its scheme.
var userinfoRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#]*@`)

// plainURL is a URL as it may be shown: without a user name or password, and
// without a query or a fragment.
//
// **A URL is a place a credential gets put.** An endpoint written
// `https://key:secret@store.internal/`, or a destination carrying a shared
// access signature after its `?`, is a credential in a field that was meant to
// name a place, and a page that exists to say where backups go must not repeat
// it. The userinfo is stripped by pattern and not by parsing because a value
// that does not parse as a URL is the one most likely to be hand-written, and
// the pattern reads up to the last `@` before the path, so a password holding
// an unescaped one is stripped whole. The query and fragment go with it: nothing
// about where bytes land lives there, and what rides there is the part that is
// a secret.
func plainURL(raw string) string {
	s := userinfoRe.ReplaceAllString(strings.TrimSpace(raw), "$1")
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// stamp renders an instant the way a recovery window is read: the date it was,
// and how long ago that is.
func stamp(raw string) string {
	t, ok := parseWhen(raw)
	if !ok {
		return "—"
	}
	return stampAt(t)
}

func stampAt(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04") + " UTC (" + age(t) + " ago)"
}

func backupShowCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:       "cnpg.backup.show",
		Summary:  "How one cluster's backups are set up and where they land: store, retention, schedules",
		Keywords: []string{"barman", "bucket", "pitr", "recoverability", "archive"},
		Examples: []plugin.Example{
			{Title: "how one cluster's backups are set up", Inputs: map[string]any{"cluster": "app-db"}},
		},
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "How one cluster's backups are arranged and where they land, read from the " +
			"Cluster and, for a cluster that archives through the barman-cloud plugin, from the " +
			"plugin's own resources. For an object store the Cluster itself states, the " +
			"destination, server name and retention. For the plugin, the ObjectStore the " +
			"cluster names: its destination, endpoint and retention, the kind of credential it " +
			"uses and the names of the Secrets it references — names only, never keys or " +
			"values — and the recovery window it reports for this cluster's server. For both, " +
			"the newest completed Backup object that names the cluster and the WAL it records " +
			"itself as spanning (the begin and end log position, the LSN, and the WAL file each " +
			"is in, each only when the Backup records it: a restore replays from the begin " +
			"position and is consistent once it has replayed the end one), the " +
			"ScheduledBackups that name the cluster (cron, method, whether suspended, when " +
			"each last ran and runs next) and the ContinuousArchiving and LastBackupSucceeded " +
			"conditions when the cluster reports them.\n\n" +
			"This is where a plugin-archived cluster's answer to `cnpg.status`'s question is, " +
			"since the Cluster resource keeps no record of backups taken through a plugin. A " +
			"URL is shown without a user name, password or query.\n\n" +
			"Each read beside the Cluster stands alone: one that is refused, or whose type or " +
			"object is absent, becomes a note in its section and a warning that the page is " +
			"partial, and never fails the rest. Nothing here reads a Secret.",
		Agent: "How one cluster's backups are set up and where they land: the mechanisms in use, the " +
			"destination, server name and retention, and for a barman-cloud plugin cluster the ObjectStore " +
			"it names (destination, endpoint, retention, credential kind, Secret names only), the " +
			"recovery window the store reports, the newest completed Backup and the WAL range it records " +
			"(begin and end LSN, begin and end WAL file, each only when set), the ScheduledBackups that " +
			"name the cluster and the ContinuousArchiving and LastBackupSucceeded conditions. " +
			"URLs carry no user info or query. " +
			"A read beside the Cluster that is refused or absent becomes a note and a partial " +
			"warning, not a failure. Secrets are never read.",
		Run: runBackupShow,
	}, clusterField("the cluster whose backups to show", true))
}

func runBackupShow(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := strings.TrimSpace(req.String("cluster"))
	if verr := checkName("cluster", name); verr != nil {
		return nil, verr.WithInput("cluster")
	}
	if name == "" {
		return nil, view.Errorf("cnpg.backup.nocluster", "no cluster named").
			WithHint(s.caller().listEverywhere() + " shows what is there").WithInput("cluster")
	}
	var c cluster
	if verr := getJSON(ctx, s, name, &c); verr != nil {
		return nil, namedCluster(verr)
	}

	// The secondary reads are made in the Cluster's own namespace, which is
	// where its ObjectStore and its ScheduledBackups are by construction —
	// whatever namespace the caller's own selection resolved to.
	beside := s
	beside.namespace, beside.allNamespace = c.Metadata.Namespace, false

	var page showPage
	page.arrangement(c)
	var store *objectStore
	if ref := c.objectStoreName(); ref != "" {
		store = page.objectStore(ctx, beside, c, ref)
	}
	page.recovery(c, s, store)
	page.latest(ctx, beside, c, s)
	page.schedules(ctx, beside, c)
	page.conditions(c)
	if page.rejected != nil {
		return nil, page.rejected
	}
	return view.Sections{Items: page.sections, Warnings: page.gaps}, nil
}

// showPage accumulates the sections of the page and the warnings for what it
// could not cover.
type showPage struct {
	sections []view.Section
	gaps     []view.Error
	// rejected is the first read beside the Cluster that the API server answered
	// 401, which ends the call and is not a note: a dashboard stops asking a tile
	// whose credential was refused twice, reading that off the error a call
	// returns (view.Error.CredentialRefused), and a page cannot be one. A 403 is
	// still a note and a partial warning.
	rejected *view.Error
}

func (p *showPage) add(id, title string, v view.View) {
	p.sections = append(p.sections, view.Section{ID: id, Title: title, View: v})
}

// note is a section that says what could not be read, or what was not there.
func (p *showPage) note(id, title, text string) {
	p.add(id, title, view.Text{Body: text})
}

// gap is the warning for a read that did not come back for a reason that is
// not itself an answer.
func (p *showPage) gap(what string, verr *view.Error) {
	p.gaps = append(p.gaps, view.Error{
		Code:    "cnpg.backup.show.partial",
		Message: what + " was not read: " + verr.Message,
		Hint:    verr.Hint,
	})
}

// failed is what a read beside the Cluster that failed comes to on the page.
//
// An absent type and an absent object are answers — the page says what they
// mean and is not partial for them. Anything else is a read that did not
// happen, whether refused or interrupted, and the page says it is missing a
// part.
func (p *showPage) failed(id, title, what string, verr *view.Error, absentType, absentObject string) {
	switch verr.Code {
	case "cnpg.crd.missing", "cnpg.resource.unserved":
		p.note(id, title, absentType)
	case "cnpg.resource.missing":
		p.note(id, title, absentObject)
	default:
		if verr.Code == "cnpg.auth.failed" && p.rejected == nil {
			p.rejected = verr
		}
		p.note(id, title, "Not read: "+verr.Message+". "+verr.Hint)
		p.gap(what, verr)
	}
}

// arrangement is the section that says what takes this cluster's backups.
func (p *showPage) arrangement(c cluster) {
	pairs := []view.Pair{{Key: "Cluster", Value: c.Metadata.Namespace + "/" + c.Metadata.Name}}
	methods := c.configuredMethods()
	if len(methods) == 0 {
		value := "nothing — this cluster configures no backup, so nothing backs it up"
		if c.Spec.Backup != nil {
			value = "nothing — its `.spec.backup` names no mechanism, so nothing takes a backup"
		}
		pairs = append(pairs, view.Pair{Key: "Taken by", Value: value})
		p.add("arrangement", "Backups", view.KeyValue{Pairs: pairs})
		return
	}
	var by []string
	for _, m := range methods {
		if m == "plugin" {
			m = "the " + c.walArchiverPlugin() + " plugin, which also archives the WAL"
		}
		by = append(by, m)
	}
	pairs = append(pairs, view.Pair{Key: "Taken by", Value: strings.Join(by, " · ")})

	if b := c.Spec.Backup; b != nil && b.BarmanObjectStore != nil {
		pairs = append(pairs,
			view.Pair{Key: "Destination", Value: orDash(plainURL(b.BarmanObjectStore.DestinationPath))},
			view.Pair{Key: "Server name", Value: serverNameLine(c)},
		)
	}
	if b := c.Spec.Backup; b != nil {
		pairs = append(pairs, view.Pair{Key: "Retention", Value: statedOr(b.RetentionPolicy)})
	}
	if c.walArchiverPlugin() != "" {
		if ref := c.objectStoreName(); ref != "" {
			pairs = append(pairs,
				view.Pair{Key: "Object store", Value: c.Metadata.Namespace + "/" + ref},
				view.Pair{Key: "Server name", Value: serverNameLine(c)},
			)
		} else {
			pairs = append(pairs, view.Pair{Key: "Plugin configuration", Value: "kept in the plugin's own " +
				"resources, which this does not read"})
		}
	}
	p.add("arrangement", "Backups", view.KeyValue{Pairs: pairs})
}

// serverNameLine is the name the backups are filed under, and says when that is
// the cluster's own name by default.
func serverNameLine(c cluster) string {
	name := c.archiveServerName()
	if name == c.Metadata.Name {
		return name + " (the cluster's own name)"
	}
	return name
}

func statedOr(v string) string {
	if v = strings.TrimSpace(v); v == "" {
		return "not stated"
	}
	return v
}

// objectStore reads the ObjectStore the cluster names and lays it out. It
// returns the store when it was read, for the recovery window.
func (p *showPage) objectStore(ctx context.Context, s selection, c cluster, ref string) *objectStore {
	const id, title = "object-store", "Object store"
	o, verr := readObjectStore(ctx, s, c, ref)
	if verr != nil {
		p.failed(id, title, "the ObjectStore "+ref, verr,
			"The cluster names the ObjectStore "+ref+" and this cluster has no ObjectStore resource type ("+
				objectStoreCRD+" is not registered): the barman-cloud plugin is not installed, so nothing "+
				"it is asked to do can run.",
			"The cluster names the ObjectStore "+ref+" and there is none by that name in namespace "+
				c.Metadata.Namespace+": the plugin has nowhere to archive to, and a backup asked of it fails.")
		return nil
	}
	cfg := o.Spec.Configuration
	pairs := []view.Pair{
		{Key: "Object store", Value: o.Metadata.Namespace + "/" + o.Metadata.Name},
		{Key: "Destination", Value: orDash(plainURL(cfg.DestinationPath))},
	}
	if u := plainURL(cfg.EndpointURL); u != "" {
		pairs = append(pairs, view.Pair{Key: "Endpoint", Value: u})
	}
	pairs = append(pairs,
		view.Pair{Key: "Retention", Value: statedOr(o.Spec.RetentionPolicy)},
		view.Pair{Key: "Credentials", Value: o.credentials()},
	)
	if ca := strings.TrimSpace(cfg.EndpointCA.Name); ca != "" {
		pairs = append(pairs, view.Pair{Key: "CA bundle", Value: "Secret " + ca})
	}
	p.add(id, title, view.KeyValue{Pairs: pairs})
	return &o
}

// readObjectStore reads the ObjectStore named ref in the namespace of the
// cluster that names it.
func readObjectStore(ctx context.Context, s selection, c cluster, ref string) (objectStore, *view.Error) {
	s.namespace, s.allNamespace = c.Metadata.Namespace, false
	return fetch[objectStore](ctx, s, objectStoreCRD, ref)
}

// recovery is the span a restore can reach and when a backup last worked: from
// the ObjectStore's status for a plugin, from the Cluster's own for the
// in-tree mechanisms, and absent where neither was read.
func (p *showPage) recovery(c cluster, s selection, store *objectStore) {
	const id, title = "recovery", "Recovery window"
	columns := []view.Column{{Name: "Server"}, {Name: "First recoverable point"},
		{Name: "Last successful backup"}, {Name: "Last failed backup"}}
	server := c.archiveServerName()

	switch {
	case c.objectStoreName() != "":
		if store == nil {
			return
		}
		window, ok := store.Status.ServerRecoveryWindow[server]
		if !ok {
			listed := make([]string, 0, len(store.Status.ServerRecoveryWindow))
			for name := range store.Status.ServerRecoveryWindow {
				listed = append(listed, name)
			}
			sort.Strings(listed)
			seen := "it reports none yet"
			if len(listed) > 0 {
				seen = "it reports " + strings.Join(listed, ", ")
			}
			p.note(id, title, "The ObjectStore's status has no recovery window for the server "+server+
				" — "+seen+". A cluster that has not completed a backup has none, and a server name that "+
				"differs from the one the cluster files its backups under shows the same.")
			return
		}
		p.add(id, title, view.Table{Columns: columns, Total: 1, Rows: [][]string{{
			server, stamp(window.FirstRecoverabilityPoint),
			stamp(window.LastSuccessfulBackupTime), stamp(window.LastFailedBackupTime)}}})
	case c.Spec.Backup != nil:
		last, ever := c.lastSuccess()
		from, failed := c.recoverableFrom(), c.Status.LastFailedBackup
		if !ever && from == "" && failed == "" {
			p.note(id, title, "The Cluster resource records no backup yet. "+s.backupListOf(c)+
				" lists the Backup objects, which say whether one was ever asked for.")
			return
		}
		success := "—"
		if ever {
			success = stampAt(last.at)
		}
		p.add(id, title, view.Table{Columns: columns, Total: 1, Rows: [][]string{{
			server, stamp(from), success, stamp(failed)}}})
	}
}

// latest is the newest backup of this cluster that completed and the WAL it
// records itself as spanning: the begin and end log position and the file each
// is in. The Cluster resource keeps none of it — its recovery window is two
// instants, and says nothing of where in the log a restore starts or when it is
// consistent — and the Backup objects, which are the one place it is, are read
// beside the Cluster like the ObjectStore and the schedules, standing alone.
//
// The newest completed one and not the newest: a backup that is running has the
// first position and not the second, and one that failed is not what somebody
// restores from. cnpg.backup.list has the range of every backup, which is the
// one to open for an older one.
func (p *showPage) latest(ctx context.Context, beside selection, c cluster, s selection) {
	const id, title = "latest-backup", "Latest backup"
	var list backupList
	if verr := getResource(ctx, beside, backupCRD, "", &list); verr != nil {
		p.failed(id, title, "the list of Backup objects", verr,
			"This cluster has no Backup resource type ("+backupCRD+" is not registered), so no backup was "+
				"ever asked of it.", "")
		return
	}
	mine := backupsFor(list.Items, c.Metadata.Name)
	if len(mine) == 0 {
		p.note(id, title, "No Backup object names this cluster, so there is no backup to read a WAL range from.")
		return
	}
	i := slices.IndexFunc(mine, backupObject.done)
	if i < 0 {
		p.note(id, title, "None of the "+format.CountOf(len(mine), "Backup object")+" naming this cluster has completed, "+
			"so there is no finished backup to read a WAL range from. "+s.backupListOf(c)+" says what each is doing.")
		return
	}
	b := mine[i]
	pairs := []view.Pair{
		{Key: "Backup", Value: b.Metadata.Name},
		{Key: "Method", Value: orDash(b.resolvedMethod())},
		{Key: "Finished", Value: stamp(b.Status.StoppedAt)},
	}
	r := b.walRange()
	for _, f := range []view.Pair{
		{Key: "Begin LSN", Value: r.beginLSN}, {Key: "End LSN", Value: r.endLSN},
		{Key: "Begin WAL", Value: r.beginWal}, {Key: "End WAL", Value: r.endWal},
	} {
		if f.Value != "" {
			pairs = append(pairs, f)
		}
	}
	if !r.recorded() {
		pairs = append(pairs, view.Pair{Key: "WAL range", Value: "not recorded on this Backup"})
	}
	p.add(id, title, view.KeyValue{Pairs: pairs})
}

// schedules reads the ScheduledBackups that name the cluster.
func (p *showPage) schedules(ctx context.Context, s selection, c cluster) {
	const id, title = "schedules", "Schedules"
	var list scheduledBackupList
	if verr := getResource(ctx, s, scheduledBackupCRD, "", &list); verr != nil {
		p.failed(id, title, "the ScheduledBackups", verr,
			"This cluster has no ScheduledBackup resource type ("+scheduledBackupCRD+" is not registered), "+
				"so nothing of CloudNativePG's takes a backup on a schedule.", "")
		return
	}
	mine := scheduledFor(list.Items, c.Metadata.Name)
	if len(mine) == 0 {
		p.note(id, title, "No ScheduledBackup names this cluster, so no schedule of CloudNativePG's takes a "+
			"backup of it. A CronJob or a person creating Backup objects would not show here.")
		return
	}
	t := view.Table{Columns: []view.Column{
		{Name: "Schedule"}, {Name: "Status", Kind: view.KindStatus}, {Name: "Cron"}, {Name: "Method"},
		{Name: "Last ran"}, {Name: "Next runs"}, {Name: "Error"},
	}}
	for _, sb := range mine {
		last, next := "never", "—"
		if at, ok := parseWhen(sb.Status.LastScheduleTime); ok {
			last = age(at) + " ago"
		}
		switch at, ok := parseWhen(sb.Status.NextScheduleTime); {
		case sb.Spec.Suspend:
			next = "suspended"
		case ok:
			next = "in " + until(at)
		}
		t.Rows = append(t.Rows, []string{
			sb.Metadata.Name, sb.status(), orDash(sb.Spec.Schedule), sb.method(), last, next,
			orDash(firstLine(sb.Status.Error)),
		})
	}
	t.Total = len(t.Rows)
	p.add(id, title, t)
}

// conditions shows the two conditions that are about backups, when the cluster
// reports them: they come out of the read already made.
func (p *showPage) conditions(c cluster) {
	t := view.Table{Columns: []view.Column{
		{Name: "Condition"}, {Name: "Status", Kind: view.KindStatus}, {Name: "Reason"}, {Name: "Message"},
	}}
	for _, want := range conditionsOfBackups {
		for _, cond := range c.Status.Conditions {
			if cond.Type != want {
				continue
			}
			status := "warn"
			switch cond.Status {
			case "True":
				status = "ok"
			case "False":
				status = "fail"
			}
			t.Rows = append(t.Rows, []string{cond.Type, status, orDash(cond.Reason), orDash(firstLine(cond.Message))})
		}
	}
	if len(t.Rows) == 0 {
		return
	}
	t.Total = len(t.Rows)
	p.add("conditions", "Conditions", t)
}
