package main

import (
	"io/fs"
	"strconv"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Every name here is somebody else's: an object's key is whatever the last
// writer to the bucket chose, and a file under a directory this uploads or a
// bucket this downloads is named by the same hand. A renderer strips the
// control characters from a cell on the way to a terminal, which is right of a
// body and wrong of a name — a key holding an escape sequence came out as
// another, ordinary key, a newline split a row, and nothing said the name was
// odd — so a name goes in a row, a label or a sentence through
// plugin.ListedName, as it is when it reads as itself and quoted, its
// characters written out, when it does not.
//
// What stays as it was: a value handed back to the server (a cursor, the key
// of a request), a command or an argument a hint gives its reader, which
// Surface.Call and its kin quote for the shell, and the text a server wrote.

// address is bucket/key as the one name a message or a preview shows an object
// by. One name and not two, so that a key which needs quoting is quoted whole,
// bucket and all, and a reader sees a single address rather than a bucket
// beside a quoted key.
func address(bucket, key string) string { return plugin.ListedName(bucket + "/" + key) }

// quoted is name for a sentence that quotes every name it holds, as these
// messages always have: in double quotes whether it needs them or not, and so
// byte for byte what %q wrote for a name that reads as itself.
//
// Not %q, which keeps a character that draws as nothing — a Braille blank, a
// variation selector — raw inside the quotes, so that "prod/db" and the same
// name with one of them on its end read alike in the very sentence meant to
// say which of the two was missing. A name plugin.ListedName has to quote is
// quoted by it, with those characters named.
func quoted(name string) string {
	if shown := plugin.ListedName(name); shown != name {
		return shown
	}
	return strconv.Quote(name)
}

// fsReason is err's text with the path an *fs.PathError names shown as a name,
// for a failure on a file whose name came from a key or from a directory
// somebody else filled. The error words its path raw — "mkdir /out/a\x1b[2J:
// permission denied" — which no part of a message built around it can undo, so
// the path is spelled here and the operating system's reason kept as it is. An
// error that is not one reads as it always did.
func fsReason(err error) string {
	pathErr, ok := err.(*fs.PathError)
	if !ok {
		return err.Error()
	}
	return pathErr.Op + " " + plugin.ListedName(pathErr.Path) + ": " + pathErr.Err.Error()
}
