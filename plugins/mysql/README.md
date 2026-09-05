# mysql

MySQL: connection health, schema, rows and activity

## Capabilities

| Capability          | Safety      | Summary                                                               |
|---------------------|-------------|-----------------------------------------------------------------------|
| mysql.activity      | write       | What every connected session is doing right now                       |
| mysql.database.list | read        | List databases on this server, with their sizes                       |
| mysql.dump          | write       | Back up one database to a SQL file, for a person at a terminal        |
| mysql.overview      | read        | Everything about this connection at a glance                          |
| mysql.query         | write       | Run a read-only query                                                 |
| mysql.restore       | destructive | Restore a mysql.dump file into a database, for a person at a terminal |
| mysql.schema        | read        | Describe a database's tables, columns and keys — no values            |
| mysql.status        | read        | Whether the database answers, and what it is                          |
| mysql.table.list    | read        | List tables with their row estimates and sizes                        |

## Configuration

Under `plugins: mysql:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: mysql@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key          | Read by                                                                                                                                   | Help                                                                |
|--------------|-------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------|
| database     | mysql.activity, mysql.database.list, mysql.dump, mysql.overview, mysql.query, mysql.restore, mysql.schema, mysql.status, mysql.table.list | database to select (optional — the server is reachable without one) |
| dump.include | mysql.dump                                                                                                                                | what to put in the file                                             |
| host         | mysql.activity, mysql.database.list, mysql.dump, mysql.overview, mysql.query, mysql.restore, mysql.schema, mysql.status, mysql.table.list | database host                                                       |
| limit        | mysql.activity, mysql.database.list, mysql.query, mysql.schema, mysql.table.list                                                          | how many sessions to show                                           |
| port         | mysql.activity, mysql.database.list, mysql.dump, mysql.overview, mysql.query, mysql.restore, mysql.schema, mysql.status, mysql.table.list | database port                                                       |
| tls          | mysql.activity, mysql.database.list, mysql.dump, mysql.overview, mysql.query, mysql.restore, mysql.schema, mysql.status, mysql.table.list | TLS negotiation mode                                                |
| user         | mysql.activity, mysql.database.list, mysql.dump, mysql.overview, mysql.query, mysql.restore, mysql.schema, mysql.status, mysql.table.list | user to connect as                                                  |

## mysql.activity

Classified write for what it discloses rather than what it changes: the info column carries whatever literals are in the statements currently running.

`mysql overview --detail` keeps the same rows without that column — state, time and command — which answers "is anything stuck" without handing back anything anybody stored, so the glanceable form stays in the read tier.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.activity                                                                                                                                                                                                       |
| summary        | What every connected session is doing right now                                                                                                                                                                      |
| safety         | write                                                                                                                                                                                                                |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql activity \[--limit \<int>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                              |
| mcp-tool       | mysql_activity                                                                                                                                                                                                       |
| mcp exposure   | off by default — \`rta mcp serve --allow-write mysql\`                                                                                                                                                               |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:limit    | int, default 50, from config plugins.mysql.limit — how many sessions to show                                                                                                                                         |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.database.list

Names, table counts and on-disk sizes. Sizes come from INFORMATION_SCHEMA and are what the storage engine last reported rather than a live measurement — close enough to find the big one, not close enough to bill on.

Only the databases this user may see: MySQL filters INFORMATION_SCHEMA by grant, so a short list here means a narrow grant and not an empty server.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.database.list                                                                                                                                                                                                  |
| summary        | List databases on this server, with their sizes                                                                                                                                                                      |
| safety         | read                                                                                                                                                                                                                 |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql database list \[--limit \<int>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                         |
| mcp-tool       | mysql_database_list                                                                                                                                                                                                  |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:limit    | int, default 100, from config plugins.mysql.limit — how many databases to show                                                                                                                                       |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.dump

The whole database as a SQL file you can restore. **Refuses MCP outright rather than asking for a grant** — pg.dump's line: a full dump's one authorized use is everything, and an agent that needs rows asks for mysql.query, which is bounded per call.

Runs `mysqldump` rather than reimplementing it: a restorable dump has to get character sets, triggers, routines and quoting right, and a file that will not restore is worse than no capability at all. Routines, events and triggers are included — mysqldump omits routines and events by default, which is how dumps quietly stop round-tripping. The password reaches the child through its environment, never argv; option files are ignored (--no-defaults), so an ambient ~/.my.cnf credential is never silently spent.

Consistent for what can be: --single-transaction reads every InnoDB table from one snapshot, and the receipt counts the non-transactional tables that are read live outside it rather than claiming a guarantee they cannot have. GTID state is not carried (--set-gtid-purged=OFF): this file seeds a database, not a replica — carrying it makes the restore demand SUPER and fail on any server that is not brand new.

Created with O_EXCL at 0600, never over an existing file; a failed run takes its half-written file with it. The receipt names the restore command.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.dump                                                                                                                                                                                                           |
| summary        | Back up one database to a SQL file, for a person at a terminal                                                                                                                                                       |
| safety         | write                                                                                                                                                                                                                |
| idempotent     | false                                                                                                                                                                                                                |
| cli            | rta mysql dump \[--out \<path>\] \[--include \<string>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                           |
| mcp-tool       | mysql_dump                                                                                                                                                                                                           |
| mcp exposure   | off by default — \`rta mcp serve --allow-write mysql\`                                                                                                                                                               |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:out      | path, local (never offered to MCP callers) — file to write; refused if it already exists                                                                                                                             |
| input:include  | string, default all, one of: all\|schema\|data, from config plugins.mysql.dump.include — what to put in the file                                                                                                     |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.overview

What server this is, how long it has been up, how much of its connection budget is in use, and the largest databases on it.

--detail adds what every session is doing, without the statement text — state, time and command, which answers "is anything stuck" and hands back nothing anybody stored. The statement text is mysql.activity, and it is a write for exactly that reason.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.overview                                                                                                                                                                                                       |
| summary        | Everything about this connection at a glance                                                                                                                                                                         |
| safety         | read                                                                                                                                                                                                                 |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql overview \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\] \[--detail\]                                                    |
| mcp-tool       | mysql_overview                                                                                                                                                                                                       |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |
| input:detail   | bool, default false — return the full detailed view instead of the compact summary                                                                                                                                   |

## mysql.query

Runs inside a READ ONLY transaction, so the server refuses any statement that would write. rta does not inspect the SQL and does not try to — MySQL enforces it, which is the only place the enforcement is worth trusting.

**Classified write for what it discloses, not what it changes.** It returns rows, and there is no table it may read by default because there is no table known to be safe. So it needs the write tier for this namespace, which is the operator saying once that this agent may read this database's contents; the read tier below it describes the database and hands back nothing stored in it. Where the connection is a named profile, every call in this namespace already needs a grant on top.

Over --limit rows it is refused rather than shortened: a truncated result set is a different answer wearing the right shape.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.query                                                                                                                                                                                                          |
| summary        | Run a read-only query                                                                                                                                                                                                |
| safety         | write                                                                                                                                                                                                                |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql query \<sql> \[--limit \<int>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                          |
| mcp-tool       | mysql_query                                                                                                                                                                                                          |
| mcp exposure   | off by default — \`rta mcp serve --allow-write mysql\`                                                                                                                                                               |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:sql      | string, required — the statement to run                                                                                                                                                                              |
| input:limit    | int, default 50, from config plugins.mysql.limit — how many rows to allow before refusing                                                                                                                            |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.restore

The other half of mysql.dump — the file back into a database. **Refuses MCP outright** for the dump's reason run in reverse: the dump refuses because everything would leave, and a restore is everything arriving, written into a live database — with mysqldump's own DROP TABLE statements running first. Neither direction has a blast radius a grant could name, so both belong to the person at the keyboard.

**A database already holding tables is refused.** Whether a dump drops objects first was decided when mysqldump wrote it, so there is no --clean to offer — restore into a fresh database, which stays one CREATE DATABASE away. rta does not create it: a typo'd name becoming a new database is worse than the refusal. A read-only server — a replica, usually — is refused before anything runs; restore on the primary, which is the only path that keeps the two the same database.

Stops at the first error, which is the strongest guarantee MySQL allows: DDL commits implicitly, so a failed restore cannot roll back and the receipt says so. --force — the flag that counts errors quietly and calls the survivor a restore — is never passed. LOAD DATA LOCAL is disabled, so a dump file cannot direct the client to read this machine's own files into the server.

| Field                | Value                                                                                                                                                                                                                |
|----------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | mysql.restore                                                                                                                                                                                                        |
| summary              | Restore a mysql.dump file into a database, for a person at a terminal                                                                                                                                                |
| safety               | destructive                                                                                                                                                                                                          |
| idempotent           | false                                                                                                                                                                                                                |
| cli                  | rta mysql restore \<file> \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                                          |
| mcp-tool             | mysql_restore                                                                                                                                                                                                        |
| mcp exposure         | off by default — \`rta mcp serve --allow-destructive mysql.restore\`                                                                                                                                                 |
| grant required (mcp) | yes — a person must run \`rta grant allow mysql.restore\`                                                                                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:file           | path, required, local (never offered to MCP callers) — the dump to restore — what mysql.dump wrote                                                                                                                   |
| input:host           | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port           | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user           | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database       | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls            | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password       | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.schema

The shape of a database as a tree: every table, its columns with their types and nullability, and which of them are keys.

Names and types only, never a value. That is what keeps it in the read tier — an agent that can describe a database still cannot read one row of it, and mysql.query is where rows live.

Name one table to expand only that one, which is also how to see a database too large to draw whole.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.schema                                                                                                                                                                                                         |
| summary        | Describe a database's tables, columns and keys — no values                                                                                                                                                           |
| safety         | read                                                                                                                                                                                                                 |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql schema \[schema\] \[--table \<string>\] \[--limit \<int>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]               |
| mcp-tool       | mysql_schema                                                                                                                                                                                                         |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:schema   | string, default , completes — database to describe (defaults to the connected one)                                                                                                                                   |
| input:table    | string, default , completes — expand only this table                                                                                                                                                                 |
| input:limit    | int, default 100, from config plugins.mysql.limit — how many tables to expand                                                                                                                                        |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.status

The cheapest possible call: connect, ask the server what it is, disconnect. Useful on its own as a reachability check, and it is the call whose failure carries the classified hint for every connection problem the others would hit later.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.status                                                                                                                                                                                                         |
| summary        | Whether the database answers, and what it is                                                                                                                                                                         |
| safety         | read                                                                                                                                                                                                                 |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql status \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                                                   |
| mcp-tool       | mysql_status                                                                                                                                                                                                         |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |

## mysql.table.list

Names, engines, row estimates and on-disk sizes for one database.

The row counts are estimates the storage engine keeps, not COUNT(*). InnoDB's can be off by a wide margin on a busy table — they are for finding the big one, and a number that has to be right needs mysql.query.

| Field          | Value                                                                                                                                                                                                                |
|----------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id             | mysql.table.list                                                                                                                                                                                                     |
| summary        | List tables with their row estimates and sizes                                                                                                                                                                       |
| safety         | read                                                                                                                                                                                                                 |
| idempotent     | true                                                                                                                                                                                                                 |
| cli            | rta mysql table list \[schema\] \[--limit \<int>\] \[--host \<string>\] \[--port \<int>\] \[--user \<string>\] \[--database \<string>\] \[--tls \<string>\] \[--password \<secret>\]                                 |
| mcp-tool       | mysql_table_list                                                                                                                                                                                                     |
| profiles       | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow mysql --profile \<name>\`                                                                                  |
| input:schema   | string, default , completes — database to describe (defaults to the connected one)                                                                                                                                   |
| input:limit    | int, default 200, from config plugins.mysql.limit — how many tables to show                                                                                                                                          |
| input:host     | string, default localhost, local (never offered to MCP callers), from config plugins.mysql.host, filled by a profile's tunnel (the forward's host) — database host                                                   |
| input:port     | int, default 3306, local (never offered to MCP callers), from config plugins.mysql.port, filled by a profile's tunnel (the forward's port) — database port                                                           |
| input:user     | string, default root, local (never offered to MCP callers), from config plugins.mysql.user — user to connect as                                                                                                      |
| input:database | string, default , local (never offered to MCP callers), from config plugins.mysql.database — database to select (optional — the server is reachable without one)                                                     |
| input:tls      | string, default preferred, one of: false\|preferred\|true\|skip-verify, local (never offered to MCP callers), from config plugins.mysql.tls, filled by a profile's tunnel (the forward's tls) — TLS negotiation mode |
| input:password | secret, local (never offered to MCP callers), from $RTA_MYSQL_PASSWORD — password for the user                                                                                                                       |
