# keycloak

Keycloak: users, clients, roles, flows, sessions, events — and a realm graded against named controls

## Capabilities

| Capability            | Safety | Summary                                                                                                                |
|-----------------------|--------|------------------------------------------------------------------------------------------------------------------------|
| keycloak.audit        | read   | Grade the realm: second factor, brute force, passwords, clients, tokens, events, admins — each against a named control |
| keycloak.client.list  | read   | Every client: public or confidential, which grants it may use, whether it enforces PKCE                                |
| keycloak.client.show  | read   | One client: grants, redirect URIs, origins, scopes, the settings that matter, its service account's roles              |
| keycloak.event.admin  | read   | Admin events, newest first: what was created, changed or deleted in the realm, by whom                                 |
| keycloak.event.list   | read   | Login events, newest first: logins, failures, token refreshes, who and from where                                      |
| keycloak.flow.list    | read   | The authentication flows, and which one each kind of login is bound to                                                 |
| keycloak.flow.show    | read   | One flow's steps as a tree: each authenticator and whether it is required, alternative, conditional or disabled        |
| keycloak.overview     | read   | One realm at a glance: size, protections, event logging, the flows in force                                            |
| keycloak.role.list    | read   | The realm's roles, or one client's, and which are composites                                                           |
| keycloak.session.list | read   | Who is signed in: sessions per client, or the sessions of one user or one client                                       |
| keycloak.user.list    | read   | Who exists in the realm, whether each is enabled, verified and has a second factor                                     |
| keycloak.user.show    | read   | One user: profile, credential types, effective roles, groups and sessions                                              |

## Configuration

Under `plugins: keycloak:` in rta's configuration, or in a profile's `set:`. An installed plugin's section is pinned to the artifact — `plugins: keycloak@<digest>:` — and `rta doctor` prints the exact line. The caller always wins, so a configured value is a default, never a lock.

| Key        | Read by                                                                                                                                                                                                                                             | Help                                                                       |
|------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------|
| auth-realm | keycloak.audit, keycloak.client.list, keycloak.client.show, keycloak.event.admin, keycloak.event.list, keycloak.flow.list, keycloak.flow.show, keycloak.overview, keycloak.role.list, keycloak.session.list, keycloak.user.list, keycloak.user.show | the realm the client lives in, when it is not the one being read           |
| ca-file    | keycloak.audit, keycloak.client.list, keycloak.client.show, keycloak.event.admin, keycloak.event.list, keycloak.flow.list, keycloak.flow.show, keycloak.overview, keycloak.role.list, keycloak.session.list, keycloak.user.list, keycloak.user.show | PEM bundle to verify the server against, beyond the host's own trust store |
| client-id  | keycloak.audit, keycloak.client.list, keycloak.client.show, keycloak.event.admin, keycloak.event.list, keycloak.flow.list, keycloak.flow.show, keycloak.overview, keycloak.role.list, keycloak.session.list, keycloak.user.list, keycloak.user.show | the confidential client whose service account this acts as                 |
| max        | keycloak.audit, keycloak.event.admin, keycloak.event.list, keycloak.session.list, keycloak.user.list                                                                                                                                                | how many users to examine for a second factor                              |
| realm      | keycloak.audit, keycloak.client.list, keycloak.client.show, keycloak.event.admin, keycloak.event.list, keycloak.flow.list, keycloak.flow.show, keycloak.overview, keycloak.role.list, keycloak.session.list, keycloak.user.list, keycloak.user.show | the realm to read                                                          |
| url        | keycloak.audit, keycloak.client.list, keycloak.client.show, keycloak.event.admin, keycloak.event.list, keycloak.flow.list, keycloak.flow.show, keycloak.overview, keycloak.role.list, keycloak.session.list, keycloak.user.list, keycloak.user.show | Keycloak base URL — the part before /realms                                |

## keycloak.audit

Reads the realm and grades what it finds: whether a second factor is required or merely offered and how many users have one; brute-force detection; the password policy; every client's grants, redirect URIs, origins, PKCE and service-account roles; token and session lifetimes and refresh-token rotation; whether login and admin events are recorded; SSL requirement, self-registration, the master realm and the bootstrap admin; and who holds realm-admin. Every finding cites an OWASP Top 10 category and CWE, or RFC 9700 (OAuth 2.0 Security BCP), or the Keycloak guide. Compact by default; --detail is the work list, grouped, with the references at the end.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.audit                                                                                                                                                                                                |
| summary             | Grade the realm: second factor, brute force, passwords, clients, tokens, events, admins — each against a named control                                                                                        |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak audit \[--max \<int>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\] \[--detail\]         |
| mcp-tool            | keycloak_audit                                                                                                                                                                                                |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:max           | int, default 200, from config plugins.keycloak.max — how many users to examine for a second factor                                                                                                            |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |
| input:detail        | bool, default false — return the full detailed view instead of the compact summary                                                                                                                            |

## keycloak.client.list

One row per registered client, built-in ones included: its kind (public, confidential, bearer-only), the flows enabled on it, the PKCE method it enforces, whether every role lands in its tokens (full scope), and whether it is enabled. Never a secret.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.client.list                                                                                                                                                                                          |
| summary             | Every client: public or confidential, which grants it may use, whether it enforces PKCE                                                                                                                       |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak client list \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                                 |
| mcp-tool            | keycloak_client_list                                                                                                                                                                                          |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.client.show

How one application authenticates: kind and protocol, the flows enabled, every redirect URI and web origin, default and optional scopes, the enforcement settings (PKCE, token lifespans, refresh tokens, logout), and — for a client with a service account — the roles that account holds. The secret is not here and there is no capability that shows it.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.client.show                                                                                                                                                                                          |
| summary             | One client: grants, redirect URIs, origins, scopes, the settings that matter, its service account's roles                                                                                                     |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak client show \<client> \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                       |
| mcp-tool            | keycloak_client_show                                                                                                                                                                                          |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:client        | string, required, completes — the client id, as the console shows it                                                                                                                                          |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.event.admin

The realm's administrative change log: each operation, the resource it touched, the path to it, and the account and address it came from. What an incident wants first when a client or a role appeared that nobody remembers adding. Bound by --max.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.event.admin                                                                                                                                                                                          |
| summary             | Admin events, newest first: what was created, changed or deleted in the realm, by whom                                                                                                                        |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak event admin \[--max \<int>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                |
| mcp-tool            | keycloak_event_admin                                                                                                                                                                                          |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:max           | int, default 50, from config plugins.keycloak.max — how many events to list                                                                                                                                   |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.event.list

The realm's login event log, newest first — LOGIN, LOGIN_ERROR, LOGOUT, CODE_TO_TOKEN, REFRESH_TOKEN and the rest — with the user, the client and the address each came from. Filter by --type, --user or --client; bound by --max. Empty when the realm does not record events, which `rta keycloak audit` flags.

| Field               | Value                                                                                                                                                                                                                                                          |
|---------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.event.list                                                                                                                                                                                                                                            |
| summary             | Login events, newest first: logins, failures, token refreshes, who and from where                                                                                                                                                                              |
| safety              | read                                                                                                                                                                                                                                                           |
| idempotent          | true                                                                                                                                                                                                                                                           |
| cli                 | rta keycloak event list \[--type \<string>\] \[--user \<string>\] \[--client \<string>\] \[--max \<int>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\] |
| mcp-tool            | keycloak_event_list                                                                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                                                                         |
| input:type          | string, default  — one event type, e.g. LOGIN_ERROR                                                                                                                                                                                                            |
| input:user          | string, default  — events of one user, by username or id                                                                                                                                                                                                       |
| input:client        | string, default , completes — events of one client                                                                                                                                                                                                             |
| input:max           | int, default 50, from config plugins.keycloak.max — how many events to list                                                                                                                                                                                    |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms                                                  |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                                                                           |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                                                                             |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                                                                 |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                                                                         |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                                                                      |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                                                                       |

## keycloak.flow.list

Every top-level flow, built-in or custom, with the binding that puts it in force: browser, direct grant, registration, reset credentials, client authentication. A custom flow that is bound nowhere is defined but does nothing. `keycloak flow show` opens one.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.flow.list                                                                                                                                                                                            |
| summary             | The authentication flows, and which one each kind of login is bound to                                                                                                                                        |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak flow list \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                                   |
| mcp-tool            | keycloak_flow_list                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.flow.show

The executions of one flow, nested the way the console nests them. Read it for the browser flow to answer whether a second factor is required of everyone (an OTP or WebAuthn step marked required), offered to those who set one up (a conditional sub-flow, the default), or absent.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.flow.show                                                                                                                                                                                            |
| summary             | One flow's steps as a tree: each authenticator and whether it is required, alternative, conditional or disabled                                                                                               |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak flow show \<flow> \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                           |
| mcp-tool            | keycloak_flow_show                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:flow          | string, required, completes — the flow's alias, as \`keycloak flow list\` shows it                                                                                                                            |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.overview

Whether this realm is worth talking to and what state it is in: user and client counts, brute-force detection, SSL requirement, self-registration, whether login and admin events are recorded, and the flow each login goes through. The server version when the client is allowed to see it — only a master-realm client is. --detail adds the client list, the flows and the active sessions per client. For the grade, `rta keycloak audit`.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.overview                                                                                                                                                                                             |
| summary             | One realm at a glance: size, protections, event logging, the flows in force                                                                                                                                   |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak overview \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\] \[--detail\]                       |
| mcp-tool            | keycloak_overview                                                                                                                                                                                             |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |
| input:detail        | bool, default false — return the full detailed view instead of the compact summary                                                                                                                            |

## keycloak.role.list

Realm roles by default; --client names a client whose own roles to list instead (realm-management is the one that holds every administrative role). A composite role grants others when assigned, which is what makes it worth a column.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.role.list                                                                                                                                                                                            |
| summary             | The realm's roles, or one client's, and which are composites                                                                                                                                                  |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak role list \[--client \<string>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]            |
| mcp-tool            | keycloak_role_list                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:client        | string, default , completes — list this client's roles instead of the realm's                                                                                                                                 |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.session.list

By default the realm's session counts per client, active and offline — the one-line answer to "is anyone using this". --user lists one account's open sessions with their address, start and last activity; --client lists the sessions open against one application. Sessions are described, never revoked: that is a write this plugin does not have.

| Field               | Value                                                                                                                                                                                                                                       |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.session.list                                                                                                                                                                                                                       |
| summary             | Who is signed in: sessions per client, or the sessions of one user or one client                                                                                                                                                            |
| safety              | read                                                                                                                                                                                                                                        |
| idempotent          | true                                                                                                                                                                                                                                        |
| cli                 | rta keycloak session list \[--user \<string>\] \[--client \<string>\] \[--max \<int>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\] |
| mcp-tool            | keycloak_session_list                                                                                                                                                                                                                       |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                                                      |
| input:user          | string, default  — one user's sessions, by username or id                                                                                                                                                                                   |
| input:client        | string, default , completes — the sessions open against one client                                                                                                                                                                          |
| input:max           | int, default 100, from config plugins.keycloak.max — how many sessions to list for a client                                                                                                                                                 |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms                               |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                                                        |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                                                          |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                                              |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                                                      |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                                                   |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                                                    |

## keycloak.user.list

One row per user: username, email, enabled, email verified, whether an OTP is configured, and when the account was created. Service accounts are not users here — Keycloak lists them under their client. Bounded by --max; a search narrows by username, email or name.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.user.list                                                                                                                                                                                            |
| summary             | Who exists in the realm, whether each is enabled, verified and has a second factor                                                                                                                            |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak user list \[search\] \[--max \<int>\] \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]       |
| mcp-tool            | keycloak_user_list                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:search        | string, default  — username, email, first or last name to look for; empty lists from the start                                                                                                                |
| input:max           | int, default 100, from config plugins.keycloak.max — how many users to list                                                                                                                                   |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |

## keycloak.user.show

Everything the realm knows about one account except its secrets: the profile and its required actions, which kinds of credential are set (password, otp, webauthn — types and dates, never values), the realm roles in effect once composites are expanded, client roles per client, group membership, and the sessions open right now.

| Field               | Value                                                                                                                                                                                                         |
|---------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| id                  | keycloak.user.show                                                                                                                                                                                            |
| summary             | One user: profile, credential types, effective roles, groups and sessions                                                                                                                                     |
| safety              | read                                                                                                                                                                                                          |
| idempotent          | true                                                                                                                                                                                                          |
| cli                 | rta keycloak user show \<user> \[--url \<string>\] \[--realm \<string>\] \[--auth-realm \<string>\] \[--client-id \<string>\] \[--client-secret \<secret>\] \[--ca-file \<string>\]                           |
| mcp-tool            | keycloak_user_show                                                                                                                                                                                            |
| profiles            | --profile \<name> runs this against a configured connection; over MCP that always needs \`rta grant allow keycloak --profile \<name>\`                                                                        |
| input:user          | string, required — username, or the user's id                                                                                                                                                                 |
| input:url           | string, default http://127.0.0.1:8080, local (never offered to MCP callers), from config plugins.keycloak.url, filled by a profile's tunnel (the forward's url) — Keycloak base URL — the part before /realms |
| input:realm         | string, default master, local (never offered to MCP callers), from config plugins.keycloak.realm — the realm to read                                                                                          |
| input:auth-realm    | string, default , local (never offered to MCP callers), from config plugins.keycloak.auth-realm — the realm the client lives in, when it is not the one being read                                            |
| input:client-id     | string, default rta, local (never offered to MCP callers), from config plugins.keycloak.client-id — the confidential client whose service account this acts as                                                |
| input:client-secret | secret, local (never offered to MCP callers), from $RTA_KEYCLOAK_CLIENT_SECRET — that client's secret, exchanged for a short-lived token on every call                                                        |
| input:ca-file       | string, default , local (never offered to MCP callers), from config plugins.keycloak.ca-file — PEM bundle to verify the server against, beyond the host's own trust store                                     |
| dashboard           | not on the automatic dashboard — it declines to run unasked; named in \`dashboard: tiles:\` it re-runs every few seconds                                                                                      |
