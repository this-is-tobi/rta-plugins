package main

import (
	"context"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The namespaces of the lists of objects, read the way kube.overview reads its own.
//
// A namespace is a selector: an input that filters a set the connection holds. A
// selector is a list, so that a connection fenced to several namespaces is driven
// by a call that names some of them, or none, and a call that names one is the
// read it always was. Each namespace of the list is read with a call of its own
// (overviewlist.go says why that, and not the cluster read whole and filtered),
// at most readsAtOnce of them running together, and the answers are merged into
// the one table the cluster-wide read already draws, namespace column and all.
//
// **What naming none reads is the context's own namespace**, which is what kubectl
// does and what every one of these lists did: the read an agent makes by default
// is not the heavier one that spans the cluster, and `all-namespaces` stays the
// switch that widens.
//
// **A fence does not fill a blank list, because these capabilities declare the
// namespace as their Scope.** An input a capability names as its scope is the
// record a grant is checked against, and the host never fills one: the gate
// checks the records a call names before anything is resolved, so a fill after it
// would change the record under the grant. Under a profile that fences the
// namespace a call therefore names its namespaces, each of which the host holds to
// the fence, and one that names none is refused as blank; a profile's `set:
// namespace:` is no default for the same reason. What the Scope buys is a grant
// narrowed to the namespaces it was issued for: internal/grant takes every element
// of a list-typed scope as a record the call names, and a call is covered only
// when each of them is, so a grant for shop-web covers a call that names shop-web
// and refuses one that names shop-web beside shop-billing. kube.overview declares
// no Scope, because what it takes is the width of its answer and not a record, and
// is filled from the fence with every namespace it allows; the lists keep theirs.

// listing is what a list of objects reads: the call's selection and the
// namespaces it names, once each and sorted (namespacesOf).
type listing struct {
	// s carries a namespace only when the call named exactly one, as it always
	// did, and the switch for every namespace. Its Namespace is what a call that
	// names none leaves to kubectl: the context's own.
	s     selection
	names []string
}

// listingOf reads the namespaces a call names and the selection they are read
// under. A namespace beside every namespace is refused as selectionFor refuses it.
func listingOf(req plugin.Request) (listing, *view.Error) {
	names, verr := namespacesOf(req)
	if verr != nil {
		return listing{}, verr
	}
	s, verr := selectionFor(req, names)
	if verr != nil {
		return listing{}, verr
	}
	return listing{s: s, names: names}, nil
}

// scopes is the selections the listing's calls are made with: one for each of
// several namespaces named, and the selection itself otherwise, which is the one
// namespace named, the context's own or every namespace.
func (l listing) scopes() []selection {
	if len(l.names) < 2 {
		return []selection{l.s}
	}
	out := make([]selection, len(l.names))
	for i, name := range l.names {
		out[i] = l.s
		out[i].Namespace = name
	}
	return out
}

// wide is whether the page covers more than one namespace, which is when its
// rows have to say which: the one namespace is the question that was asked.
func (l listing) wide() bool { return l.s.AllNS || len(l.names) > 1 }

// scopeWords is where the listing looked, for the sentence that says it found
// nothing there.
func (l listing) scopeWords() string {
	if len(l.names) < 2 {
		return l.s.scopeWords()
	}
	return namesWords(l.names, "namespaces ")
}

// readListing runs read once for each scope of l and merges what answered, in the
// order of the namespaces. unread is every namespace that did not, and err is the
// first reason when none did: the page is then an error, as the read of one
// namespace was when it failed.
//
// The calls of a listing of several are held to readsAtOnce together and a cluster
// that does not answer is asked that many times and not once for each namespace
// (gate). A listing of one has no gate and is the call it was.
//
// A namespace the API server answered 401 in is not a row but the error of the
// call, as it is for the overview (rejected says why): the credential is the
// connection's and not the namespace's, and a page cannot be the refusal a
// dashboard stops asking after.
func readListing[T any](ctx context.Context, l listing, read func(context.Context, selection) ([]T, *view.Error)) (items []T, unread []unreadNS, err *view.Error) {
	scopes := l.scopes()
	if len(scopes) > 1 {
		ctx = withGate(ctx, readsAtOnce)
	}
	items, unread, err = readEach(scopes, func(s selection) ([]T, *view.Error) { return read(ctx, s) })
	for _, u := range unread {
		if refusal := rejected(u.err); refusal != nil {
			return nil, nil, refusal
		}
	}
	return items, unread, err
}

// unreadLead is how a row says the namespace it stands for could not be read.
const unreadLead = "could not be read — "

// withUnread marks a table partial: a row after the rows that were read for each
// namespace that could not be, naming it and the reason in the column at (the one
// that names the row's object, where a row of the others has its name), and a
// warning, because a shorter table is indistinguishable from the whole of a
// smaller thing. Total is the rows sent, those of the namespaces that could not
// be read among them, since view.Table.Total is never below the rows it holds
// (kube.metrics.pressure counts its unread nodes the same way).
//
// The warning names the namespaces and never how many were asked about: the list
// may be a fence the host filled in, and its size is an inventory.
func withUnread(t view.Table, unread []unreadNS, at int) view.Table {
	if len(unread) == 0 {
		return t
	}
	names := make([]string, len(unread))
	for i, u := range unread {
		row := make([]string, len(t.Columns))
		row[0], row[at] = u.namespace, unreadLead+oneLine(u.err.Message)
		t.Rows = append(t.Rows, row)
		names[i] = u.namespace
	}
	t.Empty = ""
	t.Total = len(t.Rows)
	t.Warnings = append(t.Warnings, view.Error{
		Code:    "kube.namespace.unreadable",
		Message: format.CountOf(len(unread), "namespace") + " could not be read: " + truncate(names, 4),
		Hint:    "the other namespaces are complete on this page; the row of each one that is not says why",
	})
	return t
}

// namespaceWords is what every list of objects says of its namespace input in its
// Description: the list, and the two things that are not kubectl's, what naming
// none reads and what a fence makes of it. Short on purpose: an agent is shown a
// capability's Description whenever it has no text of its own, and is held to 800
// bytes of it with the summary.
const namespaceWords = "`namespace` is a list, each one read with a call of its own (at most 30) and merged " +
	"into one table; naming none reads the context's own namespace. Under a profile that fences " +
	"`namespace` a call must name its namespaces, and `all-namespaces` is refused."

// agentNamespaceWords is the same for the reader of a tool listing, who has
// arguments and no flags.
const agentNamespaceWords = "`namespace` is an array of at most 30, each read with a call of its own; none " +
	"reads the context's own namespace. Under a profile that fences `namespace` a call must " +
	"name its namespaces, and `all-namespaces` is refused."
