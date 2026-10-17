package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A pod's STATUS, READY and RESTARTS are kubectl's own, because an SRE reads
// this table next to `kubectl get pods` and the two disagreeing about the
// same pod is a reason to stop trusting this one. They differed exactly in
// the states a person debugs: an Evicted pod read Failed, a pod stuck in an
// init container read PodInitializing with 0 restarts while the init
// container had restarted seven times, a pod being deleted read Running, and
// a pod nothing had scheduled yet read 0/0.
//
// Every body below is the API's own shape, decoded through podFrom like the
// pods the plugin reads from kubectl, and the expected words are the ones
// `kubectl get pods` prints for the same object.
func TestAPodIsDescribedTheWayKubectlDescribesIt(t *testing.T) {
	const (
		past   = "2020-01-01T00:00:00Z"
		future = "2999-01-01T00:00:00Z"
	)
	cases := []struct {
		name         string
		body         string
		wantReady    string
		wantStatus   string
		wantRestarts int
		wantHealthy  bool
	}{
		{
			name:       "evicted",
			body:       `{"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Failed","reason":"Evicted"}}`,
			wantReady:  "0/1",
			wantStatus: "Evicted",
		},
		{
			name: "being deleted inside its grace period is still serving",
			body: `{"metadata":{"deletionTimestamp":"` + future + `"},"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:   "1/1",
			wantStatus:  "Terminating",
			wantHealthy: true,
		},
		{
			name: "still listed after its deletion time is stuck",
			body: `{"metadata":{"deletionTimestamp":"` + past + `"},"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/1",
			wantStatus: "Terminating",
		},
		{
			name: "a pod on a lost node being deleted is Unknown",
			body: `{"metadata":{"deletionTimestamp":"` + future + `"},"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Running","reason":"NodeLost",
				"containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/1",
			wantStatus: "Unknown",
		},
		{
			name: "a pod on a lost node is not serving whatever its containers last said",
			body: `{"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Running","reason":"NodeLost",
				"containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/1",
			wantStatus: "NodeLost",
		},
		{
			name: "a finished pod being deleted is still finished",
			body: `{"metadata":{"deletionTimestamp":"` + past + `"},"spec":{"containers":[{"name":"job"}]},
				"status":{"phase":"Succeeded","containerStatuses":[{"name":"job","ready":false,
				"state":{"terminated":{"exitCode":0,"reason":"Completed"}}}]}}`,
			wantReady:   "0/1",
			wantStatus:  "Completed",
			wantHealthy: true,
		},
		{
			name: "an init container in a back-off counts its restarts",
			body: `{"spec":{"initContainers":[{"name":"migrate"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending",
				"initContainerStatuses":[{"name":"migrate","restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}],
				"containerStatuses":[{"name":"app","restartCount":0,"state":{"waiting":{"reason":"PodInitializing"}}}]}}`,
			wantReady:    "0/1",
			wantStatus:   "Init:CrashLoopBackOff",
			wantRestarts: 7,
		},
		{
			name: "an init sequence says how far it got",
			body: `{"spec":{"initContainers":[{"name":"a"},{"name":"b"},{"name":"c"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","initContainerStatuses":[
				{"name":"a","state":{"terminated":{"exitCode":0,"reason":"Completed"}}},
				{"name":"b","state":{"running":{}}},
				{"name":"c","state":{"waiting":{"reason":"PodInitializing"}}}],
				"containerStatuses":[{"name":"app","state":{"waiting":{"reason":"PodInitializing"}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "Init:1/3",
		},
		{
			name: "an init container that failed with a reason",
			body: `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","initContainerStatuses":[
				{"name":"a","state":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "Init:Error",
		},
		{
			name: "an init container that exited without a reason",
			body: `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","initContainerStatuses":[
				{"name":"a","state":{"terminated":{"exitCode":3}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "Init:ExitCode:3",
		},
		{
			name: "an init container killed by a signal",
			body: `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","initContainerStatuses":[
				{"name":"a","state":{"terminated":{"exitCode":137,"signal":9}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "Init:Signal:9",
		},
		{
			name: "a sidecar that is up counts as ready and does not hold initialization",
			body: `{"spec":{"initContainers":[{"name":"proxy","restartPolicy":"Always"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Running","conditions":[{"type":"Initialized","status":"True"}],
				"initContainerStatuses":[{"name":"proxy","ready":true,"started":true,"restartCount":3,"state":{"running":{}}}],
				"containerStatuses":[{"name":"app","ready":true,"restartCount":5,"state":{"running":{}}}]}}`,
			wantReady:    "2/2",
			wantStatus:   "Running",
			wantRestarts: 8,
			wantHealthy:  true,
		},
		{
			name: "a sidecar that is not ready keeps the pod out of the ready count",
			body: `{"spec":{"initContainers":[{"name":"proxy","restartPolicy":"Always"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Running","conditions":[{"type":"Initialized","status":"True"}],
				"initContainerStatuses":[{"name":"proxy","ready":false,"started":true,"state":{"running":{}}}],
				"containerStatuses":[{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/2",
			wantStatus: "Running",
		},
		{
			name: "restarts of a finished init container are history once the pod is initialized",
			body: `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Running","conditions":[{"type":"Initialized","status":"True"}],
				"initContainerStatuses":[{"name":"a","restartCount":2,"state":{"terminated":{"exitCode":0,"reason":"Completed"}}}],
				"containerStatuses":[{"name":"app","ready":true,"restartCount":5,"state":{"running":{}}}]}}`,
			wantReady:    "1/1",
			wantStatus:   "Running",
			wantRestarts: 5,
			wantHealthy:  true,
		},
		{
			name: "restarts of the app container are not counted while an init container still fails",
			body: `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
				"status":{"phase":"Pending",
				"initContainerStatuses":[{"name":"a","restartCount":2,"state":{"terminated":{"exitCode":1,"reason":"Error"}}}],
				"containerStatuses":[{"name":"app","restartCount":5,"state":{"waiting":{"reason":"PodInitializing"}}}]}}`,
			wantReady:    "0/1",
			wantStatus:   "Init:Error",
			wantRestarts: 2,
		},
		{
			name: "a container that exited without a reason",
			body: `{"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Failed","containerStatuses":[{"name":"app","state":{"terminated":{"exitCode":3}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "ExitCode:3",
		},
		{
			name: "a container killed by a signal",
			body: `{"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Failed","containerStatuses":[{"name":"app","state":{"terminated":{"exitCode":137,"signal":9}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "Signal:9",
		},
		{
			name: "a finished container beside a running one is Running when the pod is ready",
			body: `{"spec":{"containers":[{"name":"setup"},{"name":"app"}]},
				"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[
				{"name":"setup","state":{"terminated":{"exitCode":0,"reason":"Completed"}}},
				{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/2",
			wantStatus: "Running",
		},
		{
			name: "a finished container beside a running one is NotReady when the pod is not",
			body: `{"spec":{"containers":[{"name":"setup"},{"name":"app"}]},
				"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],"containerStatuses":[
				{"name":"setup","state":{"terminated":{"exitCode":0,"reason":"Completed"}}},
				{"name":"app","ready":true,"state":{"running":{}}}]}}`,
			wantReady:  "1/2",
			wantStatus: "NotReady",
		},
		{
			name: "a finished container beside one that failed shows the failure",
			body: `{"spec":{"containers":[{"name":"setup"},{"name":"app"}]},
				"status":{"phase":"Failed","containerStatuses":[
				{"name":"setup","state":{"terminated":{"exitCode":0,"reason":"Completed"}}},
				{"name":"app","state":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}`,
			wantReady:  "0/2",
			wantStatus: "Error",
		},
		{
			name: "a pod held back by a scheduling gate",
			body: `{"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"SchedulingGated"}]}}`,
			wantReady:  "0/1",
			wantStatus: "SchedulingGated",
		},
		{
			name:       "a pod nothing has scheduled yet is ready over the containers it asked for",
			body:       `{"spec":{"containers":[{"name":"app"},{"name":"proxy"}]},"status":{"phase":"Pending"}}`,
			wantReady:  "0/2",
			wantStatus: "Pending",
		},
		{
			name: "a waiting reason is the diagnosis",
			body: `{"spec":{"containers":[{"name":"app"}]},
				"status":{"phase":"Pending","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}}`,
			wantReady:  "0/1",
			wantStatus: "ImagePullBackOff",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := healthOf(podFrom(t, c.body))
			if h.ready != c.wantReady {
				t.Errorf("ready = %q, want %q", h.ready, c.wantReady)
			}
			if h.status != c.wantStatus {
				t.Errorf("status = %q, want %q", h.status, c.wantStatus)
			}
			if h.restarts != c.wantRestarts {
				t.Errorf("restarts = %d, want %d", h.restarts, c.wantRestarts)
			}
			if h.healthy != c.wantHealthy {
				t.Errorf("healthy = %v, want %v", h.healthy, c.wantHealthy)
			}
		})
	}
}

// A reason string is whatever the cluster says: status.reason is writable by
// anything that can update pods/status, and a container's waiting and
// terminated reasons come from a runtime. A cell must not be a place where a
// stranger can write an escape sequence or a second row, so each is shown the
// way a name is (plugin.ListedName): as itself when it reads as itself, and
// quoted with the character written out when it does not.
func TestAReasonTheClusterSuppliedIsShownNotObeyed(t *testing.T) {
	cases := map[string]string{
		"status.reason": `{"spec":{"containers":[{"name":"app"}]},
			"status":{"phase":"Failed","reason":"Evicted\u001b[2J"}}`,
		"waiting reason": `{"spec":{"containers":[{"name":"app"}]},
			"status":{"phase":"Pending","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"Boom\nSecond row"}}}]}}`,
		"terminated reason": `{"spec":{"containers":[{"name":"app"}]},
			"status":{"phase":"Failed","containerStatuses":[{"name":"app","state":{"terminated":{"exitCode":1,"reason":"Err\u001b]0;title\u0007"}}}]}}`,
		"init reason": `{"spec":{"initContainers":[{"name":"a"}],"containers":[{"name":"app"}]},
			"status":{"phase":"Pending","initContainerStatuses":[{"name":"a","state":{"waiting":{"reason":"Back\roff"}}}]}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			got := healthOf(podFrom(t, body)).status
			for _, r := range got {
				if r < ' ' || r == 0x7f {
					t.Errorf("status %q carries the control character %q", got, r)
				}
			}
			if !strings.HasPrefix(got, `"`) && !strings.HasPrefix(got, `Init:"`) {
				t.Errorf("status %q reads as itself although it holds a control character", got)
			}
		})
	}
}

// withKubectlAnswering is a kubectl that serves one file for `get pods` and an
// empty list for every other read, which is all the overview asks of the
// other resources it looks at.
func withKubectlAnswering(t *testing.T, podsFile string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "kubectl")
	body := "#!/bin/sh\ncase \"$*\" in\n" +
		"*\"get pods\"*) cat '" + podsFile + "' ;;\n" +
		"*\"get \"*) printf '{\"items\":[]}' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := kubectlBin
	kubectlBin = script
	t.Cleanup(func() { kubectlBin = orig })
}

func podRows(t *testing.T, v view.View) map[string][]string {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("got %T, want a table", v)
	}
	rows := map[string][]string{}
	for _, r := range tbl.Rows {
		rows[r[0]] = r
	}
	return rows
}

// The whole capability, over pods in the shapes the API gives the states an
// SRE debugs: what a person sees is the row, so the row is what is checked.
func TestThePodListSaysWhatKubectlSaysOfEachState(t *testing.T) {
	withKubectlAnswering(t, filepath.Join("testdata", "pods-edge.json"))

	v, err := runPodList(context.Background(), plugin.NewRequest(map[string]any{"namespace": "demo"}, false, false))
	if err != nil {
		t.Fatal(err)
	}
	rows := podRows(t, v)
	// pod, ready, status, restarts
	want := map[string][4]string{
		"evicted-0":             {"evicted-0", "0/1", "Evicted", "0"},
		"terminating-overdue-0": {"terminating-overdue-0", "1/1", "Terminating", "0"},
		"terminating-grace-0":   {"terminating-grace-0", "1/1", "Terminating", "0"},
		"init-crash-0":          {"init-crash-0", "0/1", "Init:CrashLoopBackOff", "7"},
		"init-wait-0":           {"init-wait-0", "0/1", "Init:1/3", "0"},
		"unscheduled-0":         {"unscheduled-0", "0/2", "Pending", "0"},
		"serving-0":             {"serving-0", "2/2", "Running", "12"},
	}
	if len(rows) != len(want) {
		t.Errorf("%d rows, want %d: %v", len(rows), len(want), rows)
	}
	for name, w := range want {
		r, ok := rows[name]
		if !ok {
			t.Errorf("no row for %s", name)
			continue
		}
		if got := [4]string{r[0], r[1], r[2], r[3]}; got != w {
			t.Errorf("%s = %v, want %v", name, got, w)
		}
	}
}

// --unhealthy is the judgement kube.overview makes: a pod stuck terminating
// past its deletion time is not serving, and a pod inside its grace period is
// not flagged, so every rollout does not flap the list.
func TestUnhealthyKeepsAPodStuckTerminatingAndNotOneInItsGracePeriod(t *testing.T) {
	withKubectlAnswering(t, filepath.Join("testdata", "pods-edge.json"))

	v, err := runPodList(context.Background(), plugin.NewRequest(
		map[string]any{"namespace": "demo", "unhealthy": true}, false, false))
	if err != nil {
		t.Fatal(err)
	}
	rows := podRows(t, v)
	for _, name := range []string{"evicted-0", "terminating-overdue-0", "init-crash-0", "init-wait-0", "unscheduled-0"} {
		if _, ok := rows[name]; !ok {
			t.Errorf("%s is not serving and was left out of --unhealthy", name)
		}
	}
	for _, name := range []string{"terminating-grace-0", "serving-0"} {
		if _, ok := rows[name]; ok {
			t.Errorf("%s is serving and was listed by --unhealthy", name)
		}
	}
}

// The overview shares the judgement and the table, so the pods an SRE most
// needs named in it are named in kubectl's words.
func TestTheOverviewNamesAPodStuckTerminatingAndOneStuckInitializing(t *testing.T) {
	withKubectlAnswering(t, filepath.Join("testdata", "pods-edge.json"))

	v, err := runOverview(context.Background(), plugin.NewRequest(map[string]any{"detail": true}, false, false))
	if err != nil {
		t.Fatal(err)
	}
	secs, ok := v.(view.Sections)
	if !ok {
		t.Fatalf("got %T, want sections", v)
	}
	var notReady view.Table
	var found bool
	for _, s := range secs.Items {
		if s.ID == "pods" {
			notReady, found = s.View.(view.Table)
		}
	}
	if !found {
		t.Fatalf("no 'Pods that are not ready' section in %v", secs.Items)
	}
	status := map[string]string{}
	for _, r := range notReady.Rows {
		status[r[1]] = r[3]
	}
	for pod, want := range map[string]string{
		"terminating-overdue-0": "Terminating",
		"init-crash-0":          "Init:CrashLoopBackOff",
		"init-wait-0":           "Init:1/3",
		"evicted-0":             "Evicted",
	} {
		if status[pod] != want {
			t.Errorf("%s = %q under 'not ready', want %q (all: %s)", pod, status[pod], want, fmt.Sprint(status))
		}
	}
	if _, ok := status["terminating-grace-0"]; ok {
		t.Error("a pod inside its grace period was listed as not ready")
	}
}
