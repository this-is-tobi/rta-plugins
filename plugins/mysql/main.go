// Command rta-plugin-mysql talks to a MySQL server: what it is, what is in
// it, what shape that has, what is running, where its replication stands,
// and — behind the write tier — the rows themselves.
//
// It speaks to MariaDB too, since MariaDB answers the same wire protocol and
// carries the same INFORMATION_SCHEMA. What it does not do is know anything
// MariaDB-specific: a Galera cluster's replication state has no equivalent
// here and is not guessed at. plugins/mariadb is the one that knows about
// that, and it is a separate artifact for the reason every plugin is —
// approving one binary is not approving another.
//
// Build it and put it on your $PATH as `rta-plugin-mysql`:
//
//	cd plugins/mysql && go build -o ~/.local/bin/rta-plugin-mysql .
//
// State the connection once, in rta's config, under the artifact's own
// section — `rta explain mysql.overview` prints the exact heading including
// the digest:
//
//	plugins:
//	  mysql@<digest>:
//	    host: db.internal
//	    user: app
//	    database: app
//
// and export RTA_MYSQL_PASSWORD. Every capability here reaches off the box,
// so none of them — including mysql.overview — appear on the automatic
// dashboard on their own (see cap's comment); add one explicitly once you
// have decided polling it is fine:
//
//	dashboard:
//	  tiles:
//	    - id: mysql.overview
package main

import (
	"github.com/this-is-tobi/rta-plugins/plugins/mysql/family"
	"github.com/this-is-tobi/rta/pkg/sdk"
)

func main() { sdk.Serve(family.Plugin(version)) }

// version is what this build claims to be, stamped by whatever built it:
// `-X main.version=`, which is the Makefile's flag and GoReleaser's own
// default. A build nobody stamped says "dev" rather than claiming a release
// number that was never cut — an index entry carries this verbatim, and a
// version is a fact about a release, not about the source it came from.
var version = "dev"
