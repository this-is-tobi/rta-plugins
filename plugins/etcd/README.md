# etcd

etcd v3: cluster health, members, leases and the keyspace

## Capabilities

| Capability       | Safety | Summary                                                                          |
|------------------|--------|----------------------------------------------------------------------------------|
| etcd.kv.get      | write  | What one key holds                                                               |
| etcd.kv.list     | read   | Key names under a prefix — never their contents                                  |
| etcd.kv.tree     | read   | The shape of the keyspace in one call — names only                               |
| etcd.lease.list  | read   | Outstanding leases and how long each has left                                    |
| etcd.member.list | read   | Who is in this cluster, and how each one is reachable                            |
| etcd.overview    | read   | Whether this cluster is healthy, and what it is made of                          |
| etcd.snapshot    | write  | Write a point-in-time snapshot of the whole keyspace, for a person at a terminal |

## Configuration

Under `plugins: etcd:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: etcd@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key       | Read by                                                                                                  | Help                                                     |
|-----------|----------------------------------------------------------------------------------------------------------|----------------------------------------------------------|
| ca-file   | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | PEM bundle to verify the server against                  |
| cert-file | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | client certificate, for a cluster using mTLS             |
| depth     | etcd.kv.tree                                                                                             | how many levels to expand                                |
| endpoint  | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | etcd endpoint, host\[:port\]                             |
| key-file  | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | private key for --cert-file                              |
| limit     | etcd.kv.list, etcd.kv.tree, etcd.lease.list                                                              | how many keys to return                                  |
| tls       | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | connect over TLS                                         |
| username  | etcd.kv.get, etcd.kv.list, etcd.kv.tree, etcd.lease.list, etcd.member.list, etcd.overview, etcd.snapshot | user to authenticate as, if the cluster has auth enabled |

## etcd.kv.get

The value stored at one key, with its version and lease.

**Classified write for what it discloses, not what it changes.** A Kubernetes cluster keeps its Secrets in etcd base64-encoded rather than encrypted, unless encryption at rest was turned on — so reading an arbitrary key here can be reading every secret in the cluster.

It also needs a grant naming it. That is available because this names one key: `rta grant allow etcd.kv.get /registry/services/endpoints/default/api` is a consent somebody can actually read, which a whole-namespace grant would not be.

The read tier — etcd.kv.list and etcd.kv.tree — shows names and sizes, which is usually the question and costs none of this.

| Field                | Value                                                                                                                                                                                          |
|----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | etcd.kv.get                                                                                                                                                                                    |
| summary              | What one key holds                                                                                                                                                                             |
| safety               | write                                                                                                                                                                                          |
| idempotent           | true                                                                                                                                                                                           |
| cli                  | rta etcd kv get \<key> \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool             | etcd_kv_get                                                                                                                                                                                    |
| grant required (mcp) | yes — a person must run \`rta grant allow etcd.kv.get\`                                                                                                                                        |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                             |
| input:key            | string, required — the exact key to read                                                                                                                                                       |
| input:endpoint       | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]   |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                   |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                             |
| input:cert-file      | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                      |
| input:key-file       | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                        |
| input:username       | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                           |
| input:password       | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                  |

## etcd.kv.list

Names, versions and lease bindings for every key under a prefix.

Never a value: this is the read tier, and etcd.kv.get is where contents live. No size column either, and that is the same line rather than an omission — etcd carries no length field, so the only way to report a size would be to fetch every value and decline to print it, which has already read the thing.

Bounded, and it says when it stopped. A listing that quietly ended at a thousand reads exactly like a keyspace with a thousand keys in it.

| Field           | Value                                                                                                                                                                                                                  |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | etcd.kv.list                                                                                                                                                                                                           |
| summary         | Key names under a prefix — never their contents                                                                                                                                                                        |
| safety          | read                                                                                                                                                                                                                   |
| idempotent      | true                                                                                                                                                                                                                   |
| cli             | rta etcd kv list \[prefix\] \[--limit \<int>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool        | etcd_kv_list                                                                                                                                                                                                           |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                                                     |
| input:prefix    | string, default  — only keys starting with this; empty walks the whole keyspace                                                                                                                                        |
| input:limit     | int, default 200, from config plugins.etcd.limit — how many keys to return                                                                                                                                             |
| input:endpoint  | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]                           |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                           |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                                                     |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                                              |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                                                |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                                                   |
| input:password  | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                                          |

## etcd.kv.tree

etcd keys are flat, and everything treats them as paths anyway: /registry/pods/default/... is a hierarchy that only the "/" makes visible. This reads a prefix once and draws it.

One request however deep the result goes: the levels are built here from the keys rather than fetched a level at a time, which is what makes this cheaper than the repeated listing it replaces.

Names and counts only, never a value. Same read tier as etcd.kv.list, and the reason a listing is ungated while a read is not.

| Field           | Value                                                                                                                                                                                                                                     |
|-----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | etcd.kv.tree                                                                                                                                                                                                                              |
| summary         | The shape of the keyspace in one call — names only                                                                                                                                                                                        |
| safety          | read                                                                                                                                                                                                                                      |
| idempotent      | true                                                                                                                                                                                                                                      |
| cli             | rta etcd kv tree \[prefix\] \[--depth \<int>\] \[--limit \<int>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool        | etcd_kv_tree                                                                                                                                                                                                                              |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                                                                        |
| input:prefix    | string, default  — only keys starting with this; empty walks the whole keyspace                                                                                                                                                           |
| input:depth     | int, default 4, from config plugins.etcd.depth — how many levels to expand                                                                                                                                                                |
| input:limit     | int, default 1000, from config plugins.etcd.limit — how many keys to read before stopping                                                                                                                                                 |
| input:endpoint  | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]                                              |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                                              |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                                                                        |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                                                                 |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                                                                   |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                                                                      |
| input:password  | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                                                             |

## etcd.lease.list

Every lease the cluster is holding, with its granted TTL and what remains.

Leases are how ephemeral keys die: a service that stops renewing loses its registration when the lease expires. A lease with a long TTL and no renewals is why a dead service is still in service discovery.

IDs and timings only, never the keys attached to them — the same read/write split etcd.kv.list and etcd.kv.get draw.

| Field           | Value                                                                                                                                                                                                          |
|-----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | etcd.lease.list                                                                                                                                                                                                |
| summary         | Outstanding leases and how long each has left                                                                                                                                                                  |
| safety          | read                                                                                                                                                                                                           |
| idempotent      | true                                                                                                                                                                                                           |
| cli             | rta etcd lease list \[--limit \<int>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool        | etcd_lease_list                                                                                                                                                                                                |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                                             |
| input:limit     | int, default 200, from config plugins.etcd.limit — how many leases to show                                                                                                                                     |
| input:endpoint  | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]                   |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                   |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                                             |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                                      |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                                        |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                                           |
| input:password  | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                                  |

## etcd.member.list

Member IDs, names and their client and peer URLs.

A member still learning the cluster's state has no name yet and is shown as unstarted, which is the difference between a cluster mid-join and one with a member that never came back.

| Field           | Value                                                                                                                                                                                        |
|-----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | etcd.member.list                                                                                                                                                                             |
| summary         | Who is in this cluster, and how each one is reachable                                                                                                                                        |
| safety          | read                                                                                                                                                                                         |
| idempotent      | true                                                                                                                                                                                         |
| cli             | rta etcd member list \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool        | etcd_member_list                                                                                                                                                                             |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                           |
| input:endpoint  | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\] |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                 |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                           |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                    |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                      |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                         |
| input:password  | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                |

## etcd.overview

The endpoint's own status — version, who it thinks the leader is, and how far behind its raft log is — with its storage and the member list beside it.

The storage row is the one to watch. etcd raises NOSPACE when the database file reaches its quota and then refuses every write while continuing to answer reads, which looks like a working cluster from anywhere except here. The use column is graded against that quota, and a server older than 3.6 does not report one, so the column is blank there rather than guessed.

--detail adds every member's own view, which is how a split is visible: members that disagree about who the leader is are not a cluster.

| Field           | Value                                                                                                                                                                                                  |
|-----------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | etcd.overview                                                                                                                                                                                          |
| summary         | Whether this cluster is healthy, and what it is made of                                                                                                                                                |
| safety          | read                                                                                                                                                                                                   |
| idempotent      | true                                                                                                                                                                                                   |
| cli             | rta etcd overview \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--detail\] |
| mcp-tool        | etcd_overview                                                                                                                                                                                          |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                                     |
| input:endpoint  | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]           |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                           |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                                     |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                              |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                                |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                                   |
| input:password  | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                          |
| input:detail    | bool, default false — return the full detailed view instead of the compact summary                                                                                                                     |

## etcd.snapshot

etcd's own backup: the whole keyspace at one revision, written as the file `etcdutl` restores.

**Refuses MCP outright** rather than asking for a grant, the line pg.dump and keys.backup draw — a snapshot of everything has no blast radius a grant could name. That matters more here than most places: a Kubernetes cluster keeps every object it has in etcd, and its Secrets are stored base64-encoded rather than encrypted unless somebody turned encryption at rest on, so this file is very often that cluster's secrets. An agent that needs one key asks for etcd.kv.get with a grant naming it.

**There is no `rta etcd restore`, and that is etcd rather than rta.** The v3 API streams a snapshot out and takes nothing back in — no service in the protocol carries a restore RPC. Restoring is `etcdutl snapshot restore`, which builds a data directory on disk: stop etcd, put that directory where the member's was, start it, on every member from this one file. The receipt prints that sequence rather than leaving it to be looked up on the day it is needed.

The snapshot is the connected member's own view at its own revision — a member behind the leader writes a file that is behind too — so the receipt names which member answered and where its revision was.

etcd appends a SHA256 of the database to the end of the stream, and rta hashes the bytes as they land and compares, so a transfer that stopped short is caught here rather than at restore time. Written with O_EXCL at mode 0600, never over an existing file, and a run that fails takes its partial file with it.

| Field                | Value                                                                                                                                                                                                       |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | etcd.snapshot                                                                                                                                                                                               |
| summary              | Write a point-in-time snapshot of the whole keyspace, for a person at a terminal                                                                                                                            |
| safety               | write                                                                                                                                                                                                       |
| idempotent           | false                                                                                                                                                                                                       |
| cli                  | rta etcd snapshot \[--out \<path>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] |
| mcp-tool             | none — for the person at the terminal, never an agent                                                                                                                                                       |
| grant required (mcp) | yes — a person must run \`rta grant allow etcd.snapshot\`                                                                                                                                                   |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow etcd --profile \<name>\`                                                                          |
| input:out            | path, local (never offered to MCP callers) — file to write the snapshot to; refused if it already exists                                                                                                    |
| input:endpoint       | string, default 127.0.0.1:2379, local (never offered to MCP callers), from config plugins.etcd.endpoint, filled by a profile's tunnel (the forward's address) — etcd endpoint, host\[:port\]                |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.etcd.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.etcd.ca-file — PEM bundle to verify the server against                                                                          |
| input:cert-file      | string, default , local (never offered to MCP callers), from config plugins.etcd.cert-file — client certificate, for a cluster using mTLS                                                                   |
| input:key-file       | string, default , local (never offered to MCP callers), from config plugins.etcd.key-file — private key for --cert-file                                                                                     |
| input:username       | string, default , local (never offered to MCP callers), from config plugins.etcd.username — user to authenticate as, if the cluster has auth enabled                                                        |
| input:password       | secret, local (never offered to MCP callers), from $RTA_ETCD_PASSWORD — password for the user                                                                                                               |
