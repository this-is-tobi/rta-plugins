package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

func capabilityNamed(t *testing.T, id string) plugin.Capability {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no capability %s", id)
	return plugin.Capability{}
}

func inputOf(c plugin.Capability, name string) plugin.Field {
	for _, f := range c.Inputs {
		if f.Name == name {
			return f
		}
	}
	return plugin.Field{}
}

// The lists of objects declare the namespace as a list the way the overview does,
// with the same short and the same config key so that a fence on the key binds it,
// and as their Scope, as they always did: the record a grant is checked against,
// which is what lets a grant be narrowed to the namespaces it was issued for. The
// host accepts a list-typed Scope (kv.env's keys are one) and takes every element
// of it as a record the call names. Being the scope, it is also the one input the
// host fills from no fence and no profile (plugin.ProfileFillable).
func TestEveryListOfObjectsTakesItsNamespacesAsAListThatIsItsScope(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := capabilityNamed(t, lc.id)
			f := inputOf(c, "namespace")
			if f.Type != plugin.StringSlice || f.Short != "n" || f.Config != "namespace" {
				t.Errorf("namespace is %+v, want a list with the short n bound to the config key namespace", f)
			}
			if !f.Live || f.Suggest == nil {
				t.Errorf("namespace is not completed from the cluster on a press (live: %v), which a list takes since rta v0.48.0", f.Live)
			}
			if c.Scope != "namespace" || !plugin.IsScope(c, f) {
				t.Errorf("scope = %q: a grant on this capability can no longer be narrowed to the namespaces it was issued for", c.Scope)
			}
			if plugin.ProfileFillable(c, f) {
				t.Error("a profile or a fence can fill the scope, which would change the record after the gate has checked a grant against it")
			}
			if all := inputOf(c, "all-namespaces"); all.Type != plugin.Bool || all.Widens != "namespace" {
				t.Errorf("all-namespaces is %+v, want the switch that widens the namespace", all)
			}
			if !strings.Contains(f.Help, fmt.Sprint(maxNamespaces)) || !strings.Contains(f.Help, "own when omitted") {
				t.Errorf("the Help never says the cap or what none reads: %s", f.Help)
			}
			if !strings.Contains(c.Description, namespaceWords) {
				t.Errorf("the Description never says how the namespaces are read:\n%s", c.Description)
			}
			if n := utf8.RuneCountInString(c.Description); n > 1950 {
				t.Errorf("the Description is %d runes, which leaves less than 50 of the 2000 the declaration allows", n)
			}
			if c.Agent != "" && !strings.Contains(c.Agent, agentNamespaceWords) {
				t.Errorf("the text for an agent never says how the namespaces are read:\n%s", c.Agent)
			}
			for _, e := range c.Examples {
				if v, ok := e.Inputs["namespace"]; ok {
					if _, isList := v.([]string); !isList {
						t.Errorf("the example %q gives the namespace as %T, want a list", e.Title, v)
					}
				}
			}
		})
	}
	if err := Plugin().Validate(); err != nil {
		t.Errorf("the declaration is refused: %v", err)
	}
}

// The capabilities that act on one namespace keep one, and are the only ones that
// do: a list added to one of them without being read as a list would be read as
// nothing (Request.String of a list is blank), which is the context's own. The two
// reads that show rows of every namespace take the list too, to keep the rows of
// those named (namespacefilter_test.go).
func TestOnlyTheListsOfObjectsTheOverviewAndTheTwoFilteredReadsTakeAListOfNamespaces(t *testing.T) {
	want := []string{"kube.overview", "kube.namespace.list", "kube.pvc.usage"}
	for _, lc := range listCases {
		want = append(want, lc.id)
	}
	slices.Sort(want)
	var got []string
	for _, c := range Plugin().Capabilities {
		switch f := inputOf(c, "namespace"); f.Type {
		case plugin.StringSlice:
			got = append(got, c.ID)
		case plugin.String:
			if c.ID != "kube.serviceaccount.provision" && c.ID != "kube.serviceaccount.revoke" {
				t.Errorf("%s takes the namespace as one text; only provision and revoke act on one", c.ID)
			}
		}
	}
	slices.Sort(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the capabilities that take a list of namespaces are %v, want %v", got, want)
	}
}

// A profile that fences the namespace holds each element of the list to it, and
// the switch that reads past it.
func TestAFenceHoldsEachNamespaceOfEachList(t *testing.T) {
	fence := plugin.Fence{"namespace": toAny(homelabNamespaces)}
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := capabilityNamed(t, lc.id)
			for name, caller := range map[string]map[string]any{
				"all seven":   {"namespace": homelabNamespaces},
				"three":       {"namespace": []string{"gitea", "rustfs", "crowdsec"}},
				"over MCP":    {"namespace": []any{"gitea", "vault-system"}},
				"a bare name": {"namespace": "gitea"},
			} {
				if breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller}); breach != nil {
					t.Errorf("%s: a list inside the fence was refused: %+v", name, breach)
				}
			}
			for name, caller := range map[string]map[string]any{
				"one outside":        {"namespace": []string{"gitea", "payments"}},
				"only outside":       {"namespace": []string{"payments"}},
				"every namespace":    {"all-namespaces": true},
				"a list and the all": {"namespace": []string{"gitea", "rustfs"}, "all-namespaces": true},
				"a blank element":    {"namespace": []string{"gitea", ""}},
			} {
				breach := plugin.CheckFence(c, plugin.Inputs{Fence: fence, Caller: caller})
				if breach == nil || breach.Input.Name != "namespace" {
					t.Errorf("%s got past a fence on the namespace: %+v", name, breach)
				}
			}
		})
	}
}

// The namespace is the lists' Scope, and a scope is the one input the host fills
// from nothing, so a call that names no namespace under a fence is refused as
// blank, a fence of one included. That is what a grant that can be narrowed to a
// namespace costs, and the Descriptions say it: a tile or an agent on such a
// profile names its namespaces. Without the Scope the host would fill the list
// from the fence, which is the trade kube.overview makes, and the second half
// holds the two apart.
func TestAFenceRefusesAListThatNamesNoNamespaceBecauseTheHostFillsNoScope(t *testing.T) {
	for name, fence := range map[string]plugin.Fence{
		"a fence of seven": {"namespace": toAny(homelabNamespaces)},
		"a fence of one":   {"namespace": {"gitea"}},
	} {
		for _, lc := range listCases {
			c := capabilityNamed(t, lc.id)
			in := plugin.Inputs{Fence: fence, Caller: map[string]any{}}
			breach := plugin.CheckFence(c, in)
			if breach == nil || !breach.Blank || breach.Input.Name != "namespace" {
				t.Errorf("%s, %s: a call that names no namespace got past the fence: %+v", name, lc.id, breach)
			}
			if got := plugin.ResolveRequest(c, in, false, false).StringSlice("namespace"); len(got) != 0 {
				t.Errorf("%s, %s: the host filled the namespace with %v, and a scope is never filled", name, lc.id, got)
			}

			c.Scope = ""
			filled := plugin.ResolveRequest(c, in, false, false).StringSlice("namespace")
			allowed := make([]string, len(fence["namespace"]))
			for i, a := range fence["namespace"] {
				allowed[i] = a.(string)
			}
			if strings.Join(filled, ",") != strings.Join(allowed, ",") {
				t.Errorf("%s, %s: without its Scope the list is %v, want the fence %v: the trade this Scope makes is gone from the host",
					name, lc.id, filled, allowed)
			}
		}
	}
}

// What works under a fence is a call that names its namespaces, the whole of the
// fence included, and it reads each of them and no other.
func TestACallThatNamesTheNamespacesOfAFenceReadsEachOfThemAndNoOther(t *testing.T) {
	fence := plugin.Fence{"namespace": toAny(homelabNamespaces)}
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := capabilityNamed(t, lc.id)
			in := plugin.Inputs{Fence: fence, Caller: map[string]any{"namespace": homelabNamespaces}}
			if breach := plugin.CheckFence(c, in); breach != nil {
				t.Fatalf("a call that names the seven namespaces of the fence was refused: %+v", breach)
			}
			req := plugin.ResolveRequest(c, in, false, false)
			if got := req.StringSlice("namespace"); strings.Join(got, ",") != strings.Join(homelabNamespaces, ",") {
				t.Fatalf("the handler is given %v, want the seven the caller named", got)
			}

			cluster := lc.somewhere(t, homelabNamespaces...)
			if _, err := lc.run(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			got := namespacesRead(t, cluster.listed(t, lc.resource))
			if want := slices.Clone(homelabNamespaces); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s was listed for %v, want each of the seven and not payments", lc.resource, got)
			}
		})
	}
}

// The config is the default namespaces of a call that names none, as it is for any
// input, and a profile's `set:` is not: the namespace is a scope, and a scope is
// the one input neither a profile nor a fence fills. The caller wins over the
// config, and a single name written where a list is declared is the list of that
// one.
func TestTheConfigIsTheDefaultNamespacesOfAListThatNamesNoneAndAProfilesSetIsNot(t *testing.T) {
	for _, lc := range listCases {
		t.Run(lc.id, func(t *testing.T) {
			c := capabilityNamed(t, lc.id)
			for name, tc := range map[string]struct {
				in   plugin.Inputs
				want string
			}{
				"the config, one name":       {plugin.Inputs{Config: map[string]any{"namespace": "gitea"}}, "gitea"},
				"the config, a list":         {plugin.Inputs{Config: map[string]any{"namespace": []any{"gitea", "rustfs"}}}, "gitea,rustfs"},
				"a profile's set":            {plugin.Inputs{ProfileName: "lab", Profile: map[string]any{"namespace": []string{"gitea"}}}, ""},
				"the caller over the config": {plugin.Inputs{Caller: map[string]any{"namespace": []string{"rustfs"}}, Config: map[string]any{"namespace": "gitea"}}, "rustfs"},
				"nothing states a value":     {plugin.Inputs{}, ""},
			} {
				got := strings.Join(plugin.ResolveRequest(c, tc.in, false, false).StringSlice("namespace"), ",")
				if got != tc.want {
					t.Errorf("%s: the handler reads %q, want %q", name, got, tc.want)
				}
			}
		})
	}
}
