# docker

Containers and images: what is running, what is stale, and the daily tidy-up

## Capabilities

| Capability               | Safety      | Summary                                                                       |
|--------------------------|-------------|-------------------------------------------------------------------------------|
| docker.container.inspect | write       | Everything the daemon knows about one container                               |
| docker.container.list    | read        | Containers, with state, health, ports and age                                 |
| docker.container.restart | write       | Restart a container                                                           |
| docker.container.rm      | destructive | Remove a container                                                            |
| docker.container.stop    | write       | Stop a running container                                                      |
| docker.image.list        | read        | Images, with size and age                                                     |
| docker.overview          | read        | One daemon at a glance: what is running, what is unhealthy, what disk is used |

## Configuration

Under `plugins: docker:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: docker@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key     | Read by                                                                                                                                                   | Help                                                                                  |
|---------|-----------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------|
| all     | docker.container.list                                                                                                                                     | include stopped containers                                                            |
| context | docker.container.inspect, docker.container.list, docker.container.restart, docker.container.rm, docker.container.stop, docker.image.list, docker.overview | docker context to use — the current one when omitted                                  |
| host    | docker.container.inspect, docker.container.list, docker.container.restart, docker.container.rm, docker.container.stop, docker.image.list, docker.overview | daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |

## docker.container.inspect

Image, command, state, restart policy, mounts, networks and environment. **Write rather than Read, and it needs a grant**, because a container's environment carries plaintext credentials by convention — every `-e` and every compose-file value — and deciding which of those are secret by their names is a guess, not a rule. rta would rather ask than guess wrong once.

| Field                | Value                                                                                                                                                                 |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.inspect                                                                                                                                              |
| summary              | Everything the daemon knows about one container                                                                                                                       |
| safety               | write                                                                                                                                                                 |
| idempotent           | true                                                                                                                                                                  |
| cli                  | rta docker container inspect \<container> \[--host \<string>\] \[--context \<string>\]                                                                                |
| mcp-tool             | docker_container_inspect                                                                                                                                              |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.inspect\`, optionally naming one container                                                                 |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:container      | string, required, completes — the container to inspect                                                                                                                |
| input:host           | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context        | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.container.list

Running containers by default; --all includes the stopped ones, which is usually what somebody wants before a tidy-up. Health is shown separately from state because a container can be up and failing its own healthcheck.

| Field         | Value                                                                                                                                                                 |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.container.list                                                                                                                                                 |
| summary       | Containers, with state, health, ports and age                                                                                                                         |
| safety        | read                                                                                                                                                                  |
| idempotent    | true                                                                                                                                                                  |
| cli           | rta docker container list \[--all \<bool>\] \[--host \<string>\] \[--context \<string>\]                                                                              |
| mcp-tool      | docker_container_list                                                                                                                                                 |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:all     | bool, from config plugins.docker.all — include stopped containers                                                                                                     |
| input:host    | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.container.restart

Stop then start, keeping the container's id, volumes and configuration. What it does lose is whatever was only in the process's memory and whatever was written outside a volume.

| Field                | Value                                                                                                                                                                 |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.restart                                                                                                                                              |
| summary              | Restart a container                                                                                                                                                   |
| safety               | write                                                                                                                                                                 |
| idempotent           | true                                                                                                                                                                  |
| cli                  | rta docker container restart \<container> \[--host \<string>\] \[--context \<string>\]                                                                                |
| mcp-tool             | docker_container_restart                                                                                                                                              |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.restart\`, optionally naming one container                                                                 |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:container      | string, required, completes — the container to restart                                                                                                                |
| input:host           | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context        | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.container.rm

Deletes the container and its writable layer — everything written inside it that was not on a volume is gone, and it does not come back. Named volumes survive; anonymous ones do not unless the daemon is asked to keep them, and this does not ask. A stopped container is required: this deliberately offers no --force, because "remove" and "kill first, then remove" are two decisions and only one of them was made here.

| Field                | Value                                                                                                                                                                 |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.rm                                                                                                                                                   |
| summary              | Remove a container                                                                                                                                                    |
| safety               | destructive                                                                                                                                                           |
| idempotent           | false                                                                                                                                                                 |
| cli                  | rta docker container rm \<container> \[--host \<string>\] \[--context \<string>\]                                                                                     |
| mcp-tool             | docker_container_rm                                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.rm\`, optionally naming one container                                                                      |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:container      | string, required, completes — the container to remove — it must be stopped                                                                                            |
| input:host           | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context        | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.container.stop

Sends SIGTERM and gives the container time to exit before the daemon kills it. Reversible — `docker start` brings it back with the same id, disk and configuration — which is why this is Write and not Destructive.

| Field                | Value                                                                                                                                                                 |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | docker.container.stop                                                                                                                                                 |
| summary              | Stop a running container                                                                                                                                              |
| safety               | write                                                                                                                                                                 |
| idempotent           | true                                                                                                                                                                  |
| cli                  | rta docker container stop \<container> \[--host \<string>\] \[--context \<string>\]                                                                                   |
| mcp-tool             | docker_container_stop                                                                                                                                                 |
| grant required (mcp) | yes — a person must run \`rta grant allow docker.container.stop\`, optionally naming one container                                                                    |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:container      | string, required, completes — the container to stop                                                                                                                   |
| input:host           | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context        | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.image.list

What is on this machine's disk. Dangling images — the untagged leftovers of a rebuild — are marked, since they are usually the answer to "where did my disk go".

| Field         | Value                                                                                                                                                                 |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.image.list                                                                                                                                                     |
| summary       | Images, with size and age                                                                                                                                             |
| safety        | read                                                                                                                                                                  |
| idempotent    | true                                                                                                                                                                  |
| cli           | rta docker image list \[--host \<string>\] \[--context \<string>\]                                                                                                    |
| mcp-tool      | docker_image_list                                                                                                                                                     |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:host    | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |

## docker.overview

Whether the daemon answers, how many containers are up against how many exist, anything unhealthy or recently exited, and how much disk images are taking. With --detail: the containers and the largest images themselves.

| Field         | Value                                                                                                                                                                 |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | docker.overview                                                                                                                                                       |
| summary       | One daemon at a glance: what is running, what is unhealthy, what disk is used                                                                                         |
| safety        | read                                                                                                                                                                  |
| idempotent    | true                                                                                                                                                                  |
| cli           | rta docker overview \[--host \<string>\] \[--context \<string>\] \[--detail\]                                                                                         |
| mcp-tool      | docker_overview                                                                                                                                                       |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow docker --profile \<name>\`                                  |
| input:host    | string, local (never offered to MCP callers), from config plugins.docker.host — daemon address, e.g. unix:///var/run/docker.sock — the CLI's own default when omitted |
| input:context | string, local (never offered to MCP callers), from config plugins.docker.context — docker context to use — the current one when omitted                               |
| input:detail  | bool, default false — return the full detailed view instead of the compact summary                                                                                    |
