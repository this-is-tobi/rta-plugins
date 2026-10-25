// Command rta-plugin-docker is the janitorial fast path onto a Docker
// daemon: what is running, what images are taking up the disk, one composed
// overview — and the small, bounded set of mutations a developer runs by hand
// every day.
//
// Build it and put it on your $PATH as `rta-plugin-docker`:
//
//	cd plugins/docker && go build -o ~/.local/bin/rta-plugin-docker .
//
// It needs the `docker` CLI and nothing else. Whatever daemon docker can
// already reach — Docker Desktop's socket, a rootless one, a remote context
// over SSH — this can reach, with no address to configure. See dockercli.go
// for why driving the CLI is a decision rather than a shortcut.
//
// # Why this one is not read-only, when kube is
//
// A deliberate divergence, decided rather than defaulted into.
// `container.stop`, `restart` and `rm` are bounded, single-purpose, and the
// literal loop a developer runs by hand — unlike `docker run`/`build`/
// `compose`, whose unbounded flag surface the native CLI already owns well.
// That is the same line `git` holds when it declines to grow a `git.commit`,
// drawn in a different place because the operations are a different shape.
//
// Three capabilities are **deliberately absent**: `image.prune`,
// `volume.prune` and `network.prune`. The Engine API has no server-side
// dry-run for prune, so a correct preview means reproducing dockerd's own
// filter-matching client-side, and that reproduction drifts across Engine
// versions. A dry-run that can be wrong is worse than none, because its whole
// job is to be believed. They ship when a cross-Engine conformance test
// proves the reproduction does not drift — a real gate, not a formality.
//
// Every capability here reaches off this process, so none appears on the
// automatic dashboard on its own; add one explicitly once you have decided
// polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: docker.overview
package main

import (
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk"
)

func main() { sdk.Serve(Plugin()) }

// connFields is the connection half of every capability's inputs.
//
// Both are Local, and for the reason plugins/kube states at length for its
// own `context`: **choosing which daemon a call reaches is choosing a
// destination, and a remote caller may never choose one** (Field.Local,
// a destination is a destination). `--host=tcp://somewhere-else:2375` from an agent would
// aim a stop or an rm at a machine the operator never named. Config fills
// them, a person at a terminal passes them, MCP cannot.
func connFields() []plugin.Field {
	return []plugin.Field{
		{Name: "host", Type: plugin.String, Config: "host", Local: true,
			Help: "daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted"},
		{Name: "context", Type: plugin.String, Config: "context", Local: true,
			Help: "docker context to use — the current one when omitted"},
	}
}

// cap appends the connection inputs and marks the capability off-dashboard,
// the way plugins/pg, plugins/vault and plugins/kube each do for their own.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(append(own, c.Inputs...), connFields()...)
	c.NoPreview = true
	return c
}

// nameInput is the container a mutation acts on: required, positional, and
// the grant's Scope, so a grant reads `docker.container.rm my-scratch-box`
// and authorizes exactly that container.
func nameInput(help string) plugin.Field {
	return plugin.Field{
		Name: "container", Type: plugin.String, Positional: true, Required: true,
		Help: help, Suggest: suggestContainers,
	}
}

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut — an index entry carries this verbatim, and a
// version is a fact about a release, not about the source it came from.
var version = "dev"

func Plugin() plugin.Plugin {
	return plugin.Plugin{
		Name:    "docker",
		Summary: "Containers and images: what is running, what is stale, and the daily tidy-up",
		Version: version,
		Capabilities: []plugin.Capability{
			cap(plugin.Capability{
				ID:       "docker.container.list",
				Summary:  "Containers, with state, health, ports and age",
				Keywords: []string{"ps", "running", "stopped", "compose"},
				Examples: []plugin.Example{
					{Title: "every container, stopped ones too", Inputs: map[string]any{"all": true}},
				},
				Description: "Running containers by default; `all` includes the stopped ones, which " +
					"is usually what somebody wants before a tidy-up. Health is shown separately " +
					"from state because a container can be up and failing its own healthcheck.",
				Safety:     plugin.Read,
				Idempotent: true,
				Run:        runContainerList,
			}, plugin.Field{Name: "all", Short: "a", Type: plugin.Bool, Config: "all",
				Help: "include stopped containers"}),
			cap(plugin.Capability{
				ID:       "docker.image.list",
				Summary:  "Images, with size and age",
				Keywords: []string{"dangling", "disk", "layers", "prune"},
				Description: "What is on this machine's disk. Dangling images — the untagged " +
					"leftovers of a rebuild — are marked, since they are usually the answer to " +
					"\"where did my disk go\".",
				Safety:     plugin.Read,
				Idempotent: true,
				Run:        runImageList,
			}),
			cap(plugin.Capability{
				ID:       "docker.overview",
				Summary:  "One daemon at a glance: its verdict, what is unhealthy, where the disk went",
				Keywords: []string{"summary", "status", "dashboard", "df"},
				Description: "The first row, `status`, is the verdict: ok, or warn or fail with the worst " +
					"facts in a few words. Fail is a container failing its healthcheck, restarting or " +
					"dead; warn is one that exited with a code other than 0, one that is paused or was " +
					"never started, and a disk the daemon would not report. Then how many containers " +
					"are running of those that exist, with the one-shots that exited cleanly (code 0) " +
					"counted and not named; the problems, the unhealthy and the restarting first, then " +
					"those that exited non-zero with the exit code read from their status text, then the " +
					"rest, six at most; and where the disk went, from one `docker system df`: images, " +
					"volumes and build cache, the largest first, and how much of them is reclaimable. " +
					"Adding up the size of every volume takes the daemon seconds on a host with many, " +
					"so a tile of it is re-run every 2 minutes at most, a floor a configuration can " +
					"lengthen and not shorten. A daemon that does not answer is the verdict, with why " +
					"and what to check. With `detail`: the images (a count and the leftovers of a " +
					"rebuild, from an `image ls` the compact answer does not run), every container not " +
					"running or not healthy, the one-shots included, the disk usage by kind, and the " +
					"largest images.",
				Agent: "`status` is the verdict (ok, or warn or fail with the worst facts), then the containers " +
					"(running of all, the one-shots that exited 0 counted), the problems (unhealthy and " +
					"restarting first, then exited non-zero with the exit code, then paused or never " +
					"started; six at most) and the disk (images, volumes and build cache by size, and what " +
					"is reclaimable, from docker system df, re-run every 2 minutes at most). A daemon " +
					"that does not answer is the verdict, with why. `detail` adds the unwell containers, " +
					"the disk usage by kind and the largest images.",
				Safety:     plugin.Read,
				Idempotent: true,
				Detailed:   true,
				Refresh:    overviewRefresh,
				Run:        runOverview,
			}),
			cap(plugin.Capability{
				ID:       "docker.container.inspect",
				Summary:  "Everything the daemon knows about one container",
				Keywords: []string{"details", "config", "mounts", "networks", "labels", "debug"},
				Examples: []plugin.Example{
					{Title: "one container in full", Inputs: map[string]any{"container": "web"}},
				},
				Description: "Image, command, state, restart policy, mounts, networks and " +
					"environment. **Write rather than Read, and it needs a grant**, because the " +
					"command a container was started with carries credentials passed as arguments " +
					"often enough that no filter on it can be trusted, and rta would rather ask " +
					"than guess wrong once.\n\n" +
					"**Environment values come back masked (••••••), on every surface.** The names " +
					"are shown and every value is marked as secret, so what the result tells you is " +
					"which variables a container sets, not what they are set to. " +
					"docker.container.env returns them, under a grant of its own that names the " +
					"container.",
				// Write for the command line and the mounts, which this
				// shows as they are. The environment is the other half of
				// what a container discloses, and it has its own capability
				// now: deciding which variables are secret by their names is
				// a guess, so every value is masked here and the one consent
				// that unmasks them is the grant on docker.container.env.
				Safety:     plugin.Write,
				Idempotent: true,
				NeedsGrant: true,
				Scope:      "container",
				Inputs:     []plugin.Field{nameInput("the container to inspect")},
				Run:        runInspect,
			}),
			cap(plugin.Capability{
				ID:       "docker.container.env",
				Summary:  "The environment variables one container was started with",
				Keywords: []string{"variables", "secrets", "credentials", "dotenv"},
				Examples: []plugin.Example{
					{Title: "the variables one container was started with", Inputs: map[string]any{"container": "web"}},
				},
				Description: "Every variable the container sets, with its value as set — the image's " +
					"own, every `-e` and every compose-file value.\n\n" +
					"**The values come back as set, on every surface** — none of it masked, and an " +
					"agent holding a grant for the container has them in its context from then on. " +
					"docker.container.inspect shows the names and masks the values, and costs none " +
					"of this.\n\n" +
					"**Classified write for what it discloses, not what it changes.** A container's " +
					"environment carries plaintext credentials by convention, and deciding which " +
					"variables are secret by their names is a guess, not a rule, so the consent is " +
					"given once, for the container.",
				Agent: "The variables one container sets, each with its value. `container` is the exact name " +
					"or id. Needs a grant naming the container.",
				// The call whose answer is the values the inspect page masks.
				// A reveal is its own capability so that every control that
				// binds to a capability ID — the grant, the ceiling, the
				// lock, the record — applies to it without a new one.
				Safety:     plugin.Write,
				Idempotent: true,
				NeedsGrant: true,
				Scope:      "container",
				Reveals:    true,
				Inputs:     []plugin.Field{nameInput("the container whose environment to read")},
				Run:        runEnv,
			}),
			cap(plugin.Capability{
				ID:      "docker.container.stop",
				Summary: "Stop a running container",
				Examples: []plugin.Example{
					{Title: "stop one container", Inputs: map[string]any{"container": "web"}},
				},
				Keywords: []string{"halt", "shutdown", "sigterm", "down"},
				Description: "Sends SIGTERM and gives the container time to exit before the daemon " +
					"kills it. Reversible — `docker start` brings it back with the same id, disk " +
					"and configuration — which is why this is Write and not Destructive. Except for a " +
					"container started to remove itself when it stops (AutoRemove), which the daemon removes: " +
					"the preview and the receipt say so, and an agent is refused it, as it is any stop of a " +
					"container docker could not first be asked about.",
				Safety:     plugin.Write,
				Idempotent: true,
				NeedsGrant: true,
				Scope:      "container",
				Inputs:     []plugin.Field{nameInput("the container to stop")},
				Run:        runStop,
			}),
			cap(plugin.Capability{
				ID:      "docker.container.restart",
				Summary: "Restart a container",
				Examples: []plugin.Example{
					{Title: "restart one container", Inputs: map[string]any{"container": "web"}},
				},
				Keywords: []string{"reboot", "bounce", "cycle", "reload"},
				Description: "Stop then start, keeping the container's id, volumes and " +
					"configuration. What it does lose is whatever was only in the process's memory " +
					"and whatever was written outside a volume.",
				// Not Idempotent, which stop is: a second stop finds the
				// container stopped and changes nothing, and a second restart
				// stops it and starts it again — its connections dropped and its
				// memory lost a second time. The claim reaches an MCP client as
				// idempotentHint, and a client may repeat a call marked so
				// freely, which here is a second outage.
				Safety:     plugin.Write,
				Idempotent: false,
				NeedsGrant: true,
				Scope:      "container",
				Inputs:     []plugin.Field{nameInput("the container to restart")},
				Run:        runRestart,
			}),
			cap(plugin.Capability{
				ID:      "docker.container.rm",
				Summary: "Remove a container",
				Examples: []plugin.Example{
					{Title: "remove a container that is already stopped", Inputs: map[string]any{"container": "web"}},
				},
				Keywords: []string{"delete", "prune", "cleanup", "destroy"},
				Description: "Deletes the container and its writable layer — everything written " +
					"inside it that was not on a volume is gone, and it does not come back. Named " +
					"volumes survive; anonymous ones do not unless the daemon is asked to keep " +
					"them, and this does not ask. A stopped container is required: this deliberately " +
					"offers no `force`, because \"remove\" and \"kill first, then remove\" are two " +
					"decisions and only one of them was made here.",
				// The one genuine delete in this plugin, and the place docker
				// accepts a risk kube declined for the equivalent case (no pod
				// delete). Destructive, so a person is asked before it runs and
				// an agent needs a grant for it.
				Safety:     plugin.Destructive,
				NeedsGrant: true,
				Scope:      "container",
				Inputs:     []plugin.Field{nameInput("the container to remove — it must be stopped")},
				Run:        runRemove,
			}),
		},
	}
}
