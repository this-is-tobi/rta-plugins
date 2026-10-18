# cnpg

Read the state of CloudNativePG PostgreSQL clusters

Asks for `kubeconfig` — granted, or not, with `rta plugin allow cnpg`.

## Capabilities

| Capability          | Safety | Summary                                                                                          |
|---------------------|--------|--------------------------------------------------------------------------------------------------|
| cnpg.backup.list    | read   | Backups taken of a cluster: when, how, where they went, and what failed                          |
| cnpg.backup.request | write  | Ask the operator to back a cluster up now, using that cluster's own configuration                |
| cnpg.backup.show    | read   | How one cluster's backups are set up and where they land: store, retention, schedules            |
| cnpg.list           | read   | Every CloudNativePG cluster, and whether it is healthy                                           |
| cnpg.status         | read   | One cluster in depth: instances, replication, backups, storage                                   |
| cnpg.storage        | read   | The volumes a cluster's data and WAL sit on: size, class, and whether each one is actually bound |

## Configuration

Under `plugins: cnpg:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: cnpg@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock. A profile can state a lock where it matters: its `only:` list fences an input to the values that connection reaches, and a call outside it is refused.

| Key           | Read By                                                                                       | Help                                                                                                           |
|---------------|-----------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| backup.method | cnpg.backup.request                                                                           | how to take it — its only configured mechanism when omitted; plugin asks its WAL-archiver plugin               |
| backup.online | cnpg.backup.request                                                                           | hot or cold — only with a \`method\` of volumeSnapshot, and the cluster's own choice when omitted              |
| backup.target | cnpg.backup.request                                                                           | which instance performs it — the cluster's own choice when omitted                                             |
| cluster       | cnpg.backup.list                                                                              | only this cluster's backups — every cluster's when omitted; empty lifts one a profile or the config pins       |
| cluster       | cnpg.backup.request                                                                           | the cluster to back up; empty lifts one a profile or the config pins                                           |
| cluster       | cnpg.backup.show                                                                              | the cluster whose backups to show; empty lifts one a profile or the config pins                                |
| cluster       | cnpg.list                                                                                     | only this cluster — every cluster of the namespace when omitted; empty lifts one a profile or the config pins  |
| cluster       | cnpg.status                                                                                   | the cluster to read; empty lifts one a profile or the config pins                                              |
| cluster       | cnpg.storage                                                                                  | the cluster whose volumes to read — every cluster's when omitted; empty lifts one a profile or the config pins |
| context       | cnpg.backup.list, cnpg.backup.request, cnpg.backup.show, cnpg.list, cnpg.status, cnpg.storage | kubeconfig context to use — the current one when omitted                                                       |
| limit         | cnpg.backup.list                                                                              | how many backups to list; a cut list puts the failed ones first                                                |
| namespace     | cnpg.backup.list, cnpg.backup.request, cnpg.backup.show, cnpg.list, cnpg.status, cnpg.storage | namespace to look in — the context's own when omitted                                                          |

## cnpg.backup.list

The Backup objects themselves, which the Cluster resource does not carry. `cnpg.status` answers 'is this cluster backed up' from the summary fields the operator keeps for the mechanisms it runs itself — last success, last failure, first recoverable point — which is the right answer for those and no answer for a cluster that backs up through a plugin, whose backups are recorded only here. This is the other question: which individual backups exist, how each was taken, how long it took, where the bytes went, and what the operator said when one failed. The WAL a backup spans is not in this table, which stays the same table whatever the backups record: `cnpg.backup.show` gives the range of the newest completed one.

One namespace by default, or every one with `all-namespaces`, which adds a leading Namespace column. Bounded by `limit`: the list is cut at that many rows, `total` stays the exact count there is, and a cut list says so in a warning and puts the failed backups first, so the rows that matter are not the ones the cut drops.

Credentials are not read. A Backup's status carries the object-store credential references its cluster was configured with, and this decodes none of them — the names of the secrets holding your keys are not part of any question a backup listing answers.

| Field                | Value                                                                                                                                                                                                       |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.backup.list                                                                                                                                                                                            |
| summary              | Backups taken of a cluster: when, how, where they went, and what failed                                                                                                                                     |
| safety               | read                                                                                                                                                                                                        |
| idempotent           | true                                                                                                                                                                                                        |
| cli                  | rta cnpg backup list \[--cluster \<string>\] \[--namespace \<string>\] \[--all-namespaces\] \[--limit \<int>\] \[--context \<string>\]                                                                      |
| mcp-tool             | cnpg_backup_list                                                                                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta cnpg backup list --cluster app-db --namespace production   # the backups of one cluster                                                                                                                 |
| example              | rta cnpg backup list --all-namespaces   # every backup in every namespace                                                                                                                                   |
| input:cluster        | string, completes, from config plugins.cnpg.cluster — only this cluster's backups — every cluster's when omitted; empty lifts one a profile or the config pins                                              |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:all-namespaces | bool, widens --namespace (refused while a profile fences it) — every namespace instead of one                                                                                                               |
| input:limit          | int, default 100, a value from 1 to 10000, from config plugins.cnpg.limit — how many backups to list; a cut list puts the failed ones first                                                                 |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard            | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add cnpg.backup.list\`, or + on it in the TUI, puts it there, re-run every 30s                                                 |

## cnpg.backup.request

Creates a Backup object. **rta does not take the backup and does not choose where it goes** — the operator does both, using the configuration already on the cluster: destination, credentials, retention and encryption all come from `.spec.backup` or from the WAL-archiver plugin the cluster names. A Backup carries a cluster reference and nothing else, which is what makes this safe to expose at all: there is no destination for a caller to point somewhere useful to them.

Refused when the cluster configures no backup at all. CloudNativePG accepts such a Backup and lets it fail asynchronously — verified against a running operator, which admits the object with no complaint — so the failure would surface minutes later in a place nobody is looking. rta reads the cluster first and says so instead.

`method` says how, and when it is left out rta states the one mechanism the cluster is set up for, and the receipt says rta chose it; a cluster set up for several is refused until one is named. `plugin` asks the WAL-archiver plugin the cluster names, whose name is copied from the cluster and is never a caller's to give. It is refused up front, rather than accepted and failed minutes later, when that plugin has declared it cannot take a backup or the ObjectStore it names does not exist; the receipt says where the backup goes. `target` and `online` override what the cluster settled on, and are optional: left out they mean 'do what you would have done anyway'.

| Field                | Value                                                                                                                                                                                                       |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.backup.request                                                                                                                                                                                         |
| summary              | Ask the operator to back a cluster up now, using that cluster's own configuration                                                                                                                           |
| safety               | write                                                                                                                                                                                                       |
| idempotent           | false                                                                                                                                                                                                       |
| cli                  | rta cnpg backup request \[--cluster \<string>\] \[--namespace \<string>\] \[--method \<string>\] \[--target \<string>\] \[--online \<string>\] \[--context \<string>\]                                      |
| mcp-tool             | cnpg_backup_request                                                                                                                                                                                         |
| grant required (mcp) | yes — a person must run \`rta grant allow cnpg.backup.request\`, optionally naming one cluster                                                                                                              |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta cnpg backup request --cluster app-db --namespace production   # back one cluster up now                                                                                                                 |
| example              | rta cnpg backup request --cluster app-db --namespace production --method plugin   # back one up through its WAL-archiver plugin                                                                             |
| input:cluster        | string, required, completes, from config plugins.cnpg.cluster, the scope — a call names it, and a profile's \`set:\` never fills it — the cluster to back up; empty lifts one a profile or the config pins  |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:method         | string, one of: barmanObjectStore\|volumeSnapshot\|plugin, from config plugins.cnpg.backup.method — how to take it — its only configured mechanism when omitted; plugin asks its WAL-archiver plugin        |
| input:target         | string, one of: primary\|prefer-standby, from config plugins.cnpg.backup.target — which instance performs it — the cluster's own choice when omitted                                                        |
| input:online         | string, one of: true\|false, from config plugins.cnpg.backup.online — hot or cold — only with a \`method\` of volumeSnapshot, and the cluster's own choice when omitted                                     |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard            | never a tile — a tile runs on a timer with no confirmation, and this mutates                                                                                                                                |

## cnpg.backup.show

How one cluster's backups are arranged and where they land, read from the Cluster and, for a cluster that archives through the barman-cloud plugin, from the plugin's own resources. For an object store the Cluster itself states, the destination, server name and retention. For the plugin, the ObjectStore the cluster names: its destination, endpoint and retention, the kind of credential it uses and the names of the Secrets it references — names only, never keys or values — and the recovery window it reports for this cluster's server. For both, the newest completed Backup object that names the cluster and the WAL it records itself as spanning (the begin and end log position, the LSN, and the WAL file each is in, each only when the Backup records it: a restore replays from the begin position and is consistent once it has replayed the end one), the ScheduledBackups that name the cluster (cron, method, whether suspended, when each last ran and runs next) and the ContinuousArchiving and LastBackupSucceeded conditions when the cluster reports them.

This is where a plugin-archived cluster's answer to `cnpg.status`'s question is, since the Cluster resource keeps no record of backups taken through a plugin. A URL is shown without a user name, password or query.

Each read beside the Cluster stands alone: one that is refused, or whose type or object is absent, becomes a note in its section and a warning that the page is partial, and never fails the rest. Nothing here reads a Secret.

| Field           | Value                                                                                                                                                                                                       |
|-----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | cnpg.backup.show                                                                                                                                                                                            |
| summary         | How one cluster's backups are set up and where they land: store, retention, schedules                                                                                                                       |
| safety          | read                                                                                                                                                                                                        |
| idempotent      | true                                                                                                                                                                                                        |
| cli             | rta cnpg backup show \[--cluster \<string>\] \[--namespace \<string>\] \[--context \<string>\]                                                                                                              |
| mcp-tool        | cnpg_backup_show                                                                                                                                                                                            |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example         | rta cnpg backup show --cluster app-db   # how one cluster's backups are set up                                                                                                                              |
| input:cluster   | string, required, completes, from config plugins.cnpg.cluster — the cluster whose backups to show; empty lifts one a profile or the config pins                                                             |
| input:namespace | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key       | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard       | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add cnpg.backup.show\`, or + on it in the TUI, puts it there, re-run every 30s                                                 |

## cnpg.list

One `kubectl get clusters.postgresql.cnpg.io -o json`, rendered with the columns the CRD itself declares as printer columns — so this and `kubectl get` answer the same question the same way. Ready is ready/desired instances; a cluster short of its own spec is graded as a problem whatever its phase says. Reads the context's own namespace unless `namespace` names another or `all-namespaces` reads every one, and an empty answer says which it read. `cluster` narrows the listing to one: a profile that fences it (`only: cluster:`) lists the clusters it names and no other. Reads one custom resource and nothing else: no pods, no exec, no logs.

| Field                | Value                                                                                                                                                                                                       |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.list                                                                                                                                                                                                   |
| summary              | Every CloudNativePG cluster, and whether it is healthy                                                                                                                                                      |
| safety               | read                                                                                                                                                                                                        |
| idempotent           | true                                                                                                                                                                                                        |
| cli                  | rta cnpg list \[--cluster \<string>\] \[--namespace \<string>\] \[--all-namespaces\] \[--context \<string>\]                                                                                                |
| mcp-tool             | cnpg_list                                                                                                                                                                                                   |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta cnpg list --all-namespaces   # every cluster in every namespace                                                                                                                                         |
| example              | rta cnpg list --namespace production   # the clusters of one namespace                                                                                                                                      |
| input:cluster        | string, completes, from config plugins.cnpg.cluster — only this cluster — every cluster of the namespace when omitted; empty lifts one a profile or the config pins                                         |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:all-namespaces | bool, widens --namespace (refused while a profile fences it) — every namespace instead of one                                                                                                               |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard            | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add cnpg.list\`, or + on it in the TUI, puts it there, re-run every 30s                                                        |

## cnpg.status

Everything the Cluster resource reports about itself, laid out as the questions somebody opens it to ask. Instances are listed primary first, with the role taken from the cluster's own currentPrimary rather than each instance's self-report — during a failover those disagree, and the cluster's view is the one the rest of the fields are consistent with. A switchover in flight is derived rather than printed raw: CNPG moves targetPrimary before currentPrimary, so the two differing means a promotion is happening now. Every instance on one node is reported, because the CRD's own documentation calls that the absence of high availability. Conditions are shown only when they are not satisfied. The last successful backup is reported as an age, since that is the form the question is asked in — and a backup that is not configured at all is distinguished from one that is configured and failing, which the resource spells identically. A cluster that archives through a WAL-archiver plugin is told apart from both: the resource records no backup taken through a plugin (CloudNativePG documents its backup fields as not set for them), so the row says the resource cannot tell and points at `cnpg.backup.list`, whose Backup objects are the record, and at `cnpg.backup.show`, which reads where they go, rather than claiming none ever succeeded. The primary's tenure is derived (a young primary on an old cluster is the trace of a failover), certificate expiries are graded against the same 30-day window rta's other certificate checks use, and the replication posture, resource bounds and superuser-access switch are read from the spec. The resource holds no replication positions or lag, and this reads no pod. For a cluster with replicas one row says where they are read: the pg plugin's replication page, which lists every standby's sent, write, flush and replay LSN and lag from the primary, through a profile's `kube:` forward to the read-write service `<cluster>-rw`, as a role in pg_monitor.

| Field           | Value                                                                                                                                                                                                       |
|-----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | cnpg.status                                                                                                                                                                                                 |
| summary         | One cluster in depth: instances, replication, backups, storage                                                                                                                                              |
| safety          | read                                                                                                                                                                                                        |
| idempotent      | true                                                                                                                                                                                                        |
| cli             | rta cnpg status \[--cluster \<string>\] \[--namespace \<string>\] \[--context \<string>\]                                                                                                                   |
| mcp-tool        | cnpg_status                                                                                                                                                                                                 |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example         | rta cnpg status --cluster app-db --namespace production   # one cluster in depth                                                                                                                            |
| input:cluster   | string, required, completes, from config plugins.cnpg.cluster — the cluster to read; empty lifts one a profile or the config pins                                                                           |
| input:namespace | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key       | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard       | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add cnpg.status\`, or + on it in the TUI, puts it there, re-run every 30s                                                      |

## cnpg.storage

`cnpg.status` reports the storage the spec asks for. This reports what the cluster got, which is a different question the moment anything has gone wrong: a claim still Pending, one whose capacity came back smaller than requested, and an expansion that never finished all look identical in the spec, and each is a database about to stop writing.

With no `cluster` it reads the volumes of every CloudNativePG cluster in the namespace and adds a Cluster column; `all-namespaces` reads every namespace and adds a leading Namespace column. Only volumes CloudNativePG labelled are read, never the rest of a namespace's storage.

**It does not report how full a volume is.** That comes from the kubelet's own stats endpoint through the node proxy — a different mechanism and a different permission, and one that does not survive every proxy people put in front of a cluster, which is the property this plugin is built around. A column that looked like usage and was capacity would be worse than no column. `kube.pvc.usage` reports it, graded and worst first, for whoever holds nodes/proxy.

| Field                | Value                                                                                                                                                                                                       |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.storage                                                                                                                                                                                                |
| summary              | The volumes a cluster's data and WAL sit on: size, class, and whether each one is actually bound                                                                                                            |
| safety               | read                                                                                                                                                                                                        |
| idempotent           | true                                                                                                                                                                                                        |
| cli                  | rta cnpg storage \[--cluster \<string>\] \[--namespace \<string>\] \[--all-namespaces\] \[--context \<string>\]                                                                                             |
| mcp-tool             | cnpg_storage                                                                                                                                                                                                |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`; \`only:\` in a profile's entry fences an input to the values it reaches |
| example              | rta cnpg storage --cluster app-db --namespace production   # the volumes of one cluster                                                                                                                     |
| example              | rta cnpg storage --all-namespaces   # every cluster's volumes, in every namespace                                                                                                                           |
| input:cluster        | string, completes, from config plugins.cnpg.cluster — the cluster whose volumes to read — every cluster's when omitted; empty lifts one a profile or the config pins                                        |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to look in — the context's own when omitted                                                                                               |
| input:all-namespaces | bool, widens --namespace (refused while a profile fences it) — every namespace instead of one                                                                                                               |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                                                        |
| set a key            | rta config set plugins.cnpg.cluster \<value>                                                                                                                                                                |
| dashboard            | not on the automatic dashboard — it declines to run unasked; \`rta dashboard add cnpg.storage\`, or + on it in the TUI, puts it there, re-run every 30s                                                     |
