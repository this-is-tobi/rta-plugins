package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// kube.overview: the state of one cluster, and where it is pointed.
//
// The composed capability every plugin here has (vault.overview, pg.overview)
// and the one an operator actually puts on a dashboard. It answers the
// question somebody asks before they know which question to ask.
//
// Its compact form is what a dashboard tile and an agent read, and a tile
// shows the first eight lines of it. So the first row is the verdict, in a
// word the theme colours, and the rows after it run from the state to the
// capacity to what is wrong, with the identity of the cluster last: a page that
// opened with the server's address and the kubeconfig's default namespace spent
// two of the eight lines on facts that never change, and the pods, the
// deployments and the certificates were never on the screen. The whole of the
// old page, the server and the namespace included, is the first section of
// `detail`.
//
// Nodes, pods, deployments, quotas and certificates are independent kubectl
// calls with nothing to say to each other, fetched concurrently rather than
// one after another — a dashboard tile that takes six round-trips to refresh
// instead of one is a tile somebody turns back off. Metrics are deliberately
// not a seventh: metrics-server is an add-on a bare cluster often does not
// have, and folding a frequently-absent dependency into the one view meant to
// always answer would make the common case noisier, not more useful.
// kube.metrics.pod and kube.metrics.node stay their own capabilities for
// exactly that reason.
//
// The namespaces are a list (overviewlist.go says why each is read on its own).
// Naming none reads every namespace, naming one reads that one, and naming
// several reads the pods, deployments, quotas and certificates of each with a
// call of its own and merges them into the page the single namespace gets. The
// nodes and the namespaces are cluster-scoped and are one list each whatever is
// named.

// overviewFetch is the concurrent read: what every goroutine below writes to
// its own field, read only after wg.Wait() — no field is written by more
// than one goroutine, so nothing here needs a mutex.
//
// A kind is read once for each namespace named, and what it holds is the
// namespaces that answered. xMiss is every namespace it could not be read in,
// and xErr is set, to the first of their reasons, only when there was none it
// could be read in: the kind as a whole could not be read, as it could not when
// there was one namespace to read.
type overviewFetch struct {
	namespaces list[namespaceItem]
	nsErr      *view.Error
	nodes      list[nodeItem]
	nodeErr    *view.Error
	pods       []podItem
	podErr     *view.Error
	podMiss    []unreadNS
	deps       []deploymentItem
	depErr     *view.Error
	depMiss    []unreadNS
	quotas     list[resourceQuotaItem]
	quotaErr   *view.Error
	quotaMiss  []unreadNS
	certs      []tlsCert
	certErr    *view.Error
	certMiss   []unreadNS
}

// errs is every failure of every read the page is made of, the cluster-scoped
// reads first and then each kind's, namespace by namespace.
func (f overviewFetch) errs() []*view.Error {
	out := []*view.Error{f.nsErr, f.nodeErr}
	for _, miss := range [][]unreadNS{f.podMiss, f.depMiss, f.quotaMiss, f.certMiss} {
		for _, m := range miss {
			out = append(out, m.err)
		}
	}
	return out
}

// fetchOverview asks the cluster for everything a page is made of. What a
// refresh costs is the lists it makes, and the one `config view` that read the
// kubeconfig before them: for one namespace, or for every namespace, six lists
// (the namespaces, the nodes, and the pods, deployments, quotas and TLS Secrets
// of that scope); for n namespaces named, 2 + 4n (seven namespaces are thirty),
// at most readsAtOnce of them running together. The TLS Secrets are a kept read
// per namespace (certLife), so a refresh that finds them kept makes 2 + 3n.
func fetchOverview(ctx context.Context, scopes []selection, nsSel selection, cfg *kubeconfig) overviewFetch {
	ctx = withGate(ctx, readsAtOnce)
	var f overviewFetch
	var wg sync.WaitGroup
	wg.Add(6)
	go func() { defer wg.Done(); f.nsErr = getJSON(ctx, nsSel, "namespaces", &f.namespaces) }()
	go func() { defer wg.Done(); f.nodes, f.nodeErr = fetchNodes(ctx, nsSel) }()
	go func() {
		defer wg.Done()
		f.pods, f.podMiss, f.podErr = readEach(scopes, func(s selection) ([]podItem, *view.Error) { return fetchPods(ctx, s) })
	}()
	go func() {
		defer wg.Done()
		f.deps, f.depMiss, f.depErr = readEach(scopes, func(s selection) ([]deploymentItem, *view.Error) {
			return fetchDeployments(ctx, s)
		})
	}()
	go func() {
		defer wg.Done()
		f.quotas.Items, f.quotaMiss, f.quotaErr = readEach(scopes, func(s selection) ([]resourceQuotaItem, *view.Error) {
			quotas, verr := fetchQuotas(ctx, s)
			return quotas.Items, verr
		})
	}()
	go func() {
		defer wg.Done()
		f.certs, f.certMiss, f.certErr = readEach(scopes, func(s selection) ([]tlsCert, *view.Error) {
			read, verr := tlsCerts(ctx, s, cfg)
			return read.certs, verr
		})
	}()
	wg.Wait()
	return f
}

// overviewRefresh is how often a dashboard tile of kube.overview is re-run,
// for the person who names it in their config. A run is the six lists
// above at once, three of them across every namespace and one of those every
// TLS Secret the cluster holds — and a Secret read is a line in the API
// server's audit log on any cluster that keeps one. At the dashboard's own
// every-few-seconds pace that is hundreds of cluster-wide lists an hour per
// open terminal, for an answer that does not move that fast: a kubelet
// reports every ten seconds and the node controller waits forty more before
// it calls the node NotReady, a crash-looping pod backs off for tens of
// seconds and then minutes, and quota pressure and certificate expiry move
// by the hour. A minute shows each of those as soon as the cluster has
// decided it. The Secret list is the exception to once a run: its answer is
// kept for certLife and shared with kube.cert.list (cert.go says why), so an
// hour of this tile reads the TLS Secrets four times and not sixty. The other
// kube capabilities keep the host's pace: a pod or event list is one call, and
// a person who names one while a rollout runs wants it to move.
//
// A list of namespaces is more calls to the minute and not a different pace:
// the two cluster-scoped lists and four for each namespace named, with the
// Secrets of those kept as before, so seven namespaces are thirty lists a minute
// and, between the Secret reads, twenty-three. The cap on the list
// (maxNamespaces) is what bounds it.
const overviewRefresh = time.Minute

// overviewNSFields are the namespace selection, said for an overview: a list,
// and what naming none reads is every namespace and not the context's own as it
// is for a list of pods. The switch is the same one, and widens the same input.
//
// A list has no completion of the cluster's namespaces. nsFields' is Live, which
// is how a suggestion that contacts the cluster stays something a press of the
// key asked for and never something typing caused, and the host accepts Live on a
// String input only; the same Suggest without it would be re-run, and kubectl
// with it, on every edit of a sibling box. Under a profile that fences the
// namespace the host completes from the fence, which is the list a person
// naming several is choosing from.
func overviewNSFields() []plugin.Field {
	fields := nsFields()
	fields[0].Type = plugin.StringSlice
	fields[0].Live, fields[0].Suggest = false, nil
	fields[0].Help = "namespaces to read, one call each, at most " + strconv.Itoa(maxNamespaces) +
		" — every namespace when omitted"
	fields[1].Help = "every namespace, which is also what naming none reads"
	return fields
}

// quotaPressureThreshold is when a tracked resource is worth naming in an
// overview meant to be read in passing: 80%, the point a person still has
// time to act before a namespace starts refusing pods rather than the point
// it already has.
const quotaPressureThreshold = 0.8

// restartStorm is how many restarts make a pod that is serving worth a
// mention: over this many, and the readiness it reports is hiding a loop.
//
// A restart count only goes up while the pod lives, so it is a measure of
// history and not a rate, and no number is right for every workload. Ten is
// above the few a node reboot or a rollout of a dependency causes, and below
// what a crash loop reaches inside the half hour its back-off (ten seconds,
// doubling to five minutes) takes to climb that far. A pod that is not ready
// is already named for that and is not counted here.
const restartStorm = 10

// slotPressure is how full the schedulable nodes' pod slots may be before the
// page warns. Slots are the one capacity a cluster refuses work at without
// looking busy on CPU or memory, and a tenth of them is a few minutes of
// autoscaling or a few pods of a rollout.
const slotPressure = 0.9

// rowRoom is the width a value of the compact answer aims at: 60 columns for
// a tile, which is a third of a 180-column screen, less the four cells its
// border and padding take, the widest key a full page has (`deployments`) and
// the two cells a renderer sets between a key and its value; a tile measured on
// a 120-column screen leaves exactly this. A tile wraps what is wider, and a
// wrapped value is a second line out of the eight a tile has, so the rows below
// say what fits here first and drop the optional rest. What a row must say it
// says whatever the room, in one line of at most 80 columns.
const rowRoom = 42

// lineRoom is the width of a value that is a reason and cannot be shortened to a
// tile: the 80 columns of a line less the widest key and the two cells after it.
// The page of a cluster that could not be read has four rows and lines to spare,
// and a reason cut at the width of a healthy row is cut before it says what was
// refused.
const lineRoom = 65

// sepWidth is the width of the " · " that sets one part of a row off from the
// next.
const sepWidth = 3

// worstNames is how many of the pods that are not ready the pods row names.
const worstNames = 3

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	names, verr := namespacesOf(req)
	if verr != nil {
		return nil, verr
	}
	s, verr := selectionFor(req, names)
	if verr != nil {
		return nil, verr
	}
	cfg, cfgErr := readConfig(ctx, req.Surface())
	where := s.Context
	if where == "" && cfgErr == nil {
		where = cfg.CurrentContext
	}
	if where == "" {
		where = "unknown"
	}
	id := identity{context: where}
	if cfgErr == nil {
		if cluster, _, ns, ok := cfg.find(where); ok {
			id.server = cfg.server(cluster)
			if ns == "" {
				ns = "default"
			}
			id.namespace = ns
		}
	}
	const read = " whose pods, deployments, quotas and certificates are read"
	switch {
	case len(names) == 1:
		id.namespace = names[0] + " — the only namespace" + read
	case len(names) > 1:
		id.namespace = strings.Join(names, ", ") + " — the only namespaces" + read
	}

	// The pods, deployments, quotas and certificates are those of the namespaces
	// named, and of every namespace when none is: "what is unhealthy" is not a
	// question about one of them unless somebody asked it about one. A profile
	// that fences the namespace has asked it for them, and a fence that read
	// past its namespaces here would hand out every pod name, quota and
	// certificate of the cluster that `pod list` refuses. selectionFor has
	// already refused a namespace beside every namespace. The nodes and the
	// namespaces are cluster-scoped and read as such.
	scopes := overviewScopes(s, names)
	nsSel := s
	nsSel.Namespace, nsSel.AllNS = "", false

	// The kubeconfig is read once for the whole page. The certificates of each
	// namespace need it to name the scope they keep a read under, and one that
	// could not be read is the empty one readConfig returns, which names no
	// context: each of them asking the same file the same question again would be
	// a `config view` for every namespace on top of the lists, thirty at the cap.
	known := &cfg
	var f overviewFetch
	if cfgErr == nil && len(cfg.Contexts) == 0 {
		// No kubeconfig at all, which a current kubectl does not say: it tries
		// localhost:8080 and answers with a client-go log line about a refused
		// connection, which this page then put in its cluster row as "did not
		// answer", beside a hint to check the VPN. `config view` has answered,
		// and lists no context, so no cluster is asked.
		f.nsErr = view.Errorf("kube.noconfig", "no kubeconfig context").
			WithHint(contextHint(req.Surface(),
				"ask the operator to put a kubeconfig with a context where this machine reads one",
				"this machine's kubeconfig lists no context — `kubectl config get-contexts` is the same question, "+
					"and KUBECONFIG names the files it reads"))
	} else {
		f = fetchOverview(ctx, scopes, nsSel, known)
	}

	detail := req.Bool("detail")
	// A cluster that did not accept the credential on any of the reads is the
	// page's error and not a row of it, which no read's other answers soften: a
	// dashboard stops asking a tile whose credential was refused twice, and reads
	// that off the error a call returns (view.Error.CredentialRefused). The page
	// it was before could be neither refused nor counted, so a tile on a cluster
	// that rejected its token sent the token again at every refresh.
	//
	// Only the 401. A 403 of one list says nothing of the others and is a row
	// (below, and in the rows of the page), because the credential a Role in each
	// namespace makes is refused the lists across them and answered inside each; and
	// a credential plugin that gave nothing (kube.login, kube.credential.missing)
	// reached no API server to be refused, and is the page of a cluster never
	// contacted.
	if refusal := rejected(f.errs()...); refusal != nil {
		return nil, refusal
	}
	// The namespaces list is the read that says the cluster answers at all, and
	// its refusal is the page of a cluster that refused — unless the call names the
	// namespaces it asks about, when the credential is as likely as not the one
	// that may list inside each and nowhere across them, which is the identity a
	// Role in a namespace makes, and the reads of those are the page. The refusal
	// is then a row of the detailed page and not the end of it, for one name as
	// for several: a one-namespace tile on a Role per namespace is the common case,
	// and it would otherwise be a failure on a cluster that answered every list
	// it was asked.
	if f.nsErr != nil && (len(names) == 0 || f.nsErr.Code != "kube.denied") {
		return unanswered(req.Surface(), id, names, f.nsErr, detail), nil //nolint:nilerr
	}

	p := readOverview(f, names, time.Now())
	if !detail {
		return view.KeyValue{Pairs: p.compact(id)}, nil
	}
	return p.detail(id), nil
}

// identity is where the cluster is: the facts the compact page keeps to one
// row and `detail` opens with.
type identity struct {
	context   string
	server    string
	namespace string
}

// unanswered is the page of a cluster that could not be read at all.
//
// The cluster not answering is the headline, not a failure: this is the
// capability somebody runs *because* something is wrong, so it reports the
// unreachability and everything it could still learn from the kubeconfig,
// rather than refusing outright.
//
// But "did not answer" was applied to every failure, and only one of the
// three kinds classify() distinguishes actually means silence.
//
// Two of them mean the cluster answered and said no, which rendered as
// `did not answer — namespaces is forbidden: User ... cannot list
// resource "namespaces"` — a sentence that contradicts itself inside
// its own line.
//
// The rest never reached a cluster at all: no kubeconfig, an unknown
// context, kubectl not installed, the call cancelled. Those read worse
// than the refusal did, because "did not answer" sends somebody to
// check a VPN and a firewall over a purely local problem — and
// `kubectl` missing is the most likely way this plugin fails on a
// machine the first time.
//
// classify() already draws both distinctions and hands over its own
// hint per code; all this does is stop discarding them.
//
// The cluster that answered 401 is not a page at all: runOverview returns it
// as the error it is, before this is reached (rejected says why).
//
// The default is deliberately the vaguest of the four rather than the
// most common one. "did not answer" is a claim about the network, and
// a code this does not recognise — kube.failed is the catch-all, and
// classify() may grow others — is precisely the case where that claim
// is unsupported. An unmapped code now says only that the read did not
// happen, which is the part that is always true.
//
// The compact page keeps the message to a line, and the hint whole: it is the
// one row that wraps, because advice cut short at a column is not advice, and a
// page with nothing else to show has the lines to spare.
func unanswered(sf plugin.Surface, id identity, names []string, e *view.Error, detail bool) view.View {
	answer := "could not be read"
	switch e.Code {
	case "kube.denied":
		answer = "answered, and refused"
	case "kube.noconfig", "kube.context.unknown", "kube.kubectl.missing", "kube.cancelled",
		"kube.login", "kube.credential.missing":
		answer = "was never contacted"
	case "kube.unreachable":
		answer = "did not answer"
	}
	status := view.Pair{Key: "status", Value: "fail — the cluster " + answer}
	hint := view.Pair{Key: "what to check", Value: hintOf(sf, e)}
	if detail {
		pairs := []view.Pair{status, {Key: "context", Value: id.context}}
		if id.server != "" {
			pairs = append(pairs, view.Pair{Key: "server", Value: id.server})
		}
		if id.namespace != "" {
			pairs = append(pairs, view.Pair{Key: "namespace", Value: id.namespace})
		}
		return view.KeyValue{Pairs: append(pairs,
			view.Pair{Key: "cluster", Value: answer + " — " + e.Message}, hint)}
	}
	return view.KeyValue{Pairs: []view.Pair{
		status,
		{Key: "cluster", Value: clip(oneLine(answer+" — "+e.Message), lineRoom)},
		hint,
		{Key: "context", Value: clip(contextRow(id.context, names, nil), lineRoom)},
	}}
}

// hintOf recovers the remedy from a classified error, so the overview can put
// it on its own row instead of dropping it.
func hintOf(sf plugin.Surface, e *view.Error) string {
	if e == nil || e.Hint == "" {
		return contextHint(sf, "ask the operator what this machine is configured for",
			sf.CapabilityName("kube.context.list")+" shows what this machine is configured for")
	}
	return e.Hint
}

// detail is the whole page: the verdict and the old default's rows, which the
// compact page moved here, and then the tables.
func (p overviewPage) detail(id identity) view.View {
	sections := []view.Section{
		{ID: "cluster", Title: "Cluster", View: view.KeyValue{Pairs: p.clusterRows(id)}},
	}
	// Every node, not only the troubled ones — unlike pods and deployments
	// below. There are tens of nodes where there are hundreds of pods, and the
	// full list is what makes "two are gone" mean something: which two, out of
	// how many, running what version. A three-node cluster missing one is a
	// different morning from a fifty-node cluster missing one.
	if p.f.nodeErr == nil && len(p.f.nodes.Items) > 0 {
		sections = append(sections, view.Section{
			ID: "nodes", Title: "Nodes", View: nodeTable(p.f.nodes.Items)})
	}
	if len(p.notReady) > 0 {
		sections = append(sections, view.Section{
			ID: "pods", Title: "Pods that are not ready", View: podTable(p.notReady, true)})
	}
	if len(p.storm) > 0 {
		sections = append(sections, view.Section{
			ID: "restarts", Title: fmt.Sprintf("Pods over %d restarts", restartStorm),
			View: podTable(p.storm, true)})
	}
	if p.f.depErr == nil {
		if len(p.short) > 0 {
			sections = append(sections, view.Section{
				ID: "deployments", Title: "Deployments short of their replicas",
				View: deploymentTable(p.short, true)})
		} else {
			sections = append(sections, view.Section{
				ID: "deployments", Title: "Deployments",
				View: view.Text{Body: fmt.Sprintf("all %d %s have every replica they asked for",
					len(p.f.deps), plural(len(p.f.deps), "deployment", "deployments"))}})
		}
	}
	if len(p.pressure) > 0 {
		sections = append(sections, view.Section{
			ID: "quotas", Title: "Resource quotas over 80%",
			View: view.Text{Body: strings.Join(p.pressure, "\n")}})
	}
	if len(p.expired)+len(p.soon) > 0 {
		lines := make([]string, 0, len(p.expired)+len(p.soon))
		for _, l := range p.expired {
			lines = append(lines, l+": expired")
		}
		for _, l := range p.soon {
			lines = append(lines, fmt.Sprintf("%s: expiring within %d days", l, warnDays))
		}
		sections = append(sections, view.Section{
			ID: "certs", Title: "Certificates needing attention",
			View: view.Text{Body: strings.Join(lines, "\n")}})
	}
	return view.Sections{Items: sections}
}

// clusterRows is what the compact page was before it was made to fit eight
// lines, with the verdict in front: the server and the namespace the
// kubeconfig names, the node and pod lists by name, and the reasons in full.
//
// A failure of a read is reported in the view, on its own row, rather than
// returned: every fact read beside it was read successfully, and a namespace
// whose pods a token cannot list is an ordinary least-privilege answer, not a
// failed overview.
func (p overviewPage) clusterRows(id identity) []view.Pair {
	pairs := []view.Pair{{Key: "status", Value: p.verdict()}, {Key: "context", Value: id.context}}
	if id.server != "" {
		pairs = append(pairs, view.Pair{Key: "server", Value: id.server})
	}
	if id.namespace != "" {
		pairs = append(pairs, view.Pair{Key: "namespace", Value: id.namespace})
	}
	pairs = append(pairs, view.Pair{Key: "cluster", Value: "answering"})
	pairs = append(pairs, p.namespaceRows()...)

	// Nodes before pods, because a node that is gone explains the pods and not
	// the other way round — and because pod health alone cannot see it at all
	// (node.go says why).
	//
	// A failure here is reported and stepped over rather than returned, unlike
	// the namespace read in runOverview. Nodes are cluster-scoped, and a
	// perfectly ordinary namespaced credential — the kind
	// kube.serviceaccount.provision mints — can list pods in its namespace and
	// not list nodes at all. Making that fatal would turn the one view meant to
	// always answer into one that refuses for the most common restricted
	// identity there is. Quotas and certificates are already treated this way;
	// this is the same rule.
	if p.f.nodeErr != nil {
		pairs = append(pairs, view.Pair{Key: "nodes", Value: "could not be read — " + p.f.nodeErr.Message})
	} else {
		notReady, cordoned, pressured := nodeTrouble(p.f.nodes.Items)
		pairs = append(pairs, view.Pair{Key: "nodes",
			Value: fmt.Sprintf("%d, %d not ready", len(p.f.nodes.Items), len(notReady))})
		if len(notReady) > 0 {
			pairs = append(pairs, view.Pair{Key: plural(len(notReady), "not ready", "not ready"), Value: truncate(notReady, 6)})
		}
		if len(cordoned) > 0 {
			pairs = append(pairs, view.Pair{Key: plural(len(cordoned), "cordoned", "cordoned"), Value: truncate(cordoned, 6)})
		}
		if len(pressured) > 0 {
			pairs = append(pairs, view.Pair{Key: plural(len(pressured), "under pressure", "under pressure"), Value: truncate(pressured, 6)})
		}
	}
	if p.f.podErr != nil {
		pairs = append(pairs, view.Pair{Key: "pods", Value: "could not be read — " + p.f.podErr.Message})
	} else {
		pairs = append(pairs, view.Pair{Key: "pods",
			Value: fmt.Sprintf("%d, %d not ready", len(p.f.pods), len(p.notReady))})
		if len(p.notReady) > 0 {
			names := make([]string, len(p.notReady))
			for i, pod := range p.notReady {
				names[i] = pod.Metadata.Namespace + "/" + pod.Metadata.Name
			}
			pairs = append(pairs, view.Pair{Key: plural(len(names), "not ready", "not ready"), Value: truncate(names, 6)})
		}
	}
	pairs = append(pairs, unreadRows("pods", p.f.podMiss, p.f.podErr)...)
	pairs = append(pairs, view.Pair{Key: "pod slots", Value: p.slots.value})

	// The sibling reads above say "could not be read — <why>" when they
	// fail; these two said nothing at all, so a credential without quota or
	// certificate access produced an overview that simply had no quota and
	// no certificate lines — which is also what a healthy cluster with
	// neither under pressure looks like.
	if p.f.depErr != nil {
		pairs = append(pairs, view.Pair{Key: "deployments", Value: "could not be read — " + p.f.depErr.Message})
	} else if len(p.short) > 0 {
		names := make([]string, len(p.short))
		for i, d := range p.short {
			names[i] = fmt.Sprintf("%s/%s %d/%d", d.Metadata.Namespace, d.Metadata.Name, d.Status.ReadyReplicas, desired(d))
		}
		pairs = append(pairs, view.Pair{Key: "deployments short", Value: truncate(names, 6)})
	}
	if len(p.storm) > 0 {
		names := make([]string, len(p.storm))
		for i, pod := range p.storm {
			names[i] = fmt.Sprintf("%s/%s (%d)", pod.Metadata.Namespace, pod.Metadata.Name, healthOf(pod).restarts)
		}
		pairs = append(pairs, view.Pair{Key: fmt.Sprintf("pods over %d restarts", restartStorm), Value: truncate(names, 6)})
	}
	pairs = append(pairs, unreadRows("deployments", p.f.depMiss, p.f.depErr)...)
	if p.f.quotaErr != nil {
		pairs = append(pairs, view.Pair{Key: "quotas", Value: "could not be read — " + p.f.quotaErr.Message})
	}
	pairs = append(pairs, unreadRows("quotas", p.f.quotaMiss, p.f.quotaErr)...)
	if p.f.certErr != nil {
		pairs = append(pairs, view.Pair{Key: "certificates", Value: "could not be read — " + p.f.certErr.Message})
	}
	pairs = append(pairs, unreadRows("certificates", p.f.certMiss, p.f.certErr)...)
	if len(p.pressure) > 0 {
		pairs = append(pairs, view.Pair{
			Key:   plural(len(p.pressure), "quota over 80%", "quotas over 80%"),
			Value: truncate(p.pressure, 6)})
	}
	if len(p.expired) > 0 {
		pairs = append(pairs, view.Pair{
			Key:   plural(len(p.expired), "cert expired", "certs expired"),
			Value: truncate(p.expired, 6)})
	}
	if len(p.soon) > 0 {
		pairs = append(pairs, view.Pair{
			Key:   plural(len(p.soon), "cert expiring soon", "certs expiring soon"),
			Value: truncate(p.soon, 6)})
	}
	return pairs
}
