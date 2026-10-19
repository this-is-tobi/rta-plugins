// Command rta-plugin-kube is the read-first fast path onto a Kubernetes
// cluster: which contexts this machine has, which namespaces, pods and
// deployments a cluster holds, and one composed overview — plus a small set
// of deliberate mutations: switching the current context, and minting (and
// revoking) a scoped ServiceAccount identity for an agent to use instead of
// the operator's own ambient kubeconfig (kube.serviceaccount.*, in
// serviceaccount.go).
//
// Build it and put it on your $PATH as `rta-plugin-kube`:
//
//	cd plugins/kube && go build -o ~/.local/bin/rta-plugin-kube .
//
// It needs `kubectl` and nothing else: no address to configure, no credential
// to state. Whatever cluster kubectl can already reach, this can read, using
// the same kubeconfig, the same contexts and the same exec credential plugins
// the operator already keeps working. See kubectl.go for why that is a
// decision rather than a shortcut.
//
// # What it deliberately does not do
//
// No describe, logs, exec, scale or delete on arbitrary resources — those are
// `kubectl`'s job, and this is a fast path for the common 80% — the same line
// `git` holds against growing a `git.commit`. A plugin that reaches for "just
// one more mutation" ends up as a worse copy of the CLI it was meant to save
// you from. kube.serviceaccount.provision/.revoke are not that: they are not
// a wrapped kubectl verb on a caller-chosen resource, they are a single
// purpose-built workflow (ServiceAccount + Role + RoleBinding + token,
// composed and torn down together) that no one kubectl command offers, built
// specifically so an agent never needs the operator's own credential at all.
//
// # Pin a context, or do not
//
// State one in the operator's config and every call goes there:
//
//	plugins:
//	  kube@<digest>:
//	    context: kind-rta-lab
//
// Leave it out and calls follow whatever kubectl's current context is, which
// is what a person at a terminal usually wants. `rta explain kube.pod.list`
// prints the exact heading including the digest.
//
// Every capability here reaches off the box, so none of them appear on the
// automatic dashboard on their own; add one explicitly once you have decided
// polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: kube.overview
//
// kube.overview and every list of objects (pods, deployments, events, quotas,
// claims, certificates, pod usage, provisioned ServiceAccounts) take a list of
// namespaces, each read with a call of its own, which is how a tile on a
// connection fenced to several of them names those:
//
//	dashboard:
//	  tiles:
//	    - id: kube.overview
//	      profile: staging
//	      with: {namespace: [shop-web, shop-billing]}
//
// A kube.overview tile that names none reads the whole fence of a profile that
// fences the namespace. A list of objects is scoped on its namespaces, which the
// host fills from no fence, so a tile of one on such a profile names them too;
// with no fence, a tile that names none reads the context's own namespace.
//
// kube.namespace.list and kube.pvc.usage show rows of every namespace and cannot
// ask the cluster for fewer, so their `namespace` keeps the rows of those named
// and reads nothing for each: none named shows every namespace, or the whole
// fence of a profile that has one.
package main

import (
	"strconv"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk"
)

func main() { sdk.Serve(Plugin()) }

// connFields is the connection half of every capability's inputs.
//
// # Why `context` is Local
//
// **Choosing which cluster a call reaches is choosing a destination, and a
// remote caller may never choose a destination.** That is Field.Local's own
// rule, and a destination is a destination: a service plugin declares its connection as
// ordinary inputs so config can fill them, ordinary meant published in the
// MCP tool schema and accepted from a caller, and plugin.Resolve applies
// caller values above config — so an agent could name any server it liked and
// have the operator's credential filled in beside it.
//
// A kubeconfig is that hole with the credentials already attached. It is a
// list of every cluster this machine can reach, each with a working identity,
// and `--context` selects among them. An agent free to pass one would not be
// reading "the cluster the operator is working in" but *any* cluster in the
// file — production included, from a grant issued while pointed at a lab.
// internal/tunnel states the same rule for the same reason: a caller "can
// never supply a cluster coordinate of its own".
//
// So: config fills it, a person at a terminal passes it, and MCP cannot.
// Nothing is lost — an operator who wants an agent reading a specific cluster
// states it in config, which is the deliberate act that decision deserves.
//
// `namespace` is *not* Local, and the difference is the point: a namespace is
// a record inside a cluster somebody already chose, which is exactly what
// Scope means everywhere else in rta. kv.get's key works the same way.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "context", Type: plugin.String, Config: "context", Local: true,
			Help:    "kubeconfig context to use — the current one when omitted",
			Suggest: suggestContexts},
	}
}

// nsFields are the inputs for the capabilities that list namespaced objects: the
// namespaces to read, as a list (namespacelist.go says how each is read and what
// naming none reads), and the switch that widens it.
//
// Every capability that takes them but kube.overview names `namespace` as its
// Scope, which is the record a grant is checked against and so the one thing the
// host fills from nothing (plugin.ProfileFillable): a profile that fences the
// namespace refuses as blank the call that names none, and its `set: namespace:`
// is no default. namespacelist.go says what that buys and costs, and why the
// overview, which names no record, is the other trade.
//
// The list has no completion of the cluster's namespaces. The old one was Live,
// which is how a suggestion that contacts the cluster stays something a press of
// the key asked for and never something typing caused, and the host accepts Live
// on a text input only; the same Suggest without it would be re-run, and kubectl
// with it, on every edit of a sibling box. Under a profile that fences the
// namespace the host completes from the fence, which is the list a person naming
// several is choosing from.
func nsFields() []plugin.Field {
	return []plugin.Field{
		{Name: "namespace", Short: "n", Type: plugin.StringSlice, Config: "namespace",
			Help: "namespaces to read, one call each, at most " + strconv.Itoa(maxNamespaces) +
				" — the context's own when omitted"},
		// Widens: the switch reads past `namespace`, and a profile that fences the
		// namespace to the one or two an operator named holds every value of it,
		// which the host can only extend to this switch if it is told it is one.
		{Name: "all-namespaces", Short: "A", Type: plugin.Bool,
			Widens: "namespace", Help: "every namespace instead of the ones named"},
	}
}

// filterFields is the namespace input of the two reads that show rows of every
// namespace and cannot ask the cluster for fewer: kube.namespace.list prints the
// namespaces themselves, and kube.pvc.usage the claim of every volume a kubelet
// reports. `namespace` keeps the rows of those named and reads nothing for each of
// them, so it has no switch to widen and no cap, and it is no Scope: nothing here
// is a record a grant is narrowed to, so a profile that fences the namespace
// fills a call that names none with everything the fence allows, and holds a name
// outside it to the fence. Without the input a fence had nothing to hold them
// to, and these two printed every namespace of the cluster to a connection
// fenced to a few.
func filterFields() []plugin.Field {
	return []plugin.Field{
		{Name: "namespace", Short: "n", Type: plugin.StringSlice, Config: "namespace",
			Help: "only the rows of these namespaces — every namespace when omitted"},
	}
}

// cap assembles a capability's inputs in the order a person reads them: its
// own first, then the namespace selection, then the connection.
//
// NoPreview on every capability, the way plugins/pg and plugins/vault set it
// on every one of theirs, and for the property rather than the accident: each
// of these reaches off the box, so none belongs on a dashboard that refreshes
// on a timer without somebody having said so.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(append(own, c.Inputs...), connFields()...)
	c.NoPreview = true
	return c
}

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut — an index entry carries this verbatim, and a
// version is a fact about a release, not about the source it came from.
var version = "dev"

func Plugin() plugin.Plugin {
	// prealloc reads the one append below as a loop to be sized for. It is not
	// one: this is a literal of every capability in the plugin, joined once
	// with the ServiceAccount family that lives in its own file, and a make()
	// with a hand-counted capacity would be a number to keep right for nothing.
	capabilities := []plugin.Capability{ //nolint:prealloc
		{
			ID:      "kube.context.list",
			Summary: "Every context in this machine's kubeconfig, and which one is current",
			// Words for what it returns: the contexts, which kubectx also lists,
			// and the clusters they point at. Not "environments", which rta
			// itself means by a profile, nor "switch", which is
			// kube.context.set's word: a search for either found a read that
			// needs a grant and answers a different question.
			Keywords: []string{"kubectx", "clusters"},
			Description: "Reads the kubeconfig only — no cluster is contacted, so this answers " +
				"even when every cluster in it is unreachable. The current context is marked, " +
				"and it is the one every other capability here uses unless config names another.",
			Safety:     plugin.Read,
			Idempotent: true,
			// A read that contacts nothing, and still the inventory of every
			// destination this machine can reach: each context's cluster, user,
			// default namespace and API server address. An agent is not handed
			// the names of the operator's profiles for the same reason (the
			// profile schema publishes no enum and an unknown name is refused as
			// an ungranted one is), and this would give it the same map a level
			// down. It costs a person at a terminal nothing; an agent asks the
			// operator, who knows which context it should be using.
			NeedsGrant: true,
			NoPreview:  true,
			Run:        runContextList,
		},
		cap(plugin.Capability{
			ID:       "kube.context.get",
			Summary:  "The current context in full: cluster, user and default namespace",
			Keywords: []string{"kubectx", "whoami", "cluster", "user"},
			Description: "What a call from this machine would reach right now. Reads the " +
				"kubeconfig only; the cluster is not contacted.",
			Safety:     plugin.Read,
			Idempotent: true,
			Run:        runContextGet,
		}),
		cap(plugin.Capability{
			ID:       "kube.namespace.list",
			Summary:  "Namespaces in the cluster, with their status and age",
			Keywords: []string{"ns", "projects", "tenants"},
			Description: "The first capability here that contacts the cluster, so it is also " +
				"the quickest way to find out whether the current context can reach one. " +
				filterNamespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Run:        runNamespaceList,
		}, filterFields()...),
		cap(plugin.Capability{
			ID:       "kube.node.list",
			Summary:  "Nodes, with readiness, cordon state and the pressures a kubelet reports",
			Keywords: []string{"workers", "machines", "drain", "notready", "taints"},
			Description: "Conditions, not usage — no metrics-server needed, unlike " +
				"kube.metrics.node. Three statuses and they mean different things: NotReady is " +
				"a kubelet reporting a problem, Unknown is a kubelet that stopped reporting at " +
				"all, and SchedulingDisabled is an operator having cordoned the node on purpose. " +
				"The pressure column is the kubelet's own MemoryPressure/DiskPressure/PIDPressure " +
				"— a node can be Ready and under pressure at the same time, which is the state " +
				"worth catching before it evicts anything.",
			Safety:     plugin.Read,
			Idempotent: true,
			Run:        runNodeList,
		}),
		cap(plugin.Capability{
			ID:       "kube.pod.list",
			Summary:  "Pods in one namespace or several, with readiness, restarts and age",
			Keywords: []string{"workloads", "crashloop", "oom", "pending", "containers"},
			Examples: []plugin.Example{
				{Title: "unhealthy pods across the cluster", Inputs: map[string]any{"all-namespaces": true, "unhealthy": true}},
				{Title: "the pods of one namespace", Inputs: map[string]any{"namespace": []string{"production"}}},
				{Title: "the pods of two namespaces", Inputs: map[string]any{"namespace": []string{"shop-web", "shop-billing"}}},
			},
			Description: namespaceWords + " Status, ready and restarts are what `kubectl get pods` " +
				"prints for the same pod: the status is the word that explains the phase " +
				"(Evicted, Terminating, Init:1/3, CrashLoopBackOff), and ready counts the " +
				"containers that are ready over the containers the pod's spec declares, so a pod " +
				"nothing has started yet reads 0/2 rather than 0/0. Restarts are worth reading: " +
				"a pod that is Running and has restarted forty times is not healthy, and only " +
				"one of those two facts shows in its status. `unhealthy` narrows to pods that " +
				"are not serving: Failed, Pending, Unknown, stuck terminating past the time " +
				"they were to be gone, or Running without every container ready. A pod in " +
				"Succeeded is not included — a finished Job is not a broken one. The same " +
				"judgement kube.overview makes, available here without the rest of the overview.",
			Agent: "Pods of the namespaces read. " + agentNamespaceWords + " Status, ready and restarts are " +
				"what kubectl prints: the status is the word that explains the phase (Evicted, Terminating, " +
				"Init:1/3, CrashLoopBackOff), and ready is ready containers over the containers the spec " +
				"declares, so a pod nothing has started reads 0/2. A Running pod restarted forty times is not " +
				"healthy. `unhealthy` keeps the pods that are not serving: Failed, Pending, Unknown, stuck " +
				"terminating, or Running without every container ready. A finished Job is not one.",
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runPodList,
		}, append(nsFields(), plugin.Field{Name: "unhealthy", Type: plugin.Bool,
			Help: "only pods that are not serving — Failed, Pending, Unknown, stuck terminating, or Running without every container ready"})...),
		cap(plugin.Capability{
			ID:       "kube.deployment.list",
			Summary:  "Deployments in one namespace or several, with how many replicas are actually ready",
			Keywords: []string{"rollout", "workloads", "apps", "scale"},
			Examples: []plugin.Example{
				{Title: "one namespace's deployments", Inputs: map[string]any{"namespace": []string{"production"}}},
			},
			Description: "Ready against desired, which is the number that says whether a " +
				"rollout finished. " + namespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runDeploymentList,
		}, nsFields()...),
		cap(plugin.Capability{
			ID:       "kube.event.list",
			Summary:  "What the cluster is complaining about, oldest-running problems still visible",
			Keywords: []string{"warnings", "errors", "troubleshoot", "failing", "alerts"},
			Examples: []plugin.Example{
				{Title: "what is wrong anywhere in the cluster", Inputs: map[string]any{"all-namespaces": true}},
			},
			Description: "Warnings only unless `normal`: on a cluster running any active " +
				"operator the Normal events are routine narration and outnumber the warnings " +
				"heavily.\n\nAn Event is a counter, not a log line — a recurring problem updates " +
				"the existing event rather than appending one — so first-seen and count are " +
				"reported alongside last-seen. Those two columns are the payload: an event first " +
				"seen eleven days ago with thirteen thousand occurrences is a different signal " +
				"from a one-off thirty seconds ago, and last-seen alone renders them the same. " +
				"It also means the usual \"events only go back an hour\" is true only for " +
				"problems that stopped: the TTL runs from last-seen, so anything still recurring " +
				"is never collected and its first-seen can be weeks old.\n\n" + namespaceWords,
			Agent: "Warnings only unless `normal` is set: Normal events are routine narration. An Event is a " +
				"counter, not a log line, so first-seen and count come with last-seen: an event first seen " +
				"eleven days ago with thirteen thousand occurrences is not a one-off from thirty seconds ago. " +
				"A problem that keeps recurring is never collected, so its first-seen can be weeks old. " +
				agentNamespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runEventList,
		}, append(nsFields(), plugin.Field{Name: "normal", Type: plugin.Bool,
			Help: "include Normal events, not only Warnings"})...),
		cap(plugin.Capability{
			ID:       "kube.quota.list",
			Summary:  "ResourceQuota pressure per namespace: used against hard, as a percentage",
			Keywords: []string{"limits", "resourcequota", "capacity", "usage"},
			Examples: []plugin.Example{
				{Title: "quota pressure across the cluster", Inputs: map[string]any{"all-namespaces": true}},
			},
			Description: "One row per resource a quota tracks, not one row per quota object — " +
				"cpu, memory and pod-count headroom read as a percentage rather than two numbers " +
				"to do the division on by hand. LimitRange objects are noted by count rather than " +
				"fully modeled; their shape does not table-ize alongside a used/hard resource map. " +
				"Two calls for each namespace: its quotas and its limit ranges. " + namespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runQuotaList,
		}, nsFields()...),
		cap(plugin.Capability{
			ID:       "kube.pvc.list",
			Summary:  "PersistentVolumeClaims: capacity, requested size, storage class and phase",
			Keywords: []string{"storage", "volumes", "disk", "pv", "claims"},
			Examples: []plugin.Example{
				{Title: "the claims of one namespace", Inputs: map[string]any{"namespace": []string{"production"}}},
			},
			Description: "Provisioned capacity, not how full a volume actually is — that number " +
				"lives in kubelet volume stats, a different and more involved mechanism this does " +
				"not reach. A Pending PVC (no bound PersistentVolume yet) reports its requested " +
				"size and an empty capacity, which is the honest state of an unfulfilled claim. " +
				namespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runPVCList,
		}, nsFields()...),
		cap(plugin.Capability{
			ID:       "kube.cert.list",
			Summary:  "Every TLS certificate this cluster stores as a Secret, and its expiry",
			Keywords: []string{"tls", "ssl", "expire", "expiration", "secrets", "x509"},
			Examples: []plugin.Example{
				{Title: "every certificate the cluster stores", Inputs: map[string]any{"all-namespaces": true}},
			},
			Description: "Reads type: kubernetes.io/tls Secrets only, selected server-side so no " +
				"other secret's data ever leaves the API server for this process. The TLS Secrets " +
				"it does select arrive whole, tls.key included — Kubernetes cannot project a subset " +
				"of a Secret's data, so no way of asking avoids that. Only tls.crt is decoded; the " +
				"private key is never parsed, rendered, logged or stored, but it does cross the " +
				"wire into this process, and each read is a Secret list in the API server's audit " +
				"log. So the certificates are read at most every 15 minutes per context and " +
				"namespace, and kube.overview draws on the same read: an answer can be that old, " +
				"and says so when it is. As a tile it is re-run at most hourly. The leaf " +
				"certificate's own expiry is judged on the same 30-day window `cert.expiry` and " +
				"`audit.web` use.\n\n" + namespaceWords,
			Agent: "Every kubernetes.io/tls Secret's leaf certificate: subject, expiry and a status on the same " +
				"30-day window `cert.expiry` uses. Only tls.crt is decoded. The Secrets arrive whole, private keys " +
				"included, so they are read at most every 15 minutes per context and namespace, shared with " +
				"kube.overview: an answer can be that old, and says so when it is. " + agentNamespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Refresh:    time.Hour,
			Run:        runCertList,
		}, nsFields()...),
		cap(plugin.Capability{
			ID:       "kube.metrics.pod",
			Summary:  "Pod CPU/memory usage against each pod's own limit, worst pressure first",
			Keywords: []string{"top", "oom", "throttling", "limits", "resources"},
			Examples: []plugin.Example{
				{Title: "the pods under most pressure, cluster-wide", Inputs: map[string]any{"all-namespaces": true}},
			},
			Description: "Needs the metrics-server add-on (metrics.k8s.io); a cluster without it " +
				"names that in the error rather than a bare \"not found\". Sorted by memory " +
				"pressure — the failure mode a container hits is OOMKilled, not \"CPU too high\" " +
				"— so the pod closest to its own limit leads regardless of namespace or name. " +
				"Two calls for each namespace: the usage and the pods whose limits it is read against. " +
				namespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			Scope:      "namespace",
			Run:        runMetricsPod,
		}, nsFields()...),
		cap(plugin.Capability{
			ID:       "kube.metrics.node",
			Summary:  "Node CPU/memory usage against what the node can actually allocate",
			Keywords: []string{"top", "allocatable", "capacity", "resources"},
			Description: "Same metrics-server dependency as kube.metrics.pod. Allocatable, not " +
				"capacity: a node reserves some of its own resources for the kubelet and system " +
				"daemons, and allocatable is what workloads can actually be scheduled into.",
			Safety:     plugin.Read,
			Idempotent: true,
			Run:        runMetricsNode,
		}),
		cap(plugin.Capability{
			ID:       "kube.metrics.pressure",
			Summary:  "Kernel pressure stall per node: is anything waiting, and is it getting worse",
			Keywords: []string{"psi", "contention", "starvation", "throttling"},
			Examples: []plugin.Example{
				{Title: "the pressure on one node", Inputs: map[string]any{"node": "worker-1"}},
			},
			Description: "Reads the kubelet's own Summary API, not metrics-server. Pressure " +
				"answers a question a usage percentage cannot: whether work is actually being " +
				"held up. A node at 90% CPU with nothing waiting is a node being used well.\n\n" +
				"Each resource is reported over a 10-second and a 5-minute window, and the pair " +
				"is the point — a short window above the long one is pressure building, below it " +
				"is pressure clearing. That is the shape you would otherwise open a dashboard " +
				"for.\n\nThe \"some\" series is reported, meaning at least one task was stalled. " +
				"The \"full\" series is not: for CPU it is defined as zero at system level, so a " +
				"node stalling a third of the time reads as perfectly idle through it.\n\nNeeds " +
				"cgroup v2 and a Linux kernel 4.20 or newer; nodes without it are named rather " +
				"than shown as zeroes. Needs the nodes/proxy permission — see kube.pvc.usage.",
			Agent: "Kernel pressure stall per node, from the kubelet's Summary API: whether work is being held " +
				"up, which a usage percentage cannot say. Each resource is read over a 10-second and a " +
				"5-minute window: the short above the long is pressure building, below it is clearing. Only " +
				"the \"some\" series is reported (at least one task stalled); \"full\" is zero for CPU at " +
				"system level. Needs cgroup v2 and Linux 4.20 or newer: nodes without it are named, not shown " +
				"as zeroes. Needs the nodes/proxy permission. `node` narrows to one node.",
			Safety:     plugin.Read,
			Idempotent: true,
			Run:        runMetricsPressure,
		}, plugin.Field{Name: "node", Type: plugin.String,
			Help: "one node instead of every node"}),
		cap(plugin.Capability{
			ID:       "kube.pvc.usage",
			Summary:  "How full each PersistentVolumeClaim actually is, worst first",
			Keywords: []string{"disk", "space", "capacity", "df", "storage"},
			Examples: []plugin.Example{
				{Title: "the claims on one node", Inputs: map[string]any{"node": "worker-1"}},
			},
			Description: "The number kube.pvc.list deliberately does not report, because it " +
				"comes from somewhere else entirely: the kubelet's Summary API, one call per " +
				"node, rather than the PVC objects themselves.\n\nTwo limits worth knowing. Only " +
				"volumes a live pod currently mounts are measured at all — an unmounted claim " +
				"has no kubelet reporting on it and simply will not appear. And reaching this " +
				"needs the nodes/proxy permission, which is indivisible: it covers the whole " +
				"kubelet API including exec on every pod on the node. That is why this is a " +
				"separate capability rather than columns on kube.pvc.list, and why neither it " +
				"nor kube.metrics.pressure can be granted to a minted ServiceAccount.\n\nA node " +
				"that cannot be read is named, because a missing node means missing claims.\n\n" +
				filterNamespaceWords,
			Agent: "How full each claim is, from the kubelet's Summary API (the claim list reports provisioned " +
				"capacity only), worst first. Only volumes a live pod mounts are measured: an unmounted claim " +
				"does not appear. A node that cannot be read is named, because a missing node means missing " +
				"claims. Needs the nodes/proxy permission, which covers the whole kubelet API. `node` narrows " +
				"to one node. " + agentFilterNamespaceWords,
			Safety:     plugin.Read,
			Idempotent: true,
			// A call to the kubelet of every node, through the API server's
			// nodes/proxy, which is the permission that reaches exec on every
			// pod there: a tile at the host's thirty seconds is that on every
			// node twice a minute, for volumes that fill by the hour.
			Refresh: 5 * time.Minute,
			Run:     runPVCUsage,
		}, append(filterFields(), plugin.Field{Name: "node", Type: plugin.String,
			Help: "one node instead of every node"})...),
		cap(plugin.Capability{
			ID:       "kube.overview",
			Summary:  "One cluster at a glance: its verdict, and what is not healthy",
			Keywords: []string{"summary", "health", "dashboard", "status"},
			Description: "The first row, `status`, is the verdict: ok, or warn or fail with the worst facts. Fail " +
				"is a node not Ready, a pod not serving, a deployment with no replica ready, an expired " +
				"certificate, or nothing readable at all; warn is any other fact the rows name, and a row " +
				"the credential could not read. Then: the nodes (not Ready, under pressure or cordoned, " +
				"by name, and `skew` when the kubelets span minor versions), the pods not serving " +
				"(Failed, Pending, Unknown, stuck terminating, or Running without every container ready) " +
				"by the reason they give, and serving pods restarted over 10 times, deployments short of " +
				"their replicas, TLS certificates expired or expiring within 30 days, quotas over 80%, " +
				"pod-slot headroom from the schedulable nodes' own max-pods and the context with the " +
				"kubelet versions. A finished Job (Succeeded) is not a pod that is not serving, and a " +
				"cordoned node is reported, not counted as not ready: neither is a fault. `detail` adds " +
				"the server, the namespaces and the nodes, pods and deployments themselves.\n\n" +
				"Reads more than it names: every pod, Deployment, ResourceQuota and TLS Secret of each " +
				"namespace `namespace` lists (none: every namespace; at most 30), with a call of its own " +
				"for each, so a Role per namespace is enough. Give a list in a tile's `with:`, the call " +
				"or a profile fence, not in `plugins.kube.namespace`, which provision and revoke read " +
				"as text. The nodes are always the cluster's; with namespaces named the pod slots " +
				"are not counted, a namespace a list could not be read in is named on that row, and a " +
				"missing one is a warning. The TLS Secrets arrive whole, private keys included (see " +
				"kube.cert.list), so they are read at most every 15 minutes per context and namespace, " +
				"shared with it; the rest on every run. A credential that cannot list the nodes, or the " +
				"namespaces when some are named, still gets the rest: that read is stepped over, not a " +
				"failure.",
			Agent: "`status` is the verdict (ok, or warn or fail with the worst facts), then the nodes (not Ready, under " +
				"pressure, cordoned, kubelet skew), pods not serving by reason and serving pods over 10 restarts, " +
				"deployments short of replicas, expired or expiring TLS certificates, quotas over 80%, pod-slot " +
				"headroom from the schedulable nodes' max-pods, and the context. A finished Job and a cordoned node " +
				"are not faults. `detail` adds the server, the namespaces and the pods themselves. `namespace` is " +
				"an array: each is read with a call of its own (at most 30), none reads every namespace; " +
				"named ones have no pod slots, and an unreadable or missing one is said. " +
				"Secrets (keys included) at most every 15 minutes. Without node access the rest answers.",
			// No Scope, unlike the lists that take the same two inputs, and on
			// purpose. An input a capability names as its Scope is the record a
			// grant is checked against, which no profile may fill (see
			// plugin.ProfileFillable), so a connection fenced to one namespace
			// would refuse a call that does not name it. The overview names no
			// record: its answer is the cluster's, narrowed when somebody asks,
			// and a fence that holds some namespaces is that ask, made once by
			// the operator for every call through the connection. The input is
			// a list for that reason: a tile on a connection fenced to several
			// names them all, and the host holds each element to the fence.
			Safety:     plugin.Read,
			Idempotent: true,
			Detailed:   true,
			Refresh:    overviewRefresh,
			Run:        runOverview,
		}, overviewNSFields()...),
		{
			// Not through cap(): every other capability takes a
			// `--context` saying which cluster to read, and this one's
			// whole subject is which context becomes current. Offering
			// both would be two spellings of the same argument, one of
			// which kubectl ignores.
			ID:      "kube.context.set",
			Summary: "Switch this machine's current kubeconfig context",
			Examples: []plugin.Example{
				{Title: "switch to another cluster's context", Inputs: map[string]any{"name": "staging"}},
			},
			Keywords: []string{"switch", "kubectx", "use", "change"},
			Description: "Rewrites current-context in the kubeconfig, which is what `kubectl " +
				"config use-context` does. Every later command on this machine follows it — " +
				"kubectl's, this plugin's, and anything else reading the same file — which is " +
				"why it needs a grant naming the context you mean, and why the grant is worth " +
				"reading twice before you issue it.",
			// Write and not Destructive: nothing is deleted and the
			// previous context is one call away. NeedsGrant anyway, on
			// the third trigger for one — a quiet, reversible mutation whose
			// real risk is what it silently enables afterward. Scope is
			// the context name, so a grant reads "kube.context.set
			// kind-rta-lab" and authorizes exactly that switch and no
			// other.
			Safety:     plugin.Write,
			Idempotent: true,
			NeedsGrant: true,
			Scope:      "name",
			NoPreview:  true,
			Inputs: []plugin.Field{
				{Name: "name", Type: plugin.String, Positional: true, Required: true,
					Help:    "the context to switch to — `kube.context.list` shows them, but needs a grant of its own for an agent",
					Suggest: suggestContexts},
			},
			Run: runContextSet,
		},
	}
	capabilities = append(capabilities, serviceAccountCapabilities()...)
	return plugin.Plugin{
		Name:    "kube",
		Summary: "Read-first Kubernetes: contexts, namespaces, pods, deployments",
		Version: version,
		// Everything this plugin does is a kubectl call, and kubectl cannot
		// make one without the kubeconfig. Declaring it is a request, not a
		// grant: rta denies credential locations to every plugin by default,
		// and `rta plugin allow kube` is where an operator decides — against
		// this artifact's digest, so a rebuild asks again.
		Needs:        []plugin.Need{plugin.NeedKubeconfig},
		Capabilities: capabilities,
	}
}
