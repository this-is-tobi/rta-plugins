package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The half that contacts the cluster: namespaces, pods, deployments.

type namespaceItem struct {
	Metadata meta `json:"metadata"`
	Status   struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

type podItem struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		NodeName       string     `json:"nodeName"`
		Containers     []struct{} `json:"containers"`
		InitContainers []struct {
			Name          string `json:"name"`
			RestartPolicy string `json:"restartPolicy"`
		} `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
		InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
		ContainerStatuses     []containerStatus `json:"containerStatuses"`
	} `json:"status"`
}

type containerStatus struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	Started      *bool  `json:"started"`
	RestartCount int    `json:"restartCount"`
	State        state  `json:"state"`
}

// state is a container's current state, of which exactly one field is set.
type state struct {
	Waiting *struct {
		Reason string `json:"reason"`
	} `json:"waiting,omitempty"`
	Running    *struct{} `json:"running,omitempty"`
	Terminated *struct {
		Reason   string `json:"reason"`
		ExitCode int    `json:"exitCode"`
		Signal   int    `json:"signal"`
	} `json:"terminated,omitempty"`
}

type deploymentItem struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Replicas *int `json:"replicas"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas     int `json:"readyReplicas"`
		UpdatedReplicas   int `json:"updatedReplicas"`
		AvailableReplicas int `json:"availableReplicas"`
	} `json:"status"`
}

// runNamespaceList lists the namespaces, or those of them a call names.
//
// **`namespace` keeps rows here; it is not a place to read.** Namespaces are
// cluster-scoped, so kubectl has no namespace to be asked in and the answer is the
// cluster's list whichever are wanted; what is not wanted is dropped here, before
// anything is returned. That is the honest minimum, and it is what a fence needs:
// this capability declared no namespace, so a profile that fenced the namespace
// did not bind it, and it printed every namespace of the cluster, those outside
// the fence among them.
func runNamespaceList(ctx context.Context, req plugin.Request) (view.View, error) {
	names, verr := namespaceNames(req)
	if verr != nil {
		return nil, verr
	}
	s, verr := selectionFor(req, nil)
	if verr != nil {
		return nil, verr
	}
	var out list[namespaceItem]
	if verr := getJSON(ctx, s, "namespaces", &out); verr != nil {
		return nil, verr
	}
	named := make(map[string]bool, len(names))
	for _, name := range names {
		named[name] = true
	}
	exists := make(map[string]bool, len(out.Items))
	rows := make([][]view.Cell, 0, len(out.Items))
	for _, n := range out.Items {
		exists[n.Metadata.Name] = true
		if len(names) > 0 && !named[n.Metadata.Name] {
			continue
		}
		rows = append(rows, []view.Cell{view.Plain(n.Metadata.Name), view.Plain(n.Status.Phase), ageCell(n.Metadata.CreationTimestamp)})
	}
	// Sorted before the table is filled, since a value is kept beside the row
	// it was added with.
	sort.Slice(rows, func(i, j int) bool { return rows[i][0].Text < rows[j][0].Text })
	t := view.Table{
		Columns: []view.Column{
			{Name: "namespace"}, {Name: "status", Kind: view.KindStatus}, {Name: "age", Kind: view.KindDuration},
		},
	}
	for _, row := range rows {
		t.Add(row...)
	}
	t.Total = len(t.Rows)
	empty := "no namespaces"
	if len(names) > 0 {
		empty = "no namespace called " + truncate(names, 4)
	}
	t = withEmpty(t, empty)

	// A name the cluster has no namespace of is said, because an empty table that
	// is the answer to a misspelling reads as a cluster with nothing in it, and
	// Table.Empty never reaches an agent. Not a gap in the page: nothing is missing
	// from it that exists.
	var missing []string
	for _, name := range names {
		if !exists[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Warnings = append(t.Warnings, view.Error{
			Code:     "kube.namespace.missing",
			Message:  s.where() + " has no namespace called " + truncate(missing, 4),
			Hint:     "check the name against the cluster's namespaces",
			Advisory: true,
		})
	}
	return t, nil
}

// podHealth is what a pod's row says about it, and whether it is serving.
type podHealth struct {
	ready    string
	restarts int
	status   string
	healthy  bool
}

// The words of a pod's status that the code below has to recognise rather
// than pass through.
const (
	podCompleted       = "Completed"
	podNodeLost        = "NodeLost"
	podSchedulingGated = "SchedulingGated"
	podInitializing    = "PodInitializing"
)

// podView is the READY, STATUS and RESTARTS cells of a pod, computed the way
// `kubectl get pods` computes them.
type podView struct {
	ready, total, restarts int
	status                 string
}

// viewOf is printPod from kubernetes/pkg/printers/internalversion/printers.go
// without the cells this table does not print, and it is a port rather than a
// reading of the phase because the two disagree in exactly the states a
// person opens this table to look at. The phase says Failed for a pod the
// kubelet evicted, Running for one that is being deleted, Pending for one
// whose init container has restarted seven times; the status column kubectl
// prints says Evicted, Terminating and Init:CrashLoopBackOff, and a table
// read next to `kubectl get pods` that says otherwise is one nobody trusts.
//
// The order of the steps is the algorithm and is kept as upstream has it:
// a later step overwrites the word an earlier one chose, the init loop stops
// at the first init container that has not finished, and the regular
// containers are walked last to first so that the first one in the spec has
// the final word. READY is over the containers the spec declares, plus the
// init containers that keep running beside them (restartPolicy: Always), and
// not over the statuses the kubelet has reported: a pod nothing has started
// has no statuses, and 0/0 for it is a claim that it asks for nothing.
func viewOf(p podItem) podView {
	st := p.Status
	reason := st.Phase
	if st.Reason != "" {
		reason = st.Reason
	}
	for _, c := range st.Conditions {
		if c.Type == "PodScheduled" && c.Reason == podSchedulingGated {
			reason = podSchedulingGated
		}
	}

	total := len(p.Spec.Containers)
	sidecars := map[string]bool{}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy == "Always" {
			sidecars[c.Name] = true
			total++
		}
	}

	ready, restarts, sidecarRestarts := 0, 0, 0
	initializing := false
	for i, c := range st.InitContainerStatuses {
		restarts += c.RestartCount
		if sidecars[c.Name] {
			sidecarRestarts += c.RestartCount
		}
		switch {
		case c.State.Terminated != nil && c.State.Terminated.ExitCode == 0:
			continue
		case sidecars[c.Name] && c.Started != nil && *c.Started:
			if c.Ready {
				ready++
			}
			continue
		case c.State.Terminated != nil:
			reason = "Init:" + terminationWord(c.State.Terminated.Reason, c.State.Terminated.Signal, c.State.Terminated.ExitCode)
			initializing = true
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != podInitializing:
			reason = "Init:" + c.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
			initializing = true
		}
		break
	}

	if !initializing || conditionIsTrue(p, "Initialized") {
		// What is still being counted is the containers that are running now:
		// an init container that finished and restarted twice on the way is
		// history, and the sidecars are running and counted with the rest.
		restarts = sidecarRestarts
		hasRunning := false
		errorReason := ""
		for i := len(st.ContainerStatuses) - 1; i >= 0; i-- {
			c := st.ContainerStatuses[i]
			restarts += c.RestartCount
			switch {
			case c.State.Waiting != nil && c.State.Waiting.Reason != "":
				reason = c.State.Waiting.Reason
			case c.State.Terminated != nil:
				reason = terminationWord(c.State.Terminated.Reason, c.State.Terminated.Signal, c.State.Terminated.ExitCode)
				if c.State.Terminated.ExitCode != 0 {
					errorReason = reason
				}
			case c.Ready && c.State.Running != nil:
				hasRunning = true
				ready++
			}
		}
		// A container that finished beside others that are running is not the
		// pod's word: Completed is what the last container to exit said, and
		// the pod is as ready as its Ready condition says.
		if reason == podCompleted {
			switch {
			case hasRunning && conditionIsTrue(p, "Ready"):
				reason = "Running"
			case errorReason != "":
				reason = errorReason
			case hasRunning:
				reason = "NotReady"
			}
		}
	}

	switch {
	case p.Metadata.DeletionTimestamp != nil && st.Reason == podNodeLost:
		reason = "Unknown"
	case p.Metadata.DeletionTimestamp != nil && !finished(st.Phase):
		reason = "Terminating"
	}

	// Every word above may be one the cluster supplied: status.reason is
	// writable by anything that can update pods/status, and a container's
	// reasons come from a runtime. A cell is not where a stranger writes an
	// escape sequence or a second row, so the word is shown the way a name is.
	if reason != "" {
		reason = plugin.ListedName(reason)
	}
	return podView{ready: ready, total: total, restarts: restarts, status: reason}
}

// terminationWord is how a container's end is worded when kubectl words it:
// the runtime's reason when there is one, else the signal that killed it, else
// the exit code.
func terminationWord(reason string, signal, exitCode int) string {
	switch {
	case reason != "":
		return reason
	case signal != 0:
		return fmt.Sprintf("Signal:%d", signal)
	default:
		return fmt.Sprintf("ExitCode:%d", exitCode)
	}
}

func conditionIsTrue(p podItem, condition string) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == condition {
			return c.Status == "True"
		}
	}
	return false
}

// finished is a phase a pod does not leave.
func finished(phase string) bool { return phase == "Succeeded" || phase == "Failed" }

func healthOf(p podItem) podHealth {
	v := viewOf(p)
	phase := p.Status.Phase
	return podHealth{
		ready:    fmt.Sprintf("%d/%d", v.ready, v.total),
		restarts: v.restarts,
		status:   v.status,
		// Two conditions, and the second one was originally missing.
		//
		// Running is not enough on its own: a pod whose containers are not
		// all ready is not serving, and that is the case an overview exists
		// to surface.
		//
		// But "not Running" is not enough either, because Succeeded is not a
		// failure — it is a Job that finished, a CronJob between runs, an init
		// task that did its work. Those pods are *supposed* to be sitting in a
		// terminal phase, and counting them as unhealthy made kube.overview
		// cry wolf on a cluster where nothing was wrong: every completed Job
		// ever run showed up in "not ready" until something reaped it. Failed,
		// Pending and Unknown stay unhealthy, which is the distinction that
		// matters — a pod that stopped because it was done is not a pod that
		// stopped because it broke.
		//
		// Two facts about a pod veto both, because its containers cannot say
		// them. A pod on a node the control plane gave up on carries the
		// reason NodeLost, and its containers' statuses are whatever the
		// kubelet last reported before it went quiet. And a pod still listed
		// after the time it was to be gone is stuck on a finalizer or on a
		// node that never answered. That time is when the API says the pod is
		// expected to be gone, so a pod inside its grace period is not
		// flagged: every rollout would otherwise flap this list for as long
		// as the old pods take to stop.
		healthy: p.Status.Reason != podNodeLost && !stuckDeleting(p) &&
			(phase == "Succeeded" || (phase == "Running" && v.total > 0 && v.ready == v.total)),
	}
}

func stuckDeleting(p podItem) bool {
	t := p.Metadata.DeletionTimestamp
	return t != nil && !finished(p.Status.Phase) && time.Now().After(*t)
}

func fetchPods(ctx context.Context, s selection) ([]podItem, *view.Error) {
	var out list[podItem]
	if verr := getJSON(ctx, s, "pods", &out); verr != nil {
		return nil, verr
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Metadata.Namespace != out.Items[j].Metadata.Namespace {
			return out.Items[i].Metadata.Namespace < out.Items[j].Metadata.Namespace
		}
		return out.Items[i].Metadata.Name < out.Items[j].Metadata.Name
	})
	return out.Items, nil
}

func runPodList(ctx context.Context, req plugin.Request) (view.View, error) {
	l, verr := listingOf(req)
	if verr != nil {
		return nil, verr
	}
	pods, unread, verr := readListing(ctx, l, fetchPods)
	if verr != nil {
		return nil, verr
	}
	checked := len(pods)
	if req.Bool("unhealthy") {
		kept := pods[:0]
		for _, p := range pods {
			if !healthOf(p).healthy {
				kept = append(kept, p)
			}
		}
		pods = kept
	}
	// "Every pod is serving" is the answer to a question, and a bare header is
	// not; but it is only true of pods that exist, so a namespace with none
	// says that instead.
	empty := "no pods in " + l.scopeWords()
	if checked > 0 {
		empty = fmt.Sprintf("every pod in %s is serving (%d checked)", l.scopeWords(), checked)
	}
	return withUnread(withEmpty(podTable(pods, l.wide()), empty), unread, 1), nil
}

func podTable(pods []podItem, withNS bool) view.Table {
	cols := []view.Column{}
	if withNS {
		cols = append(cols, view.Column{Name: "namespace"})
	}
	cols = append(cols,
		view.Column{Name: "pod"},
		view.Column{Name: "ready"},
		view.Column{Name: "status", Kind: view.KindStatus},
		view.Column{Name: "restarts", Kind: view.KindNumber},
		view.Column{Name: "age", Kind: view.KindDuration},
		view.Column{Name: "node"},
	)
	t := view.Table{Columns: cols}
	for _, p := range pods {
		h := healthOf(p)
		var row []view.Cell
		if withNS {
			row = append(row, view.Plain(p.Metadata.Namespace))
		}
		t.Add(append(row,
			view.Plain(p.Metadata.Name), view.Plain(h.ready), podStatusCell(h.status),
			view.Plain(fmt.Sprintf("%d", h.restarts)), ageCell(p.Metadata.CreationTimestamp), view.Plain(p.Spec.NodeName))...)
	}
	t.Total = len(t.Rows)
	return t
}

// podStatusCell is a pod's status valued as the state it names. kubectl puts
// the stage of a pod stuck in an init container in front of that state,
// Init:CrashLoopBackOff, and read by its first word every one of them would be
// valued "init" — the crash, the pull that failed and the ordinary wait alike.
// The ordinary wait, Init:0/1, has no state to name and is valued "init".
func podStatusCell(status string) view.Cell {
	stage, ok := strings.CutPrefix(status, "Init:")
	if !ok {
		return view.Plain(status)
	}
	// Up to a colon of its own: the runtime writes ExitCode:1 or Signal:9 for a
	// container that ended without a reason, the exit code the detail and the
	// state the word before it.
	word, _, _ := strings.Cut(strings.ToLower(stage), ":")
	if word != "" && strings.Trim(word, "abcdefghijklmnopqrstuvwxyz") == "" {
		return view.Status(status, word)
	}
	return view.Status(status, "init")
}

func fetchDeployments(ctx context.Context, s selection) ([]deploymentItem, *view.Error) {
	var out list[deploymentItem]
	if verr := getJSON(ctx, s, "deployments", &out); verr != nil {
		return nil, verr
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Metadata.Namespace != out.Items[j].Metadata.Namespace {
			return out.Items[i].Metadata.Namespace < out.Items[j].Metadata.Namespace
		}
		return out.Items[i].Metadata.Name < out.Items[j].Metadata.Name
	})
	return out.Items, nil
}

// desired reads spec.replicas, which is a pointer because zero and unset are
// different: a deployment scaled to zero on purpose is not the same as one
// whose replica count the API server is filling in.
func desired(d deploymentItem) int {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

func runDeploymentList(ctx context.Context, req plugin.Request) (view.View, error) {
	l, verr := listingOf(req)
	if verr != nil {
		return nil, verr
	}
	deps, unread, verr := readListing(ctx, l, fetchDeployments)
	if verr != nil {
		return nil, verr
	}
	t := withEmpty(deploymentTable(deps, l.wide()), "no deployments in "+l.scopeWords())
	return withUnread(t, unread, 1), nil
}

func deploymentTable(deps []deploymentItem, withNS bool) view.Table {
	cols := []view.Column{}
	if withNS {
		cols = append(cols, view.Column{Name: "namespace"})
	}
	cols = append(cols,
		view.Column{Name: "deployment"},
		view.Column{Name: "ready"},
		view.Column{Name: "up to date", Kind: view.KindNumber},
		view.Column{Name: "available", Kind: view.KindNumber},
		view.Column{Name: "age", Kind: view.KindDuration},
	)
	t := view.Table{Columns: cols}
	for _, d := range deps {
		var row []view.Cell
		if withNS {
			row = append(row, view.Plain(d.Metadata.Namespace))
		}
		t.Add(append(row,
			view.Plain(d.Metadata.Name),
			view.Plain(fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desired(d))),
			view.Plain(fmt.Sprintf("%d", d.Status.UpdatedReplicas)),
			view.Plain(fmt.Sprintf("%d", d.Status.AvailableReplicas)),
			ageCell(d.Metadata.CreationTimestamp))...)
	}
	t.Total = len(t.Rows)
	return t
}

// suggestNamespaces completes a namespace name, or the one element of a list of
// them that is being typed.
//
// This one *does* contact the cluster, unlike suggestContexts, and that is
// the reason it is offered on the CLI's deliberate completion and nowhere
// speculative: the whole rule turns on a completion not being an
// action. A namespace list is a read the caller could have made anyway, and
// it is bounded by the same timeout as every other call here.
//
// A list box hands it the element being typed under `namespace`, as a string
// that is empty while the element is, and this does not read it, as it never read
// the box of a single name: the answer is every namespace the context sees, the
// box matches it against the text typed, and the host leaves out the namespaces
// the box already lists and those a profile's fence does not allow. So nothing
// here tracks what was chosen or what a fence holds, and nothing in the answer
// is anything but a name.
//
// The answer is not capped. The text typed is never sent to the cluster: the host
// narrows what the box offers by it after this has answered, so a cap here would cut
// the names by their place in the list, and no typing could bring a cut one back.
func suggestNamespaces(ctx context.Context, req plugin.Request) []string {
	// Only the context is read, and deliberately not through selectionOf.
	//
	// This lists every namespace, so the namespace and all-namespaces fields
	// are not inputs to it — the previous version validated both and then
	// immediately cleared them. That was harmless until selectionOf started
	// refusing the two together, at which point completion went silent in the
	// one situation it is most wanted: somebody tab-completing a namespace on
	// a line that already carries --all-namespaces. Refusing the *call* there
	// is right; refusing to *suggest* is just withholding the list that shows
	// them what they are choosing between.
	s := selection{Context: strings.TrimSpace(req.String("context")), sf: req.Surface()}
	if verr := checkName("context", s.Context); verr != nil {
		return nil
	}
	var out list[namespaceItem]
	if verr := getJSON(ctx, s, "namespaces", &out); verr != nil {
		return nil
	}
	names := make([]string, 0, len(out.Items))
	for _, n := range out.Items {
		names = append(names, n.Metadata.Name)
	}
	sort.Strings(names)
	return names
}

// plural is one word or two, for sentences the overview builds.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// truncate keeps a joined list readable when a cluster is very unhealthy.
func truncate(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(" and %d more", len(items)-max)
}
