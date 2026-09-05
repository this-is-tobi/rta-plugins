package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func asServerError(err error, target **serverError) bool { return errors.As(err, target) }

func configGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:      "redis.config.get",
		Summary: "What the server is configured with",
		// Write for what it discloses: CONFIG GET * returns requirepass and
		// masterauth in clear, beside maxmemory and save. There is no
		// pattern that reliably excludes every credential-shaped directive
		// across versions and modules, so the whole command sits behind the
		// write tier rather than pretending a denylist is a wall.
		Safety:     plugin.Write,
		Idempotent: true,
		Description: "CONFIG GET for a pattern, as a table. Nothing here mutates.\n\n" +
			"**Classified write for what it discloses.** `requirepass` and `masterauth` come " +
			"back in clear beside everything else, and no pattern reliably excludes every " +
			"credential-shaped directive across versions and modules — so the whole command " +
			"is a write rather than a denylist pretending to be a wall. The two are masked on " +
			"human surfaces regardless.\n\n" +
			"The overview already grades the directives that matter most — maxmemory, its " +
			"policy, persistence — without this.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return configView(ctx, c, req)
			})
		},
	}, plugin.Field{Name: "pattern", Type: plugin.String, Positional: true, Default: "*",
		Help: "glob to match directive names (maxmemory*, save, *auth*)"})
}

// secretDirectives are masked on human surfaces whatever the pattern asked
// for. A denylist is not the wall — the write classification is — but a
// password printed on a shared terminal is a mistake nobody asked to make.
var secretDirectives = map[string]bool{"requirepass": true, "masterauth": true, "masteruser": true, "tls-key-file-pass": true}

func configView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	r, err := c.do(ctx, "CONFIG", "GET", req.String("pattern"))
	if err != nil {
		return nil, classify(err, c.addr)
	}
	kv := view.KeyValue{}
	for _, p := range r.pairs() {
		v := p[1]
		if v == "" {
			v = "(empty)"
		}
		kv.Pairs = append(kv.Pairs, view.Pair{Key: p[0], Value: v})
		if secretDirectives[p[0]] {
			kv.Redacted = append(kv.Redacted, p[0])
		}
	}
	if len(kv.Pairs) == 0 {
		return view.Text{Body: fmt.Sprintf("No directive matches %q on %s.", req.String("pattern"), c.addr)}, nil
	}
	return kv, nil
}

func slowlogCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:      "redis.slowlog",
		Summary: "The commands that took longest, with what they were called with",
		// Write for what it discloses: an entry carries the command line,
		// arguments included, and a slow SET's argument is a value.
		Safety:     plugin.Write,
		Idempotent: true,
		Description: "SLOWLOG GET: when each slow command ran, how long it took, who sent it and " +
			"the command line itself. The threshold is the server's `slowlog-log-slower-than`, " +
			"in microseconds; `rta redis config get slowlog*` shows it.\n\n" +
			"**Classified write for what it discloses.** An entry is the command with its " +
			"arguments, and on any server where a SET was ever slow that is a stored value.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return slowlogView(ctx, c, req)
			})
		},
	}, plugin.Field{Name: "count", Type: plugin.Int, Config: "slowlog.count", Default: 25, Min: 1, Max: 1000,
		Help: "how many entries, newest first"})
}

func slowlogView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	r, err := c.do(ctx, "SLOWLOG", "GET", strconv.Itoa(req.Int("count")))
	if err != nil {
		return nil, classify(err, c.addr)
	}
	t := view.Table{Columns: []view.Column{
		{Name: "ID"},
		{Name: "When", Kind: view.KindTimestamp},
		{Name: "Took", Kind: view.KindDuration},
		{Name: "Client"},
		{Name: "Command"},
	}}
	for _, e := range r.items {
		// Each entry: id, unix time, microseconds, [argv...], and on 4.0+
		// the client address and name.
		if len(e.items) < 4 {
			continue
		}
		client, name := "-", ""
		if len(e.items) > 4 {
			client = e.items[4].text()
		}
		if len(e.items) > 5 {
			name = e.items[5].text()
		}
		if name != "" {
			client += " (" + name + ")"
		}
		t.Rows = append(t.Rows, []string{
			e.items[0].text(),
			time.Unix(e.items[1].num, 0).Format("2006-01-02 15:04:05"),
			span(time.Duration(e.items[2].num) * time.Microsecond),
			client,
			strings.Join(e.items[3].strings(), " "),
		})
	}
	t.Total = len(t.Rows)
	if len(t.Rows) == 0 {
		return view.Text{Body: "The slow log is empty — nothing has exceeded slowlog-log-slower-than since it was last reset."}, nil
	}
	return t, nil
}

func memoryCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "redis.memory",
		Summary:    "Where the memory goes, and what the server thinks about it",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "MEMORY STATS as a table of where the bytes are — dataset, overhead, " +
			"clients, replication buffers, fragmentation — and MEMORY DOCTOR's own " +
			"diagnosis underneath, which is the server saying in words what the numbers " +
			"mean.",
		Run: func(ctx context.Context, req plugin.Request) (view.View, error) {
			return withClient(ctx, req, func(ctx context.Context, c *client) (view.View, error) {
				return memoryView(ctx, c, req)
			})
		},
	})
}

func memoryView(ctx context.Context, c *client, req plugin.Request) (view.View, error) {
	stats, err := c.do(ctx, "MEMORY", "STATS")
	if err != nil {
		return nil, classify(err, c.addr)
	}
	t := view.Table{Columns: []view.Column{{Name: "Stat"}, {Name: "Value"}}}
	for _, p := range stats.pairs() {
		t.Rows = append(t.Rows, []string{p[0], memoryStatText(p[0], p[1])})
	}
	t.Total = len(t.Rows)

	p := plugin.NewPage(ctx, req)
	p.Put("stats", t)
	doctor, err := c.do(ctx, "MEMORY", "DOCTOR")
	if err == nil {
		p.Put("doctor", view.Text{Body: strings.TrimSpace(doctor.text())})
	}
	return p.View(), nil
}

// memoryStatText renders a byte-valued stat as bytes and leaves ratios and
// counts as the server spelled them. The naming convention is the server's:
// anything ending in .bytes or named *.allocated is a size.
func memoryStatText(name, raw string) string {
	if strings.HasSuffix(name, ".bytes") || strings.HasSuffix(name, ".allocated") || name == "peak.allocated" || name == "total.allocated" {
		if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return format.Bytes(n)
		}
	}
	return raw
}
