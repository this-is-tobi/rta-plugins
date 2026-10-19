package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The namespaces and the clusters a listing names, and how it reads them.
//
// A profile fenced to several namespaces, or to several clusters, is the
// connection of a tile that names those, and the listings were the reads that
// could not be handed them: `namespace` and `cluster` were one String each, so a
// list read as text that is in no fence entry, and a call that names none under
// a multi-value fence is refused as blank. They are lists now, on the three
// reads that mean "every cluster" when none is named (cnpg.list,
// cnpg.backup.list, cnpg.storage). The capabilities that act on one cluster
// keep the single values they were, and so does every grant Scope.
//
// **Each namespace is read with a call of its own (`-n <ns>`), and never the
// whole cluster with the namespaces filtered out of it.** A credential bound by
// a Role in each namespace is refused a list across namespaces, so reading
// across would show it nothing; and a fence is meant to keep the read inside
// what the connection reaches, which a read of everything with the rest thrown
// away does not. The price is the number of calls, and the two limits below
// bound it: a refresh is one call per namespace named, or one call when none
// is, whatever the clusters named, which only narrow what each call returns.

const (
	// maxNamespaces and maxClusters are the longest lists one listing takes. A
	// list longer than that is a typo or a connection that should be several
	// tiles, and a tile refreshes on a timer: a refused list is worth more than
	// one that quietly takes thirty calls of the cluster's attention a minute.
	maxNamespaces = 30
	maxClusters   = 30
	// readsAtOnce is how many kubectl calls one listing has running.
	readsAtOnce = 6
	// maxNamed is how many namespaces a sentence names: more than that are
	// counted (selection.where).
	maxNamed = 4
)

// namesOf is the names a call gives for a list input, once each and in order: an
// empty string is no name, a name given twice is read once, and a list longer
// than limit is refused before anything is asked. Sorted, so that what is merged
// from them is in the order of the names and the same page is the same whichever
// way the list was written.
//
// **A name is used exactly as it was given.** The host held each element to the
// profile's fence as the string it received, so a name trimmed here would be one
// nothing compared; padding makes a name that is in no fence entry, and checkName
// refuses it as the invalid name it is.
//
// **The refusal for too many says what the cap is and that this call reaches
// more, and never how many it reaches, nor that anyone named them.** The list
// may be the profile's fence, filled in by the host for a call that named none,
// and the host keeps a fence's contents from an agent: an allowlist is an
// inventory, and a count of it, or a claim that the caller chose it, is a
// refusal that maps it.
func namesOf(req plugin.Request, input, noun string, limit int) ([]string, *view.Error) {
	var names []string
	for _, name := range req.StringSlice(input) {
		if name == "" {
			continue
		}
		if verr := checkName(noun, name); verr != nil {
			return nil, verr
		}
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if len(names) > limit {
		sf := req.Surface()
		hint := "name fewer " + noun + "s with " + sf.InputName(input) + ", or fence fewer in the profile or the tile that names them"
		if sf == plugin.SurfaceMCP {
			hint = "name fewer " + noun + "s, or ask the operator to fence fewer"
		}
		return nil, view.Errorf("cnpg."+noun+"s.toomany",
			"a listing reads at most %d %ss, and this call reaches more", limit, noun).WithHint(hint)
	}
	return names, nil
}

// fetch reads resource of s into a T and returns it with the error of the read.
//
// **The error is assigned before the value is returned.** `return v, read(&v)`
// leaves it to the compiler whether v is copied before or after the call that
// fills it through the pointer (the language specification leaves the order of a
// variable's read and a function call in one return unspecified), and the
// failure of the wrong order is a listing that comes back empty, with no error,
// as a health page that says nothing is wrong.
func fetch[T any](ctx context.Context, s selection, resource, name string, extra ...string) (T, *view.Error) {
	var out T
	verr := getResource(ctx, s, resource, name, &out, extra...)
	return out, verr
}

// listingOf is selectionOf for a read that takes lists: the selection, and the
// clusters that narrow it.
//
// One namespace is held in selection.namespace exactly as the single value was,
// so everything that words or draws a read of one namespace does the same for a
// list of one. Two or more are held in selection.namespaces, and none is the
// context's own, or every namespace when the switch says so.
func listingOf(req plugin.Request) (selection, []string, *view.Error) {
	s, verr := connectionOf(req)
	if verr != nil {
		return selection{}, nil, verr
	}
	namespaces, verr := namesOf(req, "namespace", "namespace", maxNamespaces)
	if verr != nil {
		return selection{}, nil, verr
	}
	clusters, verr := namesOf(req, "cluster", "cluster", maxClusters)
	if verr != nil {
		return selection{}, nil, verr
	}
	s.allNamespace = req.Bool("all-namespaces")
	switch len(namespaces) {
	case 0:
	case 1:
		s.namespace = namespaces[0]
	default:
		s.namespaces = namespaces
	}
	if verr := s.refuseBoth(); verr != nil {
		return selection{}, nil, verr
	}
	return s, clusters, nil
}

// wide reports whether the page covers more than one namespace, which is when
// its rows have to say which: the one namespace is the question that was asked.
func (s selection) wide() bool { return s.allNamespace || len(s.namespaces) > 1 }

// scopes is the selections a listing's calls are made with: one for each
// namespace of a list, and the selection itself otherwise.
func (s selection) scopes() []selection {
	if len(s.namespaces) == 0 {
		return []selection{s}
	}
	out := make([]selection, len(s.namespaces))
	for i, namespace := range s.namespaces {
		out[i] = s
		out[i].namespace, out[i].namespaces = namespace, nil
	}
	return out
}

// without is s reading the namespaces it named less those that could not be
// read, for a sentence that says where a page looked and must not claim a
// namespace it could not.
func (s selection) without(unread []string) selection {
	if len(s.namespaces) == 0 {
		return s
	}
	var left []string
	for _, namespace := range s.namespaces {
		if !slices.Contains(unread, namespace) {
			left = append(left, namespace)
		}
	}
	if len(left) == 1 {
		s.namespace, left = left[0], nil
	}
	s.namespaces = left
	return s
}

// listingArgs is the arguments of a cnpg.list call for clusters in the namespaces
// s read, through whatever reached them: the namespaces are a list whatever
// their number, which is how that capability takes them over MCP.
func (s selection) listingArgs(clusters []string) []plugin.Arg {
	args := []plugin.Arg{{Name: "cluster", Value: clusters}}
	switch {
	case len(s.namespaces) > 0:
		args = append(args, plugin.Arg{Name: "namespace", Value: s.namespaces})
	case s.namespace != "":
		args = append(args, plugin.Arg{Name: "namespace", Value: []string{s.namespace}})
	}
	return append(args, s.reach...)
}

// listed is names for a sentence: all of them while that is short, and the first
// three and a count when it is not.
func listed(names []string) string {
	if len(names) <= maxNamed {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + " and " + strconv.Itoa(len(names)-3) + " more"
}

// outcome is what the read of one scope came to.
type outcome[T any] struct {
	scope selection
	got   T
	err   *view.Error
}

// reason is why a scope is not on the page, in the words of its row.
func (o outcome[T]) reason() string { return "could not be read — " + firstLine(o.err.Message) }

// clusterWide reports whether a failure is about the connection and not about
// the namespace that met it: the same call in another namespace fails the same
// way, so asking again is only waiting for the same answer.
func clusterWide(verr *view.Error) bool {
	switch verr.Code {
	case "cnpg.unreachable", "cnpg.unauthenticated", "cnpg.context.none", "cnpg.kubectl.missing",
		"cnpg.crd.missing", "cnpg.resource.unserved":
		return true
	}
	return false
}

// readEach runs read for each scope, readsAtOnce of them at a time, and returns
// what each came to in the order of the scopes.
//
// **A connection that fails ends the queue.** A cluster that stops answering
// makes each call wait out its timeout, and thirty of them at six a time is
// minutes of a tile that cannot say why; so once a call fails for a reason that
// is the connection's (clusterWide), the calls still waiting are answered with
// that failure and never made, and a dead cluster is asked six times and not
// thirty.
func readEach[T any](ctx context.Context, scopes []selection, read func(context.Context, selection) (T, *view.Error)) []outcome[T] {
	out := make([]outcome[T], len(scopes))
	if len(scopes) == 1 {
		got, verr := read(ctx, scopes[0])
		out[0] = outcome[T]{scope: scopes[0], got: got, err: verr}
		return out
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		stopped *view.Error
		slots   = make(chan struct{}, readsAtOnce)
	)
	for i, scope := range scopes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			mu.Lock()
			halt := stopped
			mu.Unlock()
			if halt != nil {
				out[i] = outcome[T]{scope: scope, err: halt}
				return
			}
			got, verr := read(ctx, scope)
			if verr != nil && clusterWide(verr) {
				mu.Lock()
				if stopped == nil {
					stopped = verr
				}
				mu.Unlock()
			}
			out[i] = outcome[T]{scope: scope, got: got, err: verr}
		}()
	}
	wg.Wait()
	return out
}

// reading is what the scopes of a listing came to, settled into a page.
type reading[T any] struct {
	outcomes []outcome[T]
	// unread is the outcomes that did not answer, in the order of the scopes.
	unread []outcome[T]
	// read is the selection narrowed to the namespaces that did.
	read selection
}

// settle decides what the outcomes of s's scopes are: a page, with the
// namespaces that could not be read named on it, or the one error.
//
// **The page for one namespace never becomes an error because a sibling
// failed.** A namespace refused, or one that cannot be found, is a row that
// says so and the others answer. The page is an error only where the read of
// one scope is the whole page (a listing of one namespace fails as it always
// did), and where nothing answered because the connection did not: seven
// identical rows would say less than the one failure they share.
func settle[T any](s selection, outs []outcome[T]) (reading[T], *view.Error) {
	r := reading[T]{outcomes: outs, read: s}
	for _, o := range outs {
		if o.err != nil {
			r.unread = append(r.unread, o)
		}
	}
	if len(r.unread) == 0 {
		return r, nil
	}
	if len(outs) == 1 {
		return r, outs[0].err
	}
	if len(r.unread) == len(outs) {
		for _, o := range r.unread {
			if clusterWide(o.err) {
				return r, o.err
			}
		}
	}
	names := make([]string, len(r.unread))
	for i, o := range r.unread {
		names[i] = o.scope.namespace
	}
	r.read = s.without(names)
	return r, nil
}

// answered reports whether anything was read at all.
func (r reading[T]) answered() bool { return len(r.unread) < len(r.outcomes) }

// warnings is the sentence of a page that is partial: how many namespaces are
// not on it, and which. Not advisory (view.Error.Advisory): a namespace missing
// from the page is a part of the answer that is missing.
func (r reading[T]) warnings() []view.Error {
	if len(r.unread) == 0 {
		return nil
	}
	names := make([]string, len(r.unread))
	for i, o := range r.unread {
		names[i] = o.scope.namespace
	}
	return []view.Error{{
		Code: "cnpg.namespace.unreadable",
		Message: fmt.Sprintf("%d of %s could not be read: %s",
			len(r.unread), format.CountOf(len(r.outcomes), "namespace"), listed(names)),
		Hint: "the other namespaces are complete on this page; each one that is not has a row saying why",
	}}
}
