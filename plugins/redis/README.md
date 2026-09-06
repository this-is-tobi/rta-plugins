# redis

Redis: health, memory, persistence, replication, the keyspace and the slow log

## Capabilities

| Capability        | Safety | Summary                                                                          |
|-------------------|--------|----------------------------------------------------------------------------------|
| redis.client.list | read   | Who is connected, from where, and what each connection is doing                  |
| redis.cluster     | read   | The cluster as this node sees it: state, slots, and every node's role and health |
| redis.config.get  | write  | What the server is configured with                                               |
| redis.key.get     | write  | What one key holds                                                               |
| redis.key.list    | read   | Key names matching a pattern — never their contents                              |
| redis.key.tree    | read   | The shape of the keyspace in one call — names only                               |
| redis.memory      | read   | Where the memory goes, and what the server thinks about it                       |
| redis.overview    | read   | Whether this server is healthy, and what it is made of                           |
| redis.slowlog     | write  | The commands that took longest, with what they were called with                  |

## Configuration

Under `plugins: redis:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: redis@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key           | Read by                                                                                                                                        | Help                                                               |
|---------------|------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------|
| address       | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | redis address, host\[:port\]                                       |
| ca-file       | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | PEM bundle to verify the server against                            |
| cert-file     | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | client certificate, for a server using mTLS                        |
| db            | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | logical database to SELECT                                         |
| depth         | redis.key.tree                                                                                                                                 | how many levels to expand                                          |
| key-file      | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | private key for --cert-file                                        |
| limit         | redis.key.list, redis.key.tree                                                                                                                 | how many keys to return                                            |
| separator     | redis.key.tree                                                                                                                                 | the character that separates levels in key names                   |
| slowlog.count | redis.slowlog                                                                                                                                  | how many entries, newest first                                     |
| tls           | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | connect over TLS                                                   |
| username      | redis.client.list, redis.cluster, redis.config.get, redis.key.get, redis.key.list, redis.key.tree, redis.memory, redis.overview, redis.slowlog | ACL user to authenticate as (Redis 6+); empty for the default user |

## redis.client.list

CLIENT LIST as a table: address, name, age, idle time, the last command and the database — the view that answers "who is holding a thousand connections open" and "what is that client blocked on".

| Field           | Value                                                                                                                                                                                                        |
|-----------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.client.list                                                                                                                                                                                            |
| summary         | Who is connected, from where, and what each connection is doing                                                                                                                                              |
| safety          | read                                                                                                                                                                                                         |
| idempotent      | true                                                                                                                                                                                                         |
| cli             | rta redis client list \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool        | redis_client_list                                                                                                                                                                                            |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                          |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                 |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                          |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                    |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                     |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                              |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                              |

## redis.cluster

CLUSTER INFO and CLUSTER NODES from one node. A node that is not in a cluster says so rather than failing.

The state row is the one that matters: `fail` means some slot has no reachable primary and the cluster refuses writes to it, which is the outage the per-node rows below explain.

| Field           | Value                                                                                                                                                                                                    |
|-----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.cluster                                                                                                                                                                                            |
| summary         | The cluster as this node sees it: state, slots, and every node's role and health                                                                                                                         |
| safety          | read                                                                                                                                                                                                     |
| idempotent      | true                                                                                                                                                                                                     |
| cli             | rta redis cluster \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool        | redis_cluster                                                                                                                                                                                            |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                      |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]             |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                            |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                      |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                 |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                          |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                            |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                          |

## redis.config.get

CONFIG GET for a pattern, as a table. Nothing here mutates.

**Classified write for what it discloses.** `requirepass` and `masterauth` come back in clear beside everything else, and no pattern reliably excludes every credential-shaped directive across versions and modules — so the whole command is a write rather than a denylist pretending to be a wall. The two are masked on human surfaces regardless.

The overview already grades the directives that matter most — maxmemory, its policy, persistence — without this.

| Field                | Value                                                                                                                                                                                                                   |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | redis.config.get                                                                                                                                                                                                        |
| summary              | What the server is configured with                                                                                                                                                                                      |
| safety               | write                                                                                                                                                                                                                   |
| idempotent           | true                                                                                                                                                                                                                    |
| cli                  | rta redis config get \[pattern\] \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool             | redis_config_get                                                                                                                                                                                                        |
| grant required (mcp) | yes — a person must run \`rta grant allow redis.config.get\`                                                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                                     |
| input:pattern        | string, default * — glob to match directive names (maxmemory*, save, *auth*)                                                                                                                                            |
| input:address        | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                            |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                           |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                                     |
| input:cert-file      | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                               |
| input:key-file       | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                                |
| input:username       | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                         |
| input:password       | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                           |
| input:db             | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                         |

## redis.key.get

The value at one key, whatever its type: a string as itself, a hash as its fields, a list, set or sorted set as its members — bounded, and it says when it stopped.

**Classified write for what it discloses, not what it changes.** A session store keeps tokens and a cache keeps whatever the application cached, so reading an arbitrary key can be reading somebody's session. It needs a grant naming the key: `rta grant allow redis.key.get user:42:session` is a consent somebody can read.

The read tier — redis.key.list and redis.key.tree — shows names, types and TTLs, which is usually the question and costs none of this.

| Field                | Value                                                                                                                                                                                                           |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | redis.key.get                                                                                                                                                                                                   |
| summary              | What one key holds                                                                                                                                                                                              |
| safety               | write                                                                                                                                                                                                           |
| idempotent           | true                                                                                                                                                                                                            |
| cli                  | rta redis key get \<key> \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool             | redis_key_get                                                                                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow redis.key.get\`, optionally naming one key                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                             |
| input:key            | string, required — the exact key to read                                                                                                                                                                        |
| input:address        | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                    |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                   |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                             |
| input:cert-file      | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                       |
| input:key-file       | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                        |
| input:username       | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                 |
| input:password       | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                   |
| input:db             | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                 |

## redis.key.list

Names, types and time to live for every key matching a glob, walked with SCAN so the server keeps answering everybody else while it runs.

Never a value: this is the read tier, and redis.key.get is where contents live. Bounded, and it says when it stopped — a listing that quietly ended at a thousand reads exactly like a keyspace with a thousand keys in it.

| Field           | Value                                                                                                                                                                                                                                    |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.key.list                                                                                                                                                                                                                           |
| summary         | Key names matching a pattern — never their contents                                                                                                                                                                                      |
| safety          | read                                                                                                                                                                                                                                     |
| idempotent      | true                                                                                                                                                                                                                                     |
| cli             | rta redis key list \[pattern\] \[--limit \<int>\] \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool        | redis_key_list                                                                                                                                                                                                                           |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                                                      |
| input:pattern   | string, default * — glob to match key names against (user:* , *:session); * walks the whole keyspace                                                                                                                                     |
| input:limit     | int, default 200, from config plugins.redis.limit — how many keys to return                                                                                                                                                              |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                                             |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                                            |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                                                      |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                                                |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                                                 |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                                          |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                                            |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                                          |

## redis.key.tree

Redis keys are flat, and everybody names them as paths anyway: user:42:session is a hierarchy that only the separator makes visible. This walks a pattern once and draws it, with a count at every level.

Names and counts only, never a value. Same read tier as redis.key.list.

| Field           | Value                                                                                                                                                                                                                                                                                 |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.key.tree                                                                                                                                                                                                                                                                        |
| summary         | The shape of the keyspace in one call — names only                                                                                                                                                                                                                                    |
| safety          | read                                                                                                                                                                                                                                                                                  |
| idempotent      | true                                                                                                                                                                                                                                                                                  |
| cli             | rta redis key tree \[pattern\] \[--separator \<string>\] \[--depth \<int>\] \[--limit \<int>\] \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool        | redis_key_tree                                                                                                                                                                                                                                                                        |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                                                                                                   |
| input:pattern   | string, default * — glob to match key names against (user:* , *:session); * walks the whole keyspace                                                                                                                                                                                  |
| input:separator | string, default :, from config plugins.redis.separator — the character that separates levels in key names                                                                                                                                                                             |
| input:depth     | int, default 4, from config plugins.redis.depth — how many levels to expand                                                                                                                                                                                                           |
| input:limit     | int, default 1000, from config plugins.redis.limit — how many keys to walk before stopping                                                                                                                                                                                            |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                                                                                          |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                                                                                         |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                                                                                                   |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                                                                                             |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                                                                                              |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                                                                                       |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                                                                                         |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                                                                                       |

## redis.memory

MEMORY STATS as a table of where the bytes are — dataset, overhead, clients, replication buffers, fragmentation — and MEMORY DOCTOR's own diagnosis underneath, which is the server saying in words what the numbers mean.

| Field           | Value                                                                                                                                                                                                   |
|-----------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.memory                                                                                                                                                                                            |
| summary         | Where the memory goes, and what the server thinks about it                                                                                                                                              |
| safety          | read                                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                                    |
| cli             | rta redis memory \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool        | redis_memory                                                                                                                                                                                            |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                     |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]            |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                           |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                     |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                               |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                         |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                           |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                         |

## redis.overview

INFO, read once and graded: memory against maxmemory and what happens at the ceiling, when the last RDB was written and how many writes it does not cover, whether AOF is on and whether its last rewrite succeeded, the replication role and every replica's link, and each database's key count.

The memory row is the one to watch. A server at maxmemory with `noeviction` refuses every write while answering reads, which looks like a working cache from anywhere except here; one with an eviction policy quietly loses keys instead, and the evicted count is where that shows.

--detail adds the raw INFO sections, for the field this page does not show.

| Field           | Value                                                                                                                                                                                                                  |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | redis.overview                                                                                                                                                                                                         |
| summary         | Whether this server is healthy, and what it is made of                                                                                                                                                                 |
| safety          | read                                                                                                                                                                                                                   |
| idempotent      | true                                                                                                                                                                                                                   |
| cli             | rta redis overview \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] \[--detail\] |
| mcp-tool        | redis_overview                                                                                                                                                                                                         |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                                    |
| input:address   | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                           |
| input:tls       | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                          |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                                    |
| input:cert-file | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                              |
| input:key-file  | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                               |
| input:username  | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                        |
| input:password  | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                          |
| input:db        | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                        |
| input:detail    | bool, default false — return the full detailed view instead of the compact summary                                                                                                                                     |

## redis.slowlog

SLOWLOG GET: when each slow command ran, how long it took, who sent it and the command line itself. The threshold is the server's `slowlog-log-slower-than`, in microseconds; `rta redis config get slowlog*` shows it.

**Classified write for what it discloses.** An entry is the command with its arguments, and on any server where a SET was ever slow that is a stored value.

| Field                | Value                                                                                                                                                                                                                       |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | redis.slowlog                                                                                                                                                                                                               |
| summary              | The commands that took longest, with what they were called with                                                                                                                                                             |
| safety               | write                                                                                                                                                                                                                       |
| idempotent           | true                                                                                                                                                                                                                        |
| cli                  | rta redis slowlog \[--count \<int>\] \[--address \<string>\] \[--tls \<bool>\] \[--ca-file \<string>\] \[--cert-file \<string>\] \[--key-file \<string>\] \[--username \<string>\] \[--password \<secret>\] \[--db \<int>\] |
| mcp-tool             | redis_slowlog                                                                                                                                                                                                               |
| grant required (mcp) | yes — a person must run \`rta grant allow redis.slowlog\`                                                                                                                                                                   |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow redis --profile \<name>\`                                                                                         |
| input:count          | int, default 25, from config plugins.redis.slowlog.count — how many entries, newest first                                                                                                                                   |
| input:address        | string, default 127.0.0.1:6379, local (never offered to MCP callers), from config plugins.redis.address, filled by a profile's tunnel (the forward's address) — redis address, host\[:port\]                                |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.redis.tls, filled by a profile's tunnel (the forward's tls) — connect over TLS                                                               |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.redis.ca-file — PEM bundle to verify the server against                                                                                         |
| input:cert-file      | string, default , local (never offered to MCP callers), from config plugins.redis.cert-file — client certificate, for a server using mTLS                                                                                   |
| input:key-file       | string, default , local (never offered to MCP callers), from config plugins.redis.key-file — private key for --cert-file                                                                                                    |
| input:username       | string, default , local (never offered to MCP callers), from config plugins.redis.username — ACL user to authenticate as (Redis 6+); empty for the default user                                                             |
| input:password       | secret, local (never offered to MCP callers), from $RTA_REDIS_PASSWORD — password, or the ACL user's password                                                                                                               |
| input:db             | int, default 0, local (never offered to MCP callers), from config plugins.redis.db — logical database to SELECT                                                                                                             |
