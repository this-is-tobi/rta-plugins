package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// containerRow is one line of `docker ps --format json`.
type containerRow struct {
	ID           string `json:"ID"`
	Names        string `json:"Names"`
	Image        string `json:"Image"`
	State        string `json:"State"`
	Status       string `json:"Status"`
	HealthStatus string `json:"HealthStatus"`
	Ports        string `json:"Ports"`
	RunningFor   string `json:"RunningFor"`
	Size         string `json:"Size"`
}

func fetchContainers(ctx context.Context, c connection, all bool) ([]containerRow, *view.Error) {
	args := []string{"ps", "--format", "json", "--no-trunc"}
	if all {
		args = append(args, "--all")
	}
	raw, verr := run(ctx, c, args...)
	if verr != nil {
		return nil, verr
	}
	rows, verr := jsonLines[containerRow](raw)
	if verr != nil {
		return nil, verr
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Names < rows[j].Names })
	return rows, nil
}

// health folds the healthcheck into one readable cell.
//
// Docker reports "none" for a container with no healthcheck declared, which
// is not the same as healthy and must not read as a problem either.
//
// **The verdict is not always in HealthStatus.** v27.3.1's `ps --format json`
// has no such field and v29 adds it, and where it is missing the verdict is
// only in the Status text the daemon writes — "Up 3 hours (unhealthy)", "Up 5
// seconds (health: starting)" — so a container failing its healthcheck read as
// one with none, in an overview whose summary promises what is unhealthy. v29
// reads the word out of that text when the field is empty, for the API versions
// that carry it nowhere else, and this does the same: the first parenthesis to
// the end of the text, and only when what is inside is one of the three words a
// healthcheck gives. "(Paused)" and "(137)" are in the same place and are not
// one, which is why the word is checked and not merely taken.
func health(r containerRow) string {
	verdict := r.HealthStatus
	if verdict == "" {
		verdict = healthInStatus(r.Status)
	}
	switch strings.ToLower(verdict) {
	case "", "none":
		return ""
	default:
		return verdict
	}
}

func healthInStatus(status string) string {
	_, inner, ok := strings.Cut(status, "(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return ""
	}
	word := strings.TrimPrefix(strings.TrimSuffix(inner, ")"), "health: ")
	switch word {
	case "healthy", "unhealthy", "starting":
		return word
	}
	return ""
}

// kind is how a container stands, as far as an overview has room to say: the
// states a person is told apart by, and not the daemon's own list.
type kind int

const (
	// kindFine is a container that is running and not failing its healthcheck,
	// and the ones with no healthcheck are in it: rta does not know what they
	// should be doing.
	kindFine kind = iota
	// kindExitedCleanly is a one-shot that finished: a migration, a seed, a
	// build step. Finished is not failed, and kube.overview does not count a
	// Succeeded pod either.
	kindExitedCleanly
	kindUnhealthy
	kindRestarting
	kindDead
	// kindCrashed is a container that exited with a code other than 0, which
	// may be a job that failed or a service that went down, and the code says
	// which of the usual causes (137 is a kill, often the memory limit).
	kindCrashed
	// kindStopped is the rest: paused, created and never started, removing, and
	// an exit whose code the status text does not give.
	kindStopped
)

func kindOf(r containerRow) kind {
	switch strings.ToLower(r.State) {
	case "running":
		if strings.EqualFold(health(r), "unhealthy") {
			return kindUnhealthy
		}
		return kindFine
	case "restarting":
		return kindRestarting
	case "dead":
		return kindDead
	case "exited":
		code, ok := exitCode(r)
		switch {
		case !ok:
			return kindStopped
		case code == 0:
			return kindExitedCleanly
		}
		return kindCrashed
	}
	return kindStopped
}

// rank orders what an overview lists: the containers that are broken now, then
// the ones that crashed, then the rest. Finished ones are not listed.
func (k kind) rank() int {
	switch k {
	case kindUnhealthy, kindRestarting, kindDead:
		return 0
	case kindCrashed:
		return 1
	}
	return 2
}

// word is what is said beside the name of a container that is listed.
func (k kind) word(r containerRow) string {
	switch k {
	case kindUnhealthy:
		return "unhealthy"
	case kindRestarting:
		return "restarting"
	case kindDead:
		return "dead"
	case kindCrashed:
		code, _ := exitCode(r)
		return fmt.Sprintf("exit %d", code)
	}
	if r.State == "" {
		return "not running"
	}
	return strings.ToLower(r.State)
}

// exitedStatus is how the daemon writes the status of a container that exited:
// "Exited (137) 3 days ago".
var exitedStatus = regexp.MustCompile(`^Exited \((\d+)\)`)

// exitCode is the code a container exited with, read from the status text the
// daemon writes, which is the only place a `ps` carries it. It is only for a
// container that has exited, and says false for one whose status gives none, so
// that a code is never made up.
func exitCode(r containerRow) (int, bool) {
	if !strings.EqualFold(r.State, "exited") {
		return 0, false
	}
	m := exitedStatus.FindStringSubmatch(r.Status)
	if m == nil {
		return 0, false
	}
	code, err := strconv.Atoi(m[1])
	return code, err == nil
}

func containerTable(rows []containerRow) view.Table {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Names, short(r.ID), r.Image, r.State, health(r), r.Status, r.Ports,
		})
	}
	return view.Table{
		Columns: []view.Column{
			{Name: "name"}, {Name: "id"}, {Name: "image"},
			{Name: "state", Kind: view.KindStatus}, {Name: "health", Kind: view.KindStatus},
			{Name: "status"}, {Name: "ports"},
		},
		Rows: out, Total: len(out),
	}
}

func runContainerList(ctx context.Context, req plugin.Request) (view.View, error) {
	c, verr := connectionOf(req)
	if verr != nil {
		return nil, verr
	}
	rows, verr := fetchContainers(ctx, c, req.Bool("all"))
	if verr != nil {
		return nil, verr
	}
	t := containerTable(rows)
	t.Empty = "no running containers"
	if req.Bool("all") {
		t.Empty = "no containers"
	}
	return t, nil
}

// inspected is the part of `docker inspect` this plugin renders.
type inspected struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status       string `json:"Status"`
			FailingCount int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image  string   `json:"Image"`
		Cmd    []string `json:"Cmd"`
		Env    []string `json:"Env"`
		Labels map[string]string
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		AutoRemove bool `json:"AutoRemove"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// inspectOne is what the daemon says about the container the request names,
// which runInspect and runEnv both read: the two differ in what they hand
// back of it, not in how they ask.
func inspectOne(ctx context.Context, req plugin.Request) (d inspected, name string, c connection, verr *view.Error) {
	c, verr = connectionOf(req)
	if verr != nil {
		return d, "", c, verr
	}
	name = strings.TrimSpace(req.String("container"))
	if verr = checkName("container", name); verr != nil {
		return d, name, c, verr
	}
	raw, verr := run(ctx, c, "inspect", name)
	if verr != nil {
		return d, name, c, verr
	}
	var got []inspected
	if err := json.Unmarshal(raw, &got); err != nil {
		return d, name, c, view.Errorf("docker.unreadable", "docker's answer could not be read: %v", err)
	}
	if len(got) == 0 {
		return d, name, c, c.notFound(name)
	}
	return got[0], name, c, nil
}

func runInspect(ctx context.Context, req plugin.Request) (view.View, error) {
	d, name, c, verr := inspectOne(ctx, req)
	if verr != nil {
		return nil, verr
	}

	pairs := []view.Pair{
		{Key: "name", Value: strings.TrimPrefix(d.Name, "/")},
		{Key: "id", Value: short(d.ID)},
		{Key: "image", Value: d.Config.Image},
		{Key: "state", Value: d.State.Status},
	}
	if !d.State.Running && d.State.FinishedAt != "" {
		pairs = append(pairs, view.Pair{Key: "exit code", Value: fmt.Sprintf("%d", d.State.ExitCode)})
	}
	if h := d.State.Health; h != nil {
		v := h.Status
		if h.FailingCount > 0 {
			v += fmt.Sprintf(" (%d failing in a row)", h.FailingCount)
		}
		pairs = append(pairs, view.Pair{Key: "health", Value: v})
	}
	if p := d.HostConfig.RestartPolicy.Name; p != "" && p != "no" {
		pairs = append(pairs, view.Pair{Key: "restart policy", Value: p})
	}
	if len(d.Config.Cmd) > 0 {
		pairs = append(pairs, view.Pair{Key: "command", Value: strings.Join(d.Config.Cmd, " ")})
	}
	for name, n := range d.NetworkSettings.Networks {
		if n.IPAddress != "" {
			pairs = append(pairs, view.Pair{Key: "network " + name, Value: n.IPAddress})
		}
	}

	// The environment is masked below, and a mask says where the value can be
	// read: the call that returns it, spelled for whoever is looking at this.
	if len(d.Config.Env) > 0 {
		pairs = append(pairs, view.Pair{Key: view.RevealKey, Value: c.sf.Call("docker.container.env",
			append([]plugin.Arg{{Name: "container", Value: name, Positional: true}}, c.callArgs()...)...)})
	}
	sections := []view.Section{
		{ID: "container", Title: "Container", View: view.KeyValue{Pairs: pairs}},
	}
	if len(d.Mounts) > 0 {
		rows := make([][]string, 0, len(d.Mounts))
		for _, m := range d.Mounts {
			mode := "ro"
			if m.RW {
				mode = "rw"
			}
			rows = append(rows, []string{m.Type, m.Source, m.Destination, mode})
		}
		sections = append(sections, view.Section{ID: "mounts", Title: "Mounts", View: view.Table{
			Columns: []view.Column{{Name: "type"}, {Name: "source"}, {Name: "destination"}, {Name: "mode"}},
			Rows:    rows, Total: len(rows),
		}})
	}
	sections = append(sections, view.Section{
		ID: "environment", Title: "Environment", View: envView(d.Config.Env, true)})
	return view.Sections{Items: sections}, nil
}

// runEnv is the environment of one container, values as set: the reveal that
// the inspect page's masked column points at.
func runEnv(ctx context.Context, req plugin.Request) (view.View, error) {
	d, _, _, verr := inspectOne(ctx, req)
	if verr != nil {
		return nil, verr
	}
	return envView(d.Config.Env, false), nil
}

// envView renders the environment, masked or as set.
//
// **Inspect masks every value, and that is not caution for its own sake.** A
// container's environment carries plaintext credentials by convention, and a
// grant to look at one container's state is not a grant to copy its
// credentials into a terminal transcript, a tmux scrollback and, over MCP, a
// model's context. Redacted names the columns every renderer must mask, so
// `-o json` and a person's screen agree about it.
//
// **docker.container.env is the other half**: the same table with nothing
// marked, behind a grant of its own that names the container. Deciding which
// variables are secret by their names is a guess, so the choice is not made
// per variable; it is made once, per container, by the person who issues the
// grant.
func envView(env []string, masked bool) view.View {
	rows := make([][]string, 0, len(env))
	for _, e := range env {
		name, value, found := strings.Cut(e, "=")
		if !found {
			name, value = e, ""
		}
		rows = append(rows, []string{name, value})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	t := view.Table{
		Columns: []view.Column{{Name: "variable"}, {Name: "value"}},
		Rows:    rows,
		Total:   len(rows),
		Empty:   "this container declares no environment variables",
	}
	if masked {
		t.Redacted = []string{"value"}
	}
	return t
}

// mutate is the shared shape of stop, restart and rm: name the container,
// confirm it exists, describe what would happen, then do it.
//
// One function because the three differ only in the verb and in what they
// have to say about consequences — and because a dry-run that is assembled
// separately from the act it previews is a dry-run that drifts from it.
//
// target is the container it finds and acts on: the name the call gave, or
// the id a check made of that name answered with, so that what was checked is
// what is acted on (runStop). args go before it on docker's command line.
func mutate(ctx context.Context, req plugin.Request, verb, target string,
	preview func(containerRow) string, done func(containerRow) []view.Pair, args ...string,
) (view.View, error) {
	c, verr := connectionOf(req)
	if verr != nil {
		return nil, verr
	}
	name := strings.TrimSpace(req.String("container"))
	if verr := checkName("container", name); verr != nil {
		return nil, verr
	}
	if verr := checkName("container", target); verr != nil {
		return nil, verr
	}
	// Every container, stopped ones included: `rm` acts on a stopped
	// container and `stop` needs to know it is already stopped, so the
	// running-only default would make both of them wrong.
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		return nil, verr
	}
	found, ok := findContainer(rows, target)
	if !ok {
		return nil, c.notFound(name)
	}
	if req.DryRun {
		return view.Text{Body: preview(found)}, nil
	}
	if _, verr := run(ctx, c, append(append([]string{verb}, args...), target)...); verr != nil {
		return nil, verr
	}
	return view.KeyValue{Pairs: done(found)}, nil
}

// findContainer matches the way docker does: by name, by full id, or by an
// id prefix.
func findContainer(rows []containerRow, want string) (containerRow, bool) {
	for _, r := range rows {
		// Names is comma-separated when a container has several.
		for _, n := range strings.Split(r.Names, ",") {
			if strings.TrimSpace(n) == want {
				return r, true
			}
		}
		if r.ID == want || (len(want) >= 4 && strings.HasPrefix(r.ID, want)) {
			return r, true
		}
	}
	return containerRow{}, false
}

func runStop(ctx context.Context, req plugin.Request) (view.View, error) {
	name := strings.TrimSpace(req.String("container"))
	// The same connection mutate builds and refuses on first: the receipt
	// below runs only after it has answered, so this one cannot be the invalid
	// one, and is read for the daemon the line that starts it again names.
	c, _ := connectionOf(req)
	// **A container started with --rm is removed as it stops.** The daemon
	// deletes it, its writable layer and its anonymous volumes, and there is
	// nothing for `docker start` to bring back — so the stop the description
	// calls reversible, and the class Write is set by, is not the act this one
	// is. The receipt said "reversible — yes" over a container that no longer
	// existed.
	//
	// A person is told so before and after, and goes ahead: whoever started
	// the container with --rm meant it to go when it stops. An agent is
	// refused. Its grant was for the stop that can be undone; removing a
	// running container is two decisions, the same two rm refuses to make in
	// one call, and the second is not one a grant to stop was asked for.
	//
	// **An agent's stop that could not be judged is refused too.** An inspect
	// that failed — a daemon slower than the command's timeout, an ssh
	// DOCKER_HOST that dropped the connection, an answer that does not parse —
	// read as "not started with --rm", and the stop went on to remove the
	// container the refusal is there to keep, under a receipt saying `docker
	// start` brings it back. Only a name the daemon does not hold goes on, to
	// be refused in mutate's words.
	//
	// **And what is stopped is what was judged.** The stop named the container
	// again, and the daemon resolved that name a second time: a name moved to
	// another container in between was judged on one and stopped the other. The
	// stop is sent to the id the inspect answered with.
	id, removes, judged := stopTarget(ctx, req)
	if req.Surface() == plugin.SurfaceMCP {
		switch {
		case judged != nil && judged.Code != "docker.notfound":
			return nil, view.Refusef("docker.container.unjudged",
				"whether stopping %s removes it could not be told, so it was not stopped: %s", name, judged.Message).
				WithHint("a grant to stop covers a stop that can be undone: try again once docker answers, " +
					"or ask the operator to stop it")
		case removes:
			return nil, view.Refusef("docker.container.autoremove",
				"%s was started to remove itself when it stops (AutoRemove), so stopping it removes it, "+
					"with nothing left to start again", name).
				WithHint("a grant to stop covers a stop that can be undone: ask the operator to stop it, " +
					"which removes it")
		}
	}
	target := name
	if id != "" {
		target = id
	}
	return mutate(ctx, req, "stop", target,
		func(r containerRow) string {
			if !strings.EqualFold(r.State, "running") {
				return fmt.Sprintf("%s is already %s — stopping it would do nothing", r.Names, r.State)
			}
			line := fmt.Sprintf("would stop %s (%s), giving it %ds to exit before the daemon kills it",
				r.Names, r.Image, stopSeconds)
			if removes {
				line += " — it was started to remove itself when it stops (AutoRemove), so the daemon " +
					"removes it, with its writable layer and anonymous volumes, and it cannot be started again"
			}
			return line
		},
		func(r containerRow) []view.Pair {
			reversible := "yes — `" + c.startLine(r.Names) + "` brings it back unchanged"
			if removes {
				reversible = "no — it was started to remove itself when it stops (AutoRemove), and the daemon removed it"
			}
			return []view.Pair{
				{Key: "stopped", Value: r.Names},
				{Key: "image", Value: r.Image},
				{Key: "reversible", Value: reversible},
			}
		},
		fmt.Sprintf("--time=%d", stopSeconds))
}

// stopTarget is the container a stop of the one the request names acts on: the
// id the daemon's inspect answered with, and whether it is running and was
// started with --rm (HostConfig.AutoRemove), so that the daemon removes it the
// moment it stops. judged is the inspect's failure, when it failed:
// docker.notfound for a name the daemon does not hold, and anything else for a
// container whose stop nothing could judge.
func stopTarget(ctx context.Context, req plugin.Request) (id string, removes bool, judged *view.Error) {
	d, _, _, verr := inspectOne(ctx, req)
	if verr != nil {
		return "", false, verr
	}
	return d.ID, d.State.Running && d.HostConfig.AutoRemove, nil
}

func runRestart(ctx context.Context, req plugin.Request) (view.View, error) {
	name := strings.TrimSpace(req.String("container"))
	return mutate(ctx, req, "restart", name,
		func(r containerRow) string {
			return fmt.Sprintf("would restart %s (%s) — it keeps its id, volumes and configuration, "+
				"and loses whatever was only in memory", r.Names, r.Image)
		},
		func(r containerRow) []view.Pair {
			return []view.Pair{
				{Key: "restarted", Value: r.Names},
				{Key: "image", Value: r.Image},
			}
		},
		fmt.Sprintf("--time=%d", stopSeconds))
}

func runRemove(ctx context.Context, req plugin.Request) (view.View, error) {
	name := strings.TrimSpace(req.String("container"))
	c, verr := connectionOf(req)
	if verr != nil {
		return nil, verr
	}
	if verr := checkName("container", name); verr != nil {
		return nil, verr
	}
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		return nil, verr
	}
	found, ok := findContainer(rows, name)
	if !ok {
		return nil, c.notFound(name)
	}
	// Refused before the daemon would refuse it, so the reason is rta's and
	// names the remedy. **No --force here on purpose**: killing a running
	// container and removing it are two decisions, and only one of them was
	// asked for. An operator who means both says so in two calls, and the
	// second one is the one they will remember consenting to.
	if strings.EqualFold(found.State, "running") {
		return nil, view.Errorf("docker.container.running",
			"%s is running, and this removes stopped containers only", found.Names).
			WithHint("stop it first: `" + req.Surface().Call("docker.container.stop",
				append([]plugin.Arg{{Name: "container", Value: found.Names, Positional: true}},
					c.callArgs()...)...) + "`")
	}
	if req.DryRun {
		return view.Text{Body: fmt.Sprintf(
			"would remove %s (%s, %s) — its writable layer goes with it, so anything written "+
				"inside it that was not on a volume is gone for good", found.Names, found.Image, found.Status)}, nil
	}
	if _, verr := run(ctx, c, "rm", name); verr != nil {
		return nil, verr
	}
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "removed", Value: found.Names},
		{Key: "image", Value: found.Image},
		{Key: "gone with it", Value: "the container's writable layer — named volumes are untouched"},
	}}, nil
}

// suggestContainers completes a container name.
//
// Every container including stopped ones, because the mutations that use this
// act on stopped ones too — completing only what is running would hide
// exactly the containers `rm` exists for.
func suggestContainers(ctx context.Context, req plugin.Request) []string {
	c, verr := connectionOf(req)
	if verr != nil {
		return nil
	}
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		name := strings.TrimSpace(strings.Split(r.Names, ",")[0])
		if name == "" {
			continue
		}
		out = append(out, name+"\t"+r.State+", "+r.Image)
	}
	sort.Strings(out)
	return out
}
