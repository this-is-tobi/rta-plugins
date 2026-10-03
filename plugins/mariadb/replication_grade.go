package main

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
)

// The rules that turn one replica status row into a graded line, kept apart
// from the reading so each can be tested against a row a server really sent.
//
// A status here is one of three leading words — ok, warn, fail — followed by
// the reason. The words are the ones a KindStatus cell is coloured by, and the
// reason is what survives a pipe and --no-color.

// lagWarn and lagFail are how far behind the source a running replica may be
// before it is called out, measured beyond any delay it was configured to
// keep. A minute is what the replica view this replaces already called
// behind. Ten minutes is where a replica stops being a slow read copy and
// becomes one that would lose recent writes if promoted: no deployment's
// failover tolerates that without having decided it, and a replica kept that
// far back on purpose says so with a delay, which is subtracted first.
const (
	lagWarn = time.Minute
	lagFail = 10 * time.Minute
)

type facet struct {
	role, status, detail string
}

// channelRow is one replication source as the table shows it.
type channelRow struct {
	name, source, io, sql string
	behind                string
	read, applied         string
	backlog               string
	delay                 string
	status                string
	gtid                  gtidState
}

// gtidState is a replica's transaction positions, in whatever vocabulary its
// server uses: the mode it follows the source by, what it has received, what
// it has applied, and what lies between the two.
type gtidState struct {
	mode, received, applied, pending string
}

func assessChannel(row map[string]string) channelRow {
	io := strings.ToLower(row["replica_io_running"])
	sqlThread := strings.ToLower(row["replica_sql_running"])
	lag, lagKnown := atoi(row["seconds_behind_source"])
	delay, _ := atoi(row["sql_delay"])

	c := channelRow{
		name:    channelName(row),
		source:  hostPort(row["source_host"], row["source_port"]),
		io:      io,
		sql:     sqlThread,
		behind:  "-",
		read:    position(row["source_log_file"], row["read_source_log_pos"]),
		applied: position(row["relay_source_log_file"], row["exec_source_log_pos"]),
		gtid:    gtidOf(row),
	}
	c.backlog = backlog(row)
	if delay > 0 {
		c.delay = format.Duration(time.Duration(delay) * time.Second)
	}
	if lagKnown {
		c.behind = format.Duration(time.Duration(lag) * time.Second)
	}

	ioErr, sqlErr := errorOf("IO", row["last_io_errno"]), errorOf("SQL", firstSet(row["last_sql_errno"], row["last_errno"]))
	var stopped []string
	if io == "no" {
		stopped = append(stopped, "IO")
	}
	if sqlThread != "yes" {
		stopped = append(stopped, "SQL")
	}
	errs := join("; ", ioErr, sqlErr)

	switch {
	case len(stopped) > 0:
		reason := strings.Join(stopped, " and ") + " thread"
		if len(stopped) > 1 {
			reason += "s"
		}
		reason += " stopped"
		if errs == "" {
			reason += ", no error recorded"
		}
		c.status = "fail — " + join("; ", reason, errs)
	case io != "yes" && ioErr != "":
		c.status = "fail — IO thread cannot reach the source; " + ioErr
	case io != "yes":
		c.status = "warn — IO thread " + io
	case !lagKnown:
		c.status = "warn — running, but the server reports no lag figure"
	default:
		c.status = lagStatus(lag, delay)
	}
	return c
}

func lagStatus(lag, delay int64) string {
	beyond := time.Duration(lag-delay) * time.Second
	if delay > 0 && lag < delay {
		beyond = 0
	}
	text := format.Duration(beyond) + " behind"
	if delay > 0 {
		text += " beyond its " + format.Duration(time.Duration(delay)*time.Second) + " delay"
	}
	switch {
	case beyond >= lagFail:
		return "fail — " + text
	case beyond >= lagWarn:
		return "warn — " + text
	}
	return "ok"
}

func channelName(row map[string]string) string {
	if n := firstSet(row["channel_name"], row["connection_name"]); n != "" {
		return n
	}
	return "(default)"
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func join(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

func atoi(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

func hostPort(host, port string) string {
	if host == "" {
		return "-"
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func position(file, pos string) string {
	if file == "" {
		return "-"
	}
	return file + ":" + pos
}

// errno is named for the class of failure and never for the message. The
// message is the statement that failed, row literals included, and a Read
// capability returns numbers the server publishes about itself, not values
// anybody stored — so the words here are this plugin's own, one per number a
// replica is commonly stopped by, and a number it does not know stays a
// number.
var errnoMeaning = map[int64]string{
	1032: "a row the replica should have is missing",
	1045: "the source refused the replication account",
	1062: "a duplicate key on the replica",
	1146: "a table the replica does not have",
	1236: "the source no longer holds the binary log the replica asked for",
	2003: "cannot connect to the source",
	2013: "the connection to the source was lost",
}

func errorOf(thread, errno string) string {
	n, ok := atoi(errno)
	if !ok || n == 0 {
		return ""
	}
	out := thread + " errno " + strconv.FormatInt(n, 10)
	if m, ok := errnoMeaning[n]; ok {
		out += " (" + m + ")"
	}
	return out
}

var logSequence = regexp.MustCompile(`\.(\d+)$`)

// backlog is how much of what the IO thread has read the SQL thread has not
// yet applied: the replica's own queue, and the only distance in positions one
// connection to a replica can measure. How far the IO thread is behind the
// source's own log needs the source's position, which only a connection to the
// source has — so that figure is read there and compared by eye, not guessed.
//
// Within one binary log file it is bytes. Across files the sizes of the files
// in between are not known here, so it says how many files instead of
// inventing a byte count.
func backlog(row map[string]string) string {
	readFile, execFile := row["source_log_file"], row["relay_source_log_file"]
	if readFile == "" || execFile == "" {
		return "-"
	}
	if readFile == execFile {
		read, ok1 := atoi(row["read_source_log_pos"])
		exec, ok2 := atoi(row["exec_source_log_pos"])
		if !ok1 || !ok2 || read < exec {
			return "-"
		}
		return format.Bytes(read - exec)
	}
	a, b := logSequence.FindStringSubmatch(readFile), logSequence.FindStringSubmatch(execFile)
	if a == nil || b == nil {
		return "-"
	}
	x, _ := strconv.Atoi(a[1])
	y, _ := strconv.Atoi(b[1])
	if x <= y {
		return "-"
	}
	return "across " + format.CountOf(x-y+1, "binary log file")
}
