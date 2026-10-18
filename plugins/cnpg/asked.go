package main

import (
	"regexp"
	"slices"
	"strings"

	"github.com/this-is-tobi/rta/pkg/view"
)

// What a kubectl call that failed was asking for, so the failure can say so.
//
// **A refusal that always names the Cluster resource is wrong the moment the
// plugin asks for anything else, and this plugin asks for six things.** The
// classification used to word every "forbidden" as not being allowed to read
// clusters.postgresql.cnpg.io with the verb get, whichever resource or verb the
// cluster had actually refused — so an identity allowed to read clusters and
// not volumes, or not backups, or not to create one, was told it lacked the
// right it had, and sent to ask for it. And every missing resource type was
// "this cluster has no CloudNativePG operator", which is a claim about a
// process when all kubectl said was that a type is not registered.
//
// The cluster's own words come first, because they are exact: the API server
// states the verb, the resource, the group and the namespace it refused. The
// argv is the fallback for an answer that carries less, such as a proxy's
// bare "forbidden".

// asked is the request a failed call made, as far as the answer and the argv
// say it. Every field is empty when nothing said.
type asked struct {
	// verb is the right that was needed: get, list or create.
	verb string
	// resource is the type, as kubectl names it with its group.
	resource string
	// namespace is the namespace an answer named.
	namespace string
	// everywhere is set when the answer named the cluster scope, which is what
	// a refusal of a read of every namespace says.
	everywhere bool
	// name is the object a not-found named.
	name string
}

var (
	// forbiddenRe reads the API server's refusal in full:
	//
	//	persistentvolumeclaims is forbidden: User "u" cannot list resource
	//	"persistentvolumeclaims" in API group "" in the namespace "prod"
	//
	// and the same sentence naming the cluster scope in place of a namespace.
	forbiddenRe = regexp.MustCompile(`\S+ is forbidden: User "[^"]*" cannot (\w+) resource "([^"]+)"` +
		`(?: in API group "([^"]*)")?(?: in the namespace "([^"]*)"| (at the cluster scope))?`)
	// bareForbiddenRe is what is left of it when a proxy or an older server
	// words the refusal shorter: the resource and nothing about the right.
	bareForbiddenRe = regexp.MustCompile(`(\S+) is forbidden`)
	// noTypeRe is the answer to a resource type nothing registered. kubectl
	// quotes it without its group, so the argv's spelling is preferred.
	noTypeRe = regexp.MustCompile(`the server doesn't have a resource type "([^"]+)"`)
	// notFoundRe is a named object that is not there.
	notFoundRe = regexp.MustCompile(`(\S+) "([^"]*)" not found`)
)

// askedOf reads what a failed call asked for from its answer and, for what the
// answer leaves out, from its argv.
func askedOf(stderr string, args []string) asked {
	a := askedFromArgs(args)
	if m := forbiddenRe.FindStringSubmatch(stderr); m != nil {
		a.verb, a.resource = m[1], m[2]
		if m[3] != "" {
			a.resource += "." + m[3]
		}
		a.namespace, a.everywhere = m[4], m[5] != ""
		return a
	}
	if m := bareForbiddenRe.FindStringSubmatch(stderr); m != nil && a.resource == "" {
		a.resource = m[1]
	}
	if m := noTypeRe.FindStringSubmatch(stderr); m != nil && !strings.HasPrefix(a.resource, m[1]) {
		a.resource = m[1]
	}
	if m := notFoundRe.FindStringSubmatch(stderr); m != nil {
		a.resource, a.name = m[1], m[2]
	}
	return a
}

// askedFromArgs is what the argv alone says: a get with a name is a get and
// one without is a list, and a create is a create. The resource of a create is
// not in its argv, the document going in through standard input, so it is left
// to the answer.
func askedFromArgs(args []string) asked {
	if len(args) == 0 {
		return asked{}
	}
	switch args[0] {
	case "get":
		a := asked{verb: "list"}
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			a.resource = args[1]
		}
		if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
			a.verb = "get"
		}
		return a
	case "create":
		return asked{verb: "create"}
	}
	return asked{}
}

// what is the resource worded for a sentence, naming a stand-in when no
// source said.
func (a asked) what() string {
	if a.resource == "" {
		return "the resource kubectl asked for"
	}
	return a.resource
}

// denied is the cluster refusing a request, in the words of the right it
// lacked.
//
// **A refused read of every namespace says what that read needs.** Listing
// across the cluster is a right of its own, a ClusterRole bound with a
// ClusterRoleBinding, and the Role a ServiceAccount minted for one namespace
// carries does not hold it however much it can read inside that namespace. The
// refusal that arrived said only that the cluster scope was refused, which
// reads as a verdict on the resource, and sent a person with every right in
// their own namespace to ask for rights they had.
func denied(by caller, a asked, args []string) *view.Error {
	wide := slices.Contains(args, "--all-namespaces")
	verb := a.verb
	if verb == "" {
		verb = "read"
	}
	msg := "not allowed to " + verb + " " + a.what()
	switch {
	case a.namespace != "":
		msg += " in namespace " + a.namespace
	case wide:
		msg += " in every namespace"
	case a.everywhere:
		msg += " at the cluster scope"
	}
	hint := "this is the cluster refusing, not rta — you are authenticated, and "
	if a.verb == "" || a.resource == "" {
		hint += "the right it lacks is one to read " + a.what()
	} else {
		words := []string{"auth", "can-i", a.verb, a.resource}
		switch {
		case a.namespace != "":
			words = append(words, "--namespace="+a.namespace)
		case wide:
			words = append(words, "--all-namespaces")
		}
		hint += "the right it lacks is `" + a.verb + "` on `" + a.resource + "`; `" +
			by.kubectlLine(words...) + "` asks the cluster the same thing"
	}
	if wide {
		hint += ". Reading every namespace needs the right to list cluster-wide, a ClusterRole bound with a " +
			"ClusterRoleBinding; a namespaced Role, which is all a ServiceAccount minted for one namespace " +
			"carries, does not give it. Name one namespace with " + by.sf.InputName("namespace") +
			" instead of using " + by.sf.InputName("all-namespaces")
	}
	return view.Errorf("cnpg.forbidden", "%s", msg).WithHint(hint)
}

// crdMissing is a resource type nothing registered: the CRD is not there.
//
// **What is known is that a CRD is absent, and what that says about the
// operator is a guess worth stating as one.** The operator is what installs
// CloudNativePG's CRDs, so for the Cluster type an absent CRD most likely means
// it never ran here. It is not proof, and the reverse is not true at all: a CRD
// that is registered while the operator is gone reads normally, so nothing
// here can tell that case, and the page should not pretend to by naming a
// process it cannot see.
func crdMissing(by caller, a asked) *view.Error {
	verr := view.Errorf("cnpg.crd.missing", "this cluster has no %s resource type", a.what())
	if a.resource == clusterCRD {
		return verr.WithHint("CloudNativePG installs its own CRDs, so this most likely means its operator " +
			"has never been installed on this cluster, or that its CRDs were removed. `" +
			by.kubectlLine("get", "crd") + " | grep cnpg` lists the CRDs it does have; a cluster whose " +
			"CRDs are registered and whose operator is gone reads normally, and would not have failed this way")
	}
	return verr.WithHint("the CRD `" + a.what() + "` is not registered — `" +
		by.kubectlLine("get", "crd", a.what()) + "` confirms it")
}

// notServed is a 404 for a type kubectl did find: the cluster lists it and
// will not answer for it, which is a CRD being installed or removed, or an API
// behind an aggregator that is down — not an operator that is absent.
func notServed(by caller, a asked) *view.Error {
	return view.Errorf("cnpg.resource.unserved", "the API server answered 404 for %s", a.what()).
		WithHint("the type is registered and the server is not serving it right now — a CRD being installed " +
			"or removed, or an API behind an aggregator that is down; `" + by.kubectlLine("get", "crd") +
			"` and `" + by.kubectlLine("api-resources") + "` say which")
}

// objectMissing is a named object that is not there. A cluster that is
// missing is the one case with a way on, the listing of every cluster; any
// other object is named as what it is.
func objectMissing(by caller, stderr string, a asked) *view.Error {
	if a.resource == "" || a.resource == clusterCRD {
		return view.Errorf("cnpg.cluster.missing", "%s", firstLine(stderr)).
			WithHint(by.listEverywhere() + " shows what is there")
	}
	return view.Errorf("cnpg.resource.missing", "%s", firstLine(stderr)).
		WithHint("`" + by.kubectlLine("get", a.resource, "--all-namespaces") + "` shows where one by that name is, if any")
}
