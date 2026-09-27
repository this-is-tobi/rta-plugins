package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A count of one reads in the singular wherever this plugin prints one. These
// are the three that did not: a hash one field past the value bound, a hit
// ratio over a single hit or miss, and a SCAN reply with one element.

func TestOneFieldPastTheBoundIsCountedInTheSingular(t *testing.T) {
	items := make([]string, 0, 2*(maxValueItems+1))
	for i := range maxValueItems + 1 {
		items = append(items, bulk("f"+strings.Repeat("x", i)), bulk("v"))
	}
	srv := newFakeServer(t, map[string]string{
		"TYPE big": "+hash\r\n", "TTL big": ":-1\r\n", "HGETALL big": array(items...),
	})
	v, err := run(t, "redis.key.get", srv, map[string]any{"key": "big"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pairValue(v.(view.KeyValue), "…"); got != "1 more field not shown" {
		t.Errorf("marker = %q, want the one field left out counted in the singular", got)
	}
}

func TestAHitRatioOverOneHitAndOneMissIsInTheSingular(t *testing.T) {
	if got := hitRatio(1, 1); got != "50.0% (1 hit, 1 miss)" {
		t.Errorf("hitRatio(1, 1) = %q, want 50.0%% (1 hit, 1 miss)", got)
	}
	if got := hitRatio(90, 10); got != "90.0% (90 hits, 10 misses)" {
		t.Errorf("hitRatio(90, 10) = %q, want 90.0%% (90 hits, 10 misses)", got)
	}
}

func TestAScanReplyWithOneElementIsCountedInTheSingular(t *testing.T) {
	srv := newFakeServer(t, map[string]string{
		"SCAN 0 MATCH * COUNT 200": array(bulk("0")),
	})
	_, err := run(t, "redis.key.list", srv, nil)
	ve := view.AsError(err, "x")
	if ve.Code != "redis.scan.malformed" {
		t.Fatalf("err = %+v, want redis.scan.malformed", ve)
	}
	if !strings.Contains(ve.Message, "answered SCAN with 1 item, want 2") {
		t.Errorf("message = %q, want the one element counted in the singular", ve.Message)
	}
}
