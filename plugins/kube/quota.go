package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// quotaColumns: the % is used against the quota's own hard limit, so it has a
// ceiling and 100 is the point pods start being refused — KindUsage, and the
// renderer grades it. Named so
// TestOnlyTheColumnsWithACapacityBehindThemAreGraded can say so.
func quotaColumns(allNamespaces bool) []view.Column {
	cols := []view.Column{}
	if allNamespaces {
		cols = append(cols, view.Column{Name: "namespace"})
	}
	return append(cols,
		view.Column{Name: "quota"},
		view.Column{Name: "resource"},
		view.Column{Name: "used"},
		view.Column{Name: "hard"},
		view.Column{Name: "%", Kind: view.KindUsage},
	)
}

// kube.quota.list: how much of a namespace's ResourceQuota is actually used.
//
// One row per resource tracked by a quota, not one row per quota object —
// "cpu: 3.5/4 (88%)" is the sentence an SRE reads before a namespace runs out
// of headroom and starts refusing pods; "quota compute-resources exists" is
// not. LimitRange objects are noted by count rather than fully modeled: their
// shape (per-object-type default/min/max/defaultRequest) does not table-ize
// alongside a used/hard resource map, and "how many are defined" is enough to
// point someone at `kubectl describe limitrange` for the rest.

type resourceQuotaItem struct {
	Metadata meta `json:"metadata"`
	Status   struct {
		Hard map[string]string `json:"hard"`
		Used map[string]string `json:"used"`
	} `json:"status"`
}

type limitRangeItem struct {
	Metadata meta `json:"metadata"`
}

// fetchQuotas is shared with kube.overview's own composition, so both read
// the same object the same way.
func fetchQuotas(ctx context.Context, s selection) (list[resourceQuotaItem], *view.Error) {
	var quotas list[resourceQuotaItem]
	verr := getJSON(ctx, s, "resourcequotas", &quotas)
	return quotas, verr
}

// quotaScope is what the read of one namespace's quotas and limit ranges came to.
// The quotas are the page, so a namespace that cannot be read for them is
// unread; the limit ranges are the note beside it, so a namespace that cannot be
// read for them keeps its quotas and says so.
type quotaScope struct {
	quotas   []resourceQuotaItem
	limits   []limitRangeItem
	limitErr *view.Error
}

func fetchQuotaScope(ctx context.Context, s selection) ([]quotaScope, *view.Error) {
	quotas, qerr := fetchQuotas(ctx, s)
	if qerr != nil {
		return nil, qerr
	}
	var limits list[limitRangeItem]
	limitErr := getJSON(ctx, s, "limitranges", &limits)
	if refusal := rejected(limitErr); refusal != nil {
		return nil, refusal
	}
	return []quotaScope{{quotas: quotas.Items, limits: limits.Items, limitErr: limitErr}}, nil
}

func runQuotaList(ctx context.Context, req plugin.Request) (view.View, error) {
	l, verr := listingOf(req)
	if verr != nil {
		return nil, verr
	}
	scopes, unread, qerr := readListing(ctx, l, fetchQuotaScope)
	if qerr != nil {
		return nil, qerr
	}
	var quotas list[resourceQuotaItem]
	var limits list[limitRangeItem]
	var limitErrs []*view.Error
	for _, sc := range scopes {
		quotas.Items = append(quotas.Items, sc.quotas...)
		limits.Items = append(limits.Items, sc.limits...)
		limitErrs = append(limitErrs, sc.limitErr)
	}

	cols := quotaColumns(l.wide())

	sort.Slice(quotas.Items, func(i, j int) bool {
		a, b := quotas.Items[i].Metadata, quotas.Items[j].Metadata
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	rows := make([][]string, 0, len(quotas.Items))
	for _, q := range quotas.Items {
		resources := make([]string, 0, len(q.Status.Hard))
		for r := range q.Status.Hard {
			resources = append(resources, r)
		}
		sort.Strings(resources)
		for _, r := range resources {
			hard := q.Status.Hard[r]
			used := q.Status.Used[r]
			row := []string{}
			if l.wide() {
				row = append(row, q.Metadata.Namespace)
			}
			rows = append(rows, append(row, q.Metadata.Name, r, used, hard, quotaPercent(used, hard)))
		}
	}

	t := withEmpty(view.Table{Columns: cols, Rows: rows, Total: len(rows)},
		"no resource quotas in "+l.scopeWords())
	return quotaView(withUnread(t, unread, 1), limits, limitErrs...), nil
}

// quotaView folds the LimitRange count in as a trailing note rather than a
// second table — one sentence, not a page, for a signal that is "present or
// not" far more often than it is something to read row by row. limitErrs is a
// read of the limit ranges of each namespace that failed, a nil among them being
// one that did not.
func quotaView(t view.Table, limits list[limitRangeItem], limitErrs ...*view.Error) view.View {
	var warnings []view.Error
	for _, e := range limitErrs {
		if e != nil {
			warnings = append(warnings, *e)
		}
	}
	if len(warnings) > 0 {
		// **"Forbidden to list limit ranges" and "this namespace has none"
		// are different facts**, and both rendered as the quota table on its
		// own — so a reader concluded there are no limit ranges from a page
		// that never managed to look. The quota rows still answer; the part
		// that did not is said beside them.
		return view.Sections{
			Items:    []view.Section{{ID: "quotas", Title: "Resource quotas", View: t}},
			Warnings: warnings,
		}
	}
	if len(limits.Items) == 0 {
		return t
	}
	return view.Sections{Items: []view.Section{
		{ID: "quotas", Title: "Resource quotas", View: t},
		{ID: "limits", Title: "Limit ranges", View: view.Text{
			Body: fmt.Sprintf("%d %s defined — `kubectl describe limitrange` for the "+
				"per-object-type defaults and bounds",
				len(limits.Items), plural(len(limits.Items), "limit range", "limit ranges")),
		}},
	}}
}

// quotaPercentValue parses both sides as Kubernetes quantities and reports
// used against hard as a fraction. Either side failing to parse (an
// unrecognised resource, a quantity shape this plugin's minimal parser does
// not cover) reports ok=false rather than a wrong number.
func quotaPercentValue(used, hard string) (float64, bool) {
	// cpu is the one resource whose quantities are typically sub-1 and
	// suffixed "m" rather than byte-suffixed; everything else here (memory,
	// storage, ephemeral-storage) and unitless counts (pods, count/things)
	// parse the same way as bytes because a bare integer is valid input to
	// both parsers.
	if u, uok := parseCPU(used); uok {
		if h, hok := parseCPU(hard); hok && h > 0 {
			return u / h, true
		}
	}
	u, uok := parseBytes(used)
	h, hok := parseBytes(hard)
	if !uok || !hok || h == 0 {
		return 0, false
	}
	return float64(u) / float64(h), true
}

// quotaPercent is quotaPercentValue rendered for a table cell.
func quotaPercent(used, hard string) string {
	v, ok := quotaPercentValue(used, hard)
	if !ok {
		return ""
	}
	return percentOf(v, 1)
}

// quotaPressure returns "namespace/quota resource: NN%" for every tracked
// resource at or above threshold — the line kube.overview folds in, so a
// namespace running out of headroom shows up before it starts refusing pods
// rather than after.
func quotaPressure(quotas list[resourceQuotaItem], threshold float64) []string {
	hits := quotaHits(quotas, threshold)
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, fmt.Sprintf("%s/%s %s: %s", h.namespace, h.quota, h.resource, percentOf(h.used, 1)))
	}
	sort.Strings(out)
	return out
}

// quotaHit is one tracked resource at or above the threshold: where it is,
// which it is, and how much of its limit is used.
type quotaHit struct {
	namespace, quota, resource string
	used                       float64
}

// quotaHits is the resources quotaPressure names, as the facts and not as a
// sentence, so that a page which sets the fullest first can.
func quotaHits(quotas list[resourceQuotaItem], threshold float64) []quotaHit {
	var out []quotaHit
	for _, q := range quotas.Items {
		for r, hard := range q.Status.Hard {
			v, ok := quotaPercentValue(q.Status.Used[r], hard)
			if !ok || v < threshold {
				continue
			}
			out = append(out, quotaHit{namespace: q.Metadata.Namespace, quota: q.Metadata.Name, resource: r, used: v})
		}
	}
	return out
}
