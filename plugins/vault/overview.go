package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// vault.overview composes the seal status, the node's place in the cluster, the
// raft cluster's health, the current token and what the Vault holds through the
// one client its own Run already built — the same reason pg.overview calls its
// sections directly rather than through plugin.Page.AddAs, which would open one
// connection per section for what is supposed to be a single glance.
//
// NoPreview like every capability here (cap's job): the automatic dashboard
// must not decide on its own that a Vault deployment is worth polling every
// few seconds. `dashboard.tiles` still accepts it explicitly — naming a
// capability in a config file is the asking.

// overviewTimeout is how long the overview waits for a Vault in all, for the
// reads of the compact page together. The client's own wait is a minute a
// request, so a Vault that took the connection and never answered held the page
// for several minutes, and a tile for as long again at every refresh. A variable
// so a test need not wait ten seconds to see it given up on.
//
// The overview's alone: a snapshot moves the whole of the storage and a secret
// read or written is the caller's to wait for, and neither is held to it.
var overviewTimeout = 10 * time.Second

// overviewRefresh is how often a dashboard tile of vault.overview is re-run, and
// the pace a configuration cannot shorten. A run is seven requests, five of them
// authenticated and so five lines of the audit device a Vault's operator keeps,
// at every refresh for as long as the tile is open: the host's own pace would
// have written them twice as fast, and `refresh: 5s` was accepted.
const overviewRefresh = time.Minute

func overviewCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "vault.overview",
		Summary:    "Whether this Vault serves, and what the token can do",
		Keywords:   []string{"summary", "status", "health", "version", "seal", "raft"},
		Safety:     plugin.Read,
		Idempotent: true,
		Detailed:   true,
		Refresh:    overviewRefresh,
		Description: "Whether this Vault is worth talking to at all, in a few rows, worst first — never a " +
			"secret value. `status` leads and is `ok`, or `warn` or `fail` followed by the worst fact: no " +
			"active node, a raft cluster that is not healthy, a row the token may not read. Then whether " +
			"this node is the active one or a standby and who leads, for raft storage whether the cluster " +
			"is healthy and how many failures it tolerates, when the token expires and what it can do, " +
			"how many secret engines, auth methods and policies the Vault has, and last the version and " +
			"the storage.\n\n" +
			"A sealed or uninitialized Vault is its state alone: nothing else it could be asked would " +
			"answer. Reading the token spends one of its uses, which a token with a use limit is told, and " +
			"such a token is read for nothing else: every other row is a use more. A Vault that has not " +
			"answered in ten seconds is a timeout.\n\n" +
			"The rows are seven requests: the seal status, the leader, the token, the raft state, the " +
			"mounts, the auth methods and the policy list. `detail` adds the policy names, from the same " +
			"request that counts them.",
		Agent: "Whether the Vault serves, worst first: `status` is `ok`, or `warn` or `fail` and the worst fact. " +
			"Then this node (active or standby, and the leader), the raft cluster's health, when the token " +
			"expires and its policies, and counts of secret engines, auth methods and policies. A row the " +
			"token may not read says so. Sealed is one row. Reading the token spends one use. `detail` adds " +
			"the policy names.",
		Run: runOverview,
	})
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	ctx, cancel := context.WithTimeout(ctx, overviewTimeout)
	defer cancel()
	return withClient(req, func(client *vaultapi.Client) (view.View, error) {
		return overviewPage(ctx, client, req)
	})
}

func overviewPage(ctx context.Context, client *vaultapi.Client, req plugin.Request) (view.View, error) {
	r := readVault(ctx, client, req)
	if r.err != nil {
		return nil, r.err
	}
	summary := r.rows(time.Now())
	if !req.Bool("detail") {
		return summary, nil
	}
	p := plugin.NewPage(ctx, req)
	p.Put("summary", summary)
	switch {
	case r.stopped:
	case r.policiesErr != nil:
		// Classified before it is a warning, for the reason the rows give their
		// reasons in short: the client's own text is a paragraph that names the
		// request's URL, in a place that is a line.
		p.Warn(r.policiesErr)
	default:
		p.Put("policies", policyTable(r.policies))
	}
	return p.View(), nil
}

// vaultRead is what the overview asked of a Vault and what came back, each
// answer beside the reason it did not.
type vaultRead struct {
	seal    *vaultapi.SealStatusResponse
	sealErr *view.Error
	// stopped is a Vault that is sealed or not yet initialized, and was asked
	// nothing more.
	stopped bool

	leader    *vaultapi.LeaderResponse
	leaderErr *view.Error

	raft    *vaultapi.AutopilotState
	raftErr *view.Error

	token    *vaultapi.Secret
	tokenErr *view.Error

	engines, auths       int
	enginesErr, authsErr *view.Error

	policies    []string
	policiesErr *view.Error

	// err is set when nothing could be read, or when the Vault is not there.
	err error
}

// readVault makes the overview's requests, one after another through the one
// client, and keeps what each said.
//
// **A sealed Vault is one request.** Sealed, or not yet initialized, it answers
// the seal status and refuses everything after it, so the rest were asked only to
// be told what the first call had said. The state is the page, and asking nothing
// more of it is also what keeps a sealed Vault's audit log, and its operator's,
// from a burst of refused requests for every refresh of a tile.
//
// **A seal status that failed for any reason but the token's policy ends the
// read.** A Vault that refuses the connection, does not answer or is not one
// would fail the next six requests the same way, and a tile paid for each. A
// seal status the token may not read is an ordinary least-privilege token's, and
// the rest is asked: "is this Vault sealed" is the one thing this capability
// exists to say, and a row that says it could not be read is the way to say
// nothing.
//
// **A read that failed is said, not skipped.** A page with one fewer row reads as
// a deliberately compact report rather than as a question nobody answered. The
// reason is the classified one, short, and never the client's own text, which is
// a paragraph over several lines naming the request's URL — through a forward,
// the end of one that closed with the call — in a cell that is a line.
func readVault(ctx context.Context, client *vaultapi.Client, req plugin.Request) vaultRead {
	var r vaultRead
	var cause *view.Error
	read := 0
	note := func(err error) *view.Error {
		if err == nil {
			read++
			return nil
		}
		refusal := classify(err, req)
		if cause == nil {
			cause = refusal
		}
		return refusal
	}

	seal, err := client.Sys().SealStatusWithContext(ctx)
	if err != nil {
		refusal := classify(err, req)
		if refusal.Code != "vault.denied" {
			r.err = refusal
			return r
		}
		r.sealErr, cause = refusal, refusal
	} else {
		read++
		r.seal = seal
		if seal.Sealed || !seal.Initialized {
			r.stopped = true
			return r
		}
	}

	leader, err := client.Sys().LeaderWithContext(ctx)
	r.leader, r.leaderErr = leader, note(err)

	token, err := client.Auth().Token().LookupSelfWithContext(ctx)
	r.token, r.tokenErr = token, note(err)

	// **A token with a use limit is read for itself and for nothing else.** Every
	// request below is authenticated and spends a use, the seal status and the
	// leader do not, and a token made with -use-limit=3 would be spent by the
	// fourth request of the first run and refused for the rest of it: the page
	// that told it "this page spends one" would have spent five.
	spared := usesLeft(r.token)
	ask := func(read func() error) *view.Error {
		if spared != nil {
			return spared
		}
		return note(read())
	}

	if r.seal != nil && r.seal.StorageType == "raft" {
		r.raftErr = ask(func() (err error) {
			r.raft, err = client.Sys().RaftAutopilotStateWithContext(ctx)
			return err
		})
	}
	r.enginesErr = ask(func() error {
		engines, err := client.Sys().ListMountsWithContext(ctx)
		r.engines = len(engines)
		return err
	})
	r.authsErr = ask(func() error {
		auths, err := client.Sys().ListAuthWithContext(ctx)
		r.auths = len(auths)
		return err
	})
	r.policiesErr = ask(func() (err error) {
		r.policies, err = client.Sys().ListPoliciesWithContext(ctx)
		return err
	})

	if read == 0 {
		r.err = nothingRead(cause)
	}
	return r
}

// usesLeft is the reason the rest of the overview is not read, when the token it
// was read with can be used only so many times, and nil for a token that can be
// used without limit or that was not read. Vault reports num_uses once the
// lookup has been counted: the uses left, and -1 on the lookup that spent the last.
func usesLeft(token *vaultapi.Secret) *view.Error {
	if token == nil {
		return nil
	}
	n, _ := wholeNumber(token.Data["num_uses"])
	switch {
	case n > 0:
		return view.Errorf("vault.overview.spared", "the token has %s left, each read spends one", format.CountOf(int(n), "use"))
	case n < 0:
		return view.Errorf("vault.overview.spared", "the token has no uses left")
	}
	return nil
}

// grade is how bad a fact is, in the words the status row leads with and the
// theme colours: nothing, warn, fail.
type grade int

const (
	_ grade = iota
	gradeWarn
	gradeFail
)

// finding is one thing wrong, in a few words, for the status row. unread names
// the row when what is wrong is that its figure could not be read, so that
// several of them are one sentence.
type finding struct {
	grade  grade
	text   string
	unread string
}

// verdict is the status row: ok, or the worst finding with its grade. Of several
// equally bad, the first stands, and the rows are built in the order that puts
// the node before the cluster before the token. When the worst thing is that some
// rows could not be read, it names them all: a least-privilege token cannot read
// three, and a verdict that named one would read as the whole of it.
func verdict(found []finding) string {
	var worst *finding
	for i := range found {
		if worst == nil || found[i].grade > worst.grade {
			worst = &found[i]
		}
	}
	if worst == nil {
		return "ok"
	}
	word := "warn"
	if worst.grade == gradeFail {
		word = "fail"
	}
	if worst.unread == "" {
		return word + " — " + worst.text
	}
	var rows []string
	for _, f := range found {
		if f.grade == worst.grade && f.unread != "" {
			rows = append(rows, f.unread)
		}
	}
	return word + " — " + strings.Join(rows, ", ") + " could not be read"
}

// unreadWhy is why a read failed, short enough for a row. A refusal by the
// token's policy and a Vault that does not answer say the same in every place
// they happen, and the rest is the classified message, which is one line.
func unreadWhy(verr *view.Error) string {
	switch verr.Code {
	case "vault.denied":
		if _, why, ok := strings.Cut(verr.Message, " refused: "); ok {
			return why
		}
		return "permission denied"
	case "vault.timeout":
		return "did not answer in time"
	}
	return verr.Message
}

// rows is the answer a tile and an agent read by default.
//
// **The order is the order of a glance**: the verdict, whether this node serves,
// the cluster it is part of, the token the tile reads with, what the Vault holds,
// and last what it is — the version a healthy Vault and a failing one share. A
// tile is cut from the bottom, and the page this replaces opened with the state
// and ended with a token's lifetime in seconds.
func (r vaultRead) rows(now time.Time) view.KeyValue {
	if r.stopped {
		return view.KeyValue{Pairs: []view.Pair{{Key: "status", Value: r.stateText()}}}
	}
	var found []finding
	var rows []view.Pair
	add := func(key, value string, fs ...finding) {
		if value != "" {
			rows = append(rows, view.Pair{Key: key, Value: value})
		}
		found = append(found, fs...)
	}
	unread := func(key string, verr *view.Error) (string, []finding) {
		return view.CouldNotRead(unreadWhy(verr)), []finding{{gradeWarn, key + " could not be read", key}}
	}

	if r.leaderErr != nil {
		text, fs := unread("node", r.leaderErr)
		add("node", text, fs...)
	} else if r.leader != nil {
		text, fs := nodeRow(r.leader, now)
		add("node", text, fs...)
	}
	switch {
	case r.raftErr != nil:
		text, fs := unread("raft", r.raftErr)
		add("raft", text, fs...)
	case r.seal != nil && r.seal.StorageType == "raft":
		text, fs := raftRow(r.raft)
		add("raft", text, fs...)
	}
	if r.tokenErr != nil {
		text, fs := unread("token", r.tokenErr)
		add("token", text, fs...)
	} else if r.token != nil {
		add("token", tokenText(r.token))
		if policies, ok := r.token.Data["policies"]; ok {
			// A token made without the default policy carries none, and the
			// row's absence would read as a page that did not look.
			list := nameCell(policies)
			if list == "" {
				list = "none"
			}
			add("token policies", list)
		}
	}
	mounts, fs := r.mountsRow()
	add("mounts", mounts, fs...)
	if r.policiesErr != nil {
		text, fs := unread("policies", r.policiesErr)
		add("policies", text, fs...)
	} else {
		add("policies", fmt.Sprintf("%d defined", len(r.policies)))
	}
	if r.sealErr != nil {
		text, fs := unread("state", r.sealErr)
		add("state", text, fs...)
	} else {
		add("state", r.stateText())
	}
	return view.KeyValue{Pairs: append([]view.Pair{{Key: "status", Value: verdict(found)}}, rows...)}
}

// stateText is the seal state, the version and the storage: what the Vault is.
// For a Vault that is sealed or not initialized it is the page, and the word the
// theme colours red or amber leads it.
func (r vaultRead) stateText() string {
	state := "unsealed"
	if r.seal.Sealed {
		state = "sealed"
	}
	if !r.seal.Initialized {
		state = "not initialized"
	}
	text := state + " · " + r.seal.Version
	if r.stopped {
		return text
	}
	if r.seal.StorageType != "" {
		text += " · " + plugin.ListedName(r.seal.StorageType) + " storage"
	}
	return text
}

// nodeRow is whether the node asked is the one serving, and who is when it is
// not. A standby asked directly is not a fault, it is a standby; a cluster with
// no active node at all takes no request, and is.
func nodeRow(l *vaultapi.LeaderResponse, now time.Time) (string, []finding) {
	switch {
	case !l.HAEnabled:
		return "single node, HA is off", nil
	case l.IsSelf:
		if l.ActiveTime.IsZero() || l.ActiveTime.After(now) {
			return "active", nil
		}
		return "active for " + format.Uptime(now.Sub(l.ActiveTime)), nil
	case l.LeaderAddress == "":
		return "standby, no active node", []finding{{grade: gradeFail, text: "no active node"}}
	}
	return "standby (leader " + plugin.ListedName(l.LeaderAddress) + ")", nil
}

// raftRow is the raft cluster as autopilot sees it: whether it is healthy and
// how many more servers it can lose and still elect a leader. A Vault too old
// for autopilot says so; it is no fault of the cluster's.
func raftRow(s *vaultapi.AutopilotState) (string, []finding) {
	if s == nil {
		return "autopilot is not available on this Vault", nil
	}
	tolerates := "tolerates " + format.CountOf(s.FailureTolerance, "failure")
	if s.Healthy {
		return "healthy, " + tolerates, nil
	}
	var down []string
	for id, server := range s.Servers {
		if server == nil || server.Healthy {
			continue
		}
		name := server.Name
		if name == "" {
			name = id
		}
		down = append(down, plugin.ListedName(name))
	}
	sort.Strings(down)
	text, why := "not healthy", "raft is not healthy"
	if len(down) > 0 {
		list := strings.Join(down, ", ")
		text, why = text+" ("+list+")", why+": "+list
	}
	return text + ", " + tolerates, []finding{{grade: gradeWarn, text: why}}
}

// tokenText is when the token the page reads with stops working, and what the page
// costs a token that can be used only so many times. A token that never expires
// says so: a bare "0" reads as one already out of time, which is the opposite
// fact and the one an agent would act on.
func tokenText(secret *vaultapi.Secret) string {
	text := "expiry not reported"
	if ttl, ok := secret.Data["ttl"]; ok {
		text = tokenExpiry(ttl)
	}
	if uses := tokenUses(secret.Data["num_uses"]); uses != "" {
		text += ", " + uses
	}
	return text
}

func tokenExpiry(v any) string {
	n, ok := wholeNumber(v)
	switch {
	case !ok:
		return "expiry not reported"
	case n <= 0:
		return "does not expire"
	}
	return "expires in " + format.Duration(time.Duration(n)*time.Second)
}

// mountsRow counts the secret engines and the auth methods the Vault has, which
// is what the token can read of them: a count, never a path or a name. Each is
// its own request, and the one that was refused is said beside the one that
// answered.
func (r vaultRead) mountsRow() (string, []finding) {
	engines := format.CountOf(r.engines, "secret engine")
	auths := format.CountOf(r.auths, "auth method")
	unread := []finding{{gradeWarn, "mounts could not be read", "mounts"}}
	switch {
	case r.enginesErr != nil && r.authsErr != nil:
		return view.CouldNotRead(unreadWhy(r.enginesErr)), unread
	case r.enginesErr != nil:
		return auths + "; secret engines " + view.CouldNotRead(unreadWhy(r.enginesErr)), unread
	case r.authsErr != nil:
		return engines + "; auth methods " + view.CouldNotRead(unreadWhy(r.authsErr)), unread
	}
	return engines + ", " + auths, nil
}

// wholeNumber reads the integer Vault's JSON decoded into whichever type the
// decoder handed back.
func wholeNumber(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	}
	return 0, false
}

// tokenUses says what this page costs a token that can be used only so many
// times, and nothing for one that can be used without limit.
//
// **Reading a token spends one of its uses**, and the overview reads it every
// time: a token made with -use-limit=3 answered three runs and the fourth said
// "token unreadable — permission denied", on a tile that re-ran every thirty
// seconds. Vault reports num_uses once the lookup has been counted: 0 for a
// token with no limit, the uses left otherwise, and -1 on the lookup that spent
// the last, after which the token is gone. The last is said because it is the
// run after which the tile goes red, and the number alone is not there to say it.
func tokenUses(v any) string {
	n, _ := wholeNumber(v)
	switch {
	case n > 0:
		return "has " + format.CountOf(int(n), "use") + " left; this page spends one"
	case n < 0:
		return "has no uses left; this page spent the last one"
	}
	return ""
}

// nothingRead is the refusal for an overview that read nothing, and it says why
// whenever the reason is not the token's own. It was the bare "nothing could
// be read" for a Vault that refused the connection as much as for one that
// refused the token, so the one failure an agent can act on — the address is
// wrong, the server is down, the certificate does not verify — was reported
// as the one it cannot. A refusal by the token's policy stays the overview's
// own answer, since neither read was the token's to make.
func nothingRead(cause *view.Error) *view.Error {
	if cause != nil && cause.Code != "vault.denied" {
		return cause
	}
	return view.Errorf("vault.overview.unavailable", "nothing could be read").
		WithHint("this token may read neither the seal status nor itself, so there is nothing for an overview to say")
}
