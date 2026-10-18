package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The rows of kube.overview: what the cluster said, read once and put into
// words twice, in the compact page and in the longer first section of `detail`.
// Computing it once is what keeps the verdict of one from contradicting the
// other.

// overviewPage is the cluster as the page reads it.
type overviewPage struct {
	f         overviewFetch
	now       time.Time
	namespace string

	nodes, pods, deployments, certs, quotas, slots rowFacts

	// What the tables of `detail` list, found on the way to the rows.
	notReady      []podItem
	storm         []podItem
	short         []deploymentItem
	pressure      []string
	expired, soon []string
}

// rowFacts is a row's value and the clauses it adds to the verdict.
type rowFacts struct {
	value   string
	clauses []clause
}

// clause is one fact the verdict may name, ranked: a failure first, then a
// part of the page that could not be read, then a warning. The order inside a
// rank is the order of the rows.
type clause struct {
	rank int
	text string
}

const (
	rankFail = iota
	rankUnread
	rankWarn
)

func readOverview(f overviewFetch, namespace string, now time.Time) overviewPage {
	p := overviewPage{f: f, now: now, namespace: namespace}
	p.readNodes()
	p.readPods()
	p.readDeployments()
	p.readCerts()
	p.readQuotas()
	p.readSlots()
	return p
}

// withNS is whether the pods, deployments, quotas and certificates read are
// those of every namespace, in which case a name needs its namespace in front
// of it to be one.
func (p *overviewPage) withNS() bool { return p.namespace == "" }

// verdict is the first row: ok, or the worst grade and the facts that earn it,
// as many as fit and the rest counted.
func (p overviewPage) verdict() string {
	var all []clause
	for _, r := range []rowFacts{p.nodes, p.pods, p.deployments, p.certs, p.quotas, p.slots} {
		all = append(all, r.clauses...)
	}
	if len(all) == 0 {
		return "ok"
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	word := "warn"
	if all[0].rank == rankFail {
		word = "fail"
	}
	texts := make([]string, len(all))
	for i, c := range all {
		texts[i] = c.text
	}
	lead := word + " — "
	return lead + fitList(texts, rowRoom-utf8.RuneCountInString(lead), moreCount, true)
}

// compact is the page a tile and an agent read: the verdict, the state of the
// nodes and the pods, what is short of its replicas, what is about to lapse or
// run out, the capacity, and where this is — each on one line.
func (p overviewPage) compact(id identity) []view.Pair {
	return []view.Pair{
		{Key: "status", Value: p.verdict()},
		{Key: "nodes", Value: p.nodes.value},
		{Key: "pods", Value: p.pods.value},
		{Key: "deployments", Value: p.deployments.value},
		{Key: "certs", Value: p.certs.value},
		{Key: "quotas", Value: p.quotas.value},
		{Key: "pod slots", Value: p.slots.value},
		{Key: "context", Value: contextRow(id.context, p.namespace, kubeletVersions(p.f.nodes.Items))},
	}
}

// contextRow is where this is: the context, the one namespace when the page
// read only that, and the kubelet versions, last because they are identity and
// not state. The skew they may show is on the nodes row.
func contextRow(context, namespace string, versions []string) string {
	only := ""
	if namespace != "" {
		only = "namespace " + namespace + " only"
	}
	// The one namespace is said whatever the room: a short list of pods answers a
	// different question than the cluster's, and a page that did not say which is
	// read as the cluster's.
	return rowOf(clip(context, rowRoom),
		func(int) string { return only },
		func(room int) string {
			switch len(versions) {
			case 0:
				return ""
			case 1:
				return within("kubelet " + versions[0])(room)
			}
			const lead = "kubelets "
			if list := fitList(versions, room-utf8.RuneCountInString(lead), moreWords, false); list != "" {
				return lead + list
			}
			return ""
		})
}

// unreadable is how a row says the credential could not read what it is made
// of. The reason is one line, however many the cluster wrote, and as wide as a
// line may be: a row that cannot be read has nothing else to say, and a reason
// cut at the width of a healthy row is cut before it names what was refused.
// The whole of it is on the detailed page.
func unreadable(e *view.Error) string {
	const lead = "could not be read — "
	return lead + clip(oneLine(e.Message), lineRoom-utf8.RuneCountInString(lead))
}

func unreadClause(what string) []clause {
	return []clause{{rank: rankUnread, text: what + " could not be read"}}
}

func (p *overviewPage) readNodes() {
	if p.f.nodeErr != nil {
		p.nodes = rowFacts{unreadable(p.f.nodeErr), unreadClause("nodes")}
		return
	}
	nodes := p.f.nodes.Items
	if len(nodes) == 0 {
		p.nodes = rowFacts{value: "none"}
		return
	}
	var down, pressured, cordoned []string
	words := map[string]int{}
	pressuredNodes := map[string]bool{}
	for _, n := range nodes {
		h := healthOfNode(n)
		name := n.Metadata.Name
		if !h.ready {
			down = append(down, name+" "+h.condition)
			words[h.condition]++
		}
		for _, pressure := range h.pressures {
			pressured = append(pressured, name+" "+pressure)
			pressuredNodes[name] = true
		}
		if h.ready && h.cordoned {
			cordoned = append(cordoned, name+" cordoned")
		}
	}
	var clauses []clause
	if len(down) > 0 {
		clauses = append(clauses, clause{rankFail, nodesDown(len(down), words)})
	}
	if n := len(pressuredNodes); n > 0 {
		clauses = append(clauses, clause{rankWarn, fmt.Sprintf("%d %s under pressure", n, plural(n, "node", "nodes"))})
	}
	trouble := slices.Concat(down, pressured, cordoned)
	skew := ""
	if kubeletSkew(kubeletVersions(nodes)) {
		skew = "skew"
	}
	p.nodes = rowFacts{
		value: rowOf(strconv.Itoa(len(nodes)),
			// The skew is a word and the trouble is a list: the list is cut to
			// leave the word its place, because a list that is already "and 2
			// more" loses less to it than a half-done upgrade loses to a name.
			func(room int) string {
				if skew != "" {
					room -= sepWidth + utf8.RuneCountInString(skew)
				}
				return fitList(trouble, room, moreWords, true)
			},
			within(skew)),
		clauses: clauses,
	}
}

// nodesDown names the nodes the control plane has lost or that report
// themselves unwell, in the word they share when they share one: NotReady and
// Unknown send an operator to different places (healthOfNode says where).
func nodesDown(n int, words map[string]int) string {
	noun := plural(n, "node", "nodes")
	if len(words) == 1 {
		for word := range words {
			return fmt.Sprintf("%d %s %s", n, noun, word)
		}
	}
	return fmt.Sprintf("%d %s not ready", n, noun)
}

// kubeletVersions is the distinct kubelet versions of the nodes read, in order.
func kubeletVersions(nodes []nodeItem) []string {
	var out []string
	for _, n := range nodes {
		if v := n.Status.NodeInfo.KubeletVersion; v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// kubeletSkew is whether the kubelets are on more than one minor version: an
// upgrade half done, or a node nobody upgraded. A patch release apart is not
// the skew the Kubernetes version policy means.
func kubeletSkew(versions []string) bool {
	minors := map[string]bool{}
	for _, v := range versions {
		minors[minorOf(v)] = true
	}
	return len(minors) > 1
}

// minorOf is "1.30" for "v1.30.2+k3s1", and the whole text for a version that
// is not major.minor.patch.
func minorOf(version string) string {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}

func (p *overviewPage) readPods() {
	if p.f.podErr != nil {
		p.pods = rowFacts{unreadable(p.f.podErr), unreadClause("pods")}
		return
	}
	pods := p.f.pods
	if len(pods) == 0 {
		p.pods = rowFacts{value: "none"}
		return
	}
	reasons := map[string]int{}
	for _, pod := range pods {
		h := healthOf(pod)
		switch {
		case !h.healthy:
			p.notReady = append(p.notReady, pod)
			reasons[notReadyReason(h)]++
		case pod.Status.Phase == "Running" && h.restarts > restartStorm:
			p.storm = append(p.storm, pod)
		}
	}
	var clauses []clause
	if n := len(p.notReady); n > 0 {
		clauses = append(clauses, clause{rankFail, fmt.Sprintf("%d %s not ready", n, plural(n, "pod", "pods"))})
	}
	if n := len(p.storm); n > 0 {
		clauses = append(clauses, clause{rankWarn,
			fmt.Sprintf("%d %s over %d restarts", n, plural(n, "pod", "pods"), restartStorm)})
	}

	// The reasons are the pods that are not serving, counted, and they are not
	// introduced by a label: "64 · CrashLoopBackOff 1, ImagePullBackOff 1" reads as
	// the pods it is, the count of them is the verdict's, and the thirteen columns
	// of "2 not ready: " are what let the second reason stay on the line. They are
	// the reason the row is there, and are given the width of a line and not of a
	// tile: a second reason is worth the wrap a narrow tile makes of it, and a
	// reason cut to "and 1 more" is the one thing the row was for.
	head := strconv.Itoa(len(pods))
	if len(p.notReady) > 0 {
		room := lineRoom - utf8.RuneCountInString(head) - sepWidth
		head += " · " + fitList(reasonGroups(reasons), room, moreWords, true)
	}
	p.pods = rowFacts{
		value: rowOf(head,
			// Said whatever the room: a pod that restarts and still serves is on no
			// other line of the page, and the verdict is the only place else its
			// count can be, behind the failures.
			func(int) string { return stormWord(len(p.storm)) },
			func(room int) string { return fitList(p.worstPods(), room, noMore, false) }),
		clauses: clauses,
	}
}

// stormWord is what the pods row says of the serving pods that restart too
// often, or nothing when there are none.
func stormWord(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d over %d restarts", n, restartStorm)
}

// notReadyReason is the word a pod that is not serving is grouped under: the
// reason its status gives, and NotReady for a pod that is Running with a
// container that is not, whose status says only Running.
func notReadyReason(h podHealth) string {
	if h.status == "" || h.status == "Running" {
		return "NotReady"
	}
	return h.status
}

// reasonGroups is "Reason n" for each reason the pods that are not ready give,
// the commonest first.
func reasonGroups(reasons map[string]int) []string {
	words := make([]string, 0, len(reasons))
	for reason := range reasons {
		words = append(words, reason)
	}
	sort.Slice(words, func(i, j int) bool {
		if reasons[words[i]] != reasons[words[j]] {
			return reasons[words[i]] > reasons[words[j]]
		}
		return words[i] < words[j]
	})
	out := make([]string, len(words))
	for i, reason := range words {
		out[i] = fmt.Sprintf("%s %d", reason, reasons[reason])
	}
	return out
}

// worstPods names the few pods that are not ready which have restarted most,
// because a pod that keeps coming back is the one somebody opens first.
func (p *overviewPage) worstPods() []string {
	worst := slices.Clone(p.notReady)
	sort.SliceStable(worst, func(i, j int) bool {
		return healthOf(worst[i]).restarts > healthOf(worst[j]).restarts
	})
	out := make([]string, 0, min(len(worst), worstNames))
	for _, pod := range worst[:min(len(worst), worstNames)] {
		out = append(out, labelOf(pod.Metadata.Namespace, pod.Metadata.Name, p.withNS()))
	}
	return out
}

// labelOf is how a thing is named on a page: namespace/name when the page reads
// every namespace, and the name alone when it reads one and the namespace is
// said once, where the context is.
func labelOf(namespace, name string, withNS bool) string {
	if !withNS {
		return name
	}
	return namespace + "/" + name
}

func (p *overviewPage) readDeployments() {
	if p.f.depErr != nil {
		p.deployments = rowFacts{unreadable(p.f.depErr), unreadClause("deployments")}
		return
	}
	deps := p.f.deps
	if len(deps) == 0 {
		p.deployments = rowFacts{value: "none"}
		return
	}
	down := 0
	for _, d := range deps {
		if d.Status.ReadyReplicas >= desired(d) {
			continue
		}
		p.short = append(p.short, d)
		if d.Status.ReadyReplicas == 0 {
			down++
		}
	}
	sort.SliceStable(p.short, func(i, j int) bool {
		a, b := p.short[i], p.short[j]
		if (a.Status.ReadyReplicas == 0) != (b.Status.ReadyReplicas == 0) {
			return a.Status.ReadyReplicas == 0
		}
		return desired(a)-a.Status.ReadyReplicas > desired(b)-b.Status.ReadyReplicas
	})
	var clauses []clause
	if down > 0 {
		clauses = append(clauses, clause{rankFail, fmt.Sprintf("%d %s down", down, plural(down, "deployment", "deployments"))})
	}
	if n := len(p.short) - down; n > 0 {
		clauses = append(clauses, clause{rankWarn, fmt.Sprintf("%d %s short", n, plural(n, "deployment", "deployments"))})
	}
	head := strconv.Itoa(len(deps))
	if len(p.short) > 0 {
		label := fmt.Sprintf("%d short: ", len(p.short))
		names := make([]string, len(p.short))
		for i, d := range p.short {
			names[i] = fmt.Sprintf("%s %d/%d", labelOf(d.Metadata.Namespace, d.Metadata.Name, p.withNS()),
				d.Status.ReadyReplicas, desired(d))
		}
		room := rowRoom - utf8.RuneCountInString(head) - sepWidth - utf8.RuneCountInString(label)
		head += " · " + label + fitList(names, room, noMore, true)
	}
	p.deployments = rowFacts{head, clauses}
}

func (p *overviewPage) readCerts() {
	if p.f.certErr != nil {
		p.certs = rowFacts{unreadable(p.f.certErr), unreadClause("certs")}
		return
	}
	certs := p.f.certs.certs
	if len(certs) == 0 {
		p.certs = rowFacts{value: "none"}
		return
	}
	p.expired, p.soon = certPressure(certs, p.now)
	var clauses []clause
	if n := len(p.expired); n > 0 {
		clauses = append(clauses, clause{rankFail, fmt.Sprintf("%d %s expired", n, plural(n, "cert", "certs"))})
	}
	if n := len(p.soon); n > 0 {
		clauses = append(clauses, clause{rankWarn, fmt.Sprintf("%d %s expiring soon", n, plural(n, "cert", "certs"))})
	}
	value := rowOf(strconv.Itoa(len(certs)),
		func(room int) string { return p.lapsed("expired", p.expired, room, true) },
		func(room int) string { return p.lapsed("soon", p.soon, room, false) })
	p.certs = rowFacts{value, clauses}
}

// lapsed is "n word: names" for the certificates that are, or nothing for none
// of them. Where the names do not fit it is "n word" and the names are the
// detailed page's, unless must says the first has to be named: a certificate
// that is expired is the one the row is for, and one about to be is a count that
// the detailed page names.
func (p *overviewPage) lapsed(word string, labels []string, room int, must bool) string {
	if len(labels) == 0 {
		return ""
	}
	count := fmt.Sprintf("%d %s", len(labels), word)
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = p.inScope(l)
	}
	if list := fitList(names, room-utf8.RuneCountInString(count)-2, noMore, must); list != "" {
		return count + ": " + list
	}
	return count
}

// inScope drops the namespace from a "namespace/name" label when the page read
// one namespace and says so once.
func (p *overviewPage) inScope(label string) string {
	if p.withNS() {
		return label
	}
	return strings.TrimPrefix(label, p.namespace+"/")
}

func (p *overviewPage) readQuotas() {
	if p.f.quotaErr != nil {
		p.quotas = rowFacts{unreadable(p.f.quotaErr), unreadClause("quotas")}
		return
	}
	if len(p.f.quotas.Items) == 0 {
		p.quotas = rowFacts{value: "none"}
		return
	}
	p.pressure = quotaPressure(p.f.quotas, quotaPressureThreshold)
	hits := quotaHits(p.f.quotas, quotaPressureThreshold)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].used > hits[j].used })
	var clauses []clause
	if n := len(hits); n > 0 {
		clauses = append(clauses, clause{rankWarn, fmt.Sprintf("%d %s over 80%%", n, plural(n, "quota", "quotas"))})
	}
	p.quotas = rowFacts{
		value: rowOf(strconv.Itoa(len(p.f.quotas.Items)), func(room int) string {
			if len(hits) == 0 {
				return ""
			}
			lead := fmt.Sprintf("%d over 80%%: ", len(hits))
			names := make([]string, len(hits))
			for i, h := range hits {
				names[i] = fmt.Sprintf("%s %s %s",
					p.inScope(h.namespace+"/"+h.quota), h.resource, percentOf(h.used, 1))
			}
			return lead + fitList(names, room-utf8.RuneCountInString(lead), noMore, true)
		}),
		clauses: clauses,
	}
}

// readSlots is how many of the schedulable nodes' pod slots are taken. It needs
// both the nodes and the pods, so it says which it is missing, and it does not
// set the pods of one namespace against the slots of every node, a use of the
// cluster that other namespaces' pods make most of.
func (p *overviewPage) readSlots() {
	switch {
	case p.namespace != "":
		p.slots = rowFacts{value: "not counted — one namespace is read"}
		return
	case p.f.nodeErr != nil:
		p.slots = rowFacts{value: "could not be read — needs the nodes"}
		return
	case p.f.podErr != nil:
		p.slots = rowFacts{value: "could not be read — needs the pods"}
		return
	}
	slots := podSlots(p.f.nodes.Items)
	if slots == 0 {
		// "0 of 0 used" would be a claim about capacity made from a read that
		// found none to count against: no node is schedulable, which is a
		// cluster in real trouble already reported by the nodes row, or one
		// with no nodes to read.
		p.slots = rowFacts{value: "0 — no node is schedulable"}
		return
	}
	used := occupiedSlots(p.f.pods)
	var clauses []clause
	if float64(used) >= slotPressure*float64(slots) {
		clauses = append(clauses, clause{rankWarn, "pod slots " + percentOf(float64(used), float64(slots)) + " used"})
	}
	p.slots = rowFacts{
		value:   fmt.Sprintf("%d of %d used (%s)", used, slots, percentOf(float64(used), float64(slots))),
		clauses: clauses,
	}
}

// rowOf is a row: its first part, which is always there, and the parts after
// it, each of which is given the room that is left, which may be none. A part
// says nothing when it has nothing to say or cannot say it in that, and says
// more than fits only for what the row is for: fitList's must, and nothing else.
func rowOf(first string, rest ...func(room int) string) string {
	out := first
	for _, part := range rest {
		if s := part(rowRoom - utf8.RuneCountInString(out) - sepWidth); s != "" {
			out += " · " + s
		}
	}
	return out
}

// within is a part that is only said when it fits the room it is given.
func within(s string) func(room int) string {
	return func(room int) string {
		if s == "" || utf8.RuneCountInString(s) > room {
			return ""
		}
		return s
	}
}

// How the items a list leaves out are counted: "a, b and 3 more" for a list
// nothing else counts, "a, b +3" for the verdict, whose room is the tightest,
// and not at all for a list the text in front of it has already counted ("2
// short: a, b").
const (
	moreWords = " and %d more"
	moreCount = " +%d"
	noMore    = ""
)

// fitList is as many of items as fit in room, with the rest counted as more
// says. With must it says the first one whatever the room, and without it says
// nothing when not even that fits.
func fitList(items []string, room int, more string, must bool) string {
	tail := func(left int) string {
		if left == 0 || more == "" {
			return ""
		}
		return fmt.Sprintf(more, left)
	}
	for k := len(items); k >= 1; k-- {
		s := strings.Join(items[:k], ", ") + tail(len(items)-k)
		if utf8.RuneCountInString(s) <= room {
			return s
		}
	}
	if must && len(items) > 0 {
		return items[0] + tail(len(items)-1)
	}
	return ""
}

// clip cuts text to max columns, with an ellipsis where it was cut.
func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}
