// Package family is the code the MySQL family's plugins share: plugins/mysql
// and plugins/mariadb speak the same wire protocol to servers with the same
// INFORMATION_SCHEMA, and every fix to one of them used to land twice. Each
// binary serves one Engine, which names what it reaches and holds what that
// engine does differently.
package family

import (
	"context"
	"database/sql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// Engine is one server of the family as its plugin speaks to it.
//
// What differs is held here, as values beside the reason for each, rather
// than as a branch on the engine's name in the code that uses it: a reader of
// a shared file sees one path, and a reader of this one sees every place the
// two servers part ways.
type Engine struct {
	// NS is the plugin's namespace, which every capability, error code and
	// setting here is named under.
	NS string
	// Product is the server's name as a sentence says it.
	Product string
	// Summary is the plugin's own one line.
	Summary string
	// Cluster is what the overview's cluster row says it holds, in the
	// description a person reads (Cluster) and in the one an agent does
	// (ClusterAgent): Group Replication on MySQL, a Galera node on MariaDB.
	Cluster, ClusterAgent string

	// dialect is how this server is asked about its replication and its
	// cluster, and how its client is told about TLS.
	dialect

	// replicationSummary is replication.status's one line, and clusterNote
	// what its description adds for the clustering layer this server has, for
	// a person (clusterNote) and for an agent (clusterAgentNote): the same
	// facts, without the reasons.
	replicationSummary, clusterNote, clusterAgentNote string
	// standalone is the replication detail of a server doing none of it.
	standalone string

	// dumpTools and restoreTools are the client's names, tried in order; the
	// list exists so the "not installed" refusal can name what it looked for.
	dumpTools, restoreTools []string
	// clientInstall is how the "not installed" refusal says to get them.
	clientInstall string
	// dumpFlags are the flags only this client's dump takes.
	dumpFlags []string
	// dumpConsistency is the dump description's paragraphs on what its file
	// can promise, which is where the two clients differ most.
	dumpConsistency string
	// otherHalf is the receipt's way to the users and grants a dump of one
	// database leaves out, spelled the way this client spells it.
	otherHalf string
	// processHint is the hint for a dump the server refused PROCESS.
	processHint string
	// toolSkewHint is the hint for a client that does not know a flag this
	// one passed, which names the other plugin when that is the way out.
	toolSkewHint string
	// pinsTLS is whether verify-ca hands the child the fingerprint of the
	// certificate the pre-flight verified (pin.go), for a client that has no
	// mode checking a chain without the name.
	pinsTLS bool
}

// dialect is what the shared code asks of a server and cannot say once for
// both forks: the spelling of each replication statement, the privilege each
// one takes, the vocabulary of a replica's transaction positions, the
// clustering layer it may be part of, and its client's TLS flags.
type dialect interface {
	// versionString is VERSION() as the version comparisons read it.
	versionString(raw string) string
	// The replication statements, each the spelling this version answers to
	// first and the others after it — firstSupported moves on only for a
	// syntax error.
	replicaStatusStatements(serverVersion) []string
	binlogStatusStatements(serverVersion) []string
	replicaHostsStatements(serverVersion) []string
	variablesStatement() string
	// privilegeFor names what the server wants for a section, privilegeNote
	// what a grant of it would hand over besides, and grantScope what a grant
	// of it is made ON.
	privilegeFor(section, serverVersion) string
	privilegeNote(section) string
	grantScope(section) string
	vendorPairs(vars map[string]string) []view.Pair
	gtidOf(row map[string]string) gtidState
	clusterOf(ctx context.Context, db *sql.DB) clusterPart
	// tlsArgs is the client's flags for the plugin's tls input.
	tlsArgs(req plugin.Request) []string
	// sourceNote is what the dump's receipt adds to the source it describes,
	// read from db just before the dump; empty when there is nothing to add.
	sourceNote(ctx context.Context, db *sql.DB, sf plugin.Surface) string
}

// MySQL is the engine plugins/mysql serves.
var MySQL = Engine{
	NS:           "mysql",
	Product:      "MySQL",
	Summary:      "MySQL: connection health, schema, rows, activity and replication",
	Cluster:      "a Group Replication member's state, only where one is configured",
	ClusterAgent: "the Group Replication member's state where one is configured",

	dialect:            mysqlDialect{},
	replicationSummary: "Replication and Group Replication state in one place: role, lag, positions, replicas, group",
	clusterNote: "\n\nOn a Group Replication member it adds the group as this server sees it: each " +
		"member's state, role and applier queue, and whether this server still has a majority — a member " +
		"that has lost it keeps answering SELECT while the group stops committing writes. The group's tables " +
		"take SELECT on performance_schema, asked only of a server configured for a group, with the grant " +
		"named when it is missing.",
	clusterAgentNote: " On a Group Replication member it adds each member's state, role and applier " +
		"queue, and whether this server still has a majority.",
	standalone: "not replicating from anything, not in a replication group, and no replica is connected",

	dumpTools:     []string{"mysqldump"},
	restoreTools:  []string{"mysql"},
	clientInstall: "the MySQL client tools — `brew install mysql-client` or `apt install mysql-client`",
	dumpFlags: []string{
		// mysqldump 8 asks the server for tablespace DDL, which needs the
		// PROCESS privilege — a wall every application-level user hits for a
		// feature (NDB/general tablespaces) their database does not use.
		// Skipped so a user who can read the database can dump the database.
		"--no-tablespaces",
		// Portable seed, not replica state: with GTID carried, the restore
		// demands SUPER and refuses any server that is not brand new. A
		// replica is built with replication tooling, not with this file.
		"--set-gtid-purged=OFF",
	},
	dumpConsistency: "Consistent for what can be: `mysqldump --single-transaction` reads every InnoDB table from one " +
		"snapshot, and the receipt counts the non-transactional tables that are read live " +
		"outside it rather than claiming a guarantee they cannot have. GTID state is not " +
		"carried (`mysqldump --set-gtid-purged=OFF`): this file seeds a database, not a replica — " +
		"carrying it makes the restore demand SUPER and fail on any server that is not " +
		"brand new.\n\n",
	// **The other half is spelled differently per fork, and only one of them
	// has a flag for it.** Checked against both clients rather than assumed:
	// mariadb-dump 11.4 has `--system=name` ("Any combination of: all, users,
	// plugins, udfs, servers, stats, timezones"), and mysqldump 8.4 has no
	// --system at all — its usage line is `mysqldump [OPTIONS] database
	// [tables]` and the accounts are reached by naming the `mysql` database
	// like any other.
	otherHalf: "mysqldump has no system-tables option for them: `mysqldump mysql` is the other half",
	processHint: "this is the server refusing a privilege, not rta — and not the tablespace " +
		"wall, which `mysqldump --no-tablespaces` already avoids. Check SHOW GRANTS",
	// Most often a MariaDB client answering to the mysqldump name.
	toolSkewHint: "the installed client does not speak this flag — `mysqldump --version` " +
		"says what it really is. For a MariaDB server, the mariadb plugin drives " +
		"mariadb-dump with the flags that client actually has",
}

// MariaDB is the engine plugins/mariadb serves.
var MariaDB = Engine{
	NS:           "mariadb",
	Product:      "MariaDB",
	Summary:      "MariaDB: connection health, schema, rows, activity, replication and cluster state",
	Cluster:      "a Galera node's state, only where wsrep is on",
	ClusterAgent: "the Galera node's state where wsrep is on",

	dialect:            mariadbDialect{},
	replicationSummary: "Replication and Galera state in one place: role, lag, positions, replicas, cluster",
	clusterNote: "\n\nOn a Galera node it adds the cluster's own view of the node: the cluster size and " +
		"state, whether this node is in the primary component, caught up and in step with the cluster's " +
		"state, and how much flow control is holding it back. A node that has lost quorum still accepts " +
		"connections and still answers SELECT — it just stops being part of the cluster, and nothing else " +
		"about the server looks wrong while that happens.",
	clusterAgentNote: " On a Galera node it adds the cluster size and state, whether the node is in " +
		"the primary component and in step, and its flow control.",
	standalone: "not replicating from anything, not part of a cluster, and no replica is connected",

	// MariaDB 10.5 introduced the mariadb-* names and 11.0 made them primary,
	// but every distribution still ships mysqldump and mysql as symlinks to
	// the same binaries, and plenty of machines have only those. The new name
	// first so a host carrying both forks' clients gets MariaDB's.
	dumpTools:     []string{"mariadb-dump", "mysqldump"},
	restoreTools:  []string{"mariadb", "mysql"},
	clientInstall: "the MariaDB client tools — `brew install mariadb` or `apt install mariadb-client`",
	// No --no-tablespaces and no --set-gtid-purged, unlike MySQL's: both are
	// MySQL 8 spellings this client does not have, and a flag it does not have
	// is an immediate "unknown option" rather than a lesser dump. MariaDB needs
	// neither anyway — GTID state is opt-in through --gtid and stays out by
	// default, which is the portable-seed behaviour --set-gtid-purged=OFF has
	// to ask for over there, and the tablespace DDL that needs PROCESS is a
	// MySQL 8 behaviour MariaDB never grew.
	dumpFlags: nil,
	// Aria is the engine that catches people here and not on MySQL: it is
	// MariaDB's own crash-safe engine, it is what system tables use, and being
	// crash-safe is not being transactional.
	dumpConsistency: "Consistent for what can be: `mariadb-dump --single-transaction` reads every InnoDB table from one " +
		"snapshot, and the receipt counts the non-transactional tables — Aria and MyISAM — " +
		"read live outside it rather than claiming a guarantee they cannot have. On a Galera " +
		"node the receipt also says whether the node held quorum: one that has lost it still " +
		"answers queries, from its own side of the partition, and nothing about the resulting " +
		"file would say so afterwards.\n\n" +
		"MariaDB's client is a fork, not an alias, so the flags differ from the mysql " +
		"plugin's by necessity: TLS through the `mariadb-dump --ssl` family rather than " +
		"`mysqldump --ssl-mode`, and no `mysqldump --set-gtid-purged` or `mysqldump --no-tablespaces`, " +
		"which this client does not have — GTID stays " +
		"out by default here, which is what that spelling has to ask for over there. A flag " +
		"the installed client does not know is refused by name, since the likeliest cause is " +
		"MySQL's own tools answering to it.\n\n",
	otherHalf: "`mariadb-dump --system=users` is the other half",
	processHint: "this is the server refusing a privilege, not rta — check SHOW GRANTS for " +
		"the user this dump connects as",
	// The likelier failure on this side: the --ssl family is MariaDB's, so a
	// *MySQL* client wearing the mysqldump name — the shape on a host with
	// only MySQL's tools installed — refuses on the first of them.
	toolSkewHint: "the installed client does not speak this flag — `mysqldump --version` " +
		"says which fork it really is. This plugin drives MariaDB's client; for a " +
		"MySQL server, the mysql plugin drives mysqldump with the flags that client " +
		"actually has",
	pinsTLS: true,
}

// engine is the one this process serves, set by Plugin before anything is
// called. A package-level value rather than a receiver on every function:
// a binary never serves two engines, and every name and word here is the one
// it serves. A test that needs another sets it for its own duration
// (useEngine), which is safe because no test here runs in parallel.
var engine = MySQL
