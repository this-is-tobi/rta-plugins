package main

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The namespaces of kube.overview: a list, each one read with a call of its own.
//
// A profile that fences the namespace to the several an operator named is the
// connection of a tile that names them, and the overview was the one capability
// that could not be handed them: its namespace was a single name, a list read as
// text that is in no fence entry. It reads each namespace with its own kubectl
// call (-n <ns>) and not the cluster with a filter over it, for two reasons that
// are the same reason. A credential bound by a Role in each namespace is refused
// a list across namespaces, so reading across would show it nothing; and a fence
// is meant to keep the read inside what the connection reaches, which a read of
// everything with the rest thrown away does not. The cost is the number of calls
// (fetchOverview counts them), and these two constants bound it.

// maxNamespaces is the longest list one overview reads. A refresh of thirty
// namespaces is a hundred and twenty-three kubectl calls a minute, which is a lot
// to ask of a cluster and of the machine asking, and a list longer than that is
// a typo or a connection that should be several tiles. A cap that is refused is
// worth more than a tile that quietly takes the cluster's attention.
const maxNamespaces = 30

// readsAtOnce is how many kubectl calls one overview has running (see gate).
const readsAtOnce = 6

// namespacesOf is the namespaces a call names, once each and in order: a blank
// is no name, a name given twice is read once, a name that is not a DNS label is
// refused (checkNamespaceLabel, not the looser checkName that fits a context or a
// resource: each name here is a call, up to thirty at a time, to a cluster that
// has no such namespace), and a list longer than maxNamespaces is refused before
// anything is asked. Sorted, so that what is
// merged from them is sorted by namespace without sorting it again, and the
// same page is the same whichever way the list was written.
func namespacesOf(req plugin.Request) ([]string, *view.Error) {
	var names []string
	for _, name := range req.StringSlice("namespace") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if verr := checkNamespaceLabel(name); verr != nil {
			return nil, verr
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > maxNamespaces {
		sf := req.Surface()
		return nil, view.Errorf("kube.namespaces.toomany",
			"an overview reads at most %d namespaces, and this call reaches more", maxNamespaces).
			WithHint(contextHint(sf,
				"name fewer namespaces, or ask the operator to fence fewer",
				"name fewer with "+sf.InputName("namespace")+", or fence fewer in the profile or the tile that names them"))
	}
	return names, nil
}

// overviewScopes is the selections the overview's lists are made with: one for
// every namespace when none is named, and one for each namespace that is.
func overviewScopes(s selection, names []string) []selection {
	if len(names) == 0 {
		s.Namespace, s.AllNS = "", true
		return []selection{s}
	}
	scopes := make([]selection, len(names))
	for i, name := range names {
		scopes[i] = s
		scopes[i].Namespace, scopes[i].AllNS = name, false
	}
	return scopes
}

// unreadNS is a namespace one of the lists could not be read in, and why.
type unreadNS struct {
	namespace string
	err       *view.Error
}

// readEach runs read once for each scope, all at once (the gate keeps them to
// readsAtOnce kubectl calls), and merges what answered, in the order of the
// scopes. failed is every scope that did not answer, and err is the first of
// their errors when none did: the whole of the kind could not be read, as it
// could not when there was one scope to read.
func readEach[T any](scopes []selection, read func(selection) ([]T, *view.Error)) (items []T, failed []unreadNS, err *view.Error) {
	type result struct {
		items []T
		err   *view.Error
	}
	results := make([]result, len(scopes))
	var wg sync.WaitGroup
	for i, s := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].items, results[i].err = read(s)
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			failed = append(failed, unreadNS{scopes[i].Namespace, r.err})
			continue
		}
		items = append(items, r.items...)
	}
	if len(failed) == len(scopes) {
		err = failed[0].err
	}
	return items, failed, err
}

// whereWords says which namespaces, for a sentence that has the room for a few
// names and not for many: the names when they are short enough together, and
// their number when they are not.
func whereWords(failed []unreadNS) string {
	names := make([]string, len(failed))
	for i, f := range failed {
		names[i] = f.namespace
	}
	const room = 24
	if list := strings.Join(names, ", "); utf8.RuneCountInString(list) <= room {
		return list
	}
	return fmt.Sprintf("%d %s", len(names), plural(len(names), "namespace", "namespaces"))
}
