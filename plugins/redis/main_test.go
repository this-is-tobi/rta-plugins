package main

import (
	"bufio"
	"context"
	"fmt"
	stdnet "net"
	"strconv"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
	"github.com/this-is-tobi/rta/pkg/view"
)

// req builds a resolved request the way the host would — defaults applied,
// caller values on top — so these test the values a handler actually sees.
func req(t *testing.T, capID string, values map[string]any) plugin.Request {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false)
		}
	}
	t.Fatalf("no capability %q", capID)
	return plugin.Request{}
}

func TestPluginIsValid(t *testing.T) {
	if err := Plugin().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(func(string) map[string]map[string]any {
		return map[string]map[string]any{"redis.key.get": {"key": "absent"}}
	}))
}

// Every shared connection input must be Local: together they name which
// server a call reaches and as whom, and an MCP caller may not choose that.
func TestEveryConnectionInputIsLocal(t *testing.T) {
	for _, f := range connFields() {
		if !f.Local {
			t.Errorf("%s: connection input is not Local — an MCP caller could redirect this call", f.Name)
		}
	}
}

func TestOnlySecretsUseEnvFallback(t *testing.T) {
	for _, f := range connFields() {
		if f.EnvFallback && f.Type != plugin.Secret {
			t.Errorf("%s: non-secret input declares EnvFallback", f.Name)
		}
	}
}

// The line through the plugin: everything that returns something somebody
// stored is a write, and the one that names a single key needs a grant.
func TestTheDisclosingCapabilitiesAreWrites(t *testing.T) {
	want := map[string]plugin.Safety{
		"redis.overview": plugin.Read, "redis.client.list": plugin.Read, "redis.cluster": plugin.Read,
		"redis.key.list": plugin.Read, "redis.key.tree": plugin.Read, "redis.memory": plugin.Read,
		"redis.key.get": plugin.Write, "redis.config.get": plugin.Write, "redis.slowlog": plugin.Write,
	}
	for _, c := range Plugin().Capabilities {
		w, ok := want[c.ID]
		if !ok {
			t.Errorf("unexpected capability %s", c.ID)
			continue
		}
		if c.Safety != w {
			t.Errorf("%s safety = %s, want %s", c.ID, c.Safety, w)
		}
		if c.ID == "redis.key.get" && (!c.NeedsGrant || c.Scope != "key") {
			t.Errorf("redis.key.get must need a grant scoped to the key: NeedsGrant=%v Scope=%q", c.NeedsGrant, c.Scope)
		}
		if !c.NoPreview {
			t.Errorf("%s reaches off the box and must not be an automatic tile", c.ID)
		}
	}
}

// fakeServer speaks enough RESP2 to answer scripted commands. Commands are
// matched on their joined arguments; anything unscripted gets an -ERR.
type fakeServer struct {
	ln      stdnet.Listener
	answers map[string]string
	seen    []string
}

func newFakeServer(t *testing.T, answers map[string]string) *fakeServer {
	t.Helper()
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, answers: answers}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(conn stdnet.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		cmd := strings.Join(args, " ")
		s.seen = append(s.seen, cmd)
		if ans, ok := s.answers[cmd]; ok {
			_, _ = conn.Write([]byte(ans))
			continue
		}
		if strings.EqualFold(args[0], "PING") {
			_, _ = conn.Write([]byte("+PONG\r\n"))
			continue
		}
		_, _ = fmt.Fprintf(conn, "-ERR unknown command '%s'\r\n", args[0])
	}
}

func readCommand(r *bufio.Reader) ([]string, error) {
	head, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(head[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if _, err := r.ReadString('\n'); err != nil {
			return nil, err
		}
		v, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimRight(v, "\r\n"))
	}
	return args, nil
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func array(items ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(items))
	for _, it := range items {
		b.WriteString(it)
	}
	return b.String()
}

func run(t *testing.T, capID string, srv *fakeServer, values map[string]any) (view.View, error) {
	t.Helper()
	if values == nil {
		values = map[string]any{}
	}
	values["address"] = srv.addr()
	for _, c := range Plugin().Capabilities {
		if c.ID == capID {
			return c.Run(context.Background(), req(t, capID, values))
		}
	}
	t.Fatalf("no capability %q", capID)
	return nil, nil
}

const sampleInfo = "# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\nuptime_in_seconds:90061\r\n" +
	"# Clients\r\nconnected_clients:3\r\nblocked_clients:0\r\n" +
	"# Memory\r\nused_memory:800000000\r\nused_memory_rss:900000000\r\nmaxmemory:1000000000\r\nmaxmemory_policy:allkeys-lru\r\nmem_fragmentation_ratio:1.12\r\n" +
	"# Persistence\r\nloading:0\r\nrdb_changes_since_last_save:42\r\nrdb_bgsave_in_progress:0\r\nrdb_last_save_time:1700000000\r\nrdb_last_bgsave_status:ok\r\naof_enabled:1\r\naof_rewrite_in_progress:0\r\naof_last_bgrewrite_status:ok\r\n" +
	"# Stats\r\ninstantaneous_ops_per_sec:120\r\nevicted_keys:7\r\nkeyspace_hits:90\r\nkeyspace_misses:10\r\n" +
	"# Replication\r\nrole:master\r\nconnected_slaves:1\r\nslave0:ip=10.0.0.2,port=6379,state=online,offset=12345,lag=0\r\nmaster_repl_offset:12345\r\n" +
	"# Keyspace\r\ndb0:keys=1500,expires=200,avg_ttl=3600000\r\n"

func TestOverviewGradesMemoryAgainstMaxmemory(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(sampleInfo)})
	v, err := run(t, "redis.overview", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	page := v.(view.Sections)
	mem := sectionOf(t, page, "memory").(view.Table)
	if got := mem.Rows[0]; got[2] != "80.0%" || got[5] != "allkeys-lru" || got[6] != "7" {
		t.Errorf("memory row = %v", got)
	}
	repl := sectionOf(t, page, "replication").(view.Table)
	if len(repl.Rows) != 1 || repl.Rows[0][1] != "10.0.0.2:6379" || repl.Rows[0][2] != "online" {
		t.Errorf("replication rows = %v", repl.Rows)
	}
	ks := sectionOf(t, page, "keyspace").(view.Table)
	if ks.Rows[0][1] != "1500" || ks.Rows[0][3] != "1h 0m" {
		t.Errorf("keyspace row = %v", ks.Rows[0])
	}
	srvPairs := sectionOf(t, page, "server").(view.KeyValue)
	if got := pairValue(srvPairs, "hit ratio"); !strings.HasPrefix(got, "90.0%") {
		t.Errorf("hit ratio = %q", got)
	}
}

// maxmemory 0 is no ceiling, and the share must be blank rather than 0%.
func TestOverviewBlanksTheShareWithoutACeiling(t *testing.T) {
	info := strings.Replace(sampleInfo, "maxmemory:1000000000", "maxmemory:0", 1)
	srv := newFakeServer(t, map[string]string{"INFO all": bulk(info)})
	v, err := run(t, "redis.overview", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	mem := sectionOf(t, v.(view.Sections), "memory").(view.Table)
	if got := mem.Rows[0]; got[1] != "not set" || got[2] != "-" || !strings.Contains(got[5], "no ceiling") {
		t.Errorf("memory row without maxmemory = %v", got)
	}
}

func TestKeyListWalksScanAndNeverKeys(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH user:* COUNT 200": array(bulk("7"), array(bulk("user:2"), bulk("user:1"))),
		"SCAN 7 MATCH user:* COUNT 200": array(bulk("0"), array(bulk("user:3"))),
		"TYPE user:1":                   "+string\r\n",
		"TYPE user:2":                   "+hash\r\n",
		"TYPE user:3":                   "+list\r\n",
		"TTL user:1":                    ":-1\r\n",
		"TTL user:2":                    ":90\r\n",
		"TTL user:3":                    ":-1\r\n",
	})
	v, err := run(t, "redis.key.list", srv, map[string]any{"pattern": "user:*"})
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	if len(tbl.Rows) != 3 || tbl.Rows[0][0] != "user:1" || tbl.Rows[1][2] != "1m 30s" {
		t.Errorf("rows = %v", tbl.Rows)
	}
	for _, cmd := range srv.seen {
		if strings.HasPrefix(cmd, "KEYS") {
			t.Fatal("the listing used KEYS, which blocks the server")
		}
	}
}

func TestKeyListSaysWhenItStopped(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH * COUNT 200": array(bulk("0"), array(bulk("a"), bulk("b"), bulk("c"))),
		"TYPE a":                   "+string\r\n", "TYPE b": "+string\r\n", "TTL a": ":-1\r\n", "TTL b": ":-1\r\n",
	})
	v, err := run(t, "redis.key.list", srv, map[string]any{"limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(view.Table)
	last := tbl.Rows[len(tbl.Rows)-1]
	if last[0] != "…" || !strings.Contains(last[2], "stopped at 2") {
		t.Errorf("no truncation row: %v", tbl.Rows)
	}
}

func TestKeyTreeDrawsTheSeparator(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH * COUNT 200": array(bulk("0"), array(bulk("user:1:name"), bulk("user:2:name"), bulk("cache:home"))),
	})
	v, err := run(t, "redis.key.tree", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := v.(view.Tree)
	root := tree.Roots[0]
	if root.Detail != "3 keys" || len(root.Children) != 2 {
		t.Fatalf("root = %+v", root)
	}
	user := root.Children[1]
	if user.Label != "user:" || user.Detail != "2 keys" || len(user.Children) != 2 {
		t.Errorf("user node = %+v", user)
	}
}

func TestKeyGetMasksTheValueAndRefusesAMissingKey(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"TYPE session:1": "+string\r\n", "TTL session:1": ":300\r\n", "GET session:1": bulk("tok-secret"),
		"TYPE nope": "+none\r\n", "TTL nope": ":-2\r\n",
	})
	v, err := run(t, "redis.key.get", srv, map[string]any{"key": "session:1"})
	if err != nil {
		t.Fatal(err)
	}
	kv := v.(view.KeyValue)
	if pairValue(kv, "value") != "tok-secret" || len(kv.Redacted) != 1 || kv.Redacted[0] != "value" {
		t.Errorf("value not carried and redacted: %+v", kv)
	}
	_, err = run(t, "redis.key.get", srv, map[string]any{"key": "nope"})
	if ve := view.AsError(err, "x"); ve.Code != "redis.key.notfound" {
		t.Errorf("missing key = %+v", ve)
	}
}

func TestConfigGetMasksCredentials(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"CONFIG GET *": array(bulk("maxmemory"), bulk("0"), bulk("requirepass"), bulk("hunter2"), bulk("masterauth"), bulk("")),
	})
	v, err := run(t, "redis.config.get", srv, nil)
	if err != nil {
		t.Fatal(err)
	}
	kv := v.(view.KeyValue)
	if len(kv.Redacted) != 2 || kv.Redacted[0] != "requirepass" || kv.Redacted[1] != "masterauth" {
		t.Errorf("redacted = %v", kv.Redacted)
	}
	if pairValue(kv, "masterauth") != "(empty)" {
		t.Errorf("empty directive = %q", pairValue(kv, "masterauth"))
	}
}

func TestServerErrorsAreClassified(t *testing.T) {
	cases := map[string]string{
		"-NOAUTH Authentication required.\r\n":          "redis.auth.required",
		"-WRONGPASS invalid username-password pair\r\n": "redis.auth.failed",
		"-LOADING Redis is loading the dataset\r\n":     "redis.loading",
		"-MOVED 3999 127.0.0.1:6381\r\n":                "redis.cluster.redirect",
		"-NOPERM this user has no permissions\r\n":      "redis.denied",
	}
	for answer, code := range cases {
		srv := newFakeServer(t, map[string]string{"PING": answer})
		_, err := run(t, "redis.overview", srv, nil)
		if ve := view.AsError(err, "x"); ve.Code != code {
			t.Errorf("%q → %s, want %s", strings.TrimSpace(answer), ve.Code, code)
		}
	}
}

func TestAuthIsSentAsTheServerExpects(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"AUTH alice s3cret": "+OK\r\n", "SELECT 3": "+OK\r\n", "INFO all": bulk(sampleInfo),
	})
	if _, err := run(t, "redis.overview", srv, map[string]any{"username": "alice", "password": "s3cret", "db": 3}); err != nil {
		t.Fatal(err)
	}
	if srv.seen[0] != "AUTH alice s3cret" || srv.seen[1] != "SELECT 3" || srv.seen[2] != "PING" {
		t.Errorf("handshake order = %v", srv.seen[:3])
	}
}

func TestNothingListeningIsCoded(t *testing.T) {
	ln, _ := stdnet.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	_, err := run(t, "redis.overview", &fakeServer{ln: closedListener{addr}}, nil)
	if ve := view.AsError(err, "x"); ve.Code != "redis.conn.refused" {
		t.Errorf("closed port = %+v", ve)
	}
}

type closedListener struct{ a string }

func (c closedListener) Accept() (stdnet.Conn, error) { return nil, fmt.Errorf("closed") }
func (c closedListener) Close() error                 { return nil }
func (c closedListener) Addr() stdnet.Addr            { a, _ := stdnet.ResolveTCPAddr("tcp", c.a); return a }

func sectionOf(t *testing.T, s view.Sections, title string) view.View {
	t.Helper()
	for _, it := range s.Items {
		if it.Title == title {
			return it.View
		}
	}
	t.Fatalf("no section %q", title)
	return nil
}

func pairValue(kv view.KeyValue, key string) string {
	for _, p := range kv.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}
