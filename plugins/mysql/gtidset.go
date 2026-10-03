package main

import (
	"sort"
	"strconv"
	"strings"
)

// A GTID set as a server writes it: source identifiers, each followed by the
// transaction numbers it has — "uuid:1-5:7,uuid2:1-3". Parsed only far enough
// to ask which transactions one set has that another lacks and how many that
// is.
//
// A tag, which newer servers allow between the identifier and the numbers
// ("uuid:tag:1-5"), is part of the key: two sets that differ only in tag are
// different transactions, and folding them would call a replica caught up that
// is not.

type interval struct{ lo, hi int64 }

type gtidSet map[string][]interval

func parseGTIDSet(s string) (gtidSet, bool) {
	set := gtidSet{}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		segments := strings.Split(part, ":")
		key := strings.ToLower(segments[0])
		if key == "" {
			return nil, false
		}
		for _, seg := range segments[1:] {
			lo, hi, ok := parseInterval(seg)
			if !ok {
				key += ":" + seg
				continue
			}
			set[key] = append(set[key], interval{lo, hi})
		}
	}
	for k, v := range set {
		set[k] = merged(v)
	}
	return set, true
}

func parseInterval(seg string) (lo, hi int64, ok bool) {
	a, b, ranged := strings.Cut(seg, "-")
	lo, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if !ranged {
		return lo, lo, true
	}
	hi, err = strconv.ParseInt(b, 10, 64)
	if err != nil || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

func merged(in []interval) []interval {
	sort.Slice(in, func(i, j int) bool { return in[i].lo < in[j].lo })
	var out []interval
	for _, iv := range in {
		if n := len(out); n > 0 && iv.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, iv.hi)
			continue
		}
		out = append(out, iv)
	}
	return out
}

// subtract is the transactions in s that other does not have.
func (s gtidSet) subtract(other gtidSet) gtidSet {
	out := gtidSet{}
	for key, ivs := range s {
		have := other[key]
		var left []interval
		for _, iv := range ivs {
			cur := iv
			for _, h := range have {
				if h.hi < cur.lo || h.lo > cur.hi {
					continue
				}
				if h.lo > cur.lo {
					left = append(left, interval{cur.lo, h.lo - 1})
				}
				cur.lo = h.hi + 1
				if cur.lo > cur.hi {
					break
				}
			}
			if cur.lo <= cur.hi {
				left = append(left, cur)
			}
		}
		if len(left) > 0 {
			out[key] = left
		}
	}
	return out
}

func (s gtidSet) count() int64 {
	var n int64
	for _, ivs := range s {
		for _, iv := range ivs {
			n += iv.hi - iv.lo + 1
		}
	}
	return n
}

func (s gtidSet) String() string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		var b strings.Builder
		b.WriteString(k)
		for _, iv := range s[k] {
			b.WriteString(":" + strconv.FormatInt(iv.lo, 10))
			if iv.hi != iv.lo {
				b.WriteString("-" + strconv.FormatInt(iv.hi, 10))
			}
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, ",")
}
