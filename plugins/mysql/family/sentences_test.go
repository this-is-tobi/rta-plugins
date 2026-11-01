package family

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// ownFiles are the files that speak for one engine, and so may name it:
// everything else here is said by both plugins, and a sentence in it naming
// one server is wrong in the other's mouth. That is how a sentence copied
// between the two forks used to go wrong — "MySQL enforces it" in MariaDB's
// query description, its restore's guarantee and a grant hint, for as long as
// the old drift gate, which mapped one name onto the other before comparing,
// stayed green.
var ownFiles = map[string]bool{
	"engine.go":            true, // each engine's own words, side by side
	"dialect_mysql.go":     true,
	"gtidset.go":           true,
	"replication_group.go": true,
	"dialect_mariadb.go":   true,
	"galera.go":            true,
	"pin.go":               true,
}

// sharedProductNames are the phrases a shared file may say a product's name
// in, because there it means that product whichever plugin is speaking.
var sharedProductNames = []string{
	"Unknown MySQL server host", // both clients' own words, matched in their stderr
}

func TestASharedSentenceNamesNeitherServer(t *testing.T) {
	said, err := sdktest.Sentences(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range said {
		if ownFiles[filepath.Base(s.Pos.Filename)] {
			continue
		}
		rest := s.Text
		for _, phrase := range sharedProductNames {
			rest = strings.ReplaceAll(rest, phrase, "")
		}
		for _, product := range []string{MySQL.Product, MariaDB.Product} {
			// A bare name is the flavour a version string reads as (flavourOf),
			// which is the same fact whichever plugin reads it.
			if rest != product && strings.Contains(rest, product) {
				t.Errorf("%s: %q names %s in a file both plugins speak through — say engine.Product, "+
					"or move the sentence to that engine's own file", s.Pos, s.Text, product)
			}
		}
	}
}
