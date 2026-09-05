# rta plugins

The first-party plugins for [rta](https://github.com/this-is-tobi/rta), and the index rta installs them from. Each is a separate binary you install only if you want it — none is linked into `rta`, so the ones you skip cost you nothing.

| Plugin | Service | Capabilities |
| --- | --- | --- |
| [`pg`](./plugins/pg/) | PostgreSQL | 10 |
| [`mysql`](./plugins/mysql/) | MySQL. Reaches a MariaDB server for the capabilities that connect in-process, but not for `dump`/`restore`, which pass MySQL 8's own flags to a client that has them | 9 |
| [`mariadb`](./plugins/mariadb/) | MariaDB, adding Galera cluster state, replica status, and a `dump`/`restore` pair spelled the way that client spells it | 11 |
| [`etcd`](./plugins/etcd/) | etcd v3: cluster health, members, leases, the keyspace, and a snapshot of the whole backend | 7 |
| [`qdrant`](./plugins/qdrant/) | Qdrant: collections, their configuration and index health | 7 |
| [`redis`](./plugins/redis/) | Redis: health, memory, persistence, replication, the keyspace and the slow log — over RESP spoken in-package, no client library | 9 |
| [`s3`](./plugins/s3/) | S3-compatible object storage | 14 |
| [`vault`](./plugins/vault/) | HashiCorp Vault | 16 |
| [`kube`](./plugins/kube/) | Kubernetes, through the `kubectl` you already have | 19 |
| [`cnpg`](./plugins/cnpg/) | CloudNativePG: clusters, their health, replication, backups and volumes, and asking for a backup now | 5 |
| [`docker`](./plugins/docker/) | Containers and images over the local daemon socket | 7 |

Every one draws the same line in the same place: the read tier describes the thing, and anything that returns a value somebody stored is a write. `mysql.schema` tells you a database's shape and `mysql.query` returns its rows. That is what makes read worth granting.

## Installing

rta knows this index by name:

```bash
rta plugin index add official
rta plugin search postgres
rta plugin install pg
```

Install is where claims meet evidence: rta fetches the release archive, hashes it, launches it in the same sandbox any load uses, and refuses if what it declares is not what the index said. Installing is the trust decision. [Using plugins](https://github.com/this-is-tobi/rta/blob/main/docs/40-plugins/10-plugins.md) is the whole model — trust, credential grants, confinement, upgrades.

## The layout

Each folder its purpose. `plugins/<name>/` is a plugin's module. `index/<name>.yaml` is the manifest the release pipeline generated from that plugin's released binaries, and `index/` is what rta reads when this repository is attached — never edited by hand.

## Building from source

```bash
make install              # every plugin, beside your rta
make install PLUGIN=pg    # just that one
make trust                # install, then approve the ones built here — and only those
```

A binary on your `$PATH` is not consent: rta loads a plugin by running it, so a build you made still needs `rta plugin trust <name>` (or `make trust`), and a rebuild needs it again, because trust binds to the artifact's digest.

`make index` writes an attachable index from the binaries just built — `rta plugin index add local $PWD/dist/index` — so the full install path can be rehearsed on this machine.

## How a release works

Every plugin is versioned on its own. A release is tagged `plugins/<name>/v<x.y.z>` — `plugins/pg/v0.3.0` — which is also what `go install github.com/this-is-tobi/rta-plugins/plugins/pg@v0.3.0` resolves. release-please opens one pull request for every plugin a merged commit touched; merging it cuts one release per plugin, six archives each (`darwin`, `linux`, `windows` × `amd64`, `arm64`), with SLSA provenance, an SBOM and a cosign signature on the checksums file. The pipeline then regenerates `index/<name>.yaml` from the binaries it just built and commits it — a manifest is never written by hand.

Verify a release's checksums file:

```bash
gh attestation verify checksums.txt --owner this-is-tobi
```

## Writing your own

rta's [Writing a plugin](https://github.com/this-is-tobi/rta/blob/main/docs/40-plugins/20-writing-a-plugin.md) is the guide; `rta plugin new` scaffolds one that builds and runs. These eleven are worked examples, each adding one idea, in the order worth reading them (`eol`, the smallest, is built into rta itself and is the one to read first):

| Read | For |
| --- | --- |
| [`kube`](./plugins/kube/) | Shelling out to a tool the operator already has instead of linking its client library, and why |
| [`cnpg`](./plugins/cnpg/) | One plain API read against a Custom Resource, declaring a credential need rather than assuming one, and a single write among reads |
| [`mysql`](./plugins/mysql/) | A connection: declared inputs, a secret a profile fills, an endpoint role a tunnel can fill |
| [`mariadb`](./plugins/mariadb/) | Two plugins over one service family without either becoming a fork of the other |
| [`pg`](./plugins/pg/) | Safety classes doing real work — three dumps graded by what a grant can name, two refusing MCP outright |
| [`s3`](./plugins/s3/) | Live completion from the service, and a download that refuses any key landing outside the directory you named |
| [`vault`](./plugins/vault/) | A plugin where almost everything is a secret, and what that does to every declaration |
| [`etcd`](./plugins/etcd/) · [`qdrant`](./plugins/qdrant/) | Tree views, and a plugin whose whole subject is a keyspace |
| [`redis`](./plugins/redis/) | Speaking a wire protocol in-package when the client library would triple the binary |
| [`docker`](./plugins/docker/) | A local daemon socket rather than a network endpoint |

Third-party plugins are not yet accepted into this index. Publish your own — an index is a git repository with one directory in it, and `rta plugin index add <name> <repository>` attaches it like any other.

## Development

`make ci` runs what CI runs. Every module pins a released rta; `make dev RTA_DIR=../rta` points a workspace at a checkout for an SDK edit loop, and `make dev-off` removes it. A `replace` never lands in a go.mod.

Security reports: [SECURITY.md](./SECURITY.md).
