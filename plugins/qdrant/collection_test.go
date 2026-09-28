package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// A listing that worked is answered as a listing, through the capability's
// own Run rather than the helper behind it — the helper was never the part
// that failed. Run handed collectionTable's *view.Error straight back as its
// error, and a nil pointer inside an error interface is not a nil error, so
// every listing that succeeded reached the host as a failure with no code
// and no message.
func TestASuccessfulListingIsNotAnError(t *testing.T) {
	f := newFakeQdrant(t, map[string]string{
		"/collections": envelope(`{"collections":[]}`),
	})
	var run func() (view.View, error)
	for _, c := range Plugin().Capabilities {
		if c.ID == "qdrant.collection.list" {
			run = func() (view.View, error) {
				return c.Run(t.Context(), reqAt(t, f, "qdrant.collection.list", map[string]any{}))
			}
		}
	}
	if run == nil {
		t.Fatal("no qdrant.collection.list")
	}
	v, err := run()
	if err != nil {
		t.Fatalf("a listing that succeeded returned the error %#v", err)
	}
	if _, ok := v.(view.Table); !ok {
		t.Fatalf("view is %T, want a Table", v)
	}
}
