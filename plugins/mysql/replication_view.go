package main

import (
	"strings"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/view"
)

func summaryTable(f facet) view.Table {
	return view.Table{
		Columns: []view.Column{{Name: "Role"}, {Name: "Status", Kind: view.KindStatus}, {Name: "Detail"}},
		Rows:    [][]string{{f.role, f.status, f.detail}},
		Total:   1,
	}
}

func sourcesTable(channels []channelRow) view.Table {
	t := view.Table{Columns: []view.Column{
		{Name: "Channel"},
		{Name: "Source"},
		{Name: "IO"},
		{Name: "SQL"},
		{Name: "Behind", Kind: view.KindDuration},
		{Name: "Read"},
		{Name: "Applied"},
		{Name: "Backlog", Kind: view.KindBytes},
		{Name: "Status", Kind: view.KindStatus},
	}}
	for _, c := range channels {
		t.Rows = append(t.Rows, []string{c.name, c.source, c.io, c.sql, c.behind, c.read, c.applied, c.backlog, c.status})
	}
	t.Total = len(t.Rows)
	return t
}

// gtidTable is shown only for a replica that follows its source by
// transaction identifiers; one that follows by file and position has nothing
// to put in it, and a table of dashes reads like a failure to read.
func gtidTable(channels []channelRow) (view.Table, bool) {
	t := view.Table{Columns: []view.Column{
		{Name: "Channel"}, {Name: "Mode"}, {Name: "Received"}, {Name: "Applied"}, {Name: "Pending"},
	}}
	for _, c := range channels {
		g := c.gtid
		if g.mode == "" && g.received == "" && g.applied == "" {
			continue
		}
		t.Rows = append(t.Rows, []string{c.name, dash(g.mode), dash(clip(g.received)), dash(clip(g.applied)), dash(g.pending)})
	}
	t.Total = len(t.Rows)
	return t, len(t.Rows) > 0
}

func hostsTable(hosts []map[string]string) view.Table {
	withID := false
	for _, h := range hosts {
		withID = withID || h["replica_uuid"] != ""
	}
	t := view.Table{Columns: []view.Column{{Name: "Server id", Kind: view.KindNumber}, {Name: "Host"}, {Name: "Port", Kind: view.KindNumber}}}
	if withID {
		t.Columns = append(t.Columns, view.Column{Name: "UUID"})
	}
	for _, h := range hosts {
		row := []string{dash(h["server_id"]), dash(h["host"]), dash(h["port"])}
		if withID {
			row = append(row, dash(h["replica_uuid"]))
		}
		t.Rows = append(t.Rows, row)
	}
	t.Total = len(t.Rows)
	return t
}

// serverPairs is what orients a reader before the rest: which server this is,
// whether it will take writes, and where its own binary log stands. A replica
// that accepts writes is how two copies of the data come to differ, and it is
// the one thing here a connection can see about the writes it will not make.
func serverPairs(st state) []view.Pair {
	var pairs []view.Pair
	if id := st.vars["server_id"]; id != "" {
		pairs = append(pairs, view.Pair{Key: "server id", Value: id})
	}
	if ro, ok := st.vars["read_only"]; ok {
		v := "no"
		if isOn(ro) {
			v = "yes"
		}
		if isOn(st.vars["super_read_only"]) {
			v += " (super)"
		}
		pairs = append(pairs, view.Pair{Key: "read only", Value: v})
	}
	pairs = append(pairs, view.Pair{Key: "binary log", Value: binlogText(st)})
	return append(pairs, vendorPairs(st.vars)...)
}

func binlogText(st state) string {
	switch {
	case st.vars["log_bin"] != "" && !isOn(st.vars["log_bin"]):
		return "off"
	case st.unread[sectionBinlog] != nil:
		return "on, position unreadable"
	case st.binlog["file"] != "":
		return st.binlog["file"] + ":" + st.binlog["position"]
	}
	return "on"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// setWidth bounds a transaction set in a cell. A source that has run for
// years under many identities carries a set the width of a page. The cell is
// for seeing that received and applied are alike or not, and what differs is
// the pending column, which is bounded too but is the part a reader acts on;
// the whole set is one statement away through mysql.query.
const setWidth = 200

func clip(s string) string {
	if utf8.RuneCountInString(s) <= setWidth {
		return s
	}
	return strings.TrimRight(string([]rune(s)[:setWidth]), ",:-") + "…"
}
