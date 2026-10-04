package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A key, a member's name and the URLs it advertises are chosen by whoever
// writes to the store or runs the member, and every one of them reaches a
// reader's screen. Each goes through plugin.ListedName on its way there: one
// that reads as itself, spaces and accents included, is shown as it is, and one
// that does not is shown in quotes with what would not draw written out.

const (
	// oddName holds an escape sequence that would recolour what follows it and a
	// newline that would split a row, and listedOdd is how it is shown.
	oddName   = "esc\x1b[31mred\nrow"
	listedOdd = `"esc\x1b[31mred\nrow"`
	// plainName has a space and an accent, which a reader sees as themselves.
	plainName = "café menu"
	// blankName ends in a Braille blank, which Go's own quoting counts printable
	// and every font draws as nothing: it reads as "prod/db".
	blankName   = "prod/db" + braille
	listedBlank = `"prod/db\u2800"`
	braille     = "\u2800"
)

// drawsAsItself is whether s holds anything a terminal would act on.
func drawsAsItself(s string) bool { return !strings.ContainsAny(s, "\x1b\n") }

func TestAListedKeyThatDoesNotReadAsItselfIsWrittenOut(t *testing.T) {
	kvs := []*mvccpb.KeyValue{
		{Key: []byte("/" + oddName), Version: 1},
		{Key: []byte("/" + plainName), Version: 2},
	}
	tbl := kvListTable(kvs, 10, plugin.SurfaceCLI)
	if got, want := tbl.Rows[0][0], `"/esc\x1b[31mred\nrow"`; got != want {
		t.Errorf("odd key drawn as %q, want %q", got, want)
	}
	if got, want := tbl.Rows[1][0], "/"+plainName; got != want {
		t.Errorf("ordinary key drawn as %q, want it unchanged: %q", got, want)
	}
}

// What the next page starts after is the key as etcd holds it, and the warning
// that says the page stopped names it as a reader sees it.
func TestAStoppedListingCursorKeepsTheKeyAndTheWarningWritesItOut(t *testing.T) {
	for _, tc := range []struct{ key, listed string }{{oddName, listedOdd}, {blankName, listedBlank}} {
		kvs := []*mvccpb.KeyValue{{Key: []byte(tc.key), Version: 1}, {Key: []byte("zzz"), Version: 1}}
		tbl := kvListTable(kvs, 1, plugin.SurfaceCLI)
		if tbl.Page == nil || tbl.Page.Next != tc.key {
			t.Fatalf("cursor = %+v, want the key as etcd holds it, to start the next page after", tbl.Page)
		}
		if len(tbl.Warnings) != 1 {
			t.Fatalf("warnings = %+v, want the one that says it stopped", tbl.Warnings)
		}
		if msg := tbl.Warnings[0].Message; !strings.HasSuffix(msg, "more keys follow "+tc.listed) {
			t.Errorf("message = %q, want the key written out as %s", msg, tc.listed)
		}
	}

	plain := kvListTable([]*mvccpb.KeyValue{{Key: []byte(plainName)}, {Key: []byte("zzz")}}, 1, plugin.SurfaceCLI)
	if msg := plain.Warnings[0].Message; !strings.HasSuffix(msg, `more keys follow "café menu"`) {
		t.Errorf("message = %q, want an ordinary key quoted as it always was", msg)
	}
}

func TestATreeLabelThatDoesNotReadAsItselfIsWrittenOut(t *testing.T) {
	kvs := []*mvccpb.KeyValue{
		{Key: []byte("/" + oddName + "/leaf")},
		{Key: []byte("/" + oddName + "/" + plainName)},
		{Key: []byte("/" + plainName + "/leaf")},
	}
	tree := keyTree(kvs, "", 100, 5, plugin.SurfaceCLI)
	root := tree.Roots[0]
	odd := child(t, root.Children, `"`+`esc\x1b[31mred\nrow`+`"/`)
	child(t, odd.Children, "leaf")
	child(t, odd.Children, plainName)
	child(t, root.Children, plainName+"/", "leaf")

	var walk func([]view.Node)
	walk = func(nodes []view.Node) {
		for _, n := range nodes {
			if !drawsAsItself(n.Label) {
				t.Errorf("label %q holds a character that does not draw as itself", n.Label)
			}
			walk(n.Children)
		}
	}
	walk(root.Children)

	// A key that is the leaf itself, not a level.
	tree = keyTree([]*mvccpb.KeyValue{{Key: []byte("/" + oddName)}}, "", 100, 5, plugin.SurfaceCLI)
	child(t, tree.Roots[0].Children, listedOdd)
}

// The prefix is the tree's root, and a reader sees what they asked for named
// the way every level below it is.
func TestATreeRootThatDoesNotReadAsItselfIsWrittenOut(t *testing.T) {
	prefix := "/" + oddName + "/"
	tree := keyTree([]*mvccpb.KeyValue{{Key: []byte(prefix + "leaf")}}, prefix, 100, 5, plugin.SurfaceCLI)
	if got, want := tree.Roots[0].Label, `"/esc\x1b[31mred\nrow/"`; got != want {
		t.Errorf("root = %q, want %q", got, want)
	}
	child(t, tree.Roots[0].Children, "leaf")

	tree = keyTree(nil, "/"+plainName+"/", 100, 5, plugin.SurfaceCLI)
	if got, want := tree.Roots[0].Label, "/"+plainName+"/"; got != want {
		t.Errorf("root = %q, want an ordinary prefix unchanged: %q", got, want)
	}
	if got := keyTree(nil, "", 100, 5, plugin.SurfaceCLI).Roots[0].Label; got != "/" {
		t.Errorf("root of the whole keyspace = %q, want /", got)
	}
}

func TestAKeyThatDoesNotReadAsItselfIsWrittenOutWhereItsValueIsRead(t *testing.T) {
	pairValue := func(v view.View, key string) string {
		t.Helper()
		for _, p := range v.(view.KeyValue).Pairs {
			if p.Key == key {
				return p.Value
			}
		}
		t.Fatalf("no pair %q", key)
		return ""
	}
	if got := pairValue(kvGetResult("/"+oddName, []byte("v"), 1, 1, 1, 0), "key"); got != `"/esc\x1b[31mred\nrow"` {
		t.Errorf("odd key = %q, want it written out", got)
	}
	if got := pairValue(kvGetResult("/"+plainName, []byte("v"), 1, 1, 1, 0), "key"); got != "/"+plainName {
		t.Errorf("ordinary key = %q, want it unchanged", got)
	}
}

// The sentence quotes the key, as it always did; what %q left as it was, and a
// reader could not see, is written out.
func TestAKeyThatIsNotFoundIsNamedAsAReaderSeesIt(t *testing.T) {
	r := req(t, "etcd.kv.get", nil)
	for _, tc := range []struct{ key, want string }{
		{oddName, "no key " + listedOdd},
		{blankName, "no key " + listedBlank},
		{plainName, `no key "café menu"`},
		{`say "hi"`, `no key "say \"hi\""`},
	} {
		got := noSuchKey(r, tc.key)
		if got.Message != tc.want {
			t.Errorf("%q: message = %q, want %q", tc.key, got.Message, tc.want)
		}
		if !drawsAsItself(got.Message) {
			t.Errorf("%q: message %q holds a character that does not draw as itself", tc.key, got.Message)
		}
	}
}

func TestAMemberNameAndItsURLsThatDoNotReadAsThemselvesAreWrittenOut(t *testing.T) {
	url := "http://" + oddName + ":2380"
	tbl := memberListTable([]*etcdserverpb.Member{
		{ID: 1, Name: oddName, ClientURLs: []string{url, "http://ok:2379"}, PeerURLs: []string{url}},
		{ID: 2, Name: plainName, ClientURLs: []string{"http://café:2379"}, PeerURLs: []string{"http://café:2380"}},
		{ID: 3},
	})
	listedURL := `"http://esc\x1b[31mred\nrow:2380"`
	odd := tbl.Rows[0]
	if odd[1] != listedOdd || odd[2] != listedURL+", http://ok:2379" || odd[3] != listedURL {
		t.Errorf("odd member = %q, want name %s and the URL %s written out", odd, listedOdd, listedURL)
	}
	plain := tbl.Rows[1]
	if plain[1] != plainName || plain[2] != "http://café:2379" || plain[3] != "http://café:2380" {
		t.Errorf("ordinary member = %q, want it unchanged", plain)
	}
	if unstarted := tbl.Rows[2]; unstarted[1] != "-" || unstarted[2] != "-" || unstarted[4] != "unstarted" {
		t.Errorf("member with no name yet = %q, want the placeholders it always had", unstarted)
	}
}

func TestAMemberNameThatDoesNotReadAsItselfIsWrittenOutInTheOverview(t *testing.T) {
	rows := []memberRow{
		{id: 1, name: oddName, clientURLs: []string{"http://a:2379"}},
		{id: 2, name: plainName, clientURLs: []string{"http://b:2379"}},
		{id: 3},
	}
	tbl := membersTable(rows, leaderView{})
	if got := tbl.Rows[0][1]; got != listedOdd {
		t.Errorf("odd member = %q, want %s", got, listedOdd)
	}
	if got := tbl.Rows[1][1]; got != plainName {
		t.Errorf("ordinary member = %q, want it unchanged", got)
	}
	if got := tbl.Rows[2][1]; got != "-" {
		t.Errorf("member with no name yet = %q, want -", got)
	}
}

// A member names the address it advertises in the cell that says why it was
// not asked, or could not be, and that address is the member's to choose.
func TestAnAdvertisedURLThatDoesNotReadAsItselfIsWrittenOutWhereAMemberIsNotAsked(t *testing.T) {
	const selfID, otherID, plainID = 1, 2, 3
	odd := "http://" + oddName + ":2379"
	list := serveGRPC(t, func(s *grpc.Server) {
		etcdserverpb.RegisterClusterServer(s, &memberListServer{members: []*etcdserverpb.Member{
			{ID: selfID, Name: "a", ClientURLs: []string{"http://127.0.0.1:1"}},
			{ID: otherID, Name: "b", ClientURLs: []string{odd}},
			{ID: plainID, Name: "c", ClientURLs: []string{"http://café:2379"}},
		}})
	})
	c, err := clientv3.New(clientv3.Config{Endpoints: []string{list}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	self := &clientv3.StatusResponse{Header: &etcdserverpb.ResponseHeader{MemberId: selfID}}
	rq := req(t, "etcd.overview", map[string]any{"username": "monitor", "endpoint": "https://etcd.internal:2379"})
	rows, verr := askMembers(ctx, c, rq, self)
	if verr != nil {
		t.Fatal(verr)
	}
	got := memberHealth(rows[1], leaderView{})
	want := `info — not asked: advertises "http://esc\x1b[31mred\nrow:2379", which is not https://, ` +
		"so the credentials are not sent to it"
	if got != want {
		t.Errorf("health = %q, want %q", got, want)
	}
	if !drawsAsItself(got) {
		t.Errorf("health %q holds a character that does not draw as itself", got)
	}
	got = memberHealth(rows[2], leaderView{})
	want = "info — not asked: advertises http://café:2379, which is not https://, so the credentials are not sent to it"
	if got != want {
		t.Errorf("health = %q, want an ordinary address unchanged: %q", got, want)
	}
}

// And where it was asked and did not answer: the address is the one the member
// gave, written out beside the reason.
func TestAnAdvertisedURLThatDoesNotReadAsItselfIsWrittenOutWhereAMemberDoesNotAnswer(t *testing.T) {
	odd := "http://" + oddName + ":2379"
	got := unreachableAt(context.DeadlineExceeded, odd)
	if want := `no answer within 3s at "http://esc\x1b[31mred\nrow:2379"`; got != want {
		t.Errorf("why = %q, want %q", got, want)
	}
	if !drawsAsItself(got) {
		t.Errorf("why %q holds a character that does not draw as itself", got)
	}
	if got, want := unreachableAt(context.DeadlineExceeded, "https://café:2379"), "no answer within 3s at https://café:2379"; got != want {
		t.Errorf("why = %q, want an ordinary address unchanged: %q", got, want)
	}
}

// A key etcd is handed back is the key as etcd holds it: what the next page
// continues after, and what a not-found key's hint looks for again, are spelled
// for the surface by the SDK, which shell-quotes or JSON-quotes the real bytes.
// Written out as a list shows a name, the hint would continue after a key that
// is not there and look for another one, so the one thing that must not be
// listed is the one a reader pastes.
func TestAKeyHandedBackToEtcdIsTheKeyAsItIsHeldNotAsAListShowsIt(t *testing.T) {
	for _, key := range []string{oddName, blankName, `"quoted"`, " padded "} {
		listed := plugin.ListedName(key)
		if listed == key {
			t.Fatalf("%q reads as itself, so it proves nothing here", key)
		}
		for _, sf := range []plugin.Surface{plugin.SurfaceCLI, plugin.SurfaceTUI, plugin.SurfaceMCP} {
			tbl := kvListTable([]*mvccpb.KeyValue{{Key: []byte(key)}, {Key: []byte("zzz")}}, 1, sf)
			if hint := tbl.Warnings[0].Hint; !strings.Contains(hint, sf.InputTo("after", key)) ||
				strings.Contains(hint, sf.InputTo("after", listed)) {
				t.Errorf("%q on %s: hint %q does not hand the key back as etcd holds it", key, sf, hint)
			}

			r := req(t, "etcd.kv.get", nil).WithSurface(sf)
			call := func(value string) string {
				return sf.Call("etcd.kv.list",
					append([]plugin.Arg{{Name: "prefix", Value: value, Positional: true}}, reachArgs(r)...)...)
			}
			if hint := noSuchKey(r, key).Hint; !strings.Contains(hint, call(key)) || strings.Contains(hint, call(listed)) {
				t.Errorf("%q on %s: hint %q does not look for the key as etcd holds it", key, sf, hint)
			}
		}
	}
}
