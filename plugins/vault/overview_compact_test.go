package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// The compact form is what a dashboard tile shows and what an agent reads by
// default: the verdict first, then whether this node serves, the cluster, the
// token, what the Vault holds, and what it is last. A tile is the first eight
// lines of it, and the page that used to be the default opened with the state
// and ended with a token's lifetime in seconds.

const (
	compactRaftRows   = "status,node,raft,token,token policies,mounts,policies,state"
	compactSingleRows = "status,node,token,token policies,mounts,policies,state"
)

// vaultRoutes is a healthy three-node raft Vault as the node asked, the active
// one, answers it. A test changes the routes it needs to; a route set to a
// number is that status with the paragraph a refusal comes as.
func vaultRoutes(t *testing.T) map[string]string {
	t.Helper()
	since := time.Now().Add(-(26*time.Hour + 10*time.Minute)).UTC().Format(time.RFC3339)
	return map[string]string{
		"/v1/sys/seal-status": `{"sealed":false,"initialized":true,"version":"1.14.4","storage_type":"raft"}`,
		"/v1/sys/leader": `{"ha_enabled":true,"is_self":true,"active_time":"` + since +
			`","leader_address":"http://10.0.0.1:8200"}`,
		"/v1/auth/token/lookup-self": `{"data":{"policies":["default","ops"],"ttl":5400}}`,
		"/v1/sys/storage/raft/autopilot/state": `{"data":{"healthy":true,"failure_tolerance":1,"leader":"node1",` +
			`"voters":["node1","node2","node3"],"servers":{"node1":{"name":"node1","healthy":true},` +
			`"node2":{"name":"node2","healthy":true},"node3":{"name":"node3","healthy":true}}}}`,
		"/v1/sys/mounts": `{"data":{"secret/":{"type":"kv"},"transit/":{"type":"transit"},` +
			`"cubbyhole/":{"type":"cubbyhole"},"sys/":{"type":"system"}}}`,
		"/v1/sys/auth":         `{"data":{"token/":{"type":"token"},"userpass/":{"type":"userpass"}}}`,
		"/v1/sys/policies/acl": `{"data":{"keys":["default","ops","root"]}}`,
	}
}

const denied = "denied"

// vaultAnswering serves the routes; a path whose body is the word "denied"
// answers 403 the way a token without the policy is, and an unknown one 404.
func vaultAnswering(t *testing.T, routes map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	return recordingVaultWith(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			body, ok = routes[strings.TrimSuffix(r.URL.Path, "/")]
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		case body == denied:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		default:
			_, _ = w.Write([]byte(body))
		}
	})
}

func compactOf(t *testing.T, routes map[string]string) (view.KeyValue, []string) {
	t.Helper()
	srv, asked := vaultAnswering(t, routes)
	v, err := overviewOf(t, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := v.(view.KeyValue)
	if !ok {
		t.Fatalf("the compact overview is a %s, want the rows alone", view.TypeOf(v))
	}
	return kv, asked()
}

func keysOf(kv view.KeyValue) string {
	keys := make([]string, 0, len(kv.Pairs))
	for _, p := range kv.Pairs {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ",")
}

func rowOf(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

// A tile a third of a screen wide still reads: no row is longer than a line of
// an 80-column terminal, and there are no more rows than a tile holds.
func assertFitsATile(t *testing.T, kv view.KeyValue) {
	t.Helper()
	if len(kv.Pairs) > 8 {
		t.Errorf("%d rows, a tile holds eight: %v", len(kv.Pairs), kv.Pairs)
	}
	for _, p := range kv.Pairs {
		if n := utf8.RuneCountInString(p.Key) + 2 + utf8.RuneCountInString(p.Value); n > 80 || strings.Contains(p.Value, "\n") {
			t.Errorf("row %q is %d columns or wraps: %q", p.Key, n, p.Value)
		}
	}
}

func TestAHealthyVaultIsOkFirstAndWhatItIsLast(t *testing.T) {
	kv, asked := compactOf(t, vaultRoutes(t))
	if got := keysOf(kv); got != compactRaftRows {
		t.Fatalf("rows = %s, want %s", got, compactRaftRows)
	}
	for key, want := range map[string]string{
		"status":         "ok",
		"node":           "active for 1d 2h",
		"raft":           "healthy, tolerates 1 failure",
		"token":          "expires in 1h30m",
		"token policies": "default, ops",
		"mounts":         "4 secret engines, 2 auth methods",
		"policies":       "3 defined",
		"state":          "unsealed · 1.14.4 · raft storage",
	} {
		if got := rowOf(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertFitsATile(t, kv)
	// Seven requests, one of them the unauthenticated node: the seal status, the
	// leader, the token, the raft state, the mounts, the auth methods and the
	// policy list. The page used to be two.
	if len(asked) != 7 {
		t.Errorf("%d requests %v, want 7", len(asked), asked)
	}
}

func TestAVaultWithoutRaftIsNotAskedForIt(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/seal-status"] = `{"sealed":false,"initialized":true,"version":"1.14.4","storage_type":"file"}`
	routes["/v1/sys/leader"] = `{"ha_enabled":false,"is_self":false,"leader_address":""}`
	kv, asked := compactOf(t, routes)
	if got := keysOf(kv); got != compactSingleRows {
		t.Fatalf("rows = %s, want %s", got, compactSingleRows)
	}
	if got := rowOf(kv, "node"); got != "single node, HA is off" {
		t.Errorf("node = %q", got)
	}
	if got := rowOf(kv, "state"); got != "unsealed · 1.14.4 · file storage" {
		t.Errorf("state = %q", got)
	}
	for _, line := range asked {
		if strings.Contains(line, "raft") {
			t.Errorf("asked %q of a Vault whose storage is not raft", line)
		}
	}
}

func TestAStandbyNamesItsLeader(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/leader"] = `{"ha_enabled":true,"is_self":false,"leader_address":"http://10.0.0.2:8200"}`
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "node"), "standby (leader http://10.0.0.2:8200)"; got != want {
		t.Errorf("node = %q, want %q", got, want)
	}
	if got := rowOf(kv, "status"); got != "ok" {
		t.Errorf("status = %q, want a standby asked directly not to be a fault", got)
	}
}

// A cluster of standbys with nobody active takes no request at all.
func TestANodeWithNoActiveNodeToFollowIsAFailure(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/leader"] = `{"ha_enabled":true,"is_self":false,"leader_address":""}`
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "node"), "standby, no active node"; got != want {
		t.Errorf("node = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "fail — no active node"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

func TestAnUnhealthyRaftNamesTheNodeAndWhatItTolerates(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/storage/raft/autopilot/state"] = `{"data":{"healthy":false,"failure_tolerance":0,"leader":"node1",` +
		`"servers":{"node1":{"name":"node1","healthy":true},"node2":{"name":"node2","healthy":true},` +
		`"node3":{"name":"node3","healthy":false}}}}`
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "raft"), "not healthy (node3), tolerates 0 failures"; got != want {
		t.Errorf("raft = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — raft is not healthy: node3"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)
}

func TestARaftTheTokenMayNotReadIsARowAndAWarning(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/storage/raft/autopilot/state"] = denied
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "raft"), "could not be read — permission denied"; got != want {
		t.Errorf("raft = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — raft could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "mounts"); got != "4 secret engines, 2 auth methods" {
		t.Errorf("mounts = %q, want the rows that could be read to stand", got)
	}
}

// An ordinary least-privilege token reads the seal status, the leader and
// itself, and little more. The tile says which rows it could not read, in one
// verdict, and the rows it could read stand.
func TestALeastPrivilegeTokenSaysWhichRowsItCouldNotRead(t *testing.T) {
	routes := vaultRoutes(t)
	for _, path := range []string{"/v1/sys/storage/raft/autopilot/state", "/v1/sys/mounts", "/v1/sys/auth", "/v1/sys/policies/acl"} {
		routes[path] = denied
	}
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "status"), "warn — raft, mounts, policies could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	for key, want := range map[string]string{
		"raft": "could not be read — permission denied", "mounts": "could not be read — permission denied",
		"policies": "could not be read — permission denied", "token": "expires in 1h30m", "node": "active for 1d 2h",
	} {
		if got := rowOf(kv, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertFitsATile(t, kv)
}

func TestOneOfTheMountListsRefusedIsSaidBesideTheOneThatAnswered(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/auth"] = denied
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "mounts"), "4 secret engines; auth methods could not be read — permission denied"; got != want {
		t.Errorf("mounts = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — mounts could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)
}

func TestATokenThatNeverExpiresAndOneWithAUseLimit(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/auth/token/lookup-self"] = `{"data":{"policies":["root"],"ttl":0,"renewable":false}}`
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "token"), "does not expire"; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}

	routes["/v1/auth/token/lookup-self"] = `{"data":{"policies":["default"],"ttl":600,"num_uses":3}}`
	kv, _ = compactOf(t, routes)
	if got, want := rowOf(kv, "token"), "expires in 10m, has 3 uses left; this page spends one"; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	assertFitsATile(t, kv)
}

// Every authenticated read spends a use of a token that has a limit, and the
// overview makes five: a token made with -use-limit=3 would be gone before the
// page was. It is read for itself, which is the one use the page has always
// spent and says so, and for nothing else; the rows that would have cost the
// rest say why they were not read.
func TestATokenWithAUseLimitIsReadForNothingElse(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/auth/token/lookup-self"] = `{"data":{"policies":["default"],"ttl":600,"num_uses":2}}`
	kv, asked := compactOf(t, routes)
	if got, want := rowOf(kv, "token"), "expires in 10m, has 2 uses left; this page spends one"; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	const why = "could not be read — the token has 2 uses left, each read spends one"
	for _, key := range []string{"raft", "mounts", "policies"} {
		if got := rowOf(kv, key); got != why {
			t.Errorf("%s = %q, want %q", key, got, why)
		}
	}
	if got, want := rowOf(kv, "status"), "warn — raft, mounts, policies could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "node"); !strings.HasPrefix(got, "active") {
		t.Errorf("node = %q, want the unauthenticated rows to stand", got)
	}
	if len(asked) != 3 {
		t.Errorf("%d requests %v, want the seal status, the leader and the token", len(asked), asked)
	}
	assertFitsATile(t, kv)

	routes["/v1/auth/token/lookup-self"] = `{"data":{"policies":["default"],"ttl":600,"num_uses":-1}}`
	kv, asked = compactOf(t, routes)
	if got := rowOf(kv, "mounts"); got != "could not be read — the token has no uses left" {
		t.Errorf("mounts = %q", got)
	}
	if len(asked) != 3 {
		t.Errorf("%d requests %v, want three", len(asked), asked)
	}
}

func TestATokenTheVaultRefusesIsARowAndAWarning(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/auth/token/lookup-self"] = denied
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "token"), "could not be read — permission denied"; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "status"), "warn — token could not be read"; got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
	if got := rowOf(kv, "token policies"); got != "" {
		t.Errorf("token policies = %q: the row of a token that was not read says nothing twice", got)
	}
}

func TestAnEmptyVaultHasNothingDefinedButWhatItShipsWith(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/mounts"] = `{"data":{"cubbyhole/":{"type":"cubbyhole"},"sys/":{"type":"system"}}}`
	routes["/v1/sys/auth"] = `{"data":{"token/":{"type":"token"}}}`
	routes["/v1/sys/policies/acl"] = `{"data":{"keys":["default","root"]}}`
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "mounts"), "2 secret engines, 1 auth method"; got != want {
		t.Errorf("mounts = %q, want %q", got, want)
	}
	if got, want := rowOf(kv, "policies"), "2 defined"; got != want {
		t.Errorf("policies = %q, want %q", got, want)
	}
}

func TestTheSealStatusTheTokenMayNotReadIsARowAndTheRestStands(t *testing.T) {
	routes := vaultRoutes(t)
	routes["/v1/sys/seal-status"] = denied
	kv, _ := compactOf(t, routes)
	if got, want := rowOf(kv, "state"), "could not be read — permission denied"; got != want {
		t.Errorf("state = %q, want %q", got, want)
	}
	if got := rowOf(kv, "status"); !strings.HasPrefix(got, "warn — ") || !strings.Contains(got, "state") {
		t.Errorf("status = %q, want a warning that names the state", got)
	}
	if got := rowOf(kv, "token"); got != "expires in 1h30m" {
		t.Errorf("token = %q, want the token that was read to stand", got)
	}
	if got := rowOf(kv, "raft"); got != "" {
		t.Errorf("raft = %q: whether the storage is raft is in the seal status, which was not read", got)
	}
}

// The detail is the compact rows and the names of the policies, which the count
// stands for: the request that counts them is the one that names them.
func TestTheDetailIsTheRowsAndThePolicyNames(t *testing.T) {
	srv, asked := vaultAnswering(t, vaultRoutes(t))
	v, err := overviewOf(t, srv, true)
	if err != nil {
		t.Fatal(err)
	}
	page, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("detail is a %s, want a page", view.TypeOf(v))
	}
	var titles []string
	for _, it := range page.Items {
		titles = append(titles, it.Title)
	}
	if got, want := strings.Join(titles, ","), "summary,policies"; got != want {
		t.Errorf("sections = %s, want %s", got, want)
	}
	if got := keysOf(page.Items[0].View.(view.KeyValue)); got != compactRaftRows {
		t.Errorf("summary rows = %s, want the compact rows %s", got, compactRaftRows)
	}
	sameSet(t, "policy names", firstColumn(t, page.Items[1].View), []string{"default", "ops", "root"})
	if len(asked()) != 7 {
		t.Errorf("%d requests, want the same 7 as the compact form: the detail asks nothing more", len(asked()))
	}
}

func TestTheOverviewTileCannotBeRefreshedFasterThanAMinute(t *testing.T) {
	var found bool
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.overview" {
			continue
		}
		found = true
		if got := c.Pace(); got != time.Minute {
			t.Errorf("vault.overview is re-run every %s, want a minute", got)
		}
		if pace, cut := plugin.ClampPace(c, 5*time.Second); pace != time.Minute || !cut {
			t.Errorf("a configured 5s pace came to %s (cut %v), want the minute it cannot go below", pace, cut)
		}
	}
	if !found {
		t.Fatal("no vault.overview")
	}
}

func TestTheOverviewDeclaresADetailedPageAndSaysSoToAgents(t *testing.T) {
	for _, c := range Plugin().Capabilities {
		if c.ID != "vault.overview" {
			continue
		}
		if !c.Detailed {
			t.Error("vault.overview is not Detailed")
		}
		for name, text := range map[string]string{"description": c.Description, "agent": c.AgentText()} {
			if !strings.Contains(text, "`detail`") || !strings.Contains(text, "status") {
				t.Errorf("the %s text does not say what the default answer is and what `detail` adds: %q", name, text)
			}
		}
		return
	}
	t.Fatal("no vault.overview")
}

func TestUptimeIsSpelledOneWay(t *testing.T) {
	for d, want := range map[time.Duration]string{
		26*time.Hour + 10*time.Minute:   "1d 2h",
		14*24*time.Hour + 2*time.Hour:   "14d 2h",
		5*time.Hour + 30*time.Minute:    "5h 30m",
		14*time.Minute + 20*time.Second: "14m",
		42 * time.Second:                "42s",
		23*time.Hour + 59*time.Minute:   "23h 59m",
		24 * time.Hour:                  "1d 0h",
	} {
		if got := uptime(d); got != want {
			t.Errorf("uptime(%s) = %q, want %q", d, got, want)
		}
	}
}
