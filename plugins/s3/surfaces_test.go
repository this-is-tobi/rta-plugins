package main

import (
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool and its arguments, a form's box, or the connection setting the
// operator holds — never as a flag or an `rta` command line its reader has no
// terminal for, except in the one phrase that hands a command to the
// operator.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	r := func(sf plugin.Surface) plugin.Request {
		return req(t, "s3.object.get", map[string]any{"key": "k", "endpoint": "s3.internal:9000"}).WithSurface(sf)
	}
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			name:    "a bucket that is not there",
			cli:     "`rta s3 bucket list` shows what is there",
			other:   "the `s3_bucket_list` tool shows what is there",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: "shop"}, r(sf)))
			},
		},
		{
			name:    "credentials the server rejected",
			cli:     "or check --access-key",
			other:   "or check `access-key`",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(minio.ErrorResponse{Code: minio.InvalidAccessKeyID}, r(sf)))
			},
		},
		{
			name:    "nothing listening",
			cli:     "is the server up, and is --endpoint right?",
			other:   "is the server up, and is `endpoint` right?",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&net.OpError{Op: "dial", Err: errors.New("connection refused")}, r(sf)))
			},
		},
		{
			name:    "a certificate nothing trusts",
			cli:     "the CA that issued it belongs in --ca-file — a local MinIO's self-signed public.crt is its own CA",
			other:   "the CA that issued it belongs in `ca-file` — a local MinIO's self-signed public.crt is its own CA",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(x509.UnknownAuthorityError{}, r(sf)))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain s3.overview` lists every input and where each one can come from",
			other:   "ask the operator to run `rta explain s3.overview`, which lists every input",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("handshake went sideways"), r(sf)))
			},
		},
		{
			name:    "an object too large to print",
			cli:     "use --out to write it to a file instead of printing it",
			other:   "ask the operator to run `rta s3 object get 'report q1.csv' --bucket shop --out <file>`",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return outHint(r(sf), "shop", "report q1.csv")
			},
		},
		{
			name:    "a destination already taken",
			cli:     "rta s3 object rm reports/q1.csv --bucket shop",
			other:   `s3_object_rm {"key":"reports/q1.csv"}`,
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return rmCall(sf, "shop", "reports/q1.csv")
			},
		},
		{
			name:    "nothing to upload",
			cli:     "give <value>, or --file to upload from disk",
			other:   `give the "value" argument`,
			surface: plugin.SurfaceMCP,
			say:     noValueHint,
		},
		{
			name:    "nothing to upload, in a form",
			cli:     "give <value>, or --file to upload from disk",
			other:   "give the value box, or the file box to upload from disk",
			surface: plugin.SurfaceTUI,
			say:     noValueHint,
		},
		{
			name:    "a directory that is not there",
			cli:     "`rta s3 bucket download --out <dir>` writes one",
			other:   "`s3.bucket.download out=<dir>` writes one",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				_, _, verr := planUpload(sf, filepath.Join(t.TempDir(), "nope"), 10)
				return refusal(verr)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if said := tc.say(plugin.SurfaceCLI); !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			said := tc.say(tc.surface)
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}
