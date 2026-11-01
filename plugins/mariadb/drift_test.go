package main

import (
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// This plugin's code is a copy of what plugins/mysql now serves from its
// family package, for both servers, until this one builds on that package
// too. The gate that held the two copies alike is gone with the second copy
// of the shared files; what stays is the check that every sentence here was
// worded for MariaDB.

// meansMySQL is every phrase this plugin's text may name MySQL in, because
// it means MySQL itself there: its tools answering in MariaDB's place, or its
// server on the other end of this plugin's connection. Everywhere else the
// server is MariaDB.
//
// A sentence carried over from the other fork with that fork's name still in
// it reads naturally right up to the moment somebody follows it: this
// plugin's query description, its restore's guarantee and a grant hint called
// the server MySQL for as long as a gate that mapped one name onto the other
// stayed green. A sentence ported from plugins/mysql is worded for MariaDB
// here, or its phrase is added to this list when it does mean MySQL.
var meansMySQL = []string{
	"MySQL's own tools",              // a dump flag refused because the other client answered
	"Unknown MySQL server host",      // the client's own words, matched in its stderr
	"MySQL server, the mysql plugin", // the way out when MySQL is what answered
}

func TestEverySentenceNamesTheServerThisPluginTalksTo(t *testing.T) {
	said, err := sdktest.Sentences(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range said {
		rest := s.Text
		for _, phrase := range meansMySQL {
			rest = strings.ReplaceAll(rest, phrase, "")
		}
		// The flavour a version string reads as, when it names no fork.
		if rest == "MySQL" || !strings.Contains(rest, "MySQL") {
			continue
		}
		t.Errorf("%s: %q names MySQL where the server this plugin talks to is MariaDB — "+
			"word it for MariaDB, or add the phrase to meansMySQL if it does mean MySQL",
			s.Pos, s.Text)
	}
}
