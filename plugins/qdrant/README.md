# qdrant

Qdrant: collections, their configuration and index health

## Capabilities

| Capability             | Safety      | Summary                                                                      |
|------------------------|-------------|------------------------------------------------------------------------------|
| qdrant.collection.list | read        | Every collection, with its size and index status                             |
| qdrant.collection.show | read        | How one collection is configured, and whether its index is built             |
| qdrant.dump            | write       | Back up one collection to a snapshot file, for a person at a terminal        |
| qdrant.overview        | read        | What this instance is and what it holds                                      |
| qdrant.points.count    | read        | How many points a collection holds, exactly                                  |
| qdrant.points.scroll   | write       | Read points out of a collection                                              |
| qdrant.restore         | destructive | Restore a qdrant.dump snapshot into a collection, for a person at a terminal |

## Configuration

Under `plugins: qdrant:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: qdrant@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key         | Read by                                                                                                                                 | Help                                                                       |
|-------------|-----------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------|
| ca-file     | qdrant.collection.list, qdrant.collection.show, qdrant.dump, qdrant.overview, qdrant.points.count, qdrant.points.scroll, qdrant.restore | PEM bundle to verify the server against, beyond the host's own trust store |
| collection  | qdrant.collection.show, qdrant.dump, qdrant.points.count, qdrant.points.scroll, qdrant.restore                                          | collection to describe                                                     |
| count.exact | qdrant.points.count                                                                                                                     | scan for an exact count rather than taking the estimate                    |
| endpoint    | qdrant.collection.list, qdrant.collection.show, qdrant.dump, qdrant.overview, qdrant.points.count, qdrant.points.scroll, qdrant.restore | Qdrant REST endpoint, host\[:port\]                                        |
| limit       | qdrant.points.scroll                                                                                                                    | how many points to return                                                  |
| tls         | qdrant.collection.list, qdrant.collection.show, qdrant.dump, qdrant.overview, qdrant.points.count, qdrant.points.scroll, qdrant.restore | use HTTPS (a local Qdrant ordinarily does not)                             |

## qdrant.collection.list

Names, point counts, how many of those vectors are actually indexed, and each collection's status.

Indexed against total is the number worth watching: a collection still building its index answers searches from what it has, so the gap between those two columns is how incomplete the answers currently are.

Names and counts only, never a point.

| Field          | Value                                                                                                                                                                                                 |
|----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | qdrant.collection.list                                                                                                                                                                                |
| summary        | Every collection, with its size and index status                                                                                                                                                      |
| safety         | read                                                                                                                                                                                                  |
| idempotent     | true                                                                                                                                                                                                  |
| cli            | rta qdrant collection list \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\]                                                                                 |
| mcp-tool       | qdrant_collection_list                                                                                                                                                                                |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:endpoint | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls      | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key  | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file  | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |

## qdrant.collection.show

Vector dimensions and distance metric, sharding and replication, payload storage, and index progress.

The dimension and the distance metric are the two that make a collection incompatible with a model: embedding with something that produces a different dimension fails loudly, and embedding with a model trained for a different metric fails silently, returning plausible and wrong neighbours.

Configuration only, never a point — this describes the shape of the data and returns none of it.

| Field            | Value                                                                                                                                                                                                 |
|------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id               | qdrant.collection.show                                                                                                                                                                                |
| summary          | How one collection is configured, and whether its index is built                                                                                                                                      |
| safety           | read                                                                                                                                                                                                  |
| idempotent       | true                                                                                                                                                                                                  |
| cli              | rta qdrant collection show \[--collection \<string>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\]                                                      |
| mcp-tool         | qdrant_collection_show                                                                                                                                                                                |
| profiles         | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:collection | string, required, completes, from config plugins.qdrant.collection — collection to describe                                                                                                           |
| input:endpoint   | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls        | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key    | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file    | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |

## qdrant.dump

One collection as a file you can restore. **Refuses MCP outright rather than asking for a grant**, the same line pg.dump and keys.backup draw: a snapshot is every payload and every raw vector in the collection, and a vector is a reversible-enough encoding of its source text. An agent that needs points asks for qdrant.points.scroll and a person names the collection in the grant.

Uses Qdrant's own snapshot API rather than scrolling points out: the snapshot is the server's restorable artifact — segments, indexes, payload schema, collection config — and a point-by-point export would restore into a collection that answers differently. The server writes the snapshot, rta streams it down, and the server-side copy is deleted so backups do not silently fill the server's own disk.

Created with O_EXCL at 0600, so an existing file is never written over; a failed run takes its half-written file with it. The receipt says the file is unencrypted, names the restore command, and reports what the collection held when the snapshot was taken.

| Field            | Value                                                                                                                                                                                                 |
|------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id               | qdrant.dump                                                                                                                                                                                           |
| summary          | Back up one collection to a snapshot file, for a person at a terminal                                                                                                                                 |
| safety           | write                                                                                                                                                                                                 |
| idempotent       | false                                                                                                                                                                                                 |
| cli              | rta qdrant dump \[--collection \<string>\] \[--out \<path>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\]                                               |
| mcp-tool         | qdrant_dump                                                                                                                                                                                           |
| mcp exposure     | off by default — \`rta mcp serve --allow-write qdrant\`                                                                                                                                               |
| profiles         | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:collection | string, required, completes, from config plugins.qdrant.collection — collection to dump                                                                                                               |
| input:out        | path, local (never offered to MCP callers) — file to write; refused if it already exists                                                                                                              |
| input:endpoint   | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls        | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key    | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file    | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |

## qdrant.overview

Version, reachability and every collection with its point count and status.

The status column is the one to read. A collection in `yellow` is serving searches from a partly-built index, so its results are quietly incomplete rather than absent — which looks like a working search returning slightly wrong answers.

Describes collections and returns no point. Reading points is qdrant.points.scroll, and it is a write.

| Field          | Value                                                                                                                                                                                                 |
|----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | qdrant.overview                                                                                                                                                                                       |
| summary        | What this instance is and what it holds                                                                                                                                                               |
| safety         | read                                                                                                                                                                                                  |
| idempotent     | true                                                                                                                                                                                                  |
| cli            | rta qdrant overview \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\] \[--detail\]                                                                           |
| mcp-tool       | qdrant_overview                                                                                                                                                                                       |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:endpoint | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls      | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key  | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file  | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |
| input:detail   | bool, default false — return the full detailed view instead of the compact summary                                                                                                                    |

## qdrant.points.count

An exact count, which is the difference between this and the estimate qdrant.collection.list reports.

Exact costs a scan on a large collection, and that is the trade being made rather than a detail: the listing's number is what the segments last reported and can be stale after a bulk load, so this is the one to use when the number has to be right.

A number, never a point. This is the read tier — it says how much is there and nothing about what it is.

| Field            | Value                                                                                                                                                                                                 |
|------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id               | qdrant.points.count                                                                                                                                                                                   |
| summary          | How many points a collection holds, exactly                                                                                                                                                           |
| safety           | read                                                                                                                                                                                                  |
| idempotent       | true                                                                                                                                                                                                  |
| cli              | rta qdrant points count \[--collection \<string>\] \[--exact \<bool>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\]                                     |
| mcp-tool         | qdrant_points_count                                                                                                                                                                                   |
| profiles         | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:collection | string, required, completes, from config plugins.qdrant.collection — collection to count                                                                                                              |
| input:exact      | bool, default true, from config plugins.qdrant.count.exact — scan for an exact count rather than taking the estimate                                                                                  |
| input:endpoint   | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls        | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key    | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file    | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |

## qdrant.points.scroll

Points from one collection, with their payloads.

**Classified write for what it discloses, not what it changes.** The payloads are whatever was indexed — for most deployments, chunks of documents.

**Vectors are off by default even here.** An embedding is not a hash: it is a lossy but reversible-enough encoding, and inversion attacks recover substantial parts of the source text from embeddings alone. So --vectors is a second, separate decision rather than something that rides along with the payload.

It also needs a grant naming it, which is available because this names one collection: `rta grant allow qdrant.points.scroll support-tickets` is a consent somebody can read.

The read tier — qdrant.collection.show and qdrant.points.count — describes a collection and counts it, which is usually the question and costs none of this.

| Field                | Value                                                                                                                                                                                                          |
|----------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | qdrant.points.scroll                                                                                                                                                                                           |
| summary              | Read points out of a collection                                                                                                                                                                                |
| safety               | write                                                                                                                                                                                                          |
| idempotent           | true                                                                                                                                                                                                           |
| cli                  | rta qdrant points scroll \[--collection \<string>\] \[--limit \<int>\] \[--offset \<string>\] \[--vectors \<bool>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\] |
| mcp-tool             | qdrant_points_scroll                                                                                                                                                                                           |
| mcp exposure         | off by default — \`rta mcp serve --allow-write qdrant\`                                                                                                                                                        |
| grant required (mcp) | yes — a person must run \`rta grant allow qdrant.points.scroll\`                                                                                                                                               |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                           |
| input:collection     | string, required, completes, from config plugins.qdrant.collection — collection to read from                                                                                                                   |
| input:limit          | int, default 10, from config plugins.qdrant.limit — how many points to return                                                                                                                                  |
| input:offset         | string, default  — continue from the id the last page ended at                                                                                                                                                 |
| input:vectors        | bool, default false — include the raw vectors — a second decision, see the description                                                                                                                         |
| input:endpoint       | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\]          |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)                   |
| input:api-key        | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                            |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                        |

## qdrant.restore

The other half of qdrant.dump — the file back into a collection. **Refuses MCP outright** for the dump's reason run in reverse: the dump refuses because everything would leave, and a restore is everything arriving, becoming the collection wholesale. Neither direction has a blast radius a grant could name, so both belong to the person at the keyboard.

**A collection already holding points is refused unless --replace says that is the point**, which is the dump's no-overwrite rule pointing the other way. The collection named here does not have to be the one the snapshot came from — restoring into a fresh name is how you inspect a backup without touching the original.

Recovery is the server's own: the snapshot carries the collection's config and indexes, and priority=snapshot makes the file the authority — without it a distributed deployment prefers what its replicas already hold, which is a restore that reports success and restores nothing. The receipt reports what the collection holds afterwards, read back rather than assumed.

| Field                | Value                                                                                                                                                                                                 |
|----------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | qdrant.restore                                                                                                                                                                                        |
| summary              | Restore a qdrant.dump snapshot into a collection, for a person at a terminal                                                                                                                          |
| safety               | destructive                                                                                                                                                                                           |
| idempotent           | false                                                                                                                                                                                                 |
| cli                  | rta qdrant restore \[--collection \<string>\] \<file> \[--replace \<bool>\] \[--endpoint \<string>\] \[--tls \<bool>\] \[--api-key \<secret>\] \[--ca-file \<string>\]                                |
| mcp-tool             | qdrant_restore                                                                                                                                                                                        |
| mcp exposure         | off by default — \`rta mcp serve --allow-destructive qdrant.restore\`                                                                                                                                 |
| grant required (mcp) | yes — a person must run \`rta grant allow qdrant.restore\`                                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow qdrant --profile \<name>\`                                                                  |
| input:collection     | string, required, completes, from config plugins.qdrant.collection — collection to restore into (created if missing)                                                                                  |
| input:file           | path, required, local (never offered to MCP callers) — the snapshot to restore — what qdrant.dump wrote                                                                                               |
| input:replace        | bool — hand a collection that already holds points over to the snapshot                                                                                                                               |
| input:endpoint       | string, default 127.0.0.1:6333, local (never offered to MCP callers), from config plugins.qdrant.endpoint, filled by a profile's tunnel (the forward's address) — Qdrant REST endpoint, host\[:port\] |
| input:tls            | bool, default false, local (never offered to MCP callers), from config plugins.qdrant.tls, filled by a profile's tunnel (the forward's tls) — use HTTPS (a local Qdrant ordinarily does not)          |
| input:api-key        | secret, local (never offered to MCP callers), from $RTA_QDRANT_API_KEY — API key, for an instance that requires one                                                                                   |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.qdrant.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                               |
