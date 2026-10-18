package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// emptyDocker puts a script on dockerBin that answers every query with
// nothing: a daemon with no container and no image.
func emptyDocker(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := dockerBin
	dockerBin = bin
	t.Cleanup(func() { dockerBin = old })
}

func answer(t *testing.T, id string, values map[string]any) view.View {
	t.Helper()
	for _, c := range Plugin().Capabilities {
		if c.ID != id {
			continue
		}
		v, err := c.Run(context.Background(), plugin.NewRequest(plugin.Resolve(c, plugin.Inputs{Caller: values}), false, false))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return v
	}
	t.Fatalf("no %s capability", id)
	return nil
}

// An empty listing is still its table (docs: writing a plugin): a program
// reads zero rows, and a person is told what the nothing is. A listing used to
// answer with text, so the same tool was a table with containers and a text
// without, and an agent branching on the type met both.
func assertEmptyTable(t *testing.T, v view.View, sentence string) {
	t.Helper()
	tbl, ok := v.(view.Table)
	if !ok {
		t.Fatalf("an empty answer is a %T, want the table", v)
	}
	if len(tbl.Rows) != 0 || tbl.Total != 0 || len(tbl.Columns) == 0 {
		t.Errorf("table = %d columns, %d rows, total %d, want its columns and no rows", len(tbl.Columns), len(tbl.Rows), tbl.Total)
	}
	if tbl.Empty != sentence {
		t.Errorf("empty sentence = %q, want %q", tbl.Empty, sentence)
	}
	raw, err := view.Marshal(view.Envelope{View: v})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"rows":[]`) || !strings.Contains(string(raw), `"type":"table"`) {
		t.Errorf("an agent reads %s, want a table with rows []", raw)
	}
}

func TestAnEmptyContainerListIsItsTable(t *testing.T) {
	emptyDocker(t)
	assertEmptyTable(t, answer(t, "docker.container.list", nil), "no running containers")
	assertEmptyTable(t, answer(t, "docker.container.list", map[string]any{"all": true}), "no containers")
}

func TestAnEmptyImageListIsItsTable(t *testing.T) {
	emptyDocker(t)
	assertEmptyTable(t, answer(t, "docker.image.list", nil), "no images")
}

func TestAnEmptyEnvironmentIsItsTable(t *testing.T) {
	const none = "this container declares no environment variables"
	assertEmptyTable(t, envView(nil, false), none)
	// Inspect shows the same table in a section, masked.
	assertEmptyTable(t, envView(nil, true), none)
}
