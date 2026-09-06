# vault

HashiCorp Vault: secrets, tokens, leases, policies and transit encryption

## Capabilities

| Capability            | Safety      | Summary                                                                |
|-----------------------|-------------|------------------------------------------------------------------------|
| vault.kv.get          | write       | Reveal a secret's current version                                      |
| vault.kv.list         | read        | List secret names at a path — never values                             |
| vault.kv.set          | write       | Set (or overwrite) a secret                                            |
| vault.kv.tree         | read        | The whole shape of a KV mount in one call — names only                 |
| vault.lease.show      | read        | A lease's TTL and renewability                                         |
| vault.overview        | read        | Seal state, the current token and the policy list at a glance          |
| vault.policy.get      | read        | One policy's own rules, as HCL                                         |
| vault.policy.list     | read        | Every ACL policy defined on this Vault                                 |
| vault.restore         | destructive | Restore a vault.snapshot file into a Vault, for a person at a terminal |
| vault.seal.status     | read        | Whether Vault is initialized, sealed, and what it takes to unseal it   |
| vault.snapshot        | write       | Write a raft storage snapshot, for a person at a terminal              |
| vault.token.status    | read        | What the current token can do, and when it expires                     |
| vault.transit.decrypt | write       | Decrypt ciphertext back to its plaintext                               |
| vault.transit.encrypt | write       | Encrypt caller-supplied plaintext with a Vault-managed key             |
| vault.wrap.get        | write       | Unwrap a single-use token, once                                        |
| vault.wrap.set        | write       | Wrap data into a single-use, TTL'd token                               |

## Configuration

Under `plugins: vault:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: vault@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key           | Read by                                                                                                                                                                                                                                                                             | Help                                                                       |
|---------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------|
| address       | vault.kv.get, vault.kv.list, vault.kv.set, vault.kv.tree, vault.lease.show, vault.overview, vault.policy.get, vault.policy.list, vault.restore, vault.seal.status, vault.snapshot, vault.token.status, vault.transit.decrypt, vault.transit.encrypt, vault.wrap.get, vault.wrap.set | Vault server address                                                       |
| ca-file       | vault.kv.get, vault.kv.list, vault.kv.set, vault.kv.tree, vault.lease.show, vault.overview, vault.policy.get, vault.policy.list, vault.restore, vault.seal.status, vault.snapshot, vault.token.status, vault.transit.decrypt, vault.transit.encrypt, vault.wrap.get, vault.wrap.set | PEM bundle to verify the server against, beyond the host's own trust store |
| depth         | vault.kv.tree                                                                                                                                                                                                                                                                       | how many levels to expand                                                  |
| kv-mount      | vault.kv.get, vault.kv.list, vault.kv.set, vault.kv.tree                                                                                                                                                                                                                            | the KV v2 secrets engine's mount path                                      |
| namespace     | vault.kv.get, vault.kv.list, vault.kv.set, vault.kv.tree, vault.lease.show, vault.overview, vault.policy.get, vault.policy.list, vault.restore, vault.seal.status, vault.snapshot, vault.token.status, vault.transit.decrypt, vault.transit.encrypt, vault.wrap.get, vault.wrap.set | Vault Enterprise namespace — empty for OSS or the root namespace           |
| transit-mount | vault.transit.decrypt, vault.transit.encrypt                                                                                                                                                                                                                                        | the transit secrets engine's mount path                                    |
| wrap.ttl      | vault.wrap.set                                                                                                                                                                                                                                                                      | how long the token stays valid, unread — not the data's own lifetime       |

## vault.kv.get

Write, the same as builtin/kv's kv.get, for the same reason: revealing a secret's plaintext has blast radius even though nothing here is modified. A deleted (but not destroyed) version reports which, rather than an empty secret that looks the same as one that was never there.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.kv.get                                                                                                                                                                            |
| summary              | Reveal a secret's current version                                                                                                                                                       |
| safety               | write                                                                                                                                                                                   |
| idempotent           | true                                                                                                                                                                                    |
| cli                  | rta vault kv get \[--mount \<string>\] \<path> \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                          |
| mcp-tool             | vault_kv_get                                                                                                                                                                            |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.kv.get\`, optionally naming one path                                                                                                    |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount          | string, default secret, completes, from config plugins.vault.kv-mount — the KV v2 secrets engine's mount path                                                                           |
| input:path           | string, required, completes — the secret's path within the mount                                                                                                                        |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.kv.list

The structured equivalent of `vault kv list`: names only, the same Read/Write split builtin/kv's kv.list and kv.get already draw. A name ending in "/" is itself a path, one level further to list.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.kv.list                                                                                                                                                                           |
| summary         | List secret names at a path — never values                                                                                                                                              |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault kv list \[--mount \<string>\] \[path\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                        |
| mcp-tool        | vault_kv_list                                                                                                                                                                           |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount     | string, default secret, completes, from config plugins.vault.kv-mount — the KV v2 secrets engine's mount path                                                                           |
| input:path      | string, default , completes — list under this path; empty lists the mount's root                                                                                                        |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.kv.set

The same overwrite risk builtin/kv's kv.set carries, needing the same grant, and the same name: this always creates a brand new version rather than merging into the current one — vault.kv.list and vault.kv.get already show what is there before this replaces it.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.kv.set                                                                                                                                                                            |
| summary              | Set (or overwrite) a secret                                                                                                                                                             |
| safety               | write                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault kv set \[--mount \<string>\] \<path> \[--data \<secretSlice>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                |
| mcp-tool             | vault_kv_set                                                                                                                                                                            |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.kv.set\`, optionally naming one path                                                                                                    |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount          | string, default secret, completes, from config plugins.vault.kv-mount — the KV v2 secrets engine's mount path                                                                           |
| input:path           | string, required, completes — the secret's path within the mount                                                                                                                        |
| input:data           | secretSlice, required — key=value, repeated for more than one field                                                                                                                     |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.kv.tree

`vault kv list` answers one level at a time, and a name ending in "/" is another path to list — so learning where anything lives in somebody else's Vault means retyping the path over and over. This walks it once and draws the shape.

Names only, never values: the same Read/Write split vault.kv.list and vault.kv.get already draw, and the reason a listing is ungated while a read is not.

Bounded in both directions, and it says when it stopped. A folder the token may not list is marked and stepped over rather than ending the walk — a policy that grants part of a mount is the normal case, and the part you can see is still the answer you came for.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.kv.tree                                                                                                                                                                           |
| summary         | The whole shape of a KV mount in one call — names only                                                                                                                                  |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault kv tree \[--mount \<string>\] \[path\] \[--depth \<int>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                     |
| mcp-tool        | vault_kv_tree                                                                                                                                                                           |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount     | string, default secret, completes, from config plugins.vault.kv-mount — the KV v2 secrets engine's mount path                                                                           |
| input:path      | string, default , completes — start here; empty walks the whole mount                                                                                                                   |
| input:depth     | int, default 4, from config plugins.vault.depth — how many levels to expand                                                                                                             |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.lease.show

The structured equivalent of `vault lease lookup`: when a leased secret (a database credential, an issued certificate) expires and whether it can be renewed — never the secret the lease was issued for.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.lease.show                                                                                                                                                                        |
| summary         | A lease's TTL and renewability                                                                                                                                                          |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault lease show \<id> \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                              |
| mcp-tool        | vault_lease_show                                                                                                                                                                        |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:id        | string, required — the lease ID, as \`vault lease lookup\` or a secret's own LeaseID reports it                                                                                         |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.overview

Whether this Vault is worth talking to at all, and what the configured token can do — never a secret value. --detail adds the full policy list to the same page.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.overview                                                                                                                                                                          |
| summary         | Seal state, the current token and the policy list at a glance                                                                                                                           |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault overview \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\] \[--detail\]                                                         |
| mcp-tool        | vault_overview                                                                                                                                                                          |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |
| input:detail    | bool, default false — return the full detailed view instead of the compact summary                                                                                                      |

## vault.policy.get

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.policy.get                                                                                                                                                                        |
| summary         | One policy's own rules, as HCL                                                                                                                                                          |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault policy get \<name> \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                            |
| mcp-tool        | vault_policy_get                                                                                                                                                                        |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:name      | string, required, completes — the policy's name, as vault.policy.list shows it                                                                                                          |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.policy.list

Names, not rules — vault.policy.get shows one policy's own document. A policy names paths and capabilities, not secret values, so listing and reading policy documents stays Read the way builtin/kv's kv.recipients (public keys, not secrets) does.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.policy.list                                                                                                                                                                       |
| summary         | Every ACL policy defined on this Vault                                                                                                                                                  |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault policy list \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                                   |
| mcp-tool        | vault_policy_list                                                                                                                                                                       |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.restore

The other half of vault.snapshot — the file back into a Vault. **Refuses MCP outright** for the snapshot's reason run in reverse: the snapshot refuses because everything would leave, and a restore is everything arriving — the whole storage replaced, mounts, policies, tokens and leases included. Neither direction has a blast radius a grant could name, so both belong to the person at the keyboard.

**The restore replaces the auth state too**: tokens and leases become the snapshot's, so the token that authorized the restore may stop existing the moment it succeeds. The receipt says so, and the read-back afterwards uses seal-status — the endpoint that answers without a token.

A snapshot from a different cluster is refused by Vault itself unless --force skips the identity check — after which the Vault can only be unsealed with the source cluster's unseal keys or KMS. The refusal names the flag and that consequence together, because the flag without the keys bricks the Vault. Needs raft (integrated) storage, like the snapshot it restores.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.restore                                                                                                                                                                           |
| summary              | Restore a vault.snapshot file into a Vault, for a person at a terminal                                                                                                                  |
| safety               | destructive                                                                                                                                                                             |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault restore \<file> \[--force \<bool>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                           |
| mcp-tool             | none — for the person at the terminal, never an agent                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.restore\`                                                                                                                               |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:file           | path, required, local (never offered to MCP callers) — the snapshot to restore — what vault.snapshot wrote                                                                              |
| input:force          | bool — restore a snapshot from a different cluster (skips Vault's identity check; unsealing afterwards needs the source cluster's unseal keys or KMS)                                   |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.seal.status

The same shape builtin/kv's kv.status has, for the same reason: where the vault is and what it would take to open it, without opening it or touching a single secret. Answerable before authentication even matters — a sealed Vault refuses every token, including a valid one, so this is the first thing worth checking when anything else here fails.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.seal.status                                                                                                                                                                       |
| summary         | Whether Vault is initialized, sealed, and what it takes to unseal it                                                                                                                    |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault seal status \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                                   |
| mcp-tool        | vault_seal_status                                                                                                                                                                       |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.snapshot

Vault's own backup: the whole storage, as Vault stores it. **Refuses MCP outright** rather than asking for a grant, the line keys.backup and kv.copy draw — a snapshot of everything has no blast radius a grant could name, and an agent that needs a secret asks for vault.kv.get with a grant naming that path.

A snapshot rather than a KV export, and the difference is whether it restores. A Vault is not its secrets: it is mounts, auth methods, policies, entities, leases and transit keys that nothing can re-derive. Walking the KV tree and writing what comes back produces a file that looks like a backup and restores none of that.

It is also the safer artifact. What lands on disk is still sealed — restoring needs the same unseal keys or the same KMS — where an export would be every secret in the clear. Needs raft (integrated) storage and a token allowed to read sys/storage/raft/snapshot; with any other storage backend Vault has no such endpoint and this says so. Written with O_EXCL at mode 0600, never over an existing file, and a run that fails takes its partial file with it.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.snapshot                                                                                                                                                                          |
| summary              | Write a raft storage snapshot, for a person at a terminal                                                                                                                               |
| safety               | write                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault snapshot \[--out \<path>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                    |
| mcp-tool             | none — for the person at the terminal, never an agent                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.snapshot\`                                                                                                                              |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:out            | path, local (never offered to MCP callers) — file to write the snapshot to; refused if it already exists                                                                                |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.token.status

Always the caller's own token (Vault's own `auth/token/lookup-self`) — looking up an arbitrary other token by value would need that value as an input, which is a second credential to handle for a question this plugin does not otherwise need to answer. Metadata only: policies, TTL, renewability — never a value this token could itself go on to reveal.

| Field           | Value                                                                                                                                                                                   |
|-----------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id              | vault.token.status                                                                                                                                                                      |
| summary         | What the current token can do, and when it expires                                                                                                                                      |
| safety          | read                                                                                                                                                                                    |
| idempotent      | true                                                                                                                                                                                    |
| cli             | rta vault token status \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                                  |
| mcp-tool        | vault_token_status                                                                                                                                                                      |
| profiles        | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:address   | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token     | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file   | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.transit.decrypt

The reveal half of transit: whoever holds the ciphertext gets the plaintext back, which is exactly vault.kv.get's blast radius against a different store — same Write+NeedsGrant answer.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.transit.decrypt                                                                                                                                                                   |
| summary              | Decrypt ciphertext back to its plaintext                                                                                                                                                |
| safety               | write                                                                                                                                                                                   |
| idempotent           | true                                                                                                                                                                                    |
| cli                  | rta vault transit decrypt \[--mount \<string>\] \<key> \[--ciphertext \<text>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]         |
| mcp-tool             | vault_transit_decrypt                                                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.transit.decrypt\`, optionally naming one key                                                                                            |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount          | string, default transit, completes, from config plugins.vault.transit-mount — the transit secrets engine's mount path                                                                   |
| input:key            | string, required, completes — the transit key's name                                                                                                                                    |
| input:ciphertext     | text, required — the vault:v#:... ciphertext to decrypt                                                                                                                                 |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.transit.encrypt

Write, not Read+NeedsGrant like vault.kv.get: nothing here is revealed to the caller that they did not already hand over — the plaintext is theirs, only the ciphertext comes back — but the key material is materially used to produce it, which is what keeps this off Read (the same distinction gpg.sign's design draws for a signature: no key exposure, real use). The key itself never leaves Vault; that is the whole point of the transit engine.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.transit.encrypt                                                                                                                                                                   |
| summary              | Encrypt caller-supplied plaintext with a Vault-managed key                                                                                                                              |
| safety               | write                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault transit encrypt \[--mount \<string>\] \<key> \[--plaintext \<secret>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]        |
| mcp-tool             | vault_transit_encrypt                                                                                                                                                                   |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.transit.encrypt\`                                                                                                                       |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:mount          | string, default transit, completes, from config plugins.vault.transit-mount — the transit secrets engine's mount path                                                                   |
| input:key            | string, required, completes — the transit key's name                                                                                                                                    |
| input:plaintext      | secret, required — the value to encrypt                                                                                                                                                 |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.wrap.get

Consumes the token: a second call against the same token gets Vault's own "wrapping token is not valid or does not exist" refusal, from Vault itself rather than anything this plugin tracks. --dry-run must not spend the one read this token has, so it calls sys/wrapping/lookup instead — creation time, path and TTL, without the payload and without consuming anything.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.wrap.get                                                                                                                                                                          |
| summary              | Unwrap a single-use token, once                                                                                                                                                         |
| safety               | write                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault wrap get \<wrapping-token> \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                                                    |
| mcp-tool             | vault_wrap_get                                                                                                                                                                          |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.wrap.get\`                                                                                                                              |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:wrapping-token | secret, required — the wrapping token vault.wrap.set returned — a different token from --token, this plugin's own auth credential                                                       |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |

## vault.wrap.set

The recipient calls vault.wrap.get with the token this returns, once — Vault destroys the cubbyhole the instant it is read, by anyone, so a second read (an eavesdropper, a retry) gets nothing. No Scope: this wraps whatever data it is handed rather than acting on a record that already exists, so there is nothing to name one grant against the way vault.kv.get names a path.

| Field                | Value                                                                                                                                                                                   |
|----------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                   | vault.wrap.set                                                                                                                                                                          |
| summary              | Wrap data into a single-use, TTL'd token                                                                                                                                                |
| safety               | write                                                                                                                                                                                   |
| idempotent           | false                                                                                                                                                                                   |
| cli                  | rta vault wrap set \[--data \<secretSlice>\] \[--ttl \<string>\] \[--address \<string>\] \[--namespace \<string>\] \[--token \<secret>\] \[--ca-file \<string>\]                        |
| mcp-tool             | vault_wrap_set                                                                                                                                                                          |
| grant required (mcp) | yes — a person must run \`rta grant allow vault.wrap.set\`                                                                                                                              |
| profiles             | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow vault --profile \<name>\`                                                     |
| input:data           | secretSlice, required — key=value, repeated for more than one field                                                                                                                     |
| input:ttl            | string, default 5m, completes, from config plugins.vault.wrap.ttl — how long the token stays valid, unread — not the data's own lifetime                                                |
| input:address        | string, default http://127.0.0.1:8200, local (never offered to MCP callers), from config plugins.vault.address, filled by a profile's tunnel (the forward's url) — Vault server address |
| input:namespace      | string, default , local (never offered to MCP callers), from config plugins.vault.namespace — Vault Enterprise namespace — empty for OSS or the root namespace                          |
| input:token          | secret, local (never offered to MCP callers), from $RTA_VAULT_TOKEN — Vault token                                                                                                       |
| input:ca-file        | string, default , local (never offered to MCP callers), from config plugins.vault.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                  |
