package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
func health(r containerRow) string {
	switch strings.ToLower(r.HealthStatus) {
	case "", "none":
		return ""
	default:
		return r.HealthStatus
	}
}

// unhealthy is the overview's definition of "worth looking at": a container
// that is not running, or one that is running and failing its own
// healthcheck. A container with no healthcheck cannot be unhealthy by this
// rule, which is right — rta does not know what it should be doing.
func unhealthy(r containerRow) bool {
	if !strings.EqualFold(r.State, "running") {
		return true
	}
	return strings.EqualFold(r.HealthStatus, "unhealthy")
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
	return containerTable(rows), nil
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
	if len(env) == 0 {
		return view.Text{Body: "this container declares no environment variables"}
	}
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
func mutate(ctx context.Context, req plugin.Request, verb string,
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
	// Every container, stopped ones included: `rm` acts on a stopped
	// container and `stop` needs to know it is already stopped, so the
	// running-only default would make both of them wrong.
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		return nil, verr
	}
	found, ok := findContainer(rows, name)
	if !ok {
		return nil, c.notFound(name)
	}
	if req.DryRun {
		return view.Text{Body: preview(found)}, nil
	}
	if _, verr := run(ctx, c, append([]string{verb}, args...)...); verr != nil {
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
	return mutate(ctx, req, "stop",
		func(r containerRow) string {
			if !strings.EqualFold(r.State, "running") {
				return fmt.Sprintf("%s is already %s — stopping it would do nothing", r.Names, r.State)
			}
			return fmt.Sprintf("would stop %s (%s), giving it %ds to exit before the daemon kills it",
				r.Names, r.Image, stopSeconds)
		},
		func(r containerRow) []view.Pair {
			return []view.Pair{
				{Key: "stopped", Value: r.Names},
				{Key: "image", Value: r.Image},
				{Key: "reversible", Value: "yes — `" + c.startLine(r.Names) + "` brings it back unchanged"},
			}
		},
		fmt.Sprintf("--time=%d", stopSeconds), name)
}

func runRestart(ctx context.Context, req plugin.Request) (view.View, error) {
	name := strings.TrimSpace(req.String("container"))
	return mutate(ctx, req, "restart",
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
		fmt.Sprintf("--time=%d", stopSeconds), name)
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
