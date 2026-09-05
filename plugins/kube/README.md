# kube

Read-first Kubernetes: contexts, namespaces, pods, deployments

Asks for `kubeconfig` — granted, or not, with `rta plugin allow kube`.

## Capabilities

| Capability                    | Safety      | Summary                                                                      |
|-------------------------------|-------------|------------------------------------------------------------------------------|
| kube.cert.list                | read        | Every TLS certificate this cluster stores as a Secret, and its expiry        |
| kube.context.get              | read        | The current context in full: cluster, user and default namespace             |
| kube.context.list             | read        | Every context in this machine's kubeconfig, and which one is current         |
| kube.context.set              | write       | Switch this machine's current kubeconfig context                             |
| kube.deployment.list          | read        | Deployments in a namespace, with how many replicas are actually ready        |
| kube.event.list               | read        | What the cluster is complaining about, oldest-running problems still visible |
| kube.metrics.node             | read        | Node CPU/memory usage against what the node can actually allocate            |
| kube.metrics.pod              | read        | Pod CPU/memory usage against each pod's own limit, worst pressure first      |
| kube.metrics.pressure         | read        | Kernel pressure stall per node: is anything waiting, and is it getting worse |
| kube.namespace.list           | read        | Namespaces in the cluster, with their status and age                         |
| kube.node.list                | read        | Nodes, with readiness, cordon state and the pressures a kubelet reports      |
| kube.overview                 | read        | One cluster at a glance: where you are pointed and what is not healthy       |
| kube.pod.list                 | read        | Pods in a namespace, with readiness, restarts and age                        |
| kube.pvc.list                 | read        | PersistentVolumeClaims: capacity, requested size, storage class and phase    |
| kube.pvc.usage                | read        | How full each PersistentVolumeClaim actually is, worst first                 |
| kube.quota.list               | read        | ResourceQuota pressure per namespace: used against hard, as a percentage     |
| kube.serviceaccount.list      | read        | ServiceAccounts this plugin has provisioned, and whether they look expired   |
| kube.serviceaccount.provision | write       | Mint a scoped ServiceAccount, Role and token for an agent to use             |
| kube.serviceaccount.revoke    | destructive | Delete a provisioned ServiceAccount, Role and RoleBinding                    |

## Configuration

Under `plugins: kube:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: kube@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key                | Read by                                                                                                                                                                                                                                                                                                                                     | Help                                                     |
|--------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------|
| context            | kube.cert.list, kube.context.get, kube.deployment.list, kube.event.list, kube.metrics.node, kube.metrics.pod, kube.metrics.pressure, kube.namespace.list, kube.node.list, kube.overview, kube.pod.list, kube.pvc.list, kube.pvc.usage, kube.quota.list, kube.serviceaccount.list, kube.serviceaccount.provision, kube.serviceaccount.revoke | kubeconfig context to use — the current one when omitted |
| namespace          | kube.cert.list, kube.deployment.list, kube.event.list, kube.metrics.pod, kube.pod.list, kube.pvc.list, kube.quota.list, kube.serviceaccount.list, kube.serviceaccount.provision, kube.serviceaccount.revoke                                                                                                                                 | namespace to read — the context's own when omitted       |
| serviceaccount.ttl | kube.serviceaccount.provision                                                                                                                                                                                                                                                                                                               | how long the minted token should last, e.g. 15m, 1h, 24h |

## kube.cert.list

Reads type: kubernetes.io/tls Secrets only, selected server-side so no other secret's data ever leaves the API server for this process. The TLS Secrets it does select arrive whole, tls.key included — Kubernetes cannot project a subset of a Secret's data, so no way of asking avoids that. Only tls.crt is decoded; the private key is never parsed, rendered, logged or stored, but it does cross the wire into this process. The leaf certificate's own expiry is judged on the same 30-day window `cert expiry` and `rta audit web` use.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.cert.list                                                                                                                                       |
| summary              | Every TLS certificate this cluster stores as a Secret, and its expiry                                                                                |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube cert list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                                    |
| mcp-tool             | kube_cert_list                                                                                                                                       |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.context.get

What a call from this machine would reach right now. Reads the kubeconfig only; the cluster is not contacted.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.context.get                                                                                                                                     |
| summary       | The current context in full: cluster, user and default namespace                                                                                     |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube context get \[--context \<string>\]                                                                                                         |
| mcp-tool      | kube_context_get                                                                                                                                     |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.context.list

Reads the kubeconfig only — no cluster is contacted, so this answers even when every cluster in it is unreachable. The current context is marked, and it is the one every other capability here uses unless config names another.

| Field      | Value                                                                |
|------------|----------------------------------------------------------------------|
| id         | kube.context.list                                                    |
| summary    | Every context in this machine's kubeconfig, and which one is current |
| safety     | read                                                                 |
| idempotent | true                                                                 |
| cli        | rta kube context list                                                |
| mcp-tool   | kube_context_list                                                    |

## kube.context.set

Rewrites current-context in the kubeconfig, which is what `kubectl config use-context` does. Every later command on this machine follows it — kubectl's, this plugin's, and anything else reading the same file — which is why it needs a grant naming the context you mean, and why the grant is worth reading twice before you issue it.

| Field                | Value                                                                                         |
|----------------------|-----------------------------------------------------------------------------------------------|
| id                   | kube.context.set                                                                              |
| summary              | Switch this machine's current kubeconfig context                                              |
| safety               | write                                                                                         |
| idempotent           | true                                                                                          |
| cli                  | rta kube context set \<name>                                                                  |
| mcp-tool             | kube_context_set                                                                              |
| mcp exposure         | off by default — \`rta mcp serve --allow-write kube\`                                         |
| grant required (mcp) | yes — a person must run \`rta grant allow kube.context.set\`, optionally naming one name      |
| input:name           | string, required, completes — the context to switch to — \`rta kube context list\` shows them |

## kube.deployment.list

Ready against desired, which is the number that says whether a rollout finished. One namespace by default, or every one with --all-namespaces.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.deployment.list                                                                                                                                 |
| summary              | Deployments in a namespace, with how many replicas are actually ready                                                                                |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube deployment list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                              |
| mcp-tool             | kube_deployment_list                                                                                                                                 |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.event.list

Warnings only unless --normal: on a cluster running any active operator the Normal events are routine narration and outnumber the warnings heavily.

An Event is a counter, not a log line — a recurring problem updates the existing event rather than appending one — so first-seen and count are reported alongside last-seen. Those two columns are the payload: an event first seen eleven days ago with thirteen thousand occurrences is a different signal from a one-off thirty seconds ago, and last-seen alone renders them the same. It also means the usual "events only go back an hour" is true only for problems that stopped: the TTL runs from last-seen, so anything still recurring is never collected and its first-seen can be weeks old.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.event.list                                                                                                                                      |
| summary              | What the cluster is complaining about, oldest-running problems still visible                                                                         |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube event list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--normal \<bool>\] \[--context \<string>\]                              |
| mcp-tool             | kube_event_list                                                                                                                                      |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:normal         | bool — include Normal events, not only Warnings                                                                                                      |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.metrics.node

Same metrics-server dependency as kube.metrics.pod. Allocatable, not capacity: a node reserves some of its own resources for the kubelet and system daemons, and allocatable is what workloads can actually be scheduled into.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.metrics.node                                                                                                                                    |
| summary       | Node CPU/memory usage against what the node can actually allocate                                                                                    |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube metrics node \[--context \<string>\]                                                                                                        |
| mcp-tool      | kube_metrics_node                                                                                                                                    |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.metrics.pod

Needs the metrics-server add-on (metrics.k8s.io); a cluster without it names that in the error rather than a bare "not found". Sorted by memory pressure — the failure mode a container hits is OOMKilled, not "CPU too high" — so the pod closest to its own limit leads regardless of namespace or name.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.metrics.pod                                                                                                                                     |
| summary              | Pod CPU/memory usage against each pod's own limit, worst pressure first                                                                              |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube metrics pod \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                                  |
| mcp-tool             | kube_metrics_pod                                                                                                                                     |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.metrics.pressure

Reads the kubelet's own Summary API, not metrics-server. Pressure answers a question a usage percentage cannot: whether work is actually being held up. A node at 90% CPU with nothing waiting is a node being used well.

Each resource is reported over a 10-second and a 5-minute window, and the pair is the point — a short window above the long one is pressure building, below it is pressure clearing. That is the shape you would otherwise open a dashboard for.

The "some" series is reported, meaning at least one task was stalled. The "full" series is not: for CPU it is defined as zero at system level, so a node stalling a third of the time reads as perfectly idle through it.

Needs cgroup v2 and a Linux kernel 4.20 or newer; nodes without it are named rather than shown as zeroes. Needs the nodes/proxy permission — see kube.pvc.usage.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.metrics.pressure                                                                                                                                |
| summary       | Kernel pressure stall per node: is anything waiting, and is it getting worse                                                                         |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube metrics pressure \[--node \<string>\] \[--context \<string>\]                                                                               |
| mcp-tool      | kube_metrics_pressure                                                                                                                                |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:node    | string — one node instead of every node                                                                                                              |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.namespace.list

The first capability here that contacts the cluster, so it is also the quickest way to find out whether the current context can reach one.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.namespace.list                                                                                                                                  |
| summary       | Namespaces in the cluster, with their status and age                                                                                                 |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube namespace list \[--context \<string>\]                                                                                                      |
| mcp-tool      | kube_namespace_list                                                                                                                                  |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.node.list

Conditions, not usage — no metrics-server needed, unlike kube.metrics.node. Three statuses and they mean different things: NotReady is a kubelet reporting a problem, Unknown is a kubelet that stopped reporting at all, and SchedulingDisabled is an operator having cordoned the node on purpose. The pressure column is the kubelet's own MemoryPressure/DiskPressure/PIDPressure — a node can be Ready and under pressure at the same time, which is the state worth catching before it evicts anything.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.node.list                                                                                                                                       |
| summary       | Nodes, with readiness, cordon state and the pressures a kubelet reports                                                                              |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube node list \[--context \<string>\]                                                                                                           |
| mcp-tool      | kube_node_list                                                                                                                                       |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.overview

The context, whether the cluster answers, how many namespaces it has, which nodes are not Ready, and every pod that is not serving — Failed, Pending, Unknown, or Running without every container ready. A finished Job in Succeeded is not one of them, and a cordoned node is reported separately rather than counted as not ready: both are deliberate states, not faults. Pod-slot headroom comes from the schedulable nodes' own max-pods, which is the number that says whether a cluster can still take work when CPU and memory look fine. With --detail: every node, deployments whose replicas are short, and the pods themselves.

Reads more than it names: every ResourceQuota and every TLS Secret in every namespace, on every run and regardless of any namespace narrowing, to report quota pressure and certificate expiry. See kube.cert.list for what reading a TLS Secret costs — it applies here too, unconditionally. A credential that cannot list nodes still gets the rest: the node read is reported as unavailable and stepped over, not treated as a failure.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.overview                                                                                                                                        |
| summary       | One cluster at a glance: where you are pointed and what is not healthy                                                                               |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube overview \[--context \<string>\] \[--detail\]                                                                                               |
| mcp-tool      | kube_overview                                                                                                                                        |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |
| input:detail  | bool, default false — return the full detailed view instead of the compact summary                                                                   |

## kube.pod.list

One namespace by default — the context's own — or every namespace with --all-namespaces. Restarts are worth reading: a pod that is Running and has restarted forty times is not healthy, and only one of those two facts shows in its status. --unhealthy narrows to pods that are not serving: Failed, Pending, Unknown, or Running without every container ready. A pod in Succeeded is not included — a finished Job is not a broken one. The same judgement kube.overview makes, available here without the rest of the overview.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.pod.list                                                                                                                                        |
| summary              | Pods in a namespace, with readiness, restarts and age                                                                                                |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube pod list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--unhealthy \<bool>\] \[--context \<string>\]                             |
| mcp-tool             | kube_pod_list                                                                                                                                        |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:unhealthy      | bool — only pods that are not serving — Failed, Pending, Unknown, or Running without every container ready                                           |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.pvc.list

Provisioned capacity, not how full a volume actually is — that number lives in kubelet volume stats, a different and more involved mechanism this does not reach. A Pending PVC (no bound PersistentVolume yet) reports its requested size and an empty capacity, which is the honest state of an unfulfilled claim.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.pvc.list                                                                                                                                        |
| summary              | PersistentVolumeClaims: capacity, requested size, storage class and phase                                                                            |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube pvc list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                                     |
| mcp-tool             | kube_pvc_list                                                                                                                                        |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.pvc.usage

The number kube.pvc.list deliberately does not report, because it comes from somewhere else entirely: the kubelet's Summary API, one call per node, rather than the PVC objects themselves.

Two limits worth knowing. Only volumes a live pod currently mounts are measured at all — an unmounted claim has no kubelet reporting on it and simply will not appear. And reaching this needs the nodes/proxy permission, which is indivisible: it covers the whole kubelet API including exec on every pod on the node. That is why this is a separate capability rather than columns on kube.pvc.list, and why neither it nor kube.metrics.pressure can be granted to a minted ServiceAccount.

A node that cannot be read is named, because a missing node means missing claims.

| Field         | Value                                                                                                                                                |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id            | kube.pvc.usage                                                                                                                                       |
| summary       | How full each PersistentVolumeClaim actually is, worst first                                                                                         |
| safety        | read                                                                                                                                                 |
| idempotent    | true                                                                                                                                                 |
| cli           | rta kube pvc usage \[--node \<string>\] \[--context \<string>\]                                                                                      |
| mcp-tool      | kube_pvc_usage                                                                                                                                       |
| profiles      | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:node    | string — one node instead of every node                                                                                                              |
| input:context | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.quota.list

One row per resource a quota tracks, not one row per quota object — cpu, memory and pod-count headroom read as a percentage rather than two numbers to do the division on by hand. LimitRange objects are noted by count rather than fully modeled; their shape does not table-ize alongside a used/hard resource map.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.quota.list                                                                                                                                      |
| summary              | ResourceQuota pressure per namespace: used against hard, as a percentage                                                                             |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube quota list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                                   |
| mcp-tool             | kube_quota_list                                                                                                                                      |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.serviceaccount.list

Only ServiceAccounts carrying provision's own label — not every ServiceAccount in the namespace. A minted token cannot be queried directly (Kubernetes does not persist a TokenRequest token as an object), so "expired" here is computed from the --ttl and issue time provision recorded as annotations at mint time — a best-effort estimate, not a live check against the API server.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.serviceaccount.list                                                                                                                             |
| summary              | ServiceAccounts this plugin has provisioned, and whether they look expired                                                                           |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta kube serviceaccount list \[--namespace \<string>\] \[--all-namespaces \<bool>\] \[--context \<string>\]                                          |
| mcp-tool             | kube_serviceaccount_list                                                                                                                             |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:namespace      | string, completes, from config plugins.kube.namespace — namespace to read — the context's own when omitted                                           |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |

## kube.serviceaccount.provision

Creates a ServiceAccount, a Role built from exactly the grants named in --grant (nothing broader — an unmapped name refuses the whole request rather than silently granting less than asked), a RoleBinding, and a token scoped to --ttl. A grant is either a kube.* capability ID (what that capability reads) or a bare word naming a cluster permission the minted identity needs but rta has no capability for: logs, workloads, services, and — the one write — rollout, which carries patch on workloads and is meant for environments where changing what runs is acceptable. Returns the assembled kubeconfig — to the terminal, or to --out, which refuses an existing file unless --force says to replace it. Refuses to run anywhere but a person's own CLI/TUI: an agent must never be able to mint its own parallel credential. There is no link enforced between --ttl and any `grant allow` TTL issued elsewhere — matching them is the operator's convention to keep, not something this checks.

| Field           | Value                                                                                                                                                                                                                                                                            |
|-----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | kube.serviceaccount.provision                                                                                                                                                                                                                                                    |
| summary         | Mint a scoped ServiceAccount, Role and token for an agent to use                                                                                                                                                                                                                 |
| safety          | write                                                                                                                                                                                                                                                                            |
| idempotent      | false                                                                                                                                                                                                                                                                            |
| cli             | rta kube serviceaccount provision \<name> \[--namespace \<string>\] \[--grant \<stringSlice>\] \[--ttl \<string>\] \[--out \<path>\] \[--force \<bool>\] \[--context \<string>\]                                                                                                 |
| mcp-tool        | kube_serviceaccount_provision                                                                                                                                                                                                                                                    |
| mcp exposure    | off by default — \`rta mcp serve --allow-write kube\`                                                                                                                                                                                                                            |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                                                                                                                                               |
| input:name      | string, required — name for the new ServiceAccount, Role and RoleBinding (all three share it)                                                                                                                                                                                    |
| input:namespace | string, required, completes, from config plugins.kube.namespace — namespace to provision the identity in                                                                                                                                                                         |
| input:grant     | stringSlice, required, one of: kube.deployment.list\|kube.event.list\|kube.metrics.pod\|kube.pod.list\|kube.pvc.list\|kube.quota.list\|logs\|rollout\|services\|workloads — what the identity may do: a kube.* capability ID, or logs, workloads, services, rollout — repeatable |
| input:ttl       | string, required, from config plugins.kube.serviceaccount.ttl — how long the minted token should last, e.g. 15m, 1h, 24h                                                                                                                                                         |
| input:out       | path, local (never offered to MCP callers) — write the kubeconfig to this file (0600) instead of printing it                                                                                                                                                                     |
| input:force     | bool, local (never offered to MCP callers) — replace --out's file if it already exists                                                                                                                                                                                           |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted                                                                                                                             |

## kube.serviceaccount.revoke

A TokenRequest bearer token has no independent early-revocation API — it stays valid until its own --ttl regardless of anything rta does. Deleting the ServiceAccount is what invalidates every token minted against it immediately, and because provision never reuses one ServiceAccount across grants, this always means "this one identity", never "every agent's access at once". Refuses to touch anything not carrying provision's own label, so this cannot be used to delete an unrelated ServiceAccount that happens to share a name. Tolerates any of the three objects already being gone (a previous partial provision, or a previous revoke run twice) rather than refusing on the first missing piece.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | kube.serviceaccount.revoke                                                                                                                           |
| summary              | Delete a provisioned ServiceAccount, Role and RoleBinding                                                                                            |
| safety               | destructive                                                                                                                                          |
| idempotent           | false                                                                                                                                                |
| cli                  | rta kube serviceaccount revoke \<name> \[--namespace \<string>\] \[--context \<string>\]                                                             |
| mcp-tool             | kube_serviceaccount_revoke                                                                                                                           |
| mcp exposure         | off by default — \`rta mcp serve --allow-destructive kube.serviceaccount.revoke\`                                                                    |
| grant required (mcp) | yes — a person must run \`rta grant allow kube.serviceaccount.revoke\`, optionally naming one name                                                   |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow kube --profile \<name>\`                   |
| input:name           | string, required — the ServiceAccount to revoke                                                                                                      |
| input:namespace      | string, required, completes, from config plugins.kube.namespace — namespace it was provisioned in                                                    |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.kube.context — kubeconfig context to use — the current one when omitted |
