# docker

Containers and images: what is running, what is stale, and the daily tidy-up

## Capabilities

| Capability               | Safety      | Summary                                                                     |
|--------------------------|-------------|-----------------------------------------------------------------------------|
| docker.container.env     | write       | The environment variables one container was started with                    |
| docker.container.inspect | write       | Everything the daemon knows about one container                             |
| docker.container.list    | read        | Containers, with state, health, ports and age                               |
| docker.container.restart | write       | Restart a container                                                         |
| docker.container.rm      | destructive | Remove a container                                                          |
| docker.container.stop    | write       | Stop a running container                                                    |
| docker.image.list        | read        | Images, with size and age                                                   |
| docker.overview          | read        | One daemon at a glance: its verdict, what is unhealthy, where the disk went |

## Configuration

Under `plugins: docker:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: docker@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock. A profile can state a lock where it matters: its `only:` list fences an input to the values that connection reaches, and a call outside it is refused.

| Key     | Read By                                                                                                                                                                         | Help                                                                                  |
|---------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------|
| all     | docker.container.list                                                                                                                                                           | include stopped containers                                                            |
| context | docker.container.env, docker.container.inspect, docker.container.list, docker.container.restart, docker.container.rm, docker.container.stop, docker.image.list, docker.overview | docker context to use — the current one when omitted                                  |
| host    | docker.container.env, docker.container.inspect, docker.container.list, docker.container.restart, docker.container.rm, docker.container.stop, docker.image.list, docker.overview | daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |

## docker.container.env

Every variable the container sets, with its value as set — the image's own, every `-e` and every compose-file value.

**The values come back as set, on every surface** — none of it masked, and an agent holding a grant for the container has them in its context from then on. docker.container.inspect shows the names and masks the values, and costs none of this.

**Classified write for what it discloses, not what it changes.** A container's environment carries plaintext credentials by convention, and deciding which variables are secret by their names is a guess, not a rule, so the consent is given once, for the container.

| Field                | Value                                                                                                                                                                                                         |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.env                                                                                                                                                                                          |
| summary              | The environment variables one container was started with                                                                                                                                                      |
| safety               | write                                                                                                                                                                                                         |
| idempotent           | true                                                                                                                                                                                                          |
| cli                  | rta docker container env \<container> \[--host \<string>\] \[--context \<string>\]                                                                                                                            |
| mcp-tool             | docker_container_env                                                                                                                                                                                          |
| reveals              | the stored value itself, which becomes part of the caller's context; nothing is changed                                                                                                                       |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.env\`, optionally naming one container                                                                                                             |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta docker container env web   # the variables one container was started with                                                                                                                                 |
| input:container      | string, required, completes — the container whose environment to read                                                                                                                                         |
| input:host           | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context        | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                  |

## docker.container.inspect

Image, command, state, restart policy, mounts, networks and environment. **Write rather than Read, and it needs a grant**, because the command a container was started with carries credentials passed as arguments often enough that no filter on it can be trusted, and rta would rather ask than guess wrong once.

**Environment values come back masked (••••••), on every surface.** The names are shown and every value is marked as secret, so what the result tells you is which variables a container sets, not what they are set to. docker.container.env returns them, under a grant of its own that names the container.

| Field                | Value                                                                                                                                                                                                         |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.inspect                                                                                                                                                                                      |
| summary              | Everything the daemon knows about one container                                                                                                                                                               |
| safety               | write                                                                                                                                                                                                         |
| idempotent           | true                                                                                                                                                                                                          |
| cli                  | rta docker container inspect \<container> \[--host \<string>\] \[--context \<string>\]                                                                                                                        |
| mcp-tool             | docker_container_inspect                                                                                                                                                                                      |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.inspect\`, optionally naming one container                                                                                                         |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta docker container inspect web   # one container in full                                                                                                                                                    |
| input:container      | string, required, completes — the container to inspect                                                                                                                                                        |
| input:host           | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context        | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                  |

## docker.container.list

Running containers by default; `all` includes the stopped ones, which is usually what somebody wants before a tidy-up. Health is shown separately from state because a container can be up and failing its own healthcheck.

| Field         | Value                                                                                                                                                                                                         |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.container.list                                                                                                                                                                                         |
| summary       | Containers, with state, health, ports and age                                                                                                                                                                 |
| safety        | read                                                                                                                                                                                                          |
| idempotent    | true                                                                                                                                                                                                          |
| cli           | rta docker container list \[--all\] \[--host \<string>\] \[--context \<string>\]                                                                                                                              |
| mcp-tool      | docker_container_list                                                                                                                                                                                         |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example       | rta docker container list --all   # every container, stopped ones too                                                                                                                                         |
| input:all     | bool, from config plugins.docker.all — include stopped containers                                                                                                                                             |
| input:host    | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key     | rta config set plugins.docker.all true                                                                                                                                                                        |
| dashboard     | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add docker.container.list\`, or + on it in the TUI, puts it there, re-run every 30s                                              |

## docker.container.restart

Stop then start, keeping the container's id, volumes and configuration. What it does lose is whatever was only in the process's memory and whatever was written outside a volume.

| Field                | Value                                                                                                                                                                                                         |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.restart                                                                                                                                                                                      |
| summary              | Restart a container                                                                                                                                                                                           |
| safety               | write                                                                                                                                                                                                         |
| idempotent           | false                                                                                                                                                                                                         |
| cli                  | rta docker container restart \<container> \[--host \<string>\] \[--context \<string>\]                                                                                                                        |
| mcp-tool             | docker_container_restart                                                                                                                                                                                      |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.restart\`, optionally naming one container                                                                                                         |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta docker container restart web   # restart one container                                                                                                                                                    |
| input:container      | string, required, completes — the container to restart                                                                                                                                                        |
| input:host           | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context        | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                  |

## docker.container.rm

Deletes the container and its writable layer — everything written inside it that was not on a volume is gone, and it does not come back. Named volumes survive; anonymous ones do not unless the daemon is asked to keep them, and this does not ask. A stopped container is required: this deliberately offers no `force`, because "remove" and "kill first, then remove" are two decisions and only one of them was made here.

| Field                | Value                                                                                                                                                                                                         |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.rm                                                                                                                                                                                           |
| summary              | Remove a container                                                                                                                                                                                            |
| safety               | destructive                                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                                         |
| cli                  | rta docker container rm \<container> \[--host \<string>\] \[--context \<string>\]                                                                                                                             |
| mcp-tool             | docker_container_rm                                                                                                                                                                                           |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.rm\`, optionally naming one container                                                                                                              |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta docker container rm web   # remove a container that is already stopped                                                                                                                                    |
| input:container      | string, required, completes — the container to remove — it must be stopped                                                                                                                                    |
| input:host           | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context        | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                  |

## docker.container.stop

Sends SIGTERM and gives the container time to exit before the daemon kills it. Reversible — `docker start` brings it back with the same id, disk and configuration — which is why this is Write and not Destructive. Except for a container started to remove itself when it stops (AutoRemove), which the daemon removes: the preview and the receipt say so, and an agent is refused it, as it is any stop of a container docker could not first be asked about.

| Field                | Value                                                                                                                                                                                                         |
|----------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.stop                                                                                                                                                                                         |
| summary              | Stop a running container                                                                                                                                                                                      |
| safety               | write                                                                                                                                                                                                         |
| idempotent           | true                                                                                                                                                                                                          |
| cli                  | rta docker container stop \<container> \[--host \<string>\] \[--context \<string>\]                                                                                                                           |
| mcp-tool             | docker_container_stop                                                                                                                                                                                         |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.stop\`, optionally naming one container                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta docker container stop web   # stop one container                                                                                                                                                          |
| input:container      | string, required, completes — the container to stop                                                                                                                                                           |
| input:host           | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context        | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                  |

## docker.image.list

What is on this machine's disk. Dangling images — the untagged leftovers of a rebuild — are marked, since they are usually the answer to "where did my disk go".

| Field         | Value                                                                                                                                                                                                         |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.image.list                                                                                                                                                                                             |
| summary       | Images, with size and age                                                                                                                                                                                     |
| safety        | read                                                                                                                                                                                                          |
| idempotent    | true                                                                                                                                                                                                          |
| cli           | rta docker image list \[--host \<string>\] \[--context \<string>\]                                                                                                                                            |
| mcp-tool      | docker_image_list                                                                                                                                                                                             |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| input:host    | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key     | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard     | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add docker.image.list\`, or + on it in the TUI, puts it there, re-run every 30s                                                  |

## docker.overview

The first row, `status`, is the verdict: ok, or warn or fail with the worst facts in a few words. Fail is a container failing its healthcheck, restarting or dead; warn is one that exited with a code other than 0, one that is paused or was never started, and a disk the daemon would not report. Then how many containers are running of those that exist, with the one-shots that exited cleanly (code 0) counted and not named; the problems, the unhealthy and the restarting first, then those that exited non-zero with the exit code read from their status text, then the rest, six at most; and where the disk went, from one `docker system df`: images, volumes and build cache, the largest first, and how much of them is reclaimable. Adding up the size of every volume takes the daemon seconds on a host with many, so a tile of it is re-run every 2 minutes at most, a floor a configuration can lengthen and not shorten. A daemon that does not answer is the verdict, with why and what to check. With `detail`: the images (a count and the leftovers of a rebuild, from an `image ls` the compact answer does not run), every container not running or not healthy, the one-shots included, the disk usage by kind, and the largest images.

| Field         | Value                                                                                                                                                                                                         |
|---------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.overview                                                                                                                                                                                               |
| summary       | One daemon at a glance: its verdict, what is unhealthy, where the disk went                                                                                                                                   |
| safety        | read                                                                                                                                                                                                          |
| idempotent    | true                                                                                                                                                                                                          |
| cli           | rta docker overview \[--host \<string>\] \[--context \<string>\] \[--detail\]                                                                                                                                 |
| mcp-tool      | docker_overview                                                                                                                                                                                               |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| input:host    | string, group network, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted                          |
| input:context | string, group network, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                                                        |
| set a key     | rta config set plugins.docker.host \<value>                                                                                                                                                                   |
| dashboard     | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add docker.overview\`, or + on it in the TUI, puts it there, re-run every 2m                                                     |
| input:detail  | bool, default false — return the full detailed view instead of the compact summary                                                                                                                            |
