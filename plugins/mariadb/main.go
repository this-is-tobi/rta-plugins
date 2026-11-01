// Command rta-plugin-mariadb talks to a MariaDB server: what it is, what is
// in it, what shape that has, what is running, where its replication stands
// and whether its cluster is healthy, and — behind the write tier — the rows
// themselves.
//
// # Why this is a separate artifact from plugins/mysql
//
// MariaDB answers the MySQL wire protocol and carries the same
// INFORMATION_SCHEMA, so the connection handling, the row bounds and the
// schema queries are the same work, and both plugins are served from one
// package for it: plugins/mysql/family, at the version this module's go.mod
// requires. What differs is held on the package's MariaDB engine: a Galera
// cluster's state, which the replication view carries beside the replica and
// source state both servers have, MariaDB's own spelling of positions,
// privileges and client flags, and its client's verify-ca pinning.
//
// It is still an artifact of its own, for the reason every plugin is: rta
// approves an artifact's content digest, not its name, and approving one
// binary is not approving another. Sharing the package changes nothing about
// that. This binary is built from a pinned version of it, so its behaviour
// and its digest move only when this module is rebuilt against a new one —
// a release of its own, approved on its own, as every plugin's dependency on
// rta's SDK already is.
//
// Build it and put it on your $PATH as `rta-plugin-mariadb`:
//
//	cd plugins/mariadb && go build -o ~/.local/bin/rta-plugin-mariadb .
//
// State the connection once, in rta's config, under the artifact's own
// section — `rta explain mariadb.overview` prints the exact heading including
// the digest:
//
//	plugins:
//	  mariadb@<digest>:
//	    host: db.internal
//	    user: app
//	    database: app
//
// and export RTA_MARIADB_PASSWORD. Every capability here reaches off the box,
// so none of them — including mariadb.overview — appear on the automatic
// dashboard on their own (see cap's comment in the family package); add one
// explicitly once you have decided polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: mariadb.overview
package main

import (
	"github.com/this-is-tobi/rta-plugins/plugins/mysql/family"
	"github.com/this-is-tobi/rta/pkg/sdk"
)

func main() { sdk.Serve(family.Plugin(family.MariaDB, version)) }

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut — an index entry carries this verbatim, and a
// version is a fact about a release, not about the source it came from.
var version = "dev"
