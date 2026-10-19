package main

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Completion for the three things somebody types here: a context, a
// namespace, a cluster name. The Live split follows the Field contract to the
// letter — a Suggest that reads a cluster answers a deliberate completion
// press only, one that reads a local file may answer the keystroke channel —
// and plugins/kube pins the same split with a test, as does main_test.go
// here.

// suggestContexts completes a context name. `kubectl config get-contexts` is
// a read of the local kubeconfig — no cluster is contacted, which is what
// lets this stay off the Live channel.
func suggestContexts(ctx context.Context, _ plugin.Request) []string {
	raw, verr := run(ctx, caller{sf: plugin.SurfaceCompletion}, "config", "get-contexts", "-o=name")
	if verr != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out
}

// suggestNamespaces completes a namespace name, or the one element of a list of
// them that is being typed, and contacts the cluster to do it — hence Live at
// every field that carries it.
//
// It reads the context and nothing of the namespace box: on a list the request
// carries the element being typed under `namespace`, and the answer is every
// namespace the context sees, for the box to match against what is typed. The
// host leaves out the namespaces the box already lists and those a profile's
// fence does not allow.
//
// The answer is not capped. The text typed is never sent to the cluster: the host
// narrows what the box offers by it after this has answered, so a cap here would cut
// the names by their place in the list, and no typing could bring a cut one back.
func suggestNamespaces(ctx context.Context, req plugin.Request) []string {
	kctx := strings.TrimSpace(req.String("context"))
	if checkName("context", kctx) != nil {
		return nil
	}
	args := []string{"get", "namespaces", "-o=name", "--request-timeout=" + requestTimeout}
	if kctx != "" {
		args = append(args, "--context="+kctx)
	}
	raw, verr := run(ctx, caller{sf: plugin.SurfaceCompletion}, args...)
	if verr != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		// `-o name` prints "namespace/foo"; the value to submit is "foo".
		if name := strings.TrimPrefix(strings.TrimSpace(line), "namespace/"); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// suggestClusters completes a cluster name, or the one element of a list of them
// that is being typed — Live, one GET for each namespace it looks in.
//
// With no namespace chosen it deliberately lists every namespace, because a
// CloudNativePG cluster conventionally lives in its own: completing against
// the kubeconfig's default namespace would answer nothing on exactly the
// setups this plugin is for. Each entry carries its namespace as the
// description, which is also the flag the caller still has to pass — the
// suggestion fills one field, not two.
//
// **The namespace beside it is read as the list it may be.** On cnpg.list,
// cnpg.backup.list and cnpg.storage the box beside the clusters is a list, and
// Request.String of a list is blank: reading it that way swept every namespace
// of the cluster for a person who had named two, which a credential bound by a
// Role in each is refused, and offered clusters of namespaces the call would not
// read. Each namespace named is looked in with a call of its own, as the
// listing does (readEach); the form has already filled in the namespaces a
// profile's fence holds, so a fenced connection never sweeps. The element being
// typed is under `cluster` and is not read: the box matches the names against it,
// and the host leaves out the clusters it already lists and those the fence does
// not allow.
//
// A name is offered once, whatever number of namespaces hold a cluster of it: a
// list of clusters keeps the ones named in each namespace it reads, and the form
// that asks for this strips the description that would tell two of them apart.
//
// The answer is not capped, for the reason suggestNamespaces gives: the clusters are
// all asked for, the text typed narrows them in the box, and a cap here would cut
// them by their place in the list, with nothing typed able to bring a cut one back.
func suggestClusters(ctx context.Context, req plugin.Request) []string {
	s, ok := completionSelection(req)
	if !ok {
		return nil
	}
	var out []string
	for _, o := range readEach(ctx, s.scopes(), func(ctx context.Context, scope selection) (clusterList, *view.Error) {
		return fetch[clusterList](ctx, scope, clusterCRD, "")
	}) {
		if o.err != nil {
			continue
		}
		for _, c := range o.got.Items {
			entry := c.Metadata.Name
			if s.wide() {
				entry += "\t--namespace=" + c.Metadata.Namespace
			}
			out = append(out, entry)
		}
	}
	sort.Strings(out)
	return slices.CompactFunc(out, func(a, b string) bool { return plugin.CandidateValue(a) == plugin.CandidateValue(b) })
}

// completionSelection is where a completion of the clusters looks: the context
// and the namespaces the box beside it names, none meaning every namespace. It
// is not listingOf, which refuses what a completion should answer through (a
// switch beside the namespaces) and a name with padding that a box does not
// keep; false for a name kubectl would take for a flag, and for more namespaces
// than a listing reads.
func completionSelection(req plugin.Request) (selection, bool) {
	s := selection{context: strings.TrimSpace(req.String("context"))}
	if checkName("context", s.context) != nil {
		return s, false
	}
	var names []string
	for _, name := range req.StringSlice("namespace") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if checkName("namespace", name) != nil {
			return s, false
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	switch {
	case len(names) > maxNamespaces:
		return s, false
	case len(names) == 0:
		s.allNamespace = true
	case len(names) == 1:
		s.namespace = names[0]
	default:
		s.namespaces = names
	}
	return s, true
}
