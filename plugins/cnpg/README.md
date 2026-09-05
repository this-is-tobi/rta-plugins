# cnpg

Read the state of CloudNativePG PostgreSQL clusters

Asks for `kubeconfig` — granted, or not, with `rta plugin allow cnpg`.

## Capabilities

| Capability          | Safety | Summary                                                                                          |
|---------------------|--------|--------------------------------------------------------------------------------------------------|
| cnpg.backup.list    | read   | Backups taken of a cluster: when, how, where they went, and what failed                          |
| cnpg.backup.request | write  | Ask the operator to back a cluster up now, using that cluster's own configuration                |
| cnpg.list           | read   | Every CloudNativePG cluster, and whether it is healthy                                           |
| cnpg.status         | read   | One cluster in depth: instances, replication, backups, storage                                   |
| cnpg.storage        | read   | The volumes a cluster's data and WAL sit on: size, class, and whether each one is actually bound |

## Configuration

Under `plugins: cnpg:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: cnpg@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key           | Read by                                                                     | Help                                                                                       |
|---------------|-----------------------------------------------------------------------------|--------------------------------------------------------------------------------------------|
| backup.method | cnpg.backup.request                                                         | how to take it — the cluster's own choice when omitted                                     |
| backup.online | cnpg.backup.request                                                         | hot or cold — only with --method volumeSnapshot, and the cluster's own choice when omitted |
| backup.target | cnpg.backup.request                                                         | which instance performs it — the cluster's own choice when omitted                         |
| cluster       | cnpg.backup.list, cnpg.backup.request, cnpg.status, cnpg.storage            | only this cluster's backups — every one in the namespace when omitted                      |
| context       | cnpg.backup.list, cnpg.backup.request, cnpg.list, cnpg.status, cnpg.storage | kubeconfig context to use — the current one when omitted                                   |
| namespace     | cnpg.backup.list, cnpg.backup.request, cnpg.list, cnpg.status, cnpg.storage | namespace to read — the context's own when omitted                                         |

## cnpg.backup.list

The Backup objects themselves, which the Cluster resource does not carry. `cnpg.status` answers 'is this cluster backed up' from three summary fields the operator maintains — last success, last failure, first recoverable point — and that is the right answer to that question. This is the other one: which individual backups exist, how each was taken, how long it took, where the bytes went, and what the operator said when one failed.

Credentials are not read. A Backup's status carries the object-store credential references its cluster was configured with, and this decodes none of them — the names of the secrets holding your keys are not part of any question a backup listing answers.

| Field           | Value                                                                                                                                                |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | cnpg.backup.list                                                                                                                                     |
| summary         | Backups taken of a cluster: when, how, where they went, and what failed                                                                              |
| safety          | read                                                                                                                                                 |
| idempotent      | true                                                                                                                                                 |
| cli             | rta cnpg backup list \[--cluster \<string>\] \[--namespace \<string>\] \[--context \<string>\]                                                       |
| mcp-tool        | cnpg_backup_list                                                                                                                                     |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`                   |
| input:cluster   | string, completes, from config plugins.cnpg.cluster — only this cluster's backups — every one in the namespace when omitted                          |
| input:namespace | string, completes, from config plugins.cnpg.namespace — namespace to read — the context's own when omitted                                           |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted |

## cnpg.backup.request

Creates a Backup object. **rta does not take the backup and does not choose where it goes** — the operator does both, using the configuration already on the cluster: destination, credentials, retention and encryption all come from `.spec.backup` or from the WAL-archiver plugin the cluster names. A Backup carries a cluster reference and nothing else, which is what makes this safe to expose at all: there is no destination for a caller to point somewhere useful to them.

Refused when the cluster configures no backup at all. CloudNativePG accepts such a Backup and lets it fail asynchronously — verified against a running operator, which admits the object with no complaint — so the failure would surface minutes later in a place nobody is looking. rta reads the cluster first and says so instead.

`--method`, `--target` and `--online` override what the cluster settled on, and are all optional: sending none of them is the ordinary call and means 'do what you would have done anyway'.

| Field                | Value                                                                                                                                                                  |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.backup.request                                                                                                                                                    |
| summary              | Ask the operator to back a cluster up now, using that cluster's own configuration                                                                                      |
| safety               | write                                                                                                                                                                  |
| idempotent           | false                                                                                                                                                                  |
| cli                  | rta cnpg backup request \[--cluster \<string>\] \[--method \<string>\] \[--target \<string>\] \[--online \<string>\] \[--namespace \<string>\] \[--context \<string>\] |
| mcp-tool             | cnpg_backup_request                                                                                                                                                    |
| mcp exposure         | off by default — \`rta mcp serve --allow-write cnpg\`                                                                                                                  |
| grant required (mcp) | yes — a person must run \`rta grant allow cnpg.backup.request\`, optionally naming one cluster                                                                         |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`                                     |
| input:cluster        | string, required, completes, from config plugins.cnpg.cluster — the cluster to back up                                                                                 |
| input:method         | string, one of: barmanObjectStore\|volumeSnapshot, from config plugins.cnpg.backup.method — how to take it — the cluster's own choice when omitted                     |
| input:target         | string, one of: primary\|prefer-standby, from config plugins.cnpg.backup.target — which instance performs it — the cluster's own choice when omitted                   |
| input:online         | string, one of: true\|false, from config plugins.cnpg.backup.online — hot or cold — only with --method volumeSnapshot, and the cluster's own choice when omitted       |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to read — the context's own when omitted                                                             |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted                   |

## cnpg.list

One `kubectl get clusters.postgresql.cnpg.io -o json`, rendered with the columns the CRD itself declares as printer columns — so this and `kubectl get` answer the same question the same way. Ready is ready/desired instances; a cluster short of its own spec is graded as a problem whatever its phase says. Reads one custom resource and nothing else: no pods, no exec, no logs.

| Field                | Value                                                                                                                                                |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | cnpg.list                                                                                                                                            |
| summary              | Every CloudNativePG cluster, and whether it is healthy                                                                                               |
| safety               | read                                                                                                                                                 |
| idempotent           | true                                                                                                                                                 |
| cli                  | rta cnpg list \[--all-namespaces \<bool>\] \[--namespace \<string>\] \[--context \<string>\]                                                         |
| mcp-tool             | cnpg_list                                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`                   |
| input:all-namespaces | bool — every namespace instead of one                                                                                                                |
| input:namespace      | string, completes, from config plugins.cnpg.namespace — namespace to read — the context's own when omitted                                           |
| input:context        | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted |

## cnpg.status

Everything the Cluster resource reports about itself, laid out as the questions somebody opens it to ask. Instances are listed primary first, with the role taken from the cluster's own currentPrimary rather than each instance's self-report — during a failover those disagree, and the cluster's view is the one the rest of the fields are consistent with. A switchover in flight is derived rather than printed raw: CNPG moves targetPrimary before currentPrimary, so the two differing means a promotion is happening now. Every instance on one node is reported, because the CRD's own documentation calls that the absence of high availability. Conditions are shown only when they are not satisfied. The last successful backup is reported as an age, since that is the form the question is asked in — and a backup that is not configured at all is distinguished from one that is configured and failing, which the resource spells identically. The primary's tenure is derived (a young primary on an old cluster is the trace of a failover), certificate expiries are graded against the same 30-day window rta's other certificate checks use, and the replication posture, resource bounds and superuser-access switch are read from the spec.

| Field           | Value                                                                                                                                                |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | cnpg.status                                                                                                                                          |
| summary         | One cluster in depth: instances, replication, backups, storage                                                                                       |
| safety          | read                                                                                                                                                 |
| idempotent      | true                                                                                                                                                 |
| cli             | rta cnpg status \[--cluster \<string>\] \[--namespace \<string>\] \[--context \<string>\]                                                            |
| mcp-tool        | cnpg_status                                                                                                                                          |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`                   |
| input:cluster   | string, required, completes, from config plugins.cnpg.cluster — the cluster to read                                                                  |
| input:namespace | string, completes, from config plugins.cnpg.namespace — namespace to read — the context's own when omitted                                           |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted |

## cnpg.storage

`cnpg.status` reports the storage the spec asks for. This reports what the cluster got, which is a different question the moment anything has gone wrong: a claim still Pending, one whose capacity came back smaller than requested, and an expansion that never finished all look identical in the spec, and each is a database about to stop writing.

**It does not report how full a volume is.** That comes from the kubelet's own stats endpoint through the node proxy — a different mechanism and a different permission, and one that does not survive every proxy people put in front of a cluster, which is the property this plugin is built around. A column that looked like usage and was capacity would be worse than no column. `rta kube pvc usage` reports it, graded and worst first, for whoever holds nodes/proxy.

| Field           | Value                                                                                                                                                |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | cnpg.storage                                                                                                                                         |
| summary         | The volumes a cluster's data and WAL sit on: size, class, and whether each one is actually bound                                                     |
| safety          | read                                                                                                                                                 |
| idempotent      | true                                                                                                                                                 |
| cli             | rta cnpg storage \[--cluster \<string>\] \[--namespace \<string>\] \[--context \<string>\]                                                           |
| mcp-tool        | cnpg_storage                                                                                                                                         |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow cnpg --profile \<name>\`                   |
| input:cluster   | string, required, completes, from config plugins.cnpg.cluster — the cluster whose volumes to read                                                    |
| input:namespace | string, completes, from config plugins.cnpg.namespace — namespace to read — the context's own when omitted                                           |
| input:context   | string, completes, local (never offered to MCP callers), from config plugins.cnpg.context — kubeconfig context to use — the current one when omitted |
