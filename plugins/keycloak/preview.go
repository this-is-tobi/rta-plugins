package main

import (
	"strconv"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// peopleNote ends the description of each capability that returns what the
// realm stores about its people.
const peopleNote = "**Classified write for what it discloses, not what it changes.** It returns what the realm " +
	"stores about the people in it — usernames, e-mail addresses, the address a login came from, who changed " +
	"what — which is why it needs a grant a person issued; the read tier describes how the realm is set up and " +
	"hands back none of that."

// preview is what a dry run of one of those capabilities says: the call it
// would make and where, from the inputs alone.
//
// **A dry run that reached the realm to describe the call would be the call.**
// Minting the token is a request and a login event in the realm's own log, and
// the answer is the people it is a preview of, so nothing is sent: the inputs
// are validated as the call validates them (newSession), and what would be
// listed is said. The conformance suite watches the data directory and cannot
// see a request that leaves the machine, which is what the test beside this
// holds it to.
func preview(req plugin.Request, what string) (view.View, error) {
	s, verr := newSession(req)
	if verr != nil {
		return nil, verr
	}
	return view.Text{Body: "would " + what + " in realm " + listed(s.realm) + " on " + s.reached()}, nil
}

// upTo is how many a listing would stop at, in the singular where it is one.
func upTo(limit int, noun string) string { return "up to " + format.CountOf(limit, noun) }

// matching is the filters a call was given, as a preview names them, and
// nothing for a filter it was not.
func matching(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			parts = append(parts, pairs[i]+" "+strconv.Quote(pairs[i+1]))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " matching " + strings.Join(parts, ", ")
}
